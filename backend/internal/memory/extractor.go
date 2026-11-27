package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/cloudwego/eino/schema"
	"gorm.io/gorm"

	"github.com/luo303/mindos/backend/internal/agent"
	"github.com/luo303/mindos/backend/internal/chatmodel"
)

// SourceReader 只能读取同账号同会话的用户消息，不暴露 chat 的仓储结构。
type SourceReader interface {
	ReadMemorySource(ctx context.Context, ownerID, conversationID, messageID int64) (Source, error)
}

// SourceMessage 只提供角色和正文供消歧，不能充当当前用户原文证据。
type SourceMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Source 包含当前用户原文和有限的邻近上下文。
type Source struct {
	Content   string
	CreatedAt time.Time
	Previous  []SourceMessage
}

func clipped(text string, limit int) string {
	runes := []rune(text)
	if len(runes) > limit {
		return string(runes[:limit])
	}
	return text
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
	settings, err := extractor.preferences.Settings(readCtx, event.OwnerID, event.ConversationID)
	if err != nil {
		return err
	}
	if settings.Mode == "off" || settings.ConversationOff {
		return nil
	}
	done, err := extractor.preferences.repo.extracted(readCtx, event)
	if err != nil {
		return err
	}
	if done {
		return nil
	}
	source, err := extractor.sources.ReadMemorySource(readCtx, event.OwnerID, event.ConversationID, event.MessageID)
	if err != nil {
		return fmt.Errorf("read preference source: %w", err)
	}
	if source.Content == "" || !safeContent(source.Content) {
		return nil
	}
	// 只提供实际读取的相关记忆和有限邻近上下文，避免把整个画像/历史发给提炼模型。
	related, err := extractor.preferences.repo.related(readCtx, event.OwnerID, source.Content, 12)
	if err != nil {
		return err
	}
	for index := range source.Previous {
		if !safeContent(source.Previous[index].Content) {
			source.Previous[index].Content = "[已省略]"
		} else {
			source.Previous[index].Content = clipped(source.Previous[index].Content, 500)
		}
	}
	input, err := json.Marshal(map[string]any{"current_user_message": clipped(source.Content, 12000), "message_time_utc": source.CreatedAt, "previous": source.Previous, "related_memories": related})
	if err != nil {
		return err
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
	limit := 1200
	if config.MaxTokens == nil || *config.MaxTokens > limit {
		config.MaxTokens = &limit
	}
	model, err := agent.NewChatModel(modelCtx, config)
	if err != nil {
		return errors.New("construct preference model failed")
	}
	result, err := model.Generate(modelCtx, []*schema.Message{schema.SystemMessage(OpenExtractPrompt), schema.UserMessage(string(input))})
	if err != nil || result == nil {
		return errors.New("preference model call failed") // 不把模型厂商回显的原文或密钥写入任务日志。
	}
	writeCtx, cancelWrite := context.WithTimeout(ctx, 3*time.Second)
	defer cancelWrite()
	err = extractor.preferences.repo.storeProposals(writeCtx, event, source.CreatedAt, decodeProposals(source.Content, result.Content), related)
	if errors.Is(err, gorm.ErrForeignKeyViolated) {
		return nil
	} // 来源已删除，不再保存或重试。
	return err
}
