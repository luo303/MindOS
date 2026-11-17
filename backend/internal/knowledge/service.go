// 本文件是知识模块的业务层：校验、编排、事务边界。
//
// 它不 import gin，也不拼 HTTP 响应；失败用本包的哨兵错误表示，
// 由 handler 翻译成统一错误码。这样同一段业务逻辑将来被 worker 复用时，
// 不需要跟着改错误处理。
package knowledge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"strings"

	"unicode/utf8"

	"go.uber.org/zap"

	"github.com/luo303/mindos/backend/internal/platform"
	"github.com/luo303/mindos/backend/internal/task"
)

// 模块级哨兵错误。handler 用 errors.Is 判断，不要比较错误字符串。
var (
	// ErrEmptyDocument 表示正文去掉空白后为空——空文档没有可切分的内容。
	ErrEmptyDocument = errors.New("document is empty")
	// ErrNotUTF8 表示内容不是合法 UTF-8（例如误传了 PDF 或压缩包）。
	ErrNotUTF8 = errors.New("document is not valid UTF-8 text")
	// ErrTooLarge 表示超过 STORAGE_MAX_UPLOAD_BYTES。
	ErrTooLarge = errors.New("document exceeds the upload size limit")
	// ErrDuplicate 表示同内容的文档已存在（content_hash 唯一索引冲突）。
	ErrDuplicate = errors.New("document content already exists")
	// ErrNotFound 表示文档不存在。
	ErrNotFound = errors.New("document not found")
	// ErrSourceMissing 表示文档的原文已不可得（文件被删、笔记没有正文），
	// 重索引无法进行。这不是"参数错"，而是数据状态已经不可解释。
	ErrSourceMissing = errors.New("document source is missing")
	// ErrInvalidMetadata 表示分类或标签不合法（超长、含分隔符、数量过多）。
	ErrInvalidMetadata = errors.New("invalid document metadata")
	// ErrStorageUnavailable 表示读原文时对象存储不可用（连不上、超时）。
	//
	// 与 ErrSourceMissing 的区别很重要：原文缺失是数据状态坏了，重试没有意义；
	// 存储不可用是暂时故障，应该重试。两者混成一个错误，就会出现
	// "MinIO 重启一下就能好的任务被判成永久失败"。
	ErrStorageUnavailable = errors.New("document storage is unavailable")
	// ErrNoProcessing 表示该文档当前没有进行中的处理可以取消。
	ErrNoProcessing = errors.New("no processing in progress")
	// ErrReindexRunning 表示同一文档已有未完成的重索引任务。
	// 与"重复导入"不同，重复导入返回既有文档（幂等），而重索引是"再排队一次"
	// 的意图——已经排着队了就没必要再来一条，直接告诉调用方在跑。
	ErrReindexRunning = errors.New("document reindex is already running")
	// ErrEmbedRunning 表示同一文档已有未完成的向量化任务。
	ErrEmbedRunning = errors.New("embedding for this document is already running")
	// ErrNotReady 表示文档还没处理完（pending/indexing），现在没有分块可向量化。
	ErrNotReady = errors.New("document is not ready for embedding")
	// ErrNoChunks 表示文档已就绪但没有任何分块——没有可向量化的内容。
	ErrNoChunks = errors.New("document has no chunks to embed")

	// —— 向量化失败分类。前三类是"再试也没用"，最后一类可以重试。——
	//
	// ErrEmbeddingNotConfigured 表示三个配置项全空。它不该出现在任务里：
	// 未配置时导入会直接跳过向量化（标 skipped），不会排任务。
	ErrEmbeddingNotConfigured = errors.New("embedding model is not configured")
	// ErrEmbeddingAuth 表示凭据被拒（401/403）：Key 错或过期。
	ErrEmbeddingAuth = errors.New("embedding provider rejected the credentials")
	// ErrEmbeddingBadRequest 表示请求被拒（模型名错、单条输入过长等）。
	ErrEmbeddingBadRequest = errors.New("embedding provider rejected the input")
	// ErrEmbeddingDimensionMismatch 表示返回维度与 vector(1024) 不符。
	ErrEmbeddingDimensionMismatch = errors.New("embedding dimension mismatch")
	// ErrEmbeddingUnavailable 表示限流、超时、5xx、连不上——等一会儿能好。
	ErrEmbeddingUnavailable = errors.New("embedding provider is unavailable")
	// ErrStaleChunks 表示这次向量化读到的分块已被重索引整体替换，结果作废。
	// 它不是失败：既不该重试（新任务已经在跑），也不该写进文档的 embed_error。
	ErrStaleChunks = errors.New("chunk set was replaced, embedding result discarded")
)

// 分页边界。上限 100 是"防止一次拉全库"的护栏，不是性能优化。
const (
	defaultPageSize = 20
	maxPageSize     = 100
)

