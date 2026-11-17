// 本文件是知识模块的向量化业务：把分块换成向量，并把状态写回文档。
//
// 两条路径，共用同一套错误分类：
//
//	导入/重索引   切分之后立刻向量化，分块与向量在同一个事务里提交；
//	单独重建      只重算已有分块的向量（换模型、补配置、修失败），不重切。
//
// 为什么单独重建要独立成任务：重切会换掉全部分块 id，已经产生的引用、
// 高亮与排序都会失效；而换模型这件事跟"切分规则"没有关系。
package knowledge

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.uber.org/zap"

	"github.com/luo303/mindos/backend/internal/platform"
	"github.com/luo303/mindos/backend/internal/task"
)

// Embedder 是本模块需要的向量模型能力。
//
// 只声明三个方法：嵌入、模型名、维度。接口定在消费方（本包），
// platform.Embedder 什么都不用写就满足它；测试里塞一个假实现即可。
type Embedder interface {
	Embed(ctx context.Context, texts []string) (vectors [][]float32, tokens int64, err error)
	Model() string
	Dimensions() int
}

// EmbedResult 是一次"重建向量"受理的结果。
type EmbedResult struct {
	Document *Document
	// MessageID 是本次受理写入 outbox 的消息编号。
	MessageID string
}

// BackfillResult 是一次批量回填的统计。
//
// 三项分开而不是只返回"排了多少"：用户需要知道"还有多少没排"
// （一次没排完要不要再点），以及"跳过多少"（已经在排的重复项不算异常）。
type BackfillResult struct {
	Enqueued  int
	Skipped   int
	Remaining int64
}

// EmbeddingProbe 是一次连通性测试的结果。
type EmbeddingProbe struct {
	Model      string
	Dimensions int
	Latency    time.Duration
}

// 回填的边界。上限 200 是"一次点击最多排多少条任务"的护栏，
// 不是性能优化：一个文档一条任务，排太多只会把队列堵住，看不出进度。
const (
	defaultBackfillLimit = 50
	maxBackfillLimit     = 200
)

// ProcessEmbed 是 document.embed 任务的执行段：读分块 → 向量化 → 写回。
//
// 只动分块向量与 documents.embed_*，不碰 documents.status：
// 分块本来是好的，向量化失败不该让一份可读的文档变成"失败"。
func (s *Service) ProcessEmbed(ctx context.Context, documentID int64) error {
	if s.embedder == nil {
		return ErrEmbeddingNotConfigured
	}
	if _, err := s.repo.GetInternal(ctx, documentID); err != nil {
		return err
	}
	sources, err := s.repo.ListChunkSources(ctx, documentID)
	if err != nil {
		return err
	}
	if len(sources) == 0 {
		return ErrNoChunks
	}

	if err := s.repo.MarkEmbedRunning(ctx, documentID); err != nil {
		return err
	}
	texts := make([]string, len(sources))
	for i := range sources {
		texts[i] = sources[i].Content
	}
	vectors, tokens, err := s.embedder.Embed(ctx, texts)
	if err != nil {
		return mapEmbedError(err)
	}
	s.log.Info("embedding chunks",
		zap.Int64("document_id", documentID),
		zap.Int("chunks", len(sources)),
		zap.Int64("prompt_tokens", tokens),
		zap.String("model", s.embedder.Model()),
	)

	pairs := make([]ChunkVector, len(sources))
	for i := range sources {
		pairs[i] = ChunkVector{ID: sources[i].ID, Vector: vectors[i]}
	}
	err = s.repo.SaveEmbeddings(ctx, documentID, s.embedder.Model(), s.embedder.Dimensions(), pairs)
	if errors.Is(err, ErrStaleChunks) {
		// 结果作废不是失败：分块已经被重索引替换，新任务会重算。
		s.log.Info("embedding result discarded, chunks were replaced",
			zap.Int64("document_id", documentID))
	}
	return err
}

// StartEmbed 受理一次"重建向量"：置 pending + 排任务。
//
// 三种情况直接拒掉，因为它们都不是"排队等着"能解决的：
//   - 没配模型（503，让用户去配）；
//   - 文档还没处理完（409，等导入跑完再说）；
//   - 已经有向量化任务在排队或在跑（409，重复点击不该排两条）。
func (s *Service) StartEmbed(ctx context.Context, ownerID, id int64) (*EmbedResult, error) {
	if s.embedder == nil {
		return nil, ErrEmbeddingNotConfigured
	}
	doc, err := s.repo.Get(ctx, ownerID, id)
	if err != nil {
		return nil, err
	}
	if doc.Status != StatusReady {
		return nil, fmt.Errorf("%w: current status is %s", ErrNotReady, doc.Status)
	}
	if doc.ChunkCount == 0 {
		return nil, ErrNoChunks
	}
	if s.tasks != nil {
		active, err := s.tasks.ListActive(ctx, task.TargetDocument, id)
		if err != nil {
			return nil, err
		}
		for _, t := range active {
			if t.Type == KindDocumentEmbed {
				return nil, ErrEmbedRunning
			}
		}
	}

	messageID, err := s.repo.MarkEmbedPendingWithTask(ctx, ownerID, id, KindDocumentEmbed)
	if err != nil {
		// 并发下两次点击可能同时通过上面的检查，由 tasks_active_uniq 兜底。
		if errors.Is(err, ErrDuplicate) {
			return nil, ErrEmbedRunning
		}
		return nil, err
	}
	doc.EmbedStatus = EmbedStatusPending
	return &EmbedResult{Document: doc, MessageID: messageID}, nil
}

