// 本文件是问答 Agent 的运行主体：构造 Eino ChatModelAgent、跑一次、收集输出。
//
// 分工：编号与 registry 由宿主维护（见 hit.go），工具只负责检索与登记；
// 这里只管"跑起来、把 token 拼成答案、守住预算"。
// 裁决（answered / refused / ungrounded）不在这里——那是 chat 层的职责，
// 因为它还要落库、要发 SSE，而 agent 不认识 HTTP。
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

// RefusalSentence 是模型被要求输出的固定拒答句。
//
// 现在没有 judge 了，这句话不再被用来判定 verdict；它只是给用户一个
// 一致、可预期的"没有依据"回复。是否拒答完全由模型按提示词决定。
const RefusalSentence = "根据现有知识库无法回答该问题"

// instruction 是系统提示词。
//
// 这是本轮架构里**唯一的约束**：宿主不再预检索、不再校验引用。
// 所以它必须把"先检索、只引用返回过的编号、没依据就拒答"写清楚，
// 但也要知道：模型不照做时系统不会拦。
const instruction = `你是 MindOS 的知识库问答助手。回答涉及用户资料、文档内容、知识库事实的问题前，
必须先调用 search_knowledge 检索；可以换关键词多次检索，但不要无意义地重复。

必须遵守：
1. 只依据 search_knowledge 返回的片段，以及 system 消息中给出的会话摘要/相关历史回答；
   不得引入外部知识、常识补全或推测。
2. 来自检索片段的结论末尾必须标注返回的编号，例如 [1]；一句话有多个来源写 [1][3]。
   来自会话摘要/相关历史的事实不需要编号。
3. 只引用本次工具真正返回过的编号；不要编造编号。
4. 检索片段和会话摘要/历史都不足以回答时，只输出这一句：` + RefusalSentence + `。
   不要解释、不要给建议、不要猜。
5. 不要照抄原文超过 30 字，用自己的话归纳。
6. 不要提"资料""上下文""片段""根据提供的内容"这类词，直接回答问题。`

// sqlInstruction 只在 SQL 工具真的注册时才拼进系统提示词。
// 否则模型会去调一个不存在的 query_knowledge，把一次普通问答变成工具错误。
const sqlInstruction = `当问题需要数量、分布、时间范围或按字段筛选（"有多少篇""按分类统计""哪些还没向量化"）时，
调用 query_knowledge 执行一条只读 SELECT。可用表与列：
- documents: id, title, source_type, mime, collection, tags, status, chunk_count, size_bytes, embed_status, embed_model, embed_dimensions, embedded_chunks, embedded_at, created_at, updated_at, owner_id
- document_chunks: id, document_id, seq, heading, start_offset, end_offset, token_count, embed_model, embedded_at, created_at

SQL 规则：只写一条 SELECT/WITH；必须显式写列名（SELECT * 会被权限拒绝）；
content 不可读，问"内容讲了什么"仍然用 search_knowledge；统计结果不需要引用编号。`

// instructionWithSQL 是注册了 SQL 工具时的完整提示词。
const instructionWithSQL = instruction + "\n\n" + sqlInstruction

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
	return Limits{MaxToolCalls: 3, MaxIterations: 5, MaxTokens: 60000}
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
	sqlRunner SQLRunner
	todos     *todo.Service
	approvals *approval.Service
	audits    *auditLog
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
func New(chatModel model.BaseModel[*schema.Message], retriever Retriever, sqlRunner SQLRunner, todos *todo.Service, approvals *approval.Service, ownerID int64, registry *Registry, maxResults int, limits Limits, onHits func([]Numbered), log *zap.Logger) *Agent {
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
		chatModel:  chatModel,
		retriever:  retriever,
		sqlRunner:  sqlRunner,
		todos:      todos,
		approvals:  approvals,
		audits:     &auditLog{},
		ownerID:    ownerID,
		registry:   registry,
		maxResults: maxResults,
		limits:     limits,
		onHits:     onHits,
		log:        log,
	}
}

// Ask 跑一次问答。
//
// onDelta 每收到一段正文调用一次；返回错误会中止本次问答（上层据此停止推送，
// 例如客户端已经断开）。它是唯一把正文送出去的口子——工具结果、思考过程
// 都不经过这里，避免把不该给用户看的内容混进答案。
func (a *Agent) Ask(ctx context.Context, history []HistoryMessage, onDelta func(string) error) (*Result, error) {
	// 一次 run 一个预算：检索与 SQL 共享 3 次，而不是各 3 次。
	budget := newToolBudget(a.limits.MaxToolCalls)

	searchTool, err := newSearchTool(a.retriever, a.ownerID, a.registry, a.maxResults, budget, a.onHits)
	if err != nil {
		return nil, fmt.Errorf("build search tool: %w", err)
	}
	tools := []tool.BaseTool{searchTool}
	if a.todos != nil {
		listTool, err := newListTodosTool(a.todos, a.ownerID, budget)
		if err != nil {
			return nil, fmt.Errorf("build list_todos tool: %w", err)
		}
		tools = append(tools, listTool)
	}
	if a.todos != nil && a.approvals != nil {
		createTool, err := newCreateTodoTool(a.todos, a.approvals, a.ownerID, budget)
		if err != nil {
			return nil, fmt.Errorf("build create_todo tool: %w", err)
		}
		tools = append(tools, createTool)
		updateTool, err := newUpdateTodoTool(a.approvals, a.ownerID, budget)
		if err != nil {
			return nil, fmt.Errorf("build update_todo tool: %w", err)
		}
		tools = append(tools, updateTool)
		deleteTool, err := newDeleteTodoTool(a.approvals, a.ownerID, budget)
		if err != nil {
			return nil, fmt.Errorf("build delete_todo tool: %w", err)
		}
		tools = append(tools, deleteTool)
	}
	if a.sqlRunner != nil {
		sqlTool, err := newSQLTool(a.sqlRunner, a.ownerID, budget, a.audits)
		if err != nil {
			return nil, fmt.Errorf("build sql tool: %w", err)
		}
		tools = append(tools, sqlTool)
	}

	// 工具列表决定提示词：没注册 query_knowledge 就不该在提示词里提它。
	instr := instruction
	if a.sqlRunner != nil {
		instr = instructionWithSQL
	}
	if a.todos != nil && a.approvals != nil {
		instr += todoInstruction
	}

	chatAgent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        "mindos-rag",
		Description: "基于个人知识库回答问题，按需检索或统计",
		Instruction: instr,
		Model:       a.chatModel,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: tools,
				// 顺序执行：并行时两个检索同时跑，编号分配顺序随调度变化，
				// 同一个问题两次运行会给出不同的 [n] 对应关系，排查时非常费劲。
				ExecuteSequentially: true,
			},
		},
		MaxIterations: a.limits.MaxIterations,
	})
	if err != nil {
		return nil, fmt.Errorf("new chat model agent: %w", err)
	}

	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: chatAgent, EnableStreaming: true})

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
	iter := runner.Run(ctx, msgs, cancelOpt)

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
