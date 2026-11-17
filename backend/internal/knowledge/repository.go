// 本文件是文档模块的数据访问层。
//
// 两条纪律：
//  1. 只用 GORM 的链式 API（`db.WithContext(ctx).Where(...)`），不混用泛型 API；
//  2. **显式列名**。SELECT * 会让以后新增的大列（embedding 1536 维 = 6KB/行）
//     被无意识地读进内存，列表接口返回 20 条就多搬 120KB。
package knowledge

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/pgvector/pgvector-go"
	"gorm.io/gorm"

	"github.com/luo303/mindos/backend/internal/outbox"
	"github.com/luo303/mindos/backend/internal/task"
)

// Repository 是 documents / document_chunks 的数据访问。
type Repository struct {
	db *gorm.DB
}

// NewRepository 创建仓储。db 由 cmd/server 注入，仓储内部不建连接。
func NewRepository(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

// documentColumns 是列表与详情读取的列。
//
// 不含 source_url：网页抓取已移出主线。error_* 必须带上——
// 缺了它详情页就读不到失败原因，用户只能去翻日志。
// 列名集中在这里，避免多个方法各自写一份字符串。
var documentColumns = []string{
	"id", "owner_id", "title", "source_type", "mime", "size_bytes", "content_hash",
	"storage_path", "content", "collection", "tags", "status", "chunk_count",
	"error_code", "error_message",
	// 向量化状态：列表与详情都要显示"这份文档能不能语义检索"，
	// 所以必须和文档一起读出来，不能让前端再查一次。
	"embed_status", "embed_model", "embed_dimensions", "embedded_chunks",
	"embed_error_code", "embed_error_message", "embedded_at",
	"created_at", "updated_at",
}

// chunkColumns 跳过 embedding 与 tsv：前者 6KB/行且本阶段不用，
// 后者是数据库生成的检索列，给前端看没有意义。
var chunkColumns = []string{
	"id", "document_id", "seq", "content", "heading",
	"start_offset", "end_offset", "token_count", "created_at",
}

// CreateWithChunks 在一个事务里写入文档与它的全部分块。
//
// 为什么必须同一事务：半途失败时"有文档但没有分块"的记录会让页面显示一个
// 空文档，用户无法判断是解析没跑还是内容真为空。事务保证要么都成功、要么都没有。
//
// 分块用 CreateInBatches 而不是逐条 Create：一次 INSERT 多行，把 N 次往返压成
// ceil(N/200) 次；200 是兼顾 SQL 长度与单条语句参数上限的经验值。
func (r *Repository) CreateWithChunks(ctx context.Context, doc *Document, chunks []Chunk) error {
	// chunk_count 是派生值。在这里统一赋值而不是让调用方自己算：
	// 它必须与本次写入的分块数严格一致，放在写入方才能保证不漏、不写错。
	doc.ChunkCount = len(chunks)

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(doc).Error; err != nil {
			return err
		}
		if len(chunks) == 0 {
			return nil
		}
		// 用下标回填而不是 range 取值：range 拿到的是副本，改它不会影响切片。
		for i := range chunks {
			chunks[i].DocumentID = doc.ID
		}
		return tx.CreateInBatches(chunks, 200).Error
	})
	return translateError("create document", err)
}

// CreateWithTask 在同一个事务里写入"待处理文档"与其任务、消息。
//
// 为什么要一个事务：异步化以后，导入被拆成"建记录 + 后台处理"两步。
// 如果先建文档再建任务，中间失败会留下一条永远 pending、又没人处理的记录；
// 反过来则是任务指向一个不存在的文档。两者都是需要人工介入的脏状态。
//
// 返回 message_id：受理响应要告诉前端"这条消息的编号"，它由 outbox 在插入时生成。
func (r *Repository) CreateWithTask(ctx context.Context, doc *Document, kind string) (string, error) {
	doc.ChunkCount = 0
	var messageID string
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(doc).Error; err != nil {
			return err
		}
		var err error
		messageID, err = appendTaskAndOutbox(ctx, tx, doc.ID, kind)
		return err
	})
	return messageID, translateError("create document with task", err)
}

