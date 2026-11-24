// 本文件是问答 Agent 的运行主体：构造 Eino ChatModelAgent、跑一次、收集输出。
//
// 分工：编号与 registry 由宿主维护（见 hit.go），工具只负责检索与登记；
// 这里只管"跑起来、把 token 拼成答案、守住预算"。
// chat 层负责落库与 SSE，并把引用编号映射到用户可点回的原文。
package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"

	"github.com/luo303/mindos/backend/internal/approval"
	"github.com/luo303/mindos/backend/internal/todo"
)

// Retriever 是 agent 需要的全部检索能力。
//
// 定义在这里而不是复用 knowledge.Searcher：接口属于调用方，agent 不该
// 为了一个方法把自己绑到知识模块的类型上。适配发生在 cmd/server。
type Retriever interface {
	// Search 只在该拥有者的知识库里检索。
	// owner 由调用方按请求传入，不能存在 agent 实例上（Builder 是共享的）。
	Search(ctx context.Context, ownerID int64, query string, limit int) ([]Hit, error)
}

// Limits 是一次问答的硬上限（AGENTS 的三重预算：步数、token、超时）。
//
// 超时不在这个结构体里：它由调用方的 context 控制（chat 层用 RAG_TIMEOUT），
// 而 ctx 会一路传到模型调用，不需要第二处配置。
type Limits struct {
	// MaxToolCalls 是工具调用次数上限。模型收到"工具失败"通常会收手。
	MaxToolCalls int
	// MaxIterations 是 ChatModel 生成轮数上限，比工具次数多留两轮给收尾。
	MaxIterations int
	// MaxTokens 是提示 + 补全的累计上限，<=0 表示不限。
	MaxTokens int64
}

// DefaultLimits 返回产品级的默认预算。
//
// 这些值刻意不做成配置项：它们不是部署差异，而是产品约束。
// 做成配置只会让人以为可以放开，然后某天收到一张意外的账单。
func DefaultLimits() Limits {
	return Limits{MaxToolCalls: 12, MaxIterations: 20, MaxTokens: 120000}
}

// ErrBudgetExceeded 表示本次问答消耗的 token 超过上限。
var ErrBudgetExceeded = errors.New("token budget exceeded")

// Result 是一次问答的产出。
type Result struct {
	Answer           string
	ToolCalls        int
	PromptTokens     int64
	CompletionTokens int64
	// Hits 是本次 run 登记过的全部片段（含编号），按登记顺序。
	//
	// 带上它而不是让调用方再去问 registry：一次问答的片段与答案是一体的，
	// 分两处取很容易出现"答案用 A 批片段、引用列表却是 B 批"的错位。
	Hits []Numbered
	// SQLAudits 是本次 run 的 SQL 调用审计（可能为空）。
	SQLAudits []SQLAudit
	// Pending 非空表示 Agent 在写工具处中断，等待审批。
	Pending *PendingApproval
}

// PendingApproval 是 Agent 暂停时返回给 chat 层的待审批信息。
type PendingApproval struct {
	ApprovalID  int64
	InterruptID string
	Tool        string
	Summary     string
}

// Agent 绑定一次问答。
//
// 为什么每次请求构造一个而不是共享：registry 与本次问题绑定，
// 共享实例就得把 registry 塞进 context，反而更难理解。
// 构造是纯内存操作，成本可以忽略。
type Agent struct {
	chatModel model.BaseModel[*schema.Message]
	retriever Retriever
	// sqlRunner 为 nil 表示没有配置只读 SQL 工具，本次 run 只挂检索。
	sqlRunner   SQLRunner
	todos       *todo.Service
	approvals   *approval.Service
	checkpoints adk.CheckPointStore
	audits      *auditLog
	// ownerID 是本次问答的用户：工具检索只能看到这个人的文档。
	// 存在实例上而不是塞进 context，是因为每个请求本来就会构造一个新 Agent。
	ownerID  int64
	registry *Registry
	// maxResults 是单次检索的条数上限。
	maxResults int
	limits     Limits
	// onHits 在每次工具检索并登记编号后回调，用来把"本次依据"推给前端。
	// 没有预检索了，检索完全由模型决定，所以这里可能就是零次或多次。
	onHits func([]Numbered)
	log    *zap.Logger
}