// documentRepository 是 Service 需要的数据访问能力。
//
// 接口定义在**使用方**而不是 Repository 一侧：Go 的接口是隐式实现的，
// *Repository 什么都不用写就满足它；而测试可以塞一个内存实现，
// 不需要数据库。这是本项目"接口按需定义"的常规做法。
type documentRepository interface {
	// —— 面向用户：全部带 ownerID ——
	CreateWithChunks(ctx context.Context, doc *Document, chunks []Chunk) error
	Get(ctx context.Context, ownerID, id int64) (*Document, error)
	GetByContentHash(ctx context.Context, ownerID int64, hash string) (*Document, error)
	List(ctx context.Context, ownerID int64, filter DocumentFilter, limit, offset int) ([]Document, error)
	Count(ctx context.Context, ownerID int64, filter DocumentFilter) (int64, error)
	ListChunks(ctx context.Context, ownerID, documentID int64, limit, offset int) ([]Chunk, error)
	CountChunks(ctx context.Context, ownerID, documentID int64) (int64, error)
	Update(ctx context.Context, ownerID, id int64, patch DocumentPatch) error
	Delete(ctx context.Context, ownerID, id int64) error
	// StoragePathRefs 返回全库还有几条文档引用这份原文（决定了能不能删对象）。
	StoragePathRefs(ctx context.Context, storagePath string) (int64, error)
	ReplaceChunks(ctx context.Context, documentID int64, chunks []Chunk) error
	// CreateWithTask 在一个事务里写待处理文档、任务与消息，返回消息编号。
	CreateWithTask(ctx context.Context, doc *Document, kind string) (string, error)
	// FinishImport 在同一事务里替换分块、置 ready，并按需排一条向量化任务。
	FinishImport(ctx context.Context, documentID int64, chunks []Chunk, enqueueEmbed bool) error
	// MarkIndexingWithTask 在同一事务里把文档置 indexing 并排一条重索引任务。
	MarkIndexingWithTask(ctx context.Context, ownerID, documentID int64, kind string) (string, error)
	// —— 异步链路：按 id 直操作，id 来自内部消息而不是客户端 ——
	// GetInternal 不带 owner 条件，只允许消费端与取消收尾使用。
	GetInternal(ctx context.Context, id int64) (*Document, error)
	// MarkFailed 记录处理最终失败与原因。
	MarkFailed(ctx context.Context, documentID int64, code, message string) error
	// MarkCancelling 把文档置为"取消中"，等 worker 停下后写终态。
	MarkCancelling(ctx context.Context, documentID int64) error
	// MarkReady 把文档置回 ready（重索引被取消时用）。
	MarkReady(ctx context.Context, documentID int64) error

	// —— 向量化 ——
	// ListChunkSources 返回分块的 id 与正文，供重算向量使用。
	ListChunkSources(ctx context.Context, documentID int64) ([]ChunkSource, error)
	// SaveEmbeddings 一个事务写回全部向量；分块已被替换时返回 ErrStaleChunks。
	SaveEmbeddings(ctx context.Context, documentID int64, model string, dimensions int, vectors []ChunkVector) error
	// MarkEmbedRunning / MarkEmbedFailed 只动向量化状态，不改 documents.status。
	MarkEmbedRunning(ctx context.Context, documentID int64) error
	MarkEmbedFailed(ctx context.Context, documentID int64, code, message string) error
	// MarkEmbedPendingWithTask 置"待向量化"并排一条任务（同一事务）。
	MarkEmbedPendingWithTask(ctx context.Context, ownerID, documentID int64, kind string) (string, error)
	// ListEmbedCandidates 找出该用户还没按当前模型向量化的已就绪文档。
	ListEmbedCandidates(ctx context.Context, ownerID int64, model string, limit int) ([]int64, error)
	// CountEmbedCandidates 返回该用户还剩多少候选，供回填报告 remaining。
	CountEmbedCandidates(ctx context.Context, ownerID int64, model string) (int64, error)
}

// taskStore 是本模块需要任务表的能力：读活跃任务与写取消终态。
//
// 定义在消费方（本包）而不是直接用 *task.Repository：service 只用两个方法，
// 窄接口让测试替身少写一堆空方法，也让"knowledge 用了任务表的哪部分"一目了然。
//
// 注意这里没有"唤醒"：消息由 relay 每 200ms 扫 outbox 投递，
// 业务侧落库即完成，不再需要主动叫醒任何人。
type taskStore interface {
	// ListActive 返回某文档当前全部未完成的任务（可能同时有 import 与 embed 各一条）。
	ListActive(ctx context.Context, targetType string, targetID int64) ([]task.Task, error)
	// CancelPending 只在任务尚未被领走时取消，返回是否抢到了。
	CancelPending(ctx context.Context, id int64) (bool, error)
}

// fileStore 是原始文件的存储能力。
//
// 只声明本模块真正调用的三个方法。Read 到 Phase 5 有了重索引才加进来——
// 接口窄一点，测试替身就少写一个空方法，也少一处"这个方法到底谁在用"的疑问。
type fileStore interface {
	Save(ctx context.Context, data []byte) (platform.SavedFile, error)
	Read(ctx context.Context, relPath string) ([]byte, error)
	Remove(ctx context.Context, relPath string) error
}

// Service 编排导入流程：校验 → 解析 → 切分 → 向量化 → 落盘 → 落库。
type Service struct {
	repo documentRepository
	// tasks 只用于"取消进行中的处理"：判断任务是排着队还是在跑。
	// 建任务走 repository 的同事务方法——文档、任务、消息必须一起写。
	tasks taskStore
	store fileStore
	// embedder 为 nil 表示没有配置向量模型：导入照常，向量化跳过。
	//
	// 用接口而不是具体类型：这里只用到三个方法，测试替身能少写两个空方法；
	// platform.Embedder 什么都不用写就满足它。
	embedder       Embedder
	maxUploadBytes int64
	log            *zap.Logger
}

