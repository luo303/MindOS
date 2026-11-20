package agent

import (
	"context"
	"fmt"

	"go.uber.org/zap"

	"github.com/luo303/mindos/backend/internal/approval"
	"github.com/luo303/mindos/backend/internal/chatmodel"
	"github.com/luo303/mindos/backend/internal/todo"
)

// Runner 是一次问答的执行体。
//
// 抽成接口是为了让 internal/chat 不认识 Eino：它只要"能 Ask 的东西"，
// 具体怎么构造模型、怎么装工具都留在这个包。测试里塞一个返回固定
// 结果的假实现就能测完整个 SSE 编排。
// HistoryMessage 是会话里已经发生的一条消息。
//
// 用 chat 自己的结构而不是 *schema.Message：chat 不该 import eino。
// agent 负责把 role/content 翻成 Eino 的消息类型。
type HistoryMessage struct {
	Role    string // system | user | assistant
	Content string
}

type Runner interface {
	Ask(ctx context.Context, history []HistoryMessage, onDelta func(string) error) (*Result, error)
}

// 编译期确认 *Agent 满足 Runner：接口与实现分开写时，最容易出现的
// 错误是方法名或签名差一点点，而这种错在调用点才暴露。
var _ Runner = (*Agent)(nil)

// Builder 按一条已解密的配置构造一次问答。
//
// 每次请求新建一个 Agent 而不是复用：registry 与本次问题绑定，
// 共享实例就得把 registry 塞进 context。构造是纯内存操作，成本可忽略。
type Builder struct {
	retriever Retriever
	// sqlRunner 为 nil 表示没有配置只读 SQL 工具。
	sqlRunner SQLRunner
	todos     *todo.Service
	approvals *approval.Service
	limits    Limits
	// maxResults 是单次检索的条数上限，来自 RAG_TOP_K。
	maxResults int
	log        *zap.Logger
}

// NewBuilder 组装构造器。retriever 由 cmd/server 注入适配器；
// maxResults 是单次工具检索的条数上限（<=0 时用 8，最大 10）。
func NewBuilder(retriever Retriever, sqlRunner SQLRunner, todos *todo.Service, approvals *approval.Service, limits Limits, maxResults int, log *zap.Logger) *Builder {
	if maxResults <= 0 {
		maxResults = 8
	}
	if maxResults > maxToolLimit {
		maxResults = maxToolLimit
	}
	return &Builder{retriever: retriever, sqlRunner: sqlRunner, todos: todos, approvals: approvals, limits: limits, maxResults: maxResults, log: log}
}

// Build 用给定配置与本次的编号表构造 Runner。
//
// 模型客户端的超时不在这里设：一次问答的整体超时由调用方的 ctx 控制，
// 两处都设会让"到底是哪一层超时"变成猜谜。
func (b *Builder) Build(ctx context.Context, ownerID int64, target *chatmodel.Resolved, registry *Registry, onHits func([]Numbered)) (Runner, error) {
	chatModel, err := NewChatModel(ctx, FromResolved(target, 0))
	if err != nil {
		return nil, fmt.Errorf("build chat model: %w", err)
	}
	return New(chatModel, b.retriever, b.sqlRunner, b.todos, b.approvals, ownerID, registry, b.maxResults, b.limits, onHits, b.log), nil
}