// New 组装一次问答。registry 必须由调用方持有——问答结束后它还要用来
// 生成引用列表与落库。
func New(chatModel model.BaseModel[*schema.Message], retriever Retriever, sqlRunner SQLRunner, todos *todo.Service, approvals *approval.Service, checkpoints adk.CheckPointStore, ownerID int64, registry *Registry, maxResults int, limits Limits, onHits func([]Numbered), log *zap.Logger) *Agent {
	if log == nil {
		log = zap.NewNop()
	}
	if limits.MaxToolCalls <= 0 {
		limits.MaxToolCalls = DefaultLimits().MaxToolCalls
	}
	if limits.MaxIterations <= 0 {
		limits.MaxIterations = DefaultLimits().MaxIterations
	}
	return &Agent{
		chatModel:   chatModel,
		retriever:   retriever,
		sqlRunner:   sqlRunner,
		todos:       todos,
		approvals:   approvals,
		checkpoints: checkpoints,
		audits:      &auditLog{},
		ownerID:     ownerID,
		registry:    registry,
		maxResults:  maxResults,
		limits:      limits,
		onHits:      onHits,
		log:         log,
	}
}

// newChatModelAgent 让新问答和审批恢复使用相同的提示词与工具配置。
func (a *Agent) newChatModelAgent(ctx context.Context) (*adk.ChatModelAgent, error) {
	budget := newToolBudget(a.limits.MaxToolCalls)
	meta := RunMetaFrom(ctx)
	canListTodos := a.todos != nil
	// 单轮问答也有 checkpoint ID，但没有会话供审批恢复，不能暴露写工具。
	canWriteTodos := canListTodos && a.approvals != nil && a.checkpoints != nil && meta.CheckpointID != "" && meta.ConversationID > 0
	hasSQL := a.sqlRunner != nil

	searchTool, err := newSearchTool(a.retriever, a.ownerID, a.registry, a.maxResults, budget, a.onHits)
	if err != nil {
		return nil, fmt.Errorf("build search tool: %w", err)
	}
	tools := []tool.BaseTool{searchTool}
	if canListTodos {
		listTool, err := newListTodosTool(a.todos, a.ownerID, budget)
		if err != nil {
			return nil, fmt.Errorf("build list_todos tool: %w", err)
		}
		tools = append(tools, listTool)
	}
	if canWriteTodos {
		createTool, err := newCreateTodoTool(a.todos, a.approvals, a.ownerID, budget)
		if err != nil {
			return nil, fmt.Errorf("build create_todo tool: %w", err)
		}
		tools = append(tools, createTool)
		updateTool, err := newUpdateTodoTool(a.todos, a.approvals, a.ownerID, budget)
		if err != nil {
			return nil, fmt.Errorf("build update_todo tool: %w", err)
		}
		tools = append(tools, updateTool)
		deleteTool, err := newDeleteTodoTool(a.todos, a.approvals, a.ownerID, budget)
		if err != nil {
			return nil, fmt.Errorf("build delete_todo tool: %w", err)
		}
		tools = append(tools, deleteTool)
	}
	if hasSQL {
		sqlTool, err := newSQLTool(a.sqlRunner, a.ownerID, budget, a.audits)
		if err != nil {
			return nil, fmt.Errorf("build sql tool: %w", err)
		}
		tools = append(tools, sqlTool)
	}

	return adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        runtimeAgentName,
		Description: loreDescription,
		Instruction: lorePrompt(hasSQL, canListTodos, canWriteTodos),
		Model:       a.chatModel,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: guardTools(tools),
				// 顺序执行：并行时两个检索同时跑，编号分配顺序随调度变化，
				// 同一个问题两次运行会给出不同的 [n] 对应关系，排查时非常费劲。
				ExecuteSequentially: true,
			},
		},
		MaxIterations: a.limits.MaxIterations,
	})
}