// appendTaskAndOutbox 在一次受理里写两条记录，返回消息编号。
//
//	tasks   —— 进度真相（跨重启、要给页面查）
//	outbox  —— 投递通道（发完即弃，定时清理）
//
// 放在仓储而不是业务层的原因：task_id 与 document_id 都依赖自增主键，
// 必须插入之后才能回填进 outbox 的 payload，业务层拿不到这个中间态。
func appendTaskAndOutbox(ctx context.Context, tx *gorm.DB, documentID int64, kind string) (string, error) {
	t := task.New(kind, documentID)
	if err := task.AppendTx(ctx, tx, t); err != nil {
		return "", err
	}
	msg := outbox.New(kind, strconv.FormatInt(documentID, 10), map[string]any{
		"document_id": documentID,
		"task_id":     t.ID,
	})
	if err := outbox.AppendTx(ctx, tx, msg); err != nil {
		return "", err
	}
	return msg.MessageID, nil
}

// MarkIndexingWithJob 在同一个事务里把文档置为 indexing 并建一条重索引任务。
//
// 为什么要事务：只改状态不建任务，页面会永远停在"索引中"；只建任务不改状态，
// 用户看不出已经在排队。两件事要么一起成立，要么都不成立。
//
// 同一文档已有未完成的任务时，任务插入会撞部分唯一索引并返回 ErrDuplicate，
// 整个事务回滚——状态也不会被动过，保持原样。
func (r *Repository) MarkIndexingWithTask(ctx context.Context, ownerID, documentID int64, kind string) (string, error) {
	var messageID string
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&Document{}).Where("owner_id = ? AND id = ?", ownerID, documentID).
			Updates(map[string]any{
				"status":     StatusIndexing,
				"updated_at": time.Now().UTC(),
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		var err error
		messageID, err = appendTaskAndOutbox(ctx, tx, documentID, kind)
		return err
	})
	return messageID, translateError("mark indexing with task", err)
}

// MarkFailed 把文档标记为处理失败，并留下可解释的原因。
//
// 只在"任务最终失败"时调用（不可恢复错误，或重试次数用尽）：
// 一次失败就标红，会让还在重试的任务与界面上的"失败"互相矛盾。
//
// 切分阶段的失败与向量化阶段的失败分开写：向量化失败走 MarkEmbedFailed，
// 它不动 documents.status。这里只处理"内容都没能落库"的情况。
func (r *Repository) MarkFailed(ctx context.Context, documentID int64, code, message string) error {
	err := r.db.WithContext(ctx).Model(&Document{}).Where("id = ?", documentID).
		Updates(map[string]any{
			"status":        StatusFailed,
			"error_code":    code,
			"error_message": message,
			"updated_at":    time.Now().UTC(),
		}).Error
	return translateError("mark document failed", err)
}

// MarkCancelling 把文档置为 cancelling：取消请求已被受理，等 worker 停下后写终态。
//
// 与直接写 failed 的区别：任务可能还要一个心跳周期才真正停下，期间文档既不是
// 成功也不是失败。用"取消中"如实表达这个中间态，比先标失败再改回来诚实。
func (r *Repository) MarkCancelling(ctx context.Context, documentID int64) error {
	err := r.db.WithContext(ctx).Model(&Document{}).Where("id = ?", documentID).
		Updates(map[string]any{
			"status":     StatusCancelling,
			"updated_at": time.Now().UTC(),
		}).Error
	return translateError("mark document cancelling", err)
}

// MarkReady 把文档置为 ready（用于重索引被取消：旧分块完好，文档本身没问题）。
func (r *Repository) MarkReady(ctx context.Context, documentID int64) error {
	err := r.db.WithContext(ctx).Model(&Document{}).Where("id = ?", documentID).
		Updates(map[string]any{
			"status":        StatusReady,
			"error_code":    nil,
			"error_message": nil,
			"updated_at":    time.Now().UTC(),
		}).Error
	return translateError("mark document ready", err)
}

