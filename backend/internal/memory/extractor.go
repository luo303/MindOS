package memory

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/luo303/mindos/backend/internal/agent"
	"github.com/luo303/mindos/backend/internal/chatmodel"
)

// SourceReader 只能读取同账号同会话的用户消息，不暴露 chat 的仓储结构。
type SourceReader interface {
	ReadMemorySource(ctx context.Context, ownerID, conversationID, messageID int64) (string, time.Time, error)
}

// Extractor 是后台提炼任务，不由 SSE handler 调用。
type Extractor struct {
	sources     SourceReader
	models      *chatmodel.Service
	preferences *Service
	parallel    chan struct{}
}

// NewExtractor 创建共享的后台提炼器；单进程最多一项，并非聊天并发上限。
func NewExtractor(sources SourceReader, models *chatmodel.Service, preferences *Service) *Extractor {
	return &Extractor{sources: sources, models: models, preferences: preferences, parallel: make(chan struct{}, 1)}
}

// Run 复核归属后调用一次模型，失败交给现有 outbox 重试。
func (extractor *Extractor) Run(ctx context.Context, event ExtractEvent) error {
	if event.OwnerID <= 0 || event.ConversationID <= 0 || event.MessageID <= 0 || event.ModelID <= 0 {
		return errors.New("invalid memory extract event ids")
	}
	select {
	case extractor.parallel <- struct{}{}:
		defer func() { <-extractor.parallel }()
	default:
		return errors.New("memory extraction busy") // 交给 dispatch 退避，不占用其他消费槽。
	}
	readCtx, cancelRead := context.WithTimeout(ctx, 3*time.Second)
	defer cancelRead()
	source, createdAt, err := extractor.sources.ReadMemorySource(readCtx, event.OwnerID, event.ConversationID, event.MessageID)
	if err != nil {
		return fmt.Errorf("read preference source: %w", err)
	}
	if !ExplicitRequest(source) {
		return nil
	}
	target, err := extractor.models.Resolve(readCtx, event.OwnerID, &event.ModelID)
	if errors.Is(err, chatmodel.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("resolve preference model: %w", err)
	}
	modelCtx, cancelModel := context.WithTimeout(ctx, 20*time.Second)
	defer cancelModel()
	config := agent.FromResolved(target, 20*time.Second)
	limit := 512
	if config.MaxTokens == nil || *config.MaxTokens > limit {
		config.MaxTokens = &limit
	}
	model, err := agent.NewChatModel(modelCtx, config)
	if err != nil {
		return errors.New("construct preference model failed")
	}
	result, err := model.Generate(modelCtx, []*schema.Message{schema.SystemMessage(ExtractPrompt), schema.UserMessage(source)})
	if err != nil || result == nil {
		return errors.New("preference model call failed") // 不把模型厂商回显的原文或密钥写入任务日志。
	}
	writeCtx, cancelWrite := context.WithTimeout(ctx, 3*time.Second)
	defer cancelWrite()
	return extractor.preferences.StoreProposal(writeCtx, event, createdAt, source, result.Content)
}
