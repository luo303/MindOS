package agent

import (
	"context"
	"fmt"

	"go.uber.org/zap"

	"github.com/luo303/mindos/backend/internal/chatmodel"
)

// Runner 是一次问答的执行体。
//
// 抽成接口是为了让 internal/chat 不认识 Eino：它只要"能 Ask 的东西"，
// 具体怎么构造模型、怎么装工具都留在这个包。测试里塞一个返回固定
// 结果的假实现就能测完整个 SSE 编排。
type Runner interface {
	Ask(ctx context.Context, question string, onDelta func(string) error) (*Result, error)
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
	limits    Limits
	log       *zap.Logger
}

// NewBuilder 组装构造器。retriever 由 cmd/server 注入适配器。
func NewBuilder(retriever Retriever, limits Limits, log *zap.Logger) *Builder {
	return &Builder{retriever: retriever, limits: limits, log: log}
}

// Build 用给定配置与本次的编号表构造 Runner。
//
// 模型客户端的超时不在这里设：一次问答的整体超时由调用方的 ctx 控制，
// 两处都设会让"到底是哪一层超时"变成猜谜。
func (b *Builder) Build(ctx context.Context, ownerID int64, target *chatmodel.Resolved, registry *Registry) (Runner, error) {
	chatModel, err := NewChatModel(ctx, FromResolved(target, 0))
	if err != nil {
		return nil, fmt.Errorf("build chat model: %w", err)
	}
	return New(chatModel, b.retriever, ownerID, registry, b.limits, b.log), nil
}
