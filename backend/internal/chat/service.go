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
	"fmt"
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

	// defaultTopK 是没配置时的兜底条数。
	defaultTopK = 8
	// maxTopK 是上限：再多只会挤掉对话与推理空间，不会让答案更好。
	maxTopK = 20
	// defaultTimeout 覆盖检索 + 模型 + 工具调用。
	defaultTimeout = 60 * time.Second

	// snippetRunes 是 retrieval 事件里给前端看的前缀长度。
	// 只给个开头让人判断"是不是这篇"，正文仍要点进去看。
	snippetRunes = 120
	// promptSnippetRunes 是拼进 prompt 的单条上限，与分块粒度对齐。
	promptSnippetRunes = 600
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

// Settings 是问答的三项可调参数（对应 RAG_* 配置）。
type Settings struct {
	TopK          int
	MinSimilarity float64
	Timeout       time.Duration
}

func (s Settings) normalized() Settings {
	if s.TopK <= 0 {
		s.TopK = defaultTopK
	}
	if s.TopK > maxTopK {
		s.TopK = maxTopK
	}
	// 阈值不做范围纠正：0 是合法值（永不拒答），负值说明配置写错了，
	// 但把它悄悄改成默认值会让人以为配置生效了。
	if s.Timeout <= 0 {
		s.Timeout = defaultTimeout
	}
	return s
}

// Searcher 是问答需要的检索能力。*knowledge.Searcher 满足它。
//
// 这里用 knowledge 的类型而不是自己复制一份：chat 与 knowledge 同属业务层，
// 复制一份 SearchResult 只会多一处"字段改了另一处忘了"的风险。
type Searcher interface {
	Search(ctx context.Context, query string, limit int, filter knowledge.DocumentFilter) (*knowledge.SearchResult, error)
}

// ModelResolver 是对话模型配置的读取能力。*chatmodel.Service 满足它。
type ModelResolver interface {
	Resolve(ctx context.Context, id *int64) (*chatmodel.Resolved, error)
	Touch(ctx context.Context, id int64) error
}