// Ask 跑一次问答。
//
// onDelta 每收到一段正文调用一次；返回错误会中止本次问答（上层据此停止推送，
// 例如客户端已经断开）。工具结果和思考过程不会从这里发送。
func (a *Agent) Ask(ctx context.Context, history []HistoryMessage, onDelta func(string) error) (*Result, error) {
	chatAgent, err := a.newChatModelAgent(ctx)
	if err != nil {
		return nil, fmt.Errorf("new chat model agent: %w", err)
	}

	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: chatAgent, EnableStreaming: true, CheckPointStore: a.checkpoints})

	// 取消句柄：ctx 被取消（客户端断开、整体超时）时立刻请求停止，
	// 并在退出前等它真正收尾——不等就可能留下一个还在烧 token 的 goroutine。
	cancelOpt, cancelFn := adk.WithCancel()
	defer func() {
		if handle, ok := cancelFn(adk.WithAgentCancelMode(adk.CancelImmediate)); ok {
			_ = handle.Wait()
		}
	}()

	// 历史消息按原样交给 Eino：user/assistant 交替，当前问题由调用方放在最后。
	msgs := make([]*schema.Message, 0, len(history))
	for _, item := range history {
		switch item.Role {
		case "system":
			// 会话摘要/冷层历史由 chat 组装成 system，放在最前面。
			msgs = append(msgs, schema.SystemMessage(item.Content))
		case "assistant":
			msgs = append(msgs, schema.AssistantMessage(item.Content, nil))
		default:
			msgs = append(msgs, schema.UserMessage(item.Content))
		}
	}
	runOpts := []adk.AgentRunOption{cancelOpt}
	meta := RunMetaFrom(ctx)
	if meta.CheckpointID != "" {
		runOpts = append(runOpts, adk.WithCheckPointID(meta.CheckpointID))
	}
	iter := runner.Run(ctx, msgs, runOpts...)

	var answer strings.Builder
	var promptTokens, completionTokens int64

	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			// 取消与超时原样带出去：上层要能区分"用户走了"和"模型挂了"。
			return nil, event.Err
		}
		if event.Action != nil && event.Action.Interrupted != nil {
			return a.handleInterrupt(ctx, event, meta.CheckpointID)
		}
		if event.Output == nil || event.Output.MessageOutput == nil {
			// 工具调用事件、内部状态事件：不是正文，跳过。
			continue
		}
		mv := event.Output.MessageOutput
		if mv.Role != schema.Assistant {
			// 工具返回结果（Role=Tool）不送给用户：它是给模型看的中间产物。
			continue
		}

		if mv.IsStreaming {
			if mv.MessageStream == nil {
				continue
			}
			usage, err := consumeStream(mv.MessageStream, &answer, onDelta)
			if err != nil {
				return nil, err
			}
			if usage != nil {
				promptTokens += int64(usage.PromptTokens)
				completionTokens += int64(usage.CompletionTokens)
				if exceeded(a.limits.MaxTokens, promptTokens, completionTokens) {
					return nil, ErrBudgetExceeded
				}
			}
			continue
		}

		if mv.Message == nil {
			continue
		}
		if chunk := mv.Message.Content; chunk != "" {
			answer.WriteString(chunk)
			if onDelta != nil {
				if err := onDelta(chunk); err != nil {
					return nil, err
				}
			}
		}
		// 非流式消息的 usage 在这一轮是准确的；流式只能拿到"最后一个 chunk"，
		// 见 consumeStream 的说明。
		if meta := mv.Message.ResponseMeta; meta != nil && meta.Usage != nil {
			promptTokens += int64(meta.Usage.PromptTokens)
			completionTokens += int64(meta.Usage.CompletionTokens)
			if exceeded(a.limits.MaxTokens, promptTokens, completionTokens) {
				return nil, ErrBudgetExceeded
			}
		}
	}

	result := &Result{
		Answer:           answer.String(),
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		Hits:             a.registry.All(),
		SQLAudits:        a.audits.all(),
	}
	a.log.Info("agent run finished",
		zap.Int("chars", len([]rune(result.Answer))),
		zap.Int("citations", a.registry.Len()),
		zap.Int64("prompt_tokens", result.PromptTokens),
		zap.Int64("completion_tokens", result.CompletionTokens),
	)
	return result, nil
}

