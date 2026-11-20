package agent

import "context"

// RunMeta 是一次问答的会话元数据，供写工具创建审批时使用。
// 通过 context 传递，避免为了几个字段改 Runner 接口。
type RunMeta struct {
	ConversationID int64
	UserMessageID  int64
	ChatModelID    int64
}

type runMetaKey struct{}

func WithRunMeta(ctx context.Context, meta RunMeta) context.Context {
	return context.WithValue(ctx, runMetaKey{}, meta)
}

func RunMetaFrom(ctx context.Context) RunMeta {
	meta, _ := ctx.Value(runMetaKey{}).(RunMeta)
	return meta
}