// StartEmbedBackfill 把"已就绪但没按当前模型向量化"的文档批量排进队列。
//
// 候选条件包含 embed_model 与当前模型不一致的文档：换模型以后，
// 状态虽然是 done，但那批向量属于旧模型，混在一起检索会得到无意义的结果。
func (s *Service) StartEmbedBackfill(ctx context.Context, ownerID int64, limit int) (BackfillResult, error) {
	if s.embedder == nil {
		return BackfillResult{}, ErrEmbeddingNotConfigured
	}
	if limit <= 0 {
		limit = defaultBackfillLimit
	}
	if limit > maxBackfillLimit {
		limit = maxBackfillLimit
	}

	ids, err := s.repo.ListEmbedCandidates(ctx, ownerID, s.embedder.Model(), limit)
	if err != nil {
		return BackfillResult{}, err
	}
	result := BackfillResult{}
	for _, id := range ids {
		if _, err := s.repo.MarkEmbedPendingWithTask(ctx, ownerID, id, KindDocumentEmbed); err != nil {
			if errors.Is(err, ErrDuplicate) {
				// 已经有活跃任务：批量操作里这不是异常，跳过即可。
				result.Skipped++
				continue
			}
			return result, err
		}
		result.Enqueued++
	}
	remaining, err := s.repo.CountEmbedCandidates(ctx, ownerID, s.embedder.Model())
	if err != nil {
		return result, err
	}
	result.Remaining = remaining
	return result, nil
}

// TestEmbedding 用一条固定短文本验证"Key 能用、模型存在、维度对得上"。
//
// 为什么不复用 /system/status：状态接口要快且可频繁轮询，不能每次都打外部模型。
// 成本只有一条短文本，但能一次性回答"配的到底能不能用"。
func (s *Service) TestEmbedding(ctx context.Context) (EmbeddingProbe, error) {
	if s.embedder == nil {
		return EmbeddingProbe{}, ErrEmbeddingNotConfigured
	}
	start := time.Now()
	vectors, _, err := s.embedder.Embed(ctx, []string{"mindos embedding probe"})
	if err != nil {
		return EmbeddingProbe{}, mapEmbedError(err)
	}
	return EmbeddingProbe{
		Model:      s.embedder.Model(),
		Dimensions: len(vectors[0]),
		Latency:    time.Since(start),
	}, nil
}

// MarkEmbedFailed 由消费端在"确定不再重试"时调用，只写向量状态与原因。
//
// 与 MarkProcessingFailed 的区别：那个会顺手把文档标成 failed，
// 而这里分块是好的——文档照样能读、能按关键词检索，只是语义检索用不了。
func (s *Service) MarkEmbedFailed(ctx context.Context, documentID int64, cause error) error {
	return s.repo.MarkEmbedFailed(ctx, documentID, ErrorCode(cause), cause.Error())
}

// mapEmbedError 把传输层错误翻成本模块的哨兵错误。
//
// 只保留一个业务哨兵，把原始原因用 %v 附在后面（不用 %w）：
// 否则错误链上会同时出现 platform 的哨兵与 knowledge 的哨兵，
// 上层判断"这是不是永久错误"时就得分清该看哪个。
func mapEmbedError(err error) error {
	// 用户取消 / 服务关闭：原样带出去，让它按取消处理，而不是"向量服务不可用"。
	// 超时不算取消——它就是"这次没等到"，重试是对的。
	if errors.Is(err, context.Canceled) {
		return err
	}
	switch {
	case errors.Is(err, platform.ErrEmbeddingUnauthorized):
		return fmt.Errorf("%w: %v", ErrEmbeddingAuth, err)
	case errors.Is(err, platform.ErrEmbeddingDimension):
		return fmt.Errorf("%w: %v", ErrEmbeddingDimensionMismatch, err)
	case errors.Is(err, platform.ErrEmbeddingRejected):
		return fmt.Errorf("%w: %v", ErrEmbeddingBadRequest, err)
	case errors.Is(err, platform.ErrEmbeddingUnavailable):
		return fmt.Errorf("%w: %v", ErrEmbeddingUnavailable, err)
	default:
		return fmt.Errorf("%w: %v", ErrEmbeddingUnavailable, err)
	}
}
