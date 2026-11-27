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

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/luo303/mindos/backend/internal/agent"
	"github.com/luo303/mindos/backend/internal/approval"
	"github.com/luo303/mindos/backend/internal/chatmodel"
	"github.com/luo303/mindos/backend/internal/knowledge"
	"github.com/luo303/mindos/backend/internal/memory"
	"github.com/luo303/mindos/backend/internal/middleware"
	"github.com/luo303/mindos/backend/internal/todo"
)

const (
	// maxQuestionRunes 与前端输入限制一致。
	maxQuestionRunes = 500

	// snippetRunes 是 retrieval 事件里给前端看的前缀长度。
	// 只给个开头让人判断"是不是这篇"，正文仍要点进去看。
	snippetRunes = 120
)

// refusalAnswer 是"库里确实没有依据"时返回给用户的固定文案。
//
// 与 ungrounded 共用同一句：对用户来说两者都是"没答上来"，
// 区别只在 qa_runs.verdict 里——那是给我们自己看的。
const refusalAnswer = "根据现有知识库无法回答该问题。可以换个问法，或者先把相关资料导入知识库。"

// stoppedAnswer 是用户主动停止且还没产生任何正文时的占位。
// 它按正常 assistant 消息落库，刷新后不会变成失败卡片。
const stoppedAnswer = "已停止生成。"

// 模块级哨兵错误，handler 用 errors.Is 判断后翻成 HTTP 错误码。
var (
	// ErrEmptyQuestion 表示问题为空。
	ErrEmptyQuestion = errors.New("question is empty")
	// ErrQuestionTooLong 表示问题超过 maxQuestionRunes。
	ErrQuestionTooLong = errors.New("question is too long")
	// ErrApprovalPending 表示会话还有待审批写操作；审批完成前不能继续对话。
	ErrApprovalPending = errors.New("conversation has a pending approval")
)

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
	// 会话模式才有；一次性 /ask 时为零值，前端按 omitempty 忽略。
	ConversationID     int64 `json:"conversation_id,omitempty"`
	UserMessageID      int64 `json:"user_message_id,omitempty"`
	AssistantMessageID int64 `json:"assistant_message_id,omitempty"`
	// AwaitingApproval 为 true 时本轮停在 checkpoint，等待审批。
	AwaitingApproval bool  `json:"awaiting_approval,omitempty"`
	ApprovalID       int64 `json:"approval_id,omitempty"`
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
	models      ModelResolver
	runs        RunStore
	builder     AnswererBuilder
	convs       *ConversationRepository
	memory      *ConversationMemory
	approvals   *approval.Service
	todos       *todo.Service
	log         *zap.Logger
	preferences *memory.Service
}

// WithPreferences 在启动装配时接入长期偏好；必须在启用 HTTP 和消费者前调用。
func (s *Service) WithPreferences(preferences *memory.Service) *Service {
	s.preferences = preferences
	return s
}

// ReadMemorySource 仅供后台提炼读取同账号同会话内的用户原文。
func (s *Service) ReadMemorySource(ctx context.Context, ownerID, conversationID, messageID int64) (string, time.Time, error) {
	source, err := s.convs.memorySource(ctx, ownerID, conversationID, messageID)
	return source.Content, source.CreatedAt, err
}

// NewService 组装编排层。依赖全部由 cmd/server 注入；memory 允许为 nil。
func NewService(models ModelResolver, runs RunStore, builder AnswererBuilder, convs *ConversationRepository, memory *ConversationMemory, approvals *approval.Service, todos *todo.Service, log *zap.Logger) *Service {
	if log == nil {
		log = zap.NewNop()
	}
	return &Service{
		models:    models,
		runs:      runs,
		builder:   builder,
		convs:     convs,
		memory:    memory,
		approvals: approvals,
		todos:     todos,
		log:       log,
	}
}

// historyLimit 是热层带进模型的最大消息条数。
// 多轮的第一阶段还没有摘要/冷层，先只做"最近 N 条"。
const historyLimit = 16