// NewService 组装业务层。maxUploadBytes 来自配置，<=0 时会被兜底成 10 MiB，
// 避免调用方忘了传导致"任何文件都超限"。
//
// embedder 允许为 nil（未配置向量模型）。注意调用方传 nil 时要用"无类型 nil"：
// 把一个值为 nil 的具体指针装进接口，接口本身并不等于 nil，
// 那时 `s.embedder == nil` 会是 false，随后在被调用时 panic。
//
// log 允许为 nil（测试里常见）：此时退化成不记录。业务逻辑本身不依赖日志，
// 但"删库成功、删文件失败"这种只能记一笔的情况需要它，所以不做成可选参数
// 之外的另一套 API。
func NewService(repo documentRepository, store fileStore, tasks taskStore, embedder Embedder, maxUploadBytes int64, log *zap.Logger) *Service {
	if maxUploadBytes <= 0 {
		maxUploadBytes = 10 << 20
	}
	if log == nil {
		log = zap.NewNop()
	}
	return &Service{
		repo:           repo,
		store:          store,
		tasks:          tasks,
		embedder:       embedder,
		maxUploadBytes: maxUploadBytes,
		log:            log,
	}
}

// FileInput 是一次文件导入请求。
//
// Collection / Tags 属于"元信息"，与正文分开传：正文来自文件流，
// 元信息来自表单字段或查询参数，混在一起会让调用方以为标签是文件内容的一部分。
type FileInput struct {
	Filename   string
	Content    io.Reader
	Collection string
	Tags       []string
}

// NoteInput 是一次笔记导入请求。
type NoteInput struct {
	Title      string
	Content    string
	Collection string
	Tags       []string
}

// UpdateInput 是一次局部更新请求。nil 表示"这次不改这一项"，
// 传空串/空切片则表示"清空它"——这两种意图不能合并。
type UpdateInput struct {
	Title      *string
	Collection *string
	Tags       *[]string
}

// 任务类型，同时也是 outbox 的 topic（消费端按它路由）与 tasks.type。
//
// 定义在业务包而不是基础设施里：取值属于业务语义，
// 分发器只需要知道"有这么个字符串"，不该认识"文档"这种概念。
const (
	KindDocumentImport  = "document.import"
	KindDocumentReindex = "document.reindex"
	// KindDocumentEmbed 只重算向量，不重新切分。
	//
	// 为什么需要它：导入任务把切分与向量化绑在一起（一个事务、一条消息），
	// 但"换模型""补配置"这两种情况不该重切——重切会换掉全部分块 id，
	// 已经发生的引用与高亮会全部失效。所以另开一个只动向量的任务。
	KindDocumentEmbed = "document.embed"
)

// ImportResult 是一次"导入受理"的结果。
//
// 异步化以后，handler 能立刻告诉用户的只有"我收下了，消息编号是多少"，
// 真正的结果要等消费端跑完才能查。
type ImportResult struct {
	Document *Document
	// MessageID 是本次受理写入 outbox 的消息编号；Existed 为 true 时为空。
	//
	// 返回 message_id 而不是内部自增 id：它是跨进程稳定、消费端用来去重的标识，
	// 用户拿它去查日志能直接对上"这条消息经历过什么"。
	MessageID string
	// Existed 表示"同内容的文档已存在，本次没有写入任何新记录"。
	Existed bool
}

// StartFileImport 受理一次文件导入：校验 → 存原文 → 写待处理文档 + 任务。
//
// 幂等语义与同步版本一致：重复导入不会更新标题、标签或分块，改这些走 PATCH。
// 理由是"同内容 = 同一条记录"——如果第二次上传能改第一次的结果，这条记录
// 就不再由内容唯一决定，existed 也失去意义。
//
// 注意这里仍然会做一次 normalizeText：类型与编码错误属于"用户传错了"，
// 必须在请求里就报给用户，不能收了 202 再在后台失败。
func (s *Service) StartFileImport(ctx context.Context, ownerID int64, in FileInput) (*ImportResult, error) {
	mime, ok := MIMEByFilename(in.Filename)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedType, filepath.Ext(in.Filename))
	}

	data, err := readAllLimited(in.Content, s.maxUploadBytes)
	if err != nil {
		return nil, err
	}
	// 只做校验与去 BOM，不保留正文：切分是后台的事，这里多留一份没有意义。
	if _, data, err = normalizeText(data); err != nil {
		return nil, err
	}
	// 元信息在落盘之前校验：参数不合法时不该先往对象存储写一份再删掉。
	collection, tags, err := normalizeMetadata(in.Collection, in.Tags)
	if err != nil {
		return nil, err
	}

	// 先落盘再落库：对象存储不在数据库事务里，顺序反过来会出现
	// "事务已提交但原文没写成功"——那才是真正不可修复的坏状态。
	saved, err := s.store.Save(ctx, data)
	if err != nil {
		return nil, fmt.Errorf("save document file: %w", err)
	}

	doc := &Document{
		OwnerID:     ownerID,
		Title:       titleFromFilename(in.Filename),
		MIME:        mime,
		SizeBytes:   int64(len(data)),
		ContentHash: saved.Hash,
		SourceType:  SourceFile,
		StoragePath: &saved.RelPath,
		Collection:  collection,
		Tags:        tags,
		// 状态由 worker 推进：pending → ready / failed。
		Status: StatusPending,
	}
	return s.startImport(ctx, ownerID, doc, &saved)
}