// Get 按"拥有者 + 主键"读取文档。
//
// owner 条件写死在 WHERE 里，不写成 `if ownerID > 0`：后者漏传时会退化成
// 按 id 全库查，这正是越权的来源。ownerID 为 0 时查不到任何行，是安全的失败方向。
func (r *Repository) Get(ctx context.Context, ownerID, id int64) (*Document, error) {
	var doc Document
	err := r.db.WithContext(ctx).
		Select(documentColumns).
		Where("owner_id = ? AND id = ?", ownerID, id).
		Take(&doc).Error
	if err != nil {
		return nil, translateError("get document", err)
	}
	return &doc, nil
}

// GetInternal 按主键读取文档，**不带 owner 条件**。
//
// 只允许异步链路调用：那里的 document_id 来自 outbox 消息（我们自己写的），
// 不是客户端传进来的。HTTP handler 一律用 Get(ctx, ownerID, id)。
func (r *Repository) GetInternal(ctx context.Context, id int64) (*Document, error) {
	var doc Document
	err := r.db.WithContext(ctx).
		Select(documentColumns).
		Where("id = ?", id).
		Take(&doc).Error
	if err != nil {
		return nil, translateError("get document internal", err)
	}
	return &doc, nil
}

// GetByContentHash 按内容哈希读取文档。
//
// 幂等导入靠它：写入撞上 content_hash 唯一索引后，用它把"已经存在的那一条"读回来。
// 不用"先查再插"是因为那在并发下有竞态（两个请求同时查不到，随后一个必然插入失败）；
// 而"先插、冲突再查"由数据库的唯一索引保证串行化——冲突方看到的必然是已提交的行。
func (r *Repository) GetByContentHash(ctx context.Context, ownerID int64, hash string) (*Document, error) {
	var doc Document
	err := r.db.WithContext(ctx).
		Select(documentColumns).
		Where("owner_id = ? AND content_hash = ?", ownerID, hash).
		Take(&doc).Error
	if err != nil {
		return nil, translateError("get document by content hash", err)
	}
	return &doc, nil
}

// DocumentFilter 是列表的筛选条件，零值表示不过滤。
//
// 用指针区分"没传"与"传了空串"：`?collection=` 在 HTTP 语义上等于没传，
// 而 PATCH 里 collection 为空串是"取消分类"。两者在这里都不能混为一谈。
type DocumentFilter struct {
	Collection *string
	Tag        *string
	Status     *string
}

// apply 把 owner 与筛选条件挂到查询上。列表与计数必须共用它，
// 否则很容易出现"列表 3 条、总数 10"这种翻页错乱的 bug。
//
// owner 条件放在最前面且**不参与任何 if**：它是隔离的唯一收口点，
// 一旦写成可选的，漏传就会变成"查全库"，而不是查不到。
func (f DocumentFilter) apply(ownerID int64, db *gorm.DB) *gorm.DB {
	db = db.Where("owner_id = ?", ownerID)
	if f.Collection != nil {
		db = db.Where("collection = ?", *f.Collection)
	}
	if f.Tag != nil {
		// ANY(tags) 走 documents_tags_idx（GIN），不需要额外的关联查询。
		db = db.Where("? = ANY(tags)", *f.Tag)
	}
	if f.Status != nil {
		db = db.Where("status = ?", *f.Status)
	}
	return db
}

// List 按创建时间倒序分页，只返回该拥有者的文档。
func (r *Repository) List(ctx context.Context, ownerID int64, filter DocumentFilter, limit, offset int) ([]Document, error) {
	docs := make([]Document, 0, limit)
	err := filter.apply(ownerID, r.db.WithContext(ctx)).
		Select(documentColumns).
		Order("created_at DESC, id DESC").
		Limit(limit).
		Offset(offset).
		Find(&docs).Error
	if err != nil {
		return nil, translateError("list documents", err)
	}
	return docs, nil
}

// Count 返回满足条件的文档总数，供分页使用。
func (r *Repository) Count(ctx context.Context, ownerID int64, filter DocumentFilter) (int64, error) {
	var total int64
	if err := filter.apply(ownerID, r.db.WithContext(ctx).Model(&Document{})).Count(&total).Error; err != nil {
		return 0, translateError("count documents", err)
	}
	return total, nil
}