// AnswererBuilder 按配置构造一次问答。*agent.Builder 满足它。
type AnswererBuilder interface {
	Build(ctx context.Context, target *chatmodel.Resolved, registry *agent.Registry) (agent.Runner, error)
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
type AskInput struct {
	Question    string
	ChatModelID *int64
	Limit       int
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
	searcher Searcher
	models   ModelResolver
	runs     RunStore
	builder  AnswererBuilder
	settings Settings
	log      *zap.Logger
}

// NewService 组装编排层。依赖全部由 cmd/server 注入。
func NewService(searcher Searcher, models ModelResolver, runs RunStore, builder AnswererBuilder, settings Settings, log *zap.Logger) *Service {
	if log == nil {
		log = zap.NewNop()
	}
	return &Service{
		searcher: searcher,
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
func (s *Service) Ask(ctx context.Context, in AskInput, emit Emitter) error {
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
	resolved, err := s.models.Resolve(runCtx, in.ChatModelID)
	if err != nil {
		return err
	}

	limit := in.Limit
	if limit <= 0 || limit > settings.TopK {
		limit = settings.TopK
	}

	// 2. 宿主的预检索。它不是 tool：阈值闸门必须在这一步判断，
	// 而"要不要调模型"这个问题只能在调模型之前问。
	searchResult, err := s.searcher.Search(runCtx, question, limit, knowledge.DocumentFilter{})
	if err != nil {
		return err
	}

	// 3. 阈值闸门。没依据就不调模型——成本为 0，编造概率为 0。
	// 注意判据是向量路的 max similarity，不是 RRF 分数：后者只跟名次有关，
	// 拿它比阈值会得到一个恒真或恒假的条件。
	if len(searchResult.Hits) == 0 || searchResult.MaxSimilarity < settings.MinSimilarity {
		// 两种情况的 reason 相同：对上层与用户来说都是"没有依据"。
		// 区分"一条没检索到"与"检索到了但不够像"只对调参有用，那是日志的事。
		return s.finishRefused(ctx, emit, start, question, resolved, searchResult, ReasonNoEvidence)
	}

	// 4. 编号并推送"本次依据"。
	registry := agent.NewRegistry()
	numbered := registry.Register(toAgentHits(searchResult.Hits))
	retrievalItems := retrievalPayload(searchResult.Hits, numbered)
	if err := emit.Retrieval(retrievalItems); err != nil {
		return err
	}

	// 5. 构造问答执行体。到这里还没成功推送过正文，失败仍算"流未开始"。
	answerer, err := s.builder.Build(runCtx, resolved, registry)
	if err != nil {
		return err
	}

	// 6. 流式跑。正文边收边推，同时留一份完整副本用于裁决与落库。
	var answer strings.Builder
	result, runErr := answerer.Ask(runCtx, buildPrompt(question, numbered), func(delta string) error {
		answer.WriteString(delta)
		return emit.Delta(delta)
	})
	if runErr != nil {
		return s.finishFailed(ctx, emit, start, question, resolved, answer.String(), runErr)
	}

	// 7. 裁决。必须等完整输出——引用校验没有"流式"版本。
	verdict, reason, citations := judge(result.Answer, registry)
	finalAnswer := result.Answer
	if verdict == VerdictUngrounded {
		// 正文撤回，换成拒答文案。原文仍在 raw_answer 里，供事后分析。
		finalAnswer = refusalAnswer
	}
	if strings.TrimSpace(finalAnswer) == "" {
		finalAnswer = refusalAnswer
	}

	// verdict 一定先于 citation：裁决没出来之前，没有任何引用是可信的。
	_ = emit.Verdict(verdict, reason)
	if verdict == VerdictAnswered {
		_ = emit.Citation(citations)
	}

	// 8. 落库。用 WithoutCancel：客户端断开时 request ctx 已取消，
	// 但这次问答已经算完了，记录不该因此丢掉。
	persistCtx, persistCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer persistCancel()

	run := &Run{
		Question:         question,
		Answer:           finalAnswer,
		RawAnswer:        stringPtr(result.Answer),
		Verdict:          verdict,
		VerdictReason:    stringPtr(reason),
		Citations:        marshalJSON(citations),
		Retrieved:        marshalJSON(retrievalItems),
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
	if err := s.models.Touch(persistCtx, resolved.ID); err != nil {
		s.log.Warn("touch chat model failed", zap.Int64("chat_model_id", resolved.ID), zap.Error(err))
	}
	return nil
}

// finishRefused 处理"不调模型直接拒答"，并落一行 refused 记录。
//
// 也要发 retrieval（空数组）：让界面能显示"检索过了，没有命中"，
// 而不是让用户面对一个凭空出现的拒答。
func (s *Service) finishRefused(ctx context.Context, emit Emitter, start time.Time, question string, resolved *chatmodel.Resolved, searchResult *knowledge.SearchResult, reason string) error {
	retrieved := retrievalPayload(searchResult.Hits, nil)
	// 发空数组而不是真实片段：相似度 0.2 的片段给用户看会被误当成"依据"。
	_ = emit.Retrieval([]RetrievalItem{})
	_ = emit.Verdict(VerdictRefused, reason)

	persistCtx, persistCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer persistCancel()

	run := &Run{
		Question:      question,
		Answer:        refusalAnswer,
		Verdict:       VerdictRefused,
		VerdictReason: stringPtr(reason),
		Citations:     marshalJSON(nil),
		Retrieved:     marshalJSON(retrieved),
		ChatModelID:   int64Ptr(resolved.ID),
		Model:         stringPtr(resolved.Model),
		LatencyMS:     int(time.Since(start).Milliseconds()),
	}
	runID := s.saveRun(persistCtx, run)

	_ = emit.Done(DonePayload{RunID: runID, ChatModelID: resolved.ID, LatencyMS: int64(run.LatencyMS)})
	return nil
}

// finishFailed 处理执行期失败：落一行 failed，并把可读原因推给前端。
//
// 上游原始错误只进日志，不进响应：它可能带 URL、账号片段或厂商的内部描述。
func (s *Service) finishFailed(ctx context.Context, emit Emitter, start time.Time, question string, resolved *chatmodel.Resolved, partial string, runErr error) error {
	code, message, retryable := classifyRunError(runErr)
	s.log.Warn("agent run failed",
		zap.Int64("chat_model_id", resolved.ID),
		zap.String("error_code", code),
		zap.Error(runErr),
	)

	persistCtx, persistCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer persistCancel()

	run := &Run{
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

// judge 是真正起作用的第二道闸。
//
// 规则顺序有意义：
//   - 没有引用但命中拒答句 → refused（知识库里确实没有，正常结果）
//   - 没有引用也不是拒答句 → ungrounded（模型没给依据，是系统缺陷）
//   - 有引用但越界 → ungrounded（编造引用，结构上被拦住）
//
// 先看拒答句再判 ungrounded，是为了不让"模型老实说不知道"被记成缺陷。
func judge(answer string, registry *agent.Registry) (string, string, []CitationItem) {
	trimmed := strings.TrimSpace(answer)
	refs := parseCitations(answer)

	if len(refs) == 0 {
		if strings.Contains(trimmed, agent.RefusalSentence) {
			return VerdictRefused, ReasonModelRefused, nil
		}
		return VerdictUngrounded, ReasonNoCitation, nil
	}

	for _, n := range refs {
		if _, ok := registry.Get(n); !ok {
			return VerdictUngrounded, ReasonUnknownCitation, nil
		}
	}

	citations := make([]CitationItem, 0, len(refs))
	for _, n := range refs {
		hit, _ := registry.Get(n)
		citations = append(citations, CitationItem{
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
	return VerdictAnswered, "", citations
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

// buildPrompt 把预检索到的片段拼进用户消息。
//
// 为什么放在 user 而不是 system：system 里是规则，正文是本次的数据，
// 混在一起会让模型把"某篇文档里的一句话"当成规则来遵守。
func buildPrompt(question string, hits []agent.Numbered) string {
	var b strings.Builder
	b.WriteString("<context>\n")
	for _, hit := range hits {
		// Seq 是 0 基的段落序号，展示时用 1 基：人对"第 0 段"没有直觉。
		fmt.Fprintf(&b, "[%d] 文档《%s》第 %d 段（相似度 %.2f）\n%s\n\n",
			hit.N, hit.Title, hit.Seq+1, hit.Similarity, truncateRunes(hit.Content, promptSnippetRunes))
	}
	b.WriteString("</context>\n\n问题：")
	b.WriteString(question)
	return b.String()
}

func toAgentHits(hits []knowledge.Hit) []agent.Hit {
	out := make([]agent.Hit, 0, len(hits))
	for _, hit := range hits {
		out = append(out, agent.Hit{
			DocumentID:  hit.DocumentID,
			ChunkID:     hit.ChunkID,
			Seq:         hit.Seq,
			Title:       hit.Title,
			Heading:     hit.Heading,
			Content:     hit.Content,
			StartOffset: hit.StartOffset,
			EndOffset:   hit.EndOffset,
			Similarity:  hit.Similarity,
		})
	}
	return out
}

// retrievalPayload 生成"本次依据"的载荷。
//
// numbered 为 nil 时表示这些片段没有编号（拒答路径）：编号填 0，
// 前端据 n>0 判断是否可点。
func retrievalPayload(hits []knowledge.Hit, numbered []agent.Numbered) []RetrievalItem {
	items := make([]RetrievalItem, 0, len(hits))
	if len(numbered) > 0 {
		// RRF 分数不在 agent.Hit 里（它只对排序有意义），但接口要如实给出，
		// 否则前端会看到一个恒为 0 的字段，误以为融合没生效。
		rrf := make(map[int64]float64, len(hits))
		for _, hit := range hits {
			rrf[hit.ChunkID] = hit.RRFScore
		}
		for _, hit := range numbered {
			items = append(items, RetrievalItem{
				N:          hit.N,
				DocumentID: hit.DocumentID,
				ChunkID:    hit.ChunkID,
				Title:      hit.Title,
				Seq:        hit.Seq,
				Similarity: hit.Similarity,
				RRFScore:   rrf[hit.ChunkID],
				Snippet:    truncateRunes(hit.Content, snippetRunes),
			})
		}
		return items
	}
	for _, hit := range hits {
		items = append(items, RetrievalItem{
			DocumentID: hit.DocumentID,
			ChunkID:    hit.ChunkID,
			Title:      hit.Title,
			Seq:        hit.Seq,
			Similarity: hit.Similarity,
			RRFScore:   hit.RRFScore,
			Snippet:    truncateRunes(hit.Content, snippetRunes),
		})
	}
	return items
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