// Ask 跑一次单轮问答：不读历史，也不写 messages。
func (s *Service) Ask(ctx context.Context, ownerID int64, in AskInput, emit Emitter) error {
	if err := validateQuestion(in.Question); err != nil {
		return err
	}
	resolved, err := s.models.Resolve(ctx, ownerID, in.ChatModelID)
	if err != nil {
		return err
	}
	return s.execute(ctx, ownerID, in, resolved, nil, nil, 0, emit)
}

// AskInConversation 在指定会话里跑一轮：先落 user 消息，再带最近 historyLimit 条历史。
func (s *Service) AskInConversation(ctx context.Context, ownerID, conversationID int64, in AskInput, emit Emitter) error {
	if err := validateQuestion(in.Question); err != nil {
		return err
	}
	if s.convs == nil {
		return ErrConversationNotFound
	}
	// 先确认会话属于当前用户：不存在/越权都返回同一个 404。
	if _, err := s.convs.Get(ctx, ownerID, conversationID); err != nil {
		return err
	}
	if s.approvals != nil {
		pending, err := s.approvals.HasPending(ctx, ownerID, conversationID)
		if err != nil {
			return err
		}
		if pending {
			return ErrApprovalPending
		}
	}
	resolved, err := s.models.Resolve(ctx, ownerID, in.ChatModelID)
	if err != nil {
		return err
	}

	var extractModelID int64
	if s.preferences != nil {
		extractModelID = resolved.ID
	}
	userMsg, err := s.convs.appendMessage(ctx, ownerID, conversationID, RoleUser, strings.TrimSpace(in.Question), RawJSON("[]"), nil, nil, extractModelID)
	if err != nil {
		return err
	}

	// 热层保留最近 historyLimit 条；冷层只看比热层更早的 seq。
	hotStart := userMsg.Seq - historyLimit
	if hotStart < 1 {
		hotStart = 1
	}
	history := make([]agent.HistoryMessage, 0, historyLimit+1)
	if msgs, err := s.convs.MessagesBefore(ctx, ownerID, conversationID, userMsg.Seq, historyLimit); err != nil {
		// 历史读失败不该让这一轮直接失败：至少还能回答当前问题。
		s.log.Warn("load conversation history failed", zap.Int64("conversation_id", conversationID), zap.Error(err))
	} else {
		for _, msg := range msgs {
			history = append(history, agent.HistoryMessage{Role: msg.Role, Content: msg.Content})
		}
	}
	if s.memory != nil {
		if block := s.memory.Context(ctx, ownerID, conversationID, hotStart, strings.TrimSpace(in.Question)); block != "" {
			history = append([]agent.HistoryMessage{{Role: "system", Content: block}}, history...)
		}
	}
	if s.preferences != nil {
		memoryGuidance := "长期偏好须由用户在记忆建议或记忆页保存才生效。不要在聊天中声称已经保存，也不要保证会出现建议。"
		if defaults, prefErr := s.preferences.Context(ctx, ownerID); prefErr != nil {
			s.log.Warn("load long-term preferences failed", zap.Error(prefErr))
		} else if defaults != "" {
			memoryGuidance += "\n" + defaults
		}
		history = append([]agent.HistoryMessage{{Role: "system", Content: memoryGuidance}}, history...)
	}

	return s.execute(ctx, ownerID, in, resolved, history, &conversationID, userMsg.ID, emit)
}