// DocumentPatch 是局部更新的字段集合，nil 表示"这次不改这一项"。
//
// 这是 PATCH 与 PUT 的区别落地处：PATCH 只改传了的字段，
// 所以不能用结构体的零值表达"清空"——空串与 nil 必须是两种意思。
type DocumentPatch struct {
	Title      *string
	Collection *string
	Tags       *StringArray
}

// Update 局部更新文档元信息。
//
// updated_at 显式写入：用 map 更新时 GORM 不会自动维护时间戳
// （结构体更新才会），漏了它会出现"改了标签但排序没变"。
func (r *Repository) Update(ctx context.Context, ownerID, id int64, patch DocumentPatch) error {
	updates := map[string]any{"updated_at": time.Now().UTC()}
	if patch.Title != nil {
		updates["title"] = *patch.Title
	}
	if patch.Collection != nil {
		// 空串写 NULL 而不是空串：读取时"未分类"只应该有一种表示，
		// 否则筛选未分类要写 (collection IS NULL OR collection = '')。
		if strings.TrimSpace(*patch.Collection) == "" {
			updates["collection"] = nil
		} else {
			updates["collection"] = strings.TrimSpace(*patch.Collection)
		}
	}
	if patch.Tags != nil {
		updates["tags"] = *patch.Tags
	}

	res := r.db.WithContext(ctx).Model(&Document{}).
		Where("owner_id = ? AND id = ?", ownerID, id).Updates(updates)
	if res.Error != nil {
		return translateError("update document", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// ListChunks 按 seq 升序返回某个文档的分块。
func (r *Repository) ListChunks(ctx context.Context, ownerID, documentID int64, limit, offset int) ([]Chunk, error) {
	chunks := make([]Chunk, 0, limit)
	err := r.db.WithContext(ctx).
		Select(chunkColumns).
		Where("document_id = ?", documentID).
		Where("EXISTS (SELECT 1 FROM documents d WHERE d.id = document_chunks.document_id AND d.owner_id = ?)", ownerID).
		Order("seq ASC").
		Limit(limit).
		Offset(offset).
		Find(&chunks).Error
	if err != nil {
		return nil, translateError("list chunks", err)
	}
	return chunks, nil
}

// CountChunks 返回某个文档的分块数。
func (r *Repository) CountChunks(ctx context.Context, ownerID, documentID int64) (int64, error) {
	var total int64
	err := r.db.WithContext(ctx).Model(&Chunk{}).
		Where("document_id = ?", documentID).
		Where("EXISTS (SELECT 1 FROM documents d WHERE d.id = document_chunks.document_id AND d.owner_id = ?)", ownerID).
		Count(&total).Error
	if err != nil {
		return 0, translateError("count chunks", err)
	}
	return total, nil
}

// Delete 删除文档。分块由外键 ON DELETE CASCADE 一并删除，不需要在这里显式删。
//
// 返回受影响行数是否为 0 来判断"本来就没有这条"：这样并发下两次删除里
// 只有一次成功，另一次得到 ErrNotFound，而不是两边都以为删掉了。
func (r *Repository) Delete(ctx context.Context, ownerID, id int64) error {
	res := r.db.WithContext(ctx).Delete(&Document{}, "owner_id = ? AND id = ?", ownerID, id)
	if res.Error != nil {
		return translateError("delete document", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// StoragePathRefs 返回还有多少条文档引用同一个原文对象。
//
// 原文按内容寻址、跨用户共享（同一份文件只存一个对象），所以删除文档时
// 不能直接删对象：别人可能还在用。这里故意**不带 owner 条件**——
// 要问的是"全库还有没有人引用"，只看自己那一份会把别人的原文删掉。
//
// 调用时机必须在"删除事务提交之后"：在事务里检查，并发删除时两个事务
// 都可能看到对方尚未删除的行，结果是"都不删"（留下孤儿对象，安全）；
// 反过来提前删对象则会让别人的文档永久失去原文（不安全）。
func (r *Repository) StoragePathRefs(ctx context.Context, storagePath string) (int64, error) {
	var total int64
	err := r.db.WithContext(ctx).Model(&Document{}).
		Where("storage_path = ?", storagePath).
		Count(&total).Error
	if err != nil {
		return 0, translateError("count storage refs", err)
	}
	return total, nil
}

// ReplaceChunks 用新分块整体替换某个文档的分块，并同步 chunk_count。
//
// 必须在同一事务里"先删后插"：中途失败时如果旧分块已经删掉，文档就变成
// 一条没有任何分块的 ready 记录，检索不到内容也无法解释。
// 事务保证失败后旧数据完整保留，用户重试即可。
func (r *Repository) ReplaceChunks(ctx context.Context, documentID int64, chunks []Chunk) error {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return replaceChunks(tx, documentID, chunks)
	})
	return translateError("replace chunks", err)
}

// FinishImport 替换分块并把文档置为 ready；需要向量化时顺带排一条任务。
//
// 为什么必须同一个事务：分块写好了但状态还停在 parsing，界面会一直显示
// "处理中"；反过来状态先变 ready、分块还没写完，用户点进去看到的是空文档。
//
// 排任务也必须在同一事务里：否则会出现"分块已就绪，但没有任何东西会去算向量"
// ——文档永远停在"待向量化"，用户只能靠手动点一下才发现。
//
// 注意这里**不调模型**：外部调用不进事务（一次调用可能几十秒，会占死连接）。
// 切分与向量化因此分成两段任务，中间态由 embed_status 如实表达。
func (r *Repository) FinishImport(ctx context.Context, documentID int64, chunks []Chunk, enqueueEmbed bool) error {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := replaceChunks(tx, documentID, chunks); err != nil {
			return err
		}
		now := time.Now().UTC()
		updates := map[string]any{
			"status":        StatusReady,
			"error_code":    nil,
			"error_message": nil,
			"updated_at":    now,
		}
		if enqueueEmbed {
			// 先把同文档未完成的向量化任务作废，再排新的。
			// 不这样做，重索引完成时会撞 tasks_active_uniq，整笔事务回滚，
			// 用户看到的却是"重新切分失败"——真正原因是一条旧任务还排着队。
			if err := cancelEmbedTasks(tx, documentID); err != nil {
				return err
			}
			updates["embed_status"] = EmbedStatusPending
			updates["embed_model"] = nil
			updates["embed_dimensions"] = nil
			updates["embedded_chunks"] = 0
			updates["embedded_at"] = nil
			updates["embed_error_code"] = nil
			updates["embed_error_message"] = nil
			if _, err := appendTaskAndOutbox(ctx, tx, documentID, KindDocumentEmbed); err != nil {
				return err
			}
		} else {
			// 未配置模型：不排任务，如实标 skipped（与 pending 区分：
			// 前者要用户去配 Key，后者只要等）。
			updates["embed_status"] = EmbedStatusSkipped
			updates["embed_model"] = nil
			updates["embed_dimensions"] = nil
			updates["embedded_chunks"] = 0
			updates["embedded_at"] = nil
			updates["embed_error_code"] = EmbedNotConfiguredCode
			updates["embed_error_message"] = "未配置向量模型，本次只做切分"
		}
		return tx.Model(&Document{}).Where("id = ?", documentID).Updates(updates).Error
	})
	return translateError("finish import", err)
}

// cancelEmbedTasks 把该文档未完成的向量化任务标成"被取代"。
//
// 用 superseded 而不是 cancelled：它不是用户取消的，而是"分块换了，
// 这次的结果已经没有意义"。两种语义分开，排查历史时才看得懂。
func cancelEmbedTasks(tx *gorm.DB, documentID int64) error {
	now := time.Now().UTC()
	return tx.Exec(
		`UPDATE tasks
		    SET status = 'cancelled', error_code = 'superseded',
		        error_message = '分块已更新，重新向量化',
		        updated_at = ?, finished_at = ?
		  WHERE type = ? AND target_type = ? AND target_id = ?
		    AND status IN ('pending', 'running')`,
		now, now, KindDocumentEmbed, "document", documentID).Error
}

// ChunkSource 是重算向量需要的最小信息：只要 id 与正文。
type ChunkSource struct {
	ID      int64  `gorm:"column:id"`
	Seq     int    `gorm:"column:seq"`
	Content string `gorm:"column:content"`
}

// ListChunkSources 按 seq 升序返回分块的 id 与正文，不读 embedding 与 tsv。
func (r *Repository) ListChunkSources(ctx context.Context, documentID int64) ([]ChunkSource, error) {
	rows := make([]ChunkSource, 0, 32)
	err := r.db.WithContext(ctx).Model(&Chunk{}).
		Select("id", "seq", "content").
		Where("document_id = ?", documentID).
		Order("seq ASC").
		Find(&rows).Error
	if err != nil {
		return nil, translateError("list chunk sources", err)
	}
	return rows, nil
}

// ChunkVector 是一条待写回的分块向量。
type ChunkVector struct {
	ID     int64
	Vector []float32
}

// SaveEmbeddings 用一批向量重写某个文档的全部分块向量，并把文档置为 done。
//
// 新鲜度闸门：只要有一行没命中（`RowsAffected == 0`），说明读到的那批分块
// 已经被重索引整体替换或文档被删了，整笔事务回滚并返回 ErrStaleChunks，
// 绝不去写 documents.embed_status。
//
// 为什么"命中 0 行"就是可靠信号：分块 id 是 bigserial，重索引是
// "删全部 + 插新的"，新旧 id 绝不复用。所以开始时读到的 id 集合，
// 提交时要么全在、要么全不在，不存在"部分还在"。
// 少了这道闸门就会出现最难查的一种脏数据：重索引刚写完新分块，
// 上一轮向量化慢一拍收尾，把状态刷成 done，而新分块一条向量都没有。
func (r *Repository) SaveEmbeddings(ctx context.Context, documentID int64, model string, dimensions int, vectors []ChunkVector) error {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		for _, v := range vectors {
			if len(v.Vector) != EmbeddingDimensions {
				return fmt.Errorf("knowledge: chunk %d has %d dimensions, expected %d",
					v.ID, len(v.Vector), EmbeddingDimensions)
			}
			res := tx.Exec(
				`UPDATE document_chunks
				    SET embedding = CAST(? AS vector), embed_model = ?, embedded_at = ?
				  WHERE id = ? AND document_id = ?`,
				pgvector.NewVector(v.Vector).String(), model, now, v.ID, documentID)
			if res.Error != nil {
				return fmt.Errorf("update vector for chunk %d: %w", v.ID, res.Error)
			}
			if res.RowsAffected == 0 {
				return ErrStaleChunks
			}
		}
		return tx.Model(&Document{}).Where("id = ?", documentID).
			Updates(map[string]any{
				"embed_status":        EmbedStatusDone,
				"embed_model":         model,
				"embed_dimensions":    dimensions,
				"embedded_chunks":     len(vectors),
				"embedded_at":         now,
				"embed_error_code":    nil,
				"embed_error_message": nil,
				"updated_at":          now,
			}).Error
	})
	return translateError("save embeddings", err)
}

