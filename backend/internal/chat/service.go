// 本文件是问答的编排层：检索 → 阈值闸门 → 编号 → 跑 agent → 裁决 → 落库。
//
// 它不认识 gin，也不认识 Eino：事件通过 Emitter 出去，模型通过 agent.Runner 进来。
// 这样整个编排（包括"什么时候拒答""引用算不算合法"）可以在没有任何
// HTTP 与模型的情况下测完。
package chat

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"go.uber.org/zap"

	"github.com/luo303/mindos/backend/internal/agent"
	"github.com/luo303/mindos/backend/internal/chatmodel"
	"github.com/luo303/mindos/backend/internal/knowledge"
)

const (
	// maxQuestionRunes 与前端输入限制一致。
	maxQuestionRunes = 500

	// defaultTimeout 覆盖模型与工具调用的整段时间。
	defaultTimeout = 60 * time.Second

	// snippetRunes 是 retrieval 事件里给前端看的前缀长度。
	// 只给个开头让人判断"是不是这篇"，正文仍要点进去看。
	snippetRunes = 120
)

// refusalAnswer 是"库里确实没有依据"时返回给用户的固定文案。
//
// 与 ungrounded 共用同一句：对用户来说两者都是"没答上来"，
// 区别只在 qa_runs.verdict 里——那是给我们自己看的。
const refusalAnswer = "根据现有知识库无法回答该问题。可以换个问法，或者先把相关资料导入知识库。"

// 模块级哨兵错误，handler 用 errors.Is 判断后翻成 HTTP 错误码。
var (
	// ErrEmptyQuestion 表示问题为空。
	ErrEmptyQuestion = errors.New("question is empty")
	// ErrQuestionTooLong 表示问题超过 maxQuestionRunes。
	ErrQuestionTooLong = errors.New("question is too long")
)

// Settings 是问答的可调参数。
//
// 检索条数不在这里：它由 RAG_TOP_K 交给 agent.Builder 作为工具上限，
// 因为"查几条"现在是模型在工具调用里决定的。阈值已随预检索一起删除。
type Settings struct {
	// Timeout 覆盖模型与工具调用的整段时间。
	Timeout time.Duration
}

func (s Settings) normalized() Settings {
	if s.Timeout <= 0 {
		s.Timeout = defaultTimeout
	}
	return s
}

// ModelResolver 是对话模型配置的读取能力。*chatmodel.Service 满足它。
type ModelResolver interface {
	Resolve(ctx context.Context, ownerID int64, id *int64) (*chatmodel.Resolved, error)
	Touch(ctx context.Context, ownerID, id int64) error
}

// AnswererBuilder 按配置构造一次问答。*agent.Builder 满足它。
//
// onHits 是工具检索后的回调：检索已移进 agent，chat 不再预检索，
// 但"本次依据"仍要推给前端，所以由 builder 把它转交给工具。
type AnswererBuilder interface {
	Build(ctx context.Context, ownerID int64, target *chatmodel.Resolved, registry *agent.Registry, onHits func([]agent.Numbered)) (agent.Runner, error)
}

// Emitter 是事件出口：handler 实现它写 SSE，测试实现它收集事件序列。
//
// 每个方法返回 error 的意义是"客户端还在不在"。返回错误时编排层会停下——
// 用户已经关掉页面，继续算下去只是白烧 token。
type Emitter interface {
	Retrieval(items []RetrievalItem) error
	Delta(text string) error
	Verdict(status, reason string) error
	Citation(items []CitationItem) error
	Done(payload DonePayload) error
	Error(code, message string, retryable bool) error
}

// RetrievalItem 是"本次依据"面板里的一条。
type RetrievalItem struct {
	N          int     `json:"n"`
	DocumentID int64   `json:"document_id"`
	ChunkID    int64   `json:"chunk_id"`
	Title      string  `json:"title"`
	Seq        int     `json:"seq"`
	Similarity float64 `json:"similarity"`
	RRFScore   float64 `json:"rrf_score"`
	Snippet    string  `json:"snippet"`
}