// StartNoteImport 受理一条笔记。
//
// 笔记的正文存在 documents.content，同时也切分成分块：检索与引用走同一套逻辑，
// 不需要为笔记单独开一条查询路径。
//
// 笔记一律按 Markdown 解析：用户自己写的东西里出现 "# 小节" 是常见用法，
// 而纯文本文件里以 # 开头的行更可能是日志，所以两者规则不同。
func (s *Service) StartNoteImport(ctx context.Context, ownerID int64, in NoteInput) (*ImportResult, error) {
	if int64(len(in.Content)) > s.maxUploadBytes {
		return nil, ErrTooLarge
	}
	text, data, err := normalizeText([]byte(in.Content))
	if err != nil {
		return nil, err
	}
	collection, tags, err := normalizeMetadata(in.Collection, in.Tags)
	if err != nil {
		return nil, err
	}

	doc := &Document{
		OwnerID:     ownerID,
		Title:       titleForNote(in.Title, text),
		MIME:        MIMEMarkdown,
		SizeBytes:   int64(len(data)),
		ContentHash: platform.ContentHash(data),
		SourceType:  SourceNote,
		Content:     &text,
		Collection:  collection,
		Tags:        tags,
		Status:      StatusPending,
	}
	return s.startImport(ctx, ownerID, doc, nil)
}

// ProcessImport 是导入任务的执行段：读原文 → 切分 → 落分块 → 排向量化任务。
//
// 【为什么切分与向量化分成两段任务】
// 向量化要调外部模型，一次可能几十秒，绝不能放进数据库事务里（会占死连接）。
// 而"分块落库"与"要去做向量化"这两件事必须同时成立，于是后者写成
// 与分块同一个事务里的一行 outbox 记录——消息由 relay 异步搬运，MQ 不在
// 一致性链条上。这样切分永远不会因为模型抖动而白跑，向量化失败也只需重算向量。
//
// 三条约定（与 dispatch.Handler 的契约一致）：
//   - 响应 ctx：取消与停止靠它传播；
//   - 可重跑：FinishImport 是"先删后插"的一个事务，重复执行结果不变；
//   - 不碰任务状态：成功/失败由 worker 统一写回。
func (s *Service) ProcessImport(ctx context.Context, documentID int64) error {
	doc, err := s.repo.GetInternal(ctx, documentID)
	if err != nil {
		return err
	}
	text, err := s.sourceText(ctx, doc)
	if err != nil {
		return err
	}
	chunks, err := buildChunks(doc.MIME, text)
	if err != nil {
		return err
	}
	// 未配置模型时只落分块并标 skipped，导入照常成功。
	return s.repo.FinishImport(ctx, documentID, chunks, s.embedder != nil)
}

// MarkProcessingFailed 写文档的失败终态与可读原因，由消费端在"确定不再重试"时调用。
//
// 拆成独立方法而不是塞进 ProcessImport：只有消费端知道"还剩几次机会"，
// 业务层不该自己判断该不该收尾——那需要第二份重试配置，早晚两处不一致。
//
// 只用于切分/解析阶段的失败；向量化阶段的失败走 MarkEmbedFailed，
// 那条路不动 documents.status。
func (s *Service) MarkProcessingFailed(ctx context.Context, documentID int64, cause error) error {
	return s.repo.MarkFailed(ctx, documentID, ErrorCode(cause), cause.Error())
}

// IsPermanent 判断一个错误是否"再试也没用"。
//
// 共同点：同样的输入、同样的代码，重试几次得到的还是同一个结果。
// 让它们跑满重试次数只会延迟用户看到错误，并占住处理名额。
// 消费端据此提前收尾，不必等次数耗尽。
func IsPermanent(err error) bool {
	switch {
	case errors.Is(err, ErrUnsupportedType),
		errors.Is(err, ErrEmptyDocument),
		errors.Is(err, ErrNotUTF8),
		errors.Is(err, ErrInvalidMetadata),
		errors.Is(err, ErrSourceMissing),
		errors.Is(err, ErrNotFound),
		// 向量化里除了"暂时不可用"以外都是永久错误：Key 错、模型名错、
		// 维度不符、没有分块，再试几次得到的结果完全一样。
		// ErrStaleChunks 不在这里——它既不是永久失败，也不该重试，
		// 消费端会把它当成"结果作废、任务成功"单独处理。
		errors.Is(err, ErrEmbeddingNotConfigured),
		errors.Is(err, ErrEmbeddingAuth),
		errors.Is(err, ErrEmbeddingBadRequest),
		errors.Is(err, ErrEmbeddingDimensionMismatch),
		errors.Is(err, ErrNoChunks):
		return true
	default:
		return false
	}
}

// ErrorCode 把错误翻译成稳定的机器可读代码。
//
// 文档表与任务表共用它：两处记的必须是同一个码，
// 否则排查时要在两份分类之间猜。
func ErrorCode(err error) string { return errorCode(err) }

// 向量化相关的错误码。写进 documents.embed_error_code 与 tasks.error_code，
// 两处必须是同一个字符串——排查时不要在两份分类之间猜。
const (
	// EmbedNotConfiguredCode 表示"没配模型所以没做"，是合法状态而非故障。
	EmbedNotConfiguredCode = "not_configured"
	EmbedAuthCode          = "embedding_auth_failed"
	EmbedBadRequestCode    = "embedding_bad_request"
	EmbedDimensionCode     = "embedding_dimension_mismatch"
	EmbedUnavailableCode   = "embedding_unavailable"
	NoChunksCode           = "no_chunks"
	// EmbedStaleCode 只写进任务历史，不写文档：结果作废不是文档的错。
	EmbedStaleCode = "stale_chunks"
)