// MarkEmbedRunning 把文档的向量化状态置为进行中，并清掉上一次的错误。
func (r *Repository) MarkEmbedRunning(ctx context.Context, documentID int64) error {
	err := r.db.WithContext(ctx).Model(&Document{}).Where("id = ?", documentID).
		Updates(map[string]any{
			"embed_status":        EmbedStatusRunning,
			"embed_error_code":    nil,
			"embed_error_message": nil,
			"updated_at":          time.Now().UTC(),
		}).Error
	return translateError("mark embed running", err)
}

// MarkEmbedFailed 只写向量化失败，不动 documents.status。
//
// 这是与 MarkFailed 的关键区别：分块是好的，文档照样能读、能按关键词检索，
// 只是语义检索暂时用不了。把它标成整篇失败，会让用户以为内容坏了。
func (r *Repository) MarkEmbedFailed(ctx context.Context, documentID int64, code, message string) error {
	err := r.db.WithContext(ctx).Model(&Document{}).Where("id = ?", documentID).
		Updates(map[string]any{
			"embed_status":        EmbedStatusFailed,
			"embed_error_code":    code,
			"embed_error_message": message,
			"updated_at":          time.Now().UTC(),
		}).Error
	return translateError("mark embed failed", err)
}

// MarkEmbedPendingWithTask 把文档置为"待向量化"并排一条任务，两件事同一事务。
//
// 单独一个事务的原因：只改状态不建任务，页面会永远停在"待向量化"；
// 只建任务不改状态，用户看不出已经在排队。
func (r *Repository) MarkEmbedPendingWithTask(ctx context.Context, ownerID, documentID int64, kind string) (string, error) {
	var messageID string
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&Document{}).Where("owner_id = ? AND id = ?", ownerID, documentID).
			Updates(map[string]any{
				"embed_status": EmbedStatusPending,
				"updated_at":   time.Now().UTC(),
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		var err error
		messageID, err = appendTaskAndOutbox(ctx, tx, documentID, kind)
		return err
	})
	return messageID, translateError("mark embed pending with task", err)
}