// CitationItem 是答案真正引用到的一条，带跳回原文所需的一切。
type CitationItem struct {
	N           int     `json:"n"`
	DocumentID  int64   `json:"document_id"`
	ChunkID     int64   `json:"chunk_id"`
	Title       string  `json:"title"`
	Seq         int     `json:"seq"`
	StartOffset int     `json:"start_offset"`
	EndOffset   int     `json:"end_offset"`
	Similarity  float64 `json:"similarity"`
}

// DonePayload 是收尾事件的内容。
type DonePayload struct {
	RunID            int64 `json:"run_id"`
	ChatModelID      int64 `json:"chat_model_id"`
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	LatencyMS        int64 `json:"latency_ms"`
}

// AskInput 是一次问答的输入。
//
// 没有 limit：检索条数由模型在工具调用里决定，上限来自 RAG_TOP_K。
type AskInput struct {
	Question    string
	ChatModelID *int64
}

// RunStore 是写入运行记录的能力。*Repository 满足它。
//
// 抽成接口而不是直接用 *Repository：编排逻辑（尤其"失败也要落一行"）
// 需要一个记录调用的替身，否则每个用例都得起一个数据库。
type RunStore interface {
	Create(ctx context.Context, run *Run) error
}

// Service 编排一次问答。
type Service struct {
	models   ModelResolver
	runs     RunStore
	builder  AnswererBuilder
	settings Settings
	log      *zap.Logger
}

// NewService 组装编排层。依赖全部由 cmd/server 注入。
func NewService(models ModelResolver, runs RunStore, builder AnswererBuilder, settings Settings, log *zap.Logger) *Service {
	if log == nil {
		log = zap.NewNop()
	}
	return &Service{
		models:   models,
		runs:     runs,
		builder:  builder,
		settings: settings,
		log:      log,
	}
}

// Ask 跑一次问答并向 emit 推送事件。
//
// 返回 error 只表示"还没开始推送就失败了"（handler 据此返回 JSON 错误）；
// 一旦第一个事件发出去，后续失败都通过 Error 事件表达并返回 nil——
// 那时 HTTP 状态码已经定死，再返回 error 也没人能用了。
func (s *Service) Ask(ctx context.Context, ownerID int64, in AskInput, emit Emitter) error {
	start := time.Now()

	question := strings.TrimSpace(in.Question)
	if question == "" {
		return ErrEmptyQuestion
	}
	if utf8.RuneCountInString(question) > maxQuestionRunes {
		return ErrQuestionTooLong
	}

	settings := s.settings.normalized()
	runCtx, cancel := context.WithTimeout(ctx, settings.Timeout)
	defer cancel()

	// 1. 选模型。这一步在任何事件之前，失败可以让 handler 返回 503/400。
	resolved, err := s.models.Resolve(runCtx, ownerID, in.ChatModelID)
	if err != nil {
		return err
	}

	// 2. 本次 run 的编号表。检索已经移进 agent，这里只负责收集工具返回的片段；
	//    模型可能检索 0 次或多次，编号随每次工具调用增长。
	registry := agent.NewRegistry()

	// 3. 构造执行体。onHits 在每次工具检索并登记编号后把"本次依据"推给前端。
	answerer, err := s.builder.Build(runCtx, ownerID, resolved, registry, func(hits []agent.Numbered) {
		if err := emit.Retrieval(retrievalPayload(hits)); err != nil {
			// 客户端可能已经断开；真正的停止由 onDelta 返回错误负责。
			s.log.Warn("emit retrieval failed", zap.Error(err))
		}
	})
	if err != nil {
		return err
	}

	// 4. 流式跑。问题原样交给模型：要不要检索、检索几次由它决定。
	var answer strings.Builder
	result, runErr := answerer.Ask(runCtx, question, func(delta string) error {
		answer.WriteString(delta)
		return emit.Delta(delta)
	})
	if runErr != nil {
		return s.finishFailed(ctx, ownerID, emit, start, question, resolved, answer.String(), runErr)
	}

	// 5. 引用只做映射：正文里出现、且这次真的登记过的编号。
	//    不做校验、不撤回——这里没有 judge。
	citations := citationsFromAnswer(result.Answer, registry)
	finalAnswer := result.Answer
	if strings.TrimSpace(finalAnswer) == "" {
		finalAnswer = refusalAnswer
	}

	_ = emit.Verdict(VerdictAnswered, "")
	if len(citations) > 0 {
		_ = emit.Citation(citations)
	}

	// 6. 落库。用 WithoutCancel：客户端断开时 request ctx 已取消，
	// 但这次问答已经算完了，记录不该因此丢掉。
	persistCtx, persistCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer persistCancel()

	run := &Run{
		OwnerID:          ownerID,
		Question:         question,
		Answer:           finalAnswer,
		RawAnswer:        stringPtr(result.Answer),
		Verdict:          VerdictAnswered,
		Citations:        marshalJSON(citations),
		Retrieved:        marshalJSON(retrievedPayload(registry.All(), result.SQLAudits)),
		ChatModelID:      int64Ptr(resolved.ID),
		Model:            stringPtr(resolved.Model),
		PromptTokens:     result.PromptTokens,
		CompletionTokens: result.CompletionTokens,
		LatencyMS:        int(time.Since(start).Milliseconds()),
	}
	runID := s.saveRun(persistCtx, run)

	_ = emit.Done(DonePayload{
		RunID:            runID,
		ChatModelID:      resolved.ID,
		PromptTokens:     result.PromptTokens,
		CompletionTokens: result.CompletionTokens,
		LatencyMS:        int64(run.LatencyMS),
	})

	// 最近使用时间失败只记 warn：它影响的是一个展示字段，不该让一次
	// 已经完成的问答返回错误。
	if err := s.models.Touch(persistCtx, ownerID, resolved.ID); err != nil {
		s.log.Warn("touch chat model failed", zap.Int64("chat_model_id", resolved.ID), zap.Error(err))
	}
	return nil
}