// errorCode 把错误翻译成稳定的机器可读代码，写进 documents.error_code。
//
// 用固定字符串而不是错误文案：文案会改、会翻译，前端与排查脚本不该跟着变。
func errorCode(err error) string {
	switch {
	case errors.Is(err, ErrUnsupportedType):
		return "unsupported_type"
	case errors.Is(err, ErrEmptyDocument):
		return "empty_document"
	case errors.Is(err, ErrNotUTF8):
		return "not_utf8"
	case errors.Is(err, ErrInvalidMetadata):
		return "invalid_metadata"
	case errors.Is(err, ErrSourceMissing):
		return "source_missing"
	case errors.Is(err, ErrStorageUnavailable):
		// 与 source_missing 分开：这是"存储暂时连不上"，重试或恢复 MinIO 后能好；
		// 前者是原文真的没了，需要用户重新导入。
		return "storage_unavailable"
	case errors.Is(err, ErrEmbeddingNotConfigured):
		return EmbedNotConfiguredCode
	case errors.Is(err, ErrEmbeddingAuth):
		return EmbedAuthCode
	case errors.Is(err, ErrEmbeddingBadRequest):
		return EmbedBadRequestCode
	case errors.Is(err, ErrEmbeddingDimensionMismatch):
		return EmbedDimensionCode
	case errors.Is(err, ErrEmbeddingUnavailable):
		return EmbedUnavailableCode
	case errors.Is(err, ErrNoChunks):
		return NoChunksCode
	case errors.Is(err, ErrStaleChunks):
		return EmbedStaleCode
	case errors.Is(err, ErrNotFound):
		return "document_not_found"
	default:
		return "process_failed"
	}
}

// startImport 写"待处理文档 + 任务"，并把"同内容已存在"翻译成 Existed。
//
// 三个分支的顺序有讲究：
//  1. 成功 → 新建；
//  2. 唯一索引冲突（ErrDuplicate）→ 读回既有记录，**不删文件**。
//     此时对象存储里的正是那条既有文档指向的原文（内容寻址下 key 由哈希决定），
//     删掉会让它变成"有记录没原文"，之后重索引就再也跑不起来；
//  3. 其他失败 → 补偿清理，且只在文件是本次新写的时候删（Reused=false）。
func (s *Service) startImport(ctx context.Context, ownerID int64, doc *Document, saved *platform.SavedFile) (*ImportResult, error) {
	// 不需要在这里叫醒任何人：消息与文档在同一个事务里写进 outbox，
	// relay 每 200ms 扫一次待发表就把它送出去了。
	// 这正是把 jobs 表换成 outbox 后省掉的一步——业务层不再持有队列客户端。
	messageID, err := s.repo.CreateWithTask(ctx, doc, KindDocumentImport)
	if err == nil {
		return &ImportResult{Document: doc, MessageID: messageID}, nil
	}
	if errors.Is(err, ErrDuplicate) {
		existing, getErr := s.repo.GetByContentHash(ctx, ownerID, doc.ContentHash)
		if getErr == nil {
			return &ImportResult{Document: existing, Existed: true}, nil
		}
		// 撞了唯一索引却读不回来，说明这条记录在冲突与读取之间被删了。
		// 这种情况极少见，退回冲突语义（409）比假装成功更诚实。
		return nil, err
	}
	if saved != nil && !saved.Reused {
		// 两个错误一起返回：清理失败不该掩盖真正的失败原因，
		// 但也不能悄悄吞掉（errors.Join 会保留两条链，日志里都看得到）。
		if rmErr := s.store.Remove(ctx, saved.RelPath); rmErr != nil {
			return nil, errors.Join(err, fmt.Errorf("cleanup stored file: %w", rmErr))
		}
	}
	return nil, err
}

// Get 读取文档详情。
func (s *Service) Get(ctx context.Context, ownerID, id int64) (*Document, error) {
	return s.repo.Get(ctx, ownerID, id)
}

// GetInternal 供异步链路按 id 读取文档，**不带 owner 条件**。
//
// 只允许 consumer 与取消收尾调用：那里的 id 来自 outbox 消息，是我们自己写的，
// 不是客户端能构造的。HTTP handler 用 Get(ctx, ownerID, id)。
func (s *Service) GetInternal(ctx context.Context, id int64) (*Document, error) {
	return s.repo.GetInternal(ctx, id)
}