// embedCandidate 是"还没按当前模型向量化"的筛选条件，列表与计数共用，
// 否则会出现"排了 50 条、remaining 却显示 49"这种对不上的账。
func embedCandidate(db *gorm.DB, ownerID int64, model string) *gorm.DB {
	return db.Where("owner_id = ?", ownerID).
		Where("status = ?", StatusReady).
		Where("chunk_count > 0").
		// 已 done 但模型不同的也属于候选：换模型后那批向量属于旧模型，
		// 与新查询向量不在同一个空间里，混着用检索结果没有意义。
		Where("(embed_status <> ? OR embed_model IS DISTINCT FROM ?)", EmbedStatusDone, model)
}

// ListEmbedCandidates 返回最多 limit 个待向量化的文档 id，按 id 升序（先来先排）。
func (r *Repository) ListEmbedCandidates(ctx context.Context, ownerID int64, model string, limit int) ([]int64, error) {
	ids := make([]int64, 0, limit)
	err := embedCandidate(r.db.WithContext(ctx).Model(&Document{}), ownerID, model).
		Order("id ASC").
		Limit(limit).
		Pluck("id", &ids).Error
	if err != nil {
		return nil, translateError("list embed candidates", err)
	}
	return ids, nil
}

// CountEmbedCandidates 返回剩余候选数，供回填报告 remaining。
func (r *Repository) CountEmbedCandidates(ctx context.Context, ownerID int64, model string) (int64, error) {
	var total int64
	err := embedCandidate(r.db.WithContext(ctx).Model(&Document{}), ownerID, model).Count(&total).Error
	if err != nil {
		return 0, translateError("count embed candidates", err)
	}
	return total, nil
}