// Resume 从 checkpoint 恢复一次中断的问答。
//
// 工具会再次进入 InvokableRun，通过 tool.GetResumeContext 读到审批决定。
func (a *Agent) Resume(ctx context.Context, checkpointID, interruptID string, decision map[string]any, onDelta func(string) error) (*Result, error) {
	chatAgent, err := a.newChatModelAgent(ctx)
	if err != nil {
		return nil, fmt.Errorf("new chat model agent: %w", err)
	}
	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: chatAgent, EnableStreaming: true, CheckPointStore: a.checkpoints})
	cancelOpt, cancelFn := adk.WithCancel()
	defer func() {
		if handle, ok := cancelFn(adk.WithAgentCancelMode(adk.CancelImmediate)); ok {
			_ = handle.Wait()
		}
	}()
	iter, err := runner.ResumeWithParams(ctx, checkpointID, &adk.ResumeParams{Targets: map[string]any{interruptID: decision}}, cancelOpt)
	if err != nil {
		return nil, fmt.Errorf("resume checkpoint: %w", err)
	}

	var answer strings.Builder
	var promptTokens, completionTokens int64
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			return nil, event.Err
		}
		if event.Action != nil && event.Action.Interrupted != nil {
			return a.handleInterrupt(ctx, event, checkpointID)
		}
		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}
		mv := event.Output.MessageOutput
		if mv.Role != schema.Assistant {
			continue
		}
		if mv.IsStreaming {
			if mv.MessageStream == nil {
				continue
			}
			usage, err := consumeStream(mv.MessageStream, &answer, onDelta)
			if err != nil {
				return nil, err
			}
			if usage != nil {
				promptTokens += int64(usage.PromptTokens)
				completionTokens += int64(usage.CompletionTokens)
				if exceeded(a.limits.MaxTokens, promptTokens, completionTokens) {
					return nil, ErrBudgetExceeded
				}
			}
			continue
		}
		if mv.Message == nil {
			continue
		}
		if chunk := mv.Message.Content; chunk != "" {
			answer.WriteString(chunk)
			if onDelta != nil {
				if err := onDelta(chunk); err != nil {
					return nil, err
				}
			}
		}
		if meta := mv.Message.ResponseMeta; meta != nil && meta.Usage != nil {
			promptTokens += int64(meta.Usage.PromptTokens)
			completionTokens += int64(meta.Usage.CompletionTokens)
			if exceeded(a.limits.MaxTokens, promptTokens, completionTokens) {
				return nil, ErrBudgetExceeded
			}
		}
	}
	return &Result{Answer: answer.String(), PromptTokens: promptTokens, CompletionTokens: completionTokens, Hits: a.registry.All(), SQLAudits: a.audits.all()}, nil
}

// handleInterrupt 把 Eino 的中断事件转成待审批结果。
//
// checkpoint 已经由 Runner 写入 PG；这里只补 interrupt_id 并读回审批，
// 不执行任何写操作。
func (a *Agent) handleInterrupt(ctx context.Context, event *adk.AgentEvent, checkpointID string) (*Result, error) {
	if checkpointID == "" {
		return nil, errors.New("agent interrupted without checkpoint id")
	}
	if a.approvals == nil {
		return nil, errors.New("agent interrupted without approval service")
	}
	info := event.Action.Interrupted
	var root *adk.InterruptCtx
	for _, c := range info.InterruptContexts {
		if c.IsRootCause {
			root = c
			break
		}
	}
	if root == nil && len(info.InterruptContexts) > 0 {
		root = info.InterruptContexts[0]
	}
	if root == nil {
		return nil, errors.New("agent interrupted without context")
	}
	if err := a.approvals.SetInterrupt(ctx, checkpointID, root.ID); err != nil {
		return nil, fmt.Errorf("save interrupt id: %w", err)
	}
	req, err := a.approvals.GetByCheckpoint(ctx, checkpointID)
	if err != nil {
		return nil, fmt.Errorf("load interrupt approval: %w", err)
	}
	return &Result{Pending: &PendingApproval{
		ApprovalID:  req.ID,
		InterruptID: root.ID,
		Tool:        req.ToolName,
		Summary:     req.Summary,
	}}, nil
}

// consumeStream 把一段流读完，正文追加到 answer 并转发给 onDelta。
//
// 返回值是该轮的 usage：只有最后一个 chunk 才带完整统计，
// 中间的 chunk 里 ResponseMeta 通常是空的，所以采用"取最后一次非空"。
func consumeStream(stream *schema.StreamReader[*schema.Message], answer *strings.Builder, onDelta func(string) error) (*schema.TokenUsage, error) {
	defer stream.Close()

	var usage *schema.TokenUsage
	for {
		chunk, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return usage, nil
		}
		if err != nil {
			return usage, fmt.Errorf("read model stream: %w", err)
		}
		if chunk == nil {
			continue
		}
		if chunk.Content != "" {
			answer.WriteString(chunk.Content)
			if onDelta != nil {
				if err := onDelta(chunk.Content); err != nil {
					return usage, err
				}
			}
		}
		if chunk.ResponseMeta != nil && chunk.ResponseMeta.Usage != nil {
			usage = chunk.ResponseMeta.Usage
		}
	}
}

func exceeded(limit, promptTokens, completionTokens int64) bool {
	if limit <= 0 {
		return false
	}
	return promptTokens+completionTokens > limit
}