// List 分页返回文档，同时给出总数供前端翻页。
//
// 总数与列表用同一个 filter，否则会出现"筛出 3 条、总数却写 10"，
// 前端按总数算出来的页数完全对不上。
func (s *Service) List(ctx context.Context, ownerID int64, filter DocumentFilter, limit, offset int) ([]Document, int64, error) {
	limit, offset = ClampPaging(limit, offset)
	docs, err := s.repo.List(ctx, ownerID, filter, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	total, err := s.repo.Count(ctx, ownerID, filter)
	if err != nil {
		return nil, 0, err
	}
	return docs, total, nil
}

// Update 修改文档的元信息（标题、分类、标签），不涉及正文与分块。
//
// 正文与它的分块、哈希是绑在一起的：改正文等于换一份文档，
// 那应该重新导入，而不是在这里悄悄改掉 content_hash 让幂等判定失效。
func (s *Service) Update(ctx context.Context, ownerID, id int64, in UpdateInput) (*Document, error) {
	patch := DocumentPatch{}
	if in.Title != nil {
		title := strings.TrimSpace(*in.Title)
		if title == "" {
			return nil, fmt.Errorf("%w: title must not be blank", ErrInvalidMetadata)
		}
		title = truncateRunes(title, 200)
		patch.Title = &title
	}
	if in.Collection != nil {
		collection := strings.TrimSpace(*in.Collection)
		if utf8.RuneCountInString(collection) > maxCollectionRunes {
			return nil, fmt.Errorf("%w: collection must be at most %d characters",
				ErrInvalidMetadata, maxCollectionRunes)
		}
		patch.Collection = &collection
	}
	if in.Tags != nil {
		tags, err := normalizeTags(*in.Tags)
		if err != nil {
			return nil, err
		}
		patch.Tags = &tags
	}
	if patch.Title == nil && patch.Collection == nil && patch.Tags == nil {
		return nil, fmt.Errorf("%w: at least one of title, collection or tags is required",
			ErrInvalidMetadata)
	}

	if err := s.repo.Update(ctx, ownerID, id, patch); err != nil {
		return nil, err
	}
	// 回读而不是在内存里拼一个：updated_at 由数据库写入，
	// 直接返回旧结构体会让前端看到"改成功了但时间没变"。
	return s.repo.Get(ctx, ownerID, id)
}

// ListChunks 返回某个文档的分块。
//
// 先读文档再读分块（而不是直接查 chunks）：文档不存在与"文档存在但没有分块"
// 是两种不同结果，前者要 404，后者是合法的空列表。
func (s *Service) ListChunks(ctx context.Context, ownerID, documentID int64, limit, offset int) ([]Chunk, int64, error) {
	if _, err := s.repo.Get(ctx, ownerID, documentID); err != nil {
		return nil, 0, err
	}
	limit, offset = ClampPaging(limit, offset)
	chunks, err := s.repo.ListChunks(ctx, ownerID, documentID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	total, err := s.repo.CountChunks(ctx, ownerID, documentID)
	if err != nil {
		return nil, 0, err
	}
	return chunks, total, nil
}

// Delete 删除文档：先删库，提交成功后再删原始文件。
//
// 顺序不能反。先删文件再删库，一旦删库失败，留下的是一条"记录在、原文没了"
// 的文档——它还能出现在列表里，但重索引永远失败；而先删库后删文件失败，
// 最坏结果只是一个没人引用的孤儿文件，不影响任何功能。
//
// 所以文件删除失败**不回滚、不返回错误**，只记一条 warn：此时用户看到的
// "文档已删除"是真的，回滚反而会让他以为没删掉。
//
// 多租户下还有第三个条件：原文按内容寻址、跨用户共享，所以删对象之前
// 要确认全库没有人还在引用它（见 StoragePathRefs）。删早了会让别人的文档
// 变成"有记录没原文"，那比留一个孤儿对象严重得多。
func (s *Service) Delete(ctx context.Context, ownerID, id int64) error {
	doc, err := s.repo.Get(ctx, ownerID, id)
	if err != nil {
		return err
	}
	if err := s.repo.Delete(ctx, ownerID, id); err != nil {
		return err
	}
	if doc.StoragePath == nil {
		return nil
	}
	// 必须在删除事务提交之后才问"还有人引用吗"。
	// 查询失败时**保留对象**：孤儿只是浪费空间，误删无法恢复。
	refs, err := s.repo.StoragePathRefs(ctx, *doc.StoragePath)
	if err != nil {
		s.log.Warn("count storage refs failed, keep the object",
			zap.Int64("document_id", id), zap.Error(err))
		return nil
	}
	if refs > 0 {
		return nil
	}
	if err := s.store.Remove(ctx, *doc.StoragePath); err != nil {
		s.log.Warn("remove document file failed",
			zap.Int64("document_id", id),
			zap.String("storage_path", *doc.StoragePath),
			zap.Error(err),
		)
	}
	return nil
}

// StartReindex 受理一次重索引：置 indexing + 建任务，返回 message_id。
//
// 与导入不同的是这里不检查原文是否可得：文件在 MinIO 里，读一次要几毫秒到几百毫秒，
// 放在请求里等于把外部依赖的延迟加到接口上。原文缺失由 worker 报 ErrSourceMissing，
// 任务落 failed 并把原因写进文档，用户看到的是"重新切分失败：原文已丢失"——
// 比在请求里 409 少一点即时性，但信息一样清楚，且状态是持久化的。
//
// 同一文档已有未完成的重索引任务时返回 ErrReindexRunning（409）：
// 重复点击不该排两条队，那只会让同一份文档被两个 worker 同时替换分块。
func (s *Service) StartReindex(ctx context.Context, ownerID, id int64) (*ImportResult, error) {
	doc, err := s.repo.Get(ctx, ownerID, id)
	if err != nil {
		return nil, err
	}
	messageID, err := s.repo.MarkIndexingWithTask(ctx, ownerID, id, KindDocumentReindex)
	if err != nil {
		if errors.Is(err, ErrDuplicate) {
			return nil, ErrReindexRunning
		}
		return nil, err
	}
	doc.Status = StatusIndexing
	return &ImportResult{Document: doc, MessageID: messageID}, nil
}

// ProcessReindex 是重索引任务的执行段：用当前的分块规则重新切分一份已有文档。
//
// 存在的意义是"规则会变"：切分粒度、标题识别、未来的语言相关处理都可能在
// 后续阶段调整，已经入库的文档不应该只能靠重新上传才能用上新规则
// （重新上传会被内容哈希的幂等挡住，根本不会重切）。
// 原文取自存储（文件）或 documents.content（笔记），不依赖客户端再传一次。
//
// 顺序是先算完、再一次事务替换：算的途中失败什么都不改，
// 替换的途中失败事务回滚，旧分块完整保留。
func (s *Service) ProcessReindex(ctx context.Context, id int64) error {
	doc, err := s.repo.GetInternal(ctx, id)
	if err != nil {
		return err
	}
	text, err := s.sourceText(ctx, doc)
	if err != nil {
		return err
	}
	chunks, err := buildChunks(doc.MIME, text)
	if err != nil {
		return err
	}
	// 与导入共用 FinishImport：换分块、置 ready、排向量化任务必须同进同退。
	return s.repo.FinishImport(ctx, id, chunks, s.embedder != nil)
}

// CancelProcessing 请求取消某文档进行中的处理。
//
// 取消是"请求"而不是"立即生效"：任务在跑时只能置成 cancelling，等消费端
// 在下一个检查点自己停下——它可能正卡在一次解析里，只有它知道何时能安全停。
// 文档状态的收尾分两种情况，取决于任务当时在什么状态：
//   - 还没被领走（CancelPending 成功）→ 立刻写终态：没有任何东西还在跑；
//   - 已经在跑 → 只标"取消中"，终态由消费端停下后写。
//     这里抢先写 failed 会与"处理其实刚好完成"互相覆盖——实测出现过
//     failed 但已有分块的半状态，所以收尾必须跟着任务状态的原子转移走。
//
// 一个文档可能同时有两条活跃任务（导入与重索引用不同的 type，唯一索引按 type 分段），
// 所以这里取消的是"全部可取消的任务"，而不是随手取最新那条。
//
// 向量化任务不在可取消之列：它只跑几秒，取消还要打断进行中的 HTTP 调用，
// 而重新点一次就是重算——取消它没有用户可见的收益。
func (s *Service) CancelProcessing(ctx context.Context, ownerID, id int64) (*Document, error) {
	if _, err := s.repo.Get(ctx, ownerID, id); err != nil {
		return nil, err
	}
	if s.tasks == nil {
		return nil, ErrNoProcessing
	}
	active, err := s.tasks.ListActive(ctx, task.TargetDocument, id)
	if err != nil {
		return nil, err
	}
	cancellable := make([]task.Task, 0, len(active))
	for _, t := range active {
		if t.Type != KindDocumentEmbed {
			cancellable = append(cancellable, t)
		}
	}
	if len(cancellable) == 0 {
		// 没有可取消的处理：可能已经完成、已经被取消，或只在上传向量。
		// 三种情况对调用方是同一件事——没有"正在处理"这件事可撤回。
		return nil, ErrNoProcessing
	}

	running := false
	for _, t := range cancellable {
		// 条件取消：只有仍然处于 pending 才抢得到。抢不到说明消费端刚领走。
		cancelled, err := s.tasks.CancelPending(ctx, t.ID)
		if err != nil {
			return nil, err
		}
		if !cancelled {
			running = true
			continue
		}
		if err := s.SettleCancelled(ctx, id, t.Type); err != nil {
			return nil, err
		}
	}

	// 已经在跑的：只能标记取消意图，消费端会在执行前与收尾时看到它。
	if running {
		if err := s.repo.MarkCancelling(ctx, id); err != nil {
			return nil, err
		}
	}
	return s.repo.Get(ctx, ownerID, id)
}

// SettleCancelled 写取消之后的业务终态。由 worker 在任务真正停下后回调，
// 也在"排队中直接取消"时被 CancelProcessing 调用。
//
// 终态取决于被取消的是哪种任务：
//   - 重索引取消 → 退回 ready：旧分块完好，文档本身没问题；
//   - 向量化取消 → 文档仍是 ready，只把向量状态写成 failed/cancelled；
//   - 导入取消 → 没有内容可用，标 failed。
func (s *Service) SettleCancelled(ctx context.Context, documentID int64, kind string) error {
	switch kind {
	case KindDocumentEmbed:
		return s.repo.MarkEmbedFailed(ctx, documentID, "cancelled", "向量化已取消")
	case KindDocumentReindex:
		return s.repo.MarkReady(ctx, documentID)
	}
	doc, err := s.repo.GetInternal(ctx, documentID)
	if err != nil {
		return err
	}
	if doc.ChunkCount > 0 {
		// 取消来晚了一步，内容其实已经写完：保留成果比标失败更诚实，
		// 否则会出现"失败但有分块"的半状态。这条也是取消与完成赛跑时的兜底。
		return s.repo.MarkReady(ctx, documentID)
	}
	return s.repo.MarkFailed(ctx, documentID, "cancelled", "处理已取消")
}

// sourceText 取出文档的原始正文，供重索引使用。
func (s *Service) sourceText(ctx context.Context, doc *Document) (string, error) {
	if doc.StoragePath != nil {
		data, err := s.store.Read(ctx, *doc.StoragePath)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return "", fmt.Errorf("%w: file %s is gone", ErrSourceMissing, *doc.StoragePath)
			}
			// 其余读失败（连不上、超时）是暂时故障，单独标记以便重试与给出准确错误码。
			return "", fmt.Errorf("%w: %w", ErrStorageUnavailable, err)
		}
		// 再走一次 normalizeText：原文可能带着 BOM，偏移必须以去 BOM 之后
		// 的字节为基准，否则重索引产出的偏移和导入时不一致。
		text, _, err := normalizeText(data)
		return text, err
	}
	if doc.Content != nil {
		text, _, err := normalizeText([]byte(*doc.Content))
		return text, err
	}
	return "", ErrSourceMissing
}