// finishFailed 处理执行期失败：落一行 failed，并把可读原因推给前端。
//
// 上游原始错误只进日志，不进响应：它可能带 URL、账号片段或厂商的内部描述。
func (s *Service) finishFailed(ctx context.Context, ownerID int64, emit Emitter, start time.Time, question string, resolved *chatmodel.Resolved, partial string, runErr error) error {
	code, message, retryable := classifyRunError(runErr)
	s.log.Warn("agent run failed",
		zap.Int64("chat_model_id", resolved.ID),
		zap.String("error_code", code),
		zap.Error(runErr),
	)

	persistCtx, persistCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer persistCancel()

	run := &Run{
		OwnerID:       ownerID,
		Question:      question,
		Answer:        partial,
		RawAnswer:     stringPtr(partial),
		Verdict:       VerdictFailed,
		VerdictReason: stringPtr(code),
		Citations:     marshalJSON(nil),
		Retrieved:     marshalJSON(nil),
		ChatModelID:   int64Ptr(resolved.ID),
		Model:         stringPtr(resolved.Model),
		LatencyMS:     int(time.Since(start).Milliseconds()),
		ErrorCode:     stringPtr(code),
	}
	runID := s.saveRun(persistCtx, run)

	_ = emit.Error(code, message, retryable)
	_ = emit.Done(DonePayload{RunID: runID, ChatModelID: resolved.ID, LatencyMS: int64(run.LatencyMS)})
	return nil
}

func (s *Service) saveRun(ctx context.Context, run *Run) int64 {
	if err := s.runs.Create(ctx, run); err != nil {
		// 记账失败不影响已经算好的回答：为一条日志把成功的问答报成失败是本末倒置。
		s.log.Warn("save qa run failed", zap.Error(err))
		return 0
	}
	return run.ID
}

// citationPattern 匹配正文里的 [n]。
//
// 只认 1~3 位数字：分块数量不可能上万，把 \d+ 放开反而会误匹配
// 代码里的 [12345] 之类的下标。
var citationPattern = regexp.MustCompile(`\[(\d{1,3})\]`)

// citationsFromAnswer 把正文里的 [n] 映射成 citation，**只做展示，不做校验**。
//
// 没有编号、编号不存在、引用越界都不影响回答本身：能映射的发出去，
// 映射不到的直接跳过（正文里会原样显示成 [9] 这样的文本）。
// 这就是"去掉 judge"之后的引用语义。
func citationsFromAnswer(answer string, registry *agent.Registry) []CitationItem {
	refs := parseCitations(answer)
	out := make([]CitationItem, 0, len(refs))
	for _, n := range refs {
		hit, ok := registry.Get(n)
		if !ok {
			continue
		}
		out = append(out, CitationItem{
			N:           n,
			DocumentID:  hit.DocumentID,
			ChunkID:     hit.ChunkID,
			Title:       hit.Title,
			Seq:         hit.Seq,
			StartOffset: hit.StartOffset,
			EndOffset:   hit.EndOffset,
			Similarity:  hit.Similarity,
		})
	}
	return out
}