// SummarizeConversation 执行一次滚动摘要任务。
//
// 认领在短事务里完成；模型调用在事务外；提交前再次校验认领，
// 防止两个 worker 同时写同一版本。
func (s *Service) SummarizeConversation(ctx context.Context, ownerID, conversationID int64) error {
	claim, err := s.convs.ClaimSummary(ctx, ownerID, conversationID)
	if err != nil {
		return err
	}
	if claim == nil {
		return nil
	}
	target, err := s.models.Resolve(ctx, ownerID, nil)
	if err != nil {
		_ = s.convs.ReleaseSummary(ctx, conversationID, int64(claim.BatchToSeq))
		s.log.Warn("summary model unavailable", zap.Int64("conversation_id", conversationID), zap.Error(err))
		return nil
	}
	messages := make([]agent.SummaryMessage, 0, len(claim.Messages))
	for _, msg := range claim.Messages {
		messages = append(messages, agent.SummaryMessage{Role: msg.Role, Content: msg.Content})
	}
	summary, err := agent.Summarize(ctx, target, claim.Previous, messages)
	if err != nil {
		_ = s.convs.ReleaseSummary(ctx, conversationID, int64(claim.BatchToSeq))
		return err
	}
	if err := s.convs.CommitSummary(ctx, claim, summary, estimateTokens(summary), target.Model); err != nil {
		_ = s.convs.ReleaseSummary(ctx, conversationID, int64(claim.BatchToSeq))
		return err
	}
	return nil
}

// validateQuestion 是单轮与多轮共用的输入校验。
func validateQuestion(raw string) error {
	question := strings.TrimSpace(raw)
	if question == "" {
		return ErrEmptyQuestion
	}
	if utf8.RuneCountInString(question) > maxQuestionRunes {
		return ErrQuestionTooLong
	}
	return nil
}