// 元信息边界。这些上限不是性能考虑，而是"让筛选有意义"：
// 几百个标签的分类法在界面上无法使用，也会让 ANY(tags) 的过滤结果失去区分度。
const (
	maxTagsPerDocument = 20
	maxTagRunes        = 32
	maxCollectionRunes = 64
)

// normalizeMetadata 校验并规整分类与标签，返回可直接入库的形态。
//
// 返回 *string 而不是 string：数据库里"未分类"是 NULL，
// 空字符串会让"未分类"出现两种表示，筛选时还要额外兼容。
func normalizeMetadata(collection string, tags []string) (*string, StringArray, error) {
	trimmed := strings.TrimSpace(collection)
	var collectionPtr *string
	if trimmed != "" {
		if utf8.RuneCountInString(trimmed) > maxCollectionRunes {
			return nil, nil, fmt.Errorf("%w: collection must be at most %d characters",
				ErrInvalidMetadata, maxCollectionRunes)
		}
		collectionPtr = &trimmed
	}
	normalized, err := normalizeTags(tags)
	if err != nil {
		return nil, nil, err
	}
	return collectionPtr, normalized, nil
}

// normalizeTags 去空白、丢空项、去重（保持输入顺序），并检查长度与数量。
//
// 不允许逗号：文件导入的标签是 `tags=a,b` 这种逗号分隔写法，
// 允许标签里带逗号就没有任何办法把它和分隔符区分开。
// 与其在解析上发明一套转义规则，不如直接拒绝——分类标签带逗号本来也很少见。
func normalizeTags(tags []string) (StringArray, error) {
	out := make(StringArray, 0, len(tags))
	seen := make(map[string]struct{}, len(tags))
	for _, raw := range tags {
		tag := strings.TrimSpace(raw)
		if tag == "" {
			continue
		}
		if strings.ContainsRune(tag, ',') {
			return nil, fmt.Errorf("%w: tag %q must not contain a comma", ErrInvalidMetadata, tag)
		}
		if utf8.RuneCountInString(tag) > maxTagRunes {
			return nil, fmt.Errorf("%w: tag %q is longer than %d characters",
				ErrInvalidMetadata, tag, maxTagRunes)
		}
		if _, dup := seen[tag]; dup {
			continue
		}
		seen[tag] = struct{}{}
		out = append(out, tag)
	}
	if len(out) > maxTagsPerDocument {
		return nil, fmt.Errorf("%w: at most %d tags are allowed", ErrInvalidMetadata, maxTagsPerDocument)
	}
	return out, nil
}