// replaceChunks 是分块替换的实际实现，接受 tx 以便与状态更新共用事务。
func replaceChunks(tx *gorm.DB, documentID int64, chunks []Chunk) error {
	// 不检查存在性的话，"删 0 行 + 插 0 行"也会提交成功，
	// 调用方会误以为重索引跑过了。
	var exists int64
	if err := tx.Model(&Document{}).Where("id = ?", documentID).Count(&exists).Error; err != nil {
		return err
	}
	if exists == 0 {
		return gorm.ErrRecordNotFound
	}
	if err := tx.Where("document_id = ?", documentID).Delete(&Chunk{}).Error; err != nil {
		return err
	}
	for i := range chunks {
		chunks[i].DocumentID = documentID
	}
	if len(chunks) > 0 {
		if err := tx.CreateInBatches(chunks, 200).Error; err != nil {
			return err
		}
	}
	// 用 map 而不是结构体更新：结构体会跳过零值字段，
	// 而"新结果恰好是 0 块"是必须真的写进去的值。
	return tx.Model(&Document{}).Where("id = ?", documentID).
		Updates(map[string]any{
			"chunk_count": len(chunks),
			"updated_at":  time.Now().UTC(),
		}).Error
}

// translateError 把 GORM 的错误翻译成模块内的哨兵错误。
//
// TranslateError 已在 platform.NewPostgres 的 gorm.Config 里打开，
// 所以唯一约束冲突会得到 gorm.ErrDuplicatedKey，不需要去解析 PG 的 23505 字符串。
// 其余错误保留原始信息（用 %w 包住），方便日志定位具体是哪条 SQL 失败。
func translateError(op string, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return ErrNotFound
	case errors.Is(err, gorm.ErrDuplicatedKey):
		return ErrDuplicate
	default:
		return fmt.Errorf("knowledge: %s: %w", op, err)
	}
}