// execute 是两种入口共用的执行体。
//
// 单轮传 conversationID=nil；多轮传会话 id 与已落库的 user 消息 id，
// 成功后会把 assistant 消息也写进 messages。
func (s *Service) execute(
	ctx context.Context, ownerID int64, in AskInput, resolved *chatmodel.Resolved,
	history []agent.HistoryMessage, conversationID *int64, userMessageID int64, emit Emitter,
) error {
	start := time.Now()
	question := strings.TrimSpace(in.Question)

	// 不设整轮 wall-clock 超时：长回答/多步工具链由单次外部调用超时
	// 与 token/工具预算约束；到达超时时，正在流式的回答不应被整轮计时器截断。
	runCtx := ctx

	// 每次 run 一个 checkpoint：写工具中断后，审批 API 用它恢复。
	checkpointID := uuid.NewString()
	// 写工具需要知道当前会话/用户消息/模型；通过 context 传，不改 Runner 接口。
	meta := agent.RunMeta{OwnerID: ownerID, CheckpointID: checkpointID, UserMessageID: userMessageID, ChatModelID: resolved.ID}
	if conversationID != nil {
		meta.ConversationID = *conversationID
	}
	runCtx = agent.WithRunMeta(runCtx, meta)

	// 本次 run 的编号表。检索已经移进 agent，这里只负责收集工具返回的片段。
	registry := agent.NewRegistry()

	// onHits 在每次工具检索并登记编号后把"本次依据"推给前端。
	answerer, err := s.builder.Build(runCtx, ownerID, resolved, registry, func(hits []agent.Numbered) {
		if err := emit.Retrieval(retrievalPayload(hits)); err != nil {
			// 客户端可能已经断开；真正的停止由 onDelta 返回错误负责。
			s.log.Warn("emit retrieval failed", zap.Error(err))
		}
	})
	if err != nil {
		return err
	}

	// 历史 + 当前问题。当前问题永远是最后一条 user。
	msgs := make([]agent.HistoryMessage, 0, len(history)+1)
	msgs = append(msgs, history...)
	msgs = append(msgs, agent.HistoryMessage{Role: RoleUser, Content: question})

	var answer strings.Builder
	result, runErr := answerer.Ask(runCtx, msgs, func(delta string) error {
		answer.WriteString(delta)
		return emit.Delta(delta)
	})
	if runErr != nil {
		return s.finishFailed(ctx, ownerID, emit, start, question, resolved, answer.String(), runErr, conversationID, userMessageID)
	}
	if result != nil && result.Pending != nil {
		// 工具已创建 pending 审批，checkpoint 已由 Runner 写入 PG。
		// partial answer 是中断前已经推给用户的正文；它必须落库，否则切页/刷新后
		// 这段内容只剩前端内存，Resume 也无法拼回最终 assistant 消息。
		partial := answer.String()
		persistCtx, persistCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer persistCancel()
		if s.approvals != nil {
			_ = s.approvals.SetInterrupt(persistCtx, checkpointID, result.Pending.InterruptID)
			if err := s.approvals.SetPartialAnswer(persistCtx, ownerID, result.Pending.ApprovalID, partial); err != nil {
				s.log.Warn("save approval partial answer failed",
					zap.Int64("approval_id", result.Pending.ApprovalID), zap.Error(err))
			}
		}
		result.Pending.PartialAnswer = partial
		payload := DonePayload{
			ChatModelID:      resolved.ID,
			AwaitingApproval: true,
			ApprovalID:       result.Pending.ApprovalID,
		}
		if conversationID != nil {
			payload.ConversationID = *conversationID
		}
		payload.UserMessageID = userMessageID
		if err := emit.Done(payload); err != nil {
			return err
		}
		return nil
	}

	// 引用只做映射：正文里出现、且这次真的登记过的编号。
	citations := citationsFromAnswer(result.Answer, registry)
	finalAnswer := result.Answer
	if strings.TrimSpace(finalAnswer) == "" {
		finalAnswer = refusalAnswer
	}

	_ = emit.Verdict(VerdictAnswered, "")
	if len(citations) > 0 {
		_ = emit.Citation(citations)
	}

	persistCtx, persistCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer persistCancel()

	// 多轮：把 assistant 消息也写进会话。写失败只记日志，不把已经生成的回答报成失败。
	var assistantMessageID int64
	if conversationID != nil {
		saved, saveErr := s.convs.AppendMessage(
			persistCtx, ownerID, *conversationID, RoleAssistant, finalAnswer, marshalJSON(citations))
		if saveErr != nil {
			s.log.Warn("save assistant message failed",
				zap.Int64("conversation_id", *conversationID), zap.Error(saveErr))
		} else {
			assistantMessageID = saved.ID
			if err := s.convs.MaybeEnqueueSummary(persistCtx, ownerID, *conversationID); err != nil {
				s.log.Warn("enqueue summary failed", zap.Int64("conversation_id", *conversationID), zap.Error(err))
			}
		}
	}

	run := &Run{
		OwnerID:          ownerID,
		ConversationID:   conversationID,
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

	done := DonePayload{
		RunID:              runID,
		ChatModelID:        resolved.ID,
		PromptTokens:       result.PromptTokens,
		CompletionTokens:   result.CompletionTokens,
		LatencyMS:          int64(run.LatencyMS),
		UserMessageID:      userMessageID,
		AssistantMessageID: assistantMessageID,
	}
	if conversationID != nil {
		done.ConversationID = *conversationID
	}
	_ = emit.Done(done)

	if err := s.models.Touch(persistCtx, ownerID, resolved.ID); err != nil {
		s.log.Warn("touch chat model failed", zap.Int64("chat_model_id", resolved.ID), zap.Error(err))
	}
	return nil
}

// finishFailed 处理执行期失败：落一行 failed，并把可读原因推给前端。
//
// 上游原始错误只进日志，不进响应：它可能带 URL、账号片段或厂商的内部描述。
func (s *Service) finishFailed(
	ctx context.Context, ownerID int64, emit Emitter, start time.Time,
	question string, resolved *chatmodel.Resolved, partial string, runErr error,
	conversationID *int64, userMessageID int64,
) error {
	code, message, retryable := classifyRunError(runErr)
	detail := errorDetail(runErr)
	s.log.Warn("agent run failed",
		zap.String("request_id", middleware.RequestIDFromContext(ctx)),
		zap.Int64("chat_model_id", resolved.ID),
		zap.String("error_code", code),
		zap.String("error_detail", detail),
		zap.Error(runErr),
	)

	persistCtx, persistCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer persistCancel()

	// 失败也写一条 assistant 消息：刷新后前端才能还原失败卡片。
	// 用户主动停止不是失败：按正常消息保存已生成内容，刷新后不显示错误。
	var assistantMessageID int64
	if conversationID != nil {
		if code == ReasonCancelled {
			content := partial
			if strings.TrimSpace(content) == "" {
				content = stoppedAnswer
			}
			saved, saveErr := s.convs.AppendMessage(
				persistCtx, ownerID, *conversationID, RoleAssistant, content, marshalJSON(nil))
			if saveErr != nil {
				s.log.Warn("save stopped assistant message failed",
					zap.Int64("conversation_id", *conversationID), zap.Error(saveErr))
			} else {
				assistantMessageID = saved.ID
			}
		} else {
			saved, saveErr := s.convs.AppendErrorMessage(
				persistCtx, ownerID, *conversationID, partial, marshalJSON(nil), code, message)
			if saveErr != nil {
				s.log.Warn("save failed assistant message failed",
					zap.Int64("conversation_id", *conversationID), zap.Error(saveErr))
			} else {
				assistantMessageID = saved.ID
			}
		}
	}

	verdict := VerdictFailed
	if code == ReasonCancelled {
		verdict = VerdictCancelled
	}
	run := &Run{
		OwnerID:        ownerID,
		ConversationID: conversationID,
		Question:       question,
		Answer:         partial,
		RawAnswer:      stringPtr(partial),
		Verdict:        verdict,
		VerdictReason:  stringPtr(code),
		Citations:      marshalJSON(nil),
		Retrieved:      marshalJSON(nil),
		ChatModelID:    int64Ptr(resolved.ID),
		Model:          stringPtr(resolved.Model),
		LatencyMS:      int(time.Since(start).Milliseconds()),
		ErrorCode:      stringPtr(code),
	}
	runID := s.saveRun(persistCtx, run)

	done := DonePayload{
		RunID:              runID,
		ChatModelID:        resolved.ID,
		LatencyMS:          int64(run.LatencyMS),
		UserMessageID:      userMessageID,
		AssistantMessageID: assistantMessageID,
	}
	if conversationID != nil {
		done.ConversationID = *conversationID
	}
	if detailed, ok := emit.(interface {
		ErrorDetail(code, message, detail string, retryable bool) error
	}); ok {
		_ = detailed.ErrorDetail(code, message, detail, retryable)
	} else {
		_ = emit.Error(code, message, retryable)
	}
	_ = emit.Done(done)
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

var diagnosticSecretPattern = regexp.MustCompile(`(?i)(sk-|bearer\s+|api[_-]?key[=: ]+)[A-Za-z0-9_\-]{8,}`)

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
	}

	// 厂商返回了结构化错误时，优先展示厂商 message；它通常比
	// “模型服务暂时不可用”更具体（限流、余额、模型名、鉴权等）。
	if status, vendorMessage, ok := agent.UpstreamErrorInfo(err); ok {
		code, fallback, retryable := classifyUpstreamStatus(status)
		if vendorMessage != "" {
			return code, vendorMessage, retryable
		}
		return code, fallback, retryable
	}

	return ReasonUpstreamError, "模型服务暂时不可用，请稍后重试", true
}

// classifyUpstreamStatus 把 HTTP 状态翻译成稳定错误码与兜底文案。
func classifyUpstreamStatus(status int) (code, message string, retryable bool) {
	switch {
	case status == 401 || status == 403:
		return "model_auth_failed", "模型服务鉴权失败，请检查 API Key 权限", false
	case status == 400 || status == 404 || status == 422:
		return "model_bad_request", "模型请求被拒绝，请检查模型名与参数", false
	case status == 429:
		return "model_rate_limited", "模型服务限流或额度不足，请稍后重试", true
	case status == 408 || status >= 500:
		return "model_unavailable", "模型厂商服务异常，请稍后重试", true
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

// errorDetail 给用户看的诊断摘要：截断、去空白、遮盖疑似密钥。
// 它不替代服务端完整 error 日志，只用于让失败卡片能定位到问题。
func errorDetail(err error) string {
	if err == nil {
		return ""
	}
	detail := strings.Join(strings.Fields(err.Error()), " ")
	if detail == "" {
		return ""
	}
	// 避免把厂商/内部完整响应原样塞进 SSE。
	detail = diagnosticSecretPattern.ReplaceAllString(detail, "$1***")
	detail = truncateRunes(detail, 600)
	return detail
}