// utf8BOM 是文件开头的字节序标记。Windows 记事本保存的 UTF-8 文件常带它。
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// normalizeText 统一做三件事：去 BOM、校验编码、拒绝空内容。
//
// 返回值里的 data 是**去掉 BOM 之后**的字节：原始文件、内容哈希、正文偏移
// 三者都以它为基准，这样"偏移能切回原文"在重索引时依然成立。
func normalizeText(data []byte) (text string, cleaned []byte, err error) {
	data = bytes.TrimPrefix(data, utf8BOM)
	if !utf8.Valid(data) {
		return "", nil, ErrNotUTF8
	}
	text = string(data)
	if strings.TrimSpace(text) == "" {
		return "", nil, ErrEmptyDocument
	}
	return text, data, nil
}

// buildChunks 是所有来源共用的"解析 + 切分"入口。
func buildChunks(mime, text string) ([]Chunk, error) {
	blocks, err := Parse(mime, text)
	if err != nil {
		return nil, err
	}
	chunks := Split(text, blocks, DefaultChunkOptions)
	if len(chunks) == 0 {
		return nil, ErrEmptyDocument
	}
	return chunks, nil
}

// readAllLimited 读取最多 limit 字节，超出返回 ErrTooLarge。
//
// 多读 1 字节是为了区分"恰好等于上限"与"超过上限"：只读 limit 的话，
// 超限文件会被静默截断成合法文档，那是比直接报错更糟的行为。
func readAllLimited(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read document: %w", err)
	}
	if int64(len(data)) > limit {
		return nil, ErrTooLarge
	}
	return data, nil
}

// ClampPaging 把分页参数收进合法区间。
//
// 导出是因为 handler 需要回显"实际生效的 limit/offset"——请求 limit=1000 时
// 实际只返回 100 条，响应里还写 1000 会让前端算错总页数。
// 放在 service 而不是 handler：这是业务约束（一次最多看多少条），
// 换一个入口（worker、CLI）时也该生效。
func ClampPaging(limit, offset int) (int, int) {
	if limit <= 0 {
		limit = defaultPageSize
	}
	if limit > maxPageSize {
		limit = maxPageSize
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

// titleFromFilename 用文件名（去扩展名）做标题。
func titleFromFilename(name string) string {
	base := filepath.Base(name)
	title := strings.TrimSpace(strings.TrimSuffix(base, filepath.Ext(base)))
	if title == "" {
		return "未命名文档"
	}
	return truncateRunes(title, 200)
}

// titleForNote 优先用用户填的标题；没填就用正文第一行。
//
// 用第一行而不是"The story begins..."这种默认值：列表里一眼能认出是哪条笔记，
// 比"未命名笔记 3"有用得多。
func titleForNote(title, content string) string {
	if t := strings.TrimSpace(title); t != "" {
		return truncateRunes(t, 200)
	}
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(line, "#"))
		if line != "" {
			return truncateRunes(line, 60)
		}
	}
	return "未命名笔记"
}

// truncateRunes 按码点截断，避免把中文或 emoji 切成半个字符。
func truncateRunes(s string, limit int) string {
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	r := []rune(s)
	return string(r[:limit])
}