// parseCitations 提取正文里出现过的编号，去重并按数字升序。
//
// 排序是为了让引用列表稳定：模型可能先写 [3] 再写 [1]，
// 界面上的顺序每次都变会让人以为内容变了。
func parseCitations(answer string) []int {
	matches := citationPattern.FindAllStringSubmatch(answer, -1)
	seen := make(map[int]struct{}, len(matches))
	out := make([]int, 0, len(matches))
	for _, m := range matches {
		n, err := strconv.Atoi(m[1])
		if err != nil || n <= 0 {
			continue
		}
		if _, ok := seen[n]; ok {
			continue
		}
		seen[n] = struct{}{}
		out = append(out, n)
	}
	sort.Ints(out)
	return out
}

// retrievedPayload 把"检索到的片段"与"SQL 审计"合成一个数组落库。
//
// 检索条目沿用原来的结构（旧行也是这个形状）；SQL 条目带 kind:"sql"，
// 只有审计信息，没有 SQL 原文或结果。前端不读这个字段，它只服务复盘。
func retrievedPayload(items []agent.Numbered, audits []agent.SQLAudit) []any {
	out := make([]any, 0, len(items)+len(audits))
	for _, item := range retrievalPayload(items) {
		out = append(out, item)
	}
	for _, audit := range audits {
		out = append(out, audit)
	}
	return out
}

// retrievalPayload 生成"本次依据"的载荷。
//
// 只接受带编号的片段：检索已经发生在工具里，所有命中在回调前都登记过编号。
func retrievalPayload(items []agent.Numbered) []RetrievalItem {
	out := make([]RetrievalItem, 0, len(items))
	for _, hit := range items {
		out = append(out, RetrievalItem{
			N:          hit.N,
			DocumentID: hit.DocumentID,
			ChunkID:    hit.ChunkID,
			Title:      hit.Title,
			Seq:        hit.Seq,
			Similarity: hit.Similarity,
			RRFScore:   hit.RRFScore,
			Snippet:    truncateRunes(hit.Content, snippetRunes),
		})
	}
	return out
}

// classifyRunError 把执行期错误翻成稳定的错误码 + 面向用户的一句话。
//
// retryable 决定界面给不给"重试"按钮：超时与上游抖动值得重试，
// 预算超限与主动取消重试一万次也一样。
func classifyRunError(err error) (code, message string, retryable bool) {
	switch {
	case errors.Is(err, context.Canceled):
		return ReasonCancelled, "已停止生成", false
	case errors.Is(err, context.DeadlineExceeded):
		return ReasonTimeout, "回答超时了，可以重试，或把问题拆小一些", true
	case errors.Is(err, agent.ErrBudgetExceeded):
		return ReasonBudget, "本次问答超出预算，已停止", false
	case errors.Is(err, knowledge.ErrEmbeddingNotConfigured):
		// 检索现在发生在工具里，没配向量模型会在 run 中途暴露：
		// 不能像以前那样返回 503，但至少给出可操作的原因。
		return "embedding_not_configured", "向量模型未配置，请先在 .env 配置 EMBEDDING_*", false
	default:
		return ReasonUpstreamError, "模型服务暂时不可用，请稍后重试", true
	}
}

func truncateRunes(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit]) + "…"
}

func stringPtr(s string) *string { return &s }

func int64Ptr(v int64) *int64 { return &v }

// marshalJSON 把载荷序列化成 jsonb 列的内容。
//
// nil 走 '[]'：两列都是 jsonb NOT NULL，写 null 会直接违反约束。
func marshalJSON(v any) RawJSON {
	if v == nil {
		return RawJSON("[]")
	}
	data, err := json.Marshal(v)
	if err != nil {
		// 这里的类型都是我们自己定义的、可序列化的结构，失败说明代码写错了。
		return RawJSON("[]")
	}
	return RawJSON(data)
}
