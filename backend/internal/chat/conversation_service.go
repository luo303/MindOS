package chat

import (
	"context"
	"strings"
)

// CreateConversation 建一个空会话。没有标题时先叫"新会话"，
// 第一条用户消息落库时会自动改成消息前缀。
func (s *Service) CreateConversation(ctx context.Context, ownerID int64, title string) (*Conversation, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		title = "新会话"
	}
	conv := &Conversation{OwnerID: ownerID, Title: titleFrom(title)}
	if err := s.convs.Create(ctx, conv); err != nil {
		return nil, err
	}
	return conv, nil
}

// ListConversations 返回当前用户的会话列表（最近更新在前）。
func (s *Service) ListConversations(ctx context.Context, ownerID int64) ([]Conversation, error) {
	return s.convs.List(ctx, ownerID, 100)
}

// GetConversation 按 owner + id 读取；不存在/越权都是 ErrConversationNotFound。
func (s *Service) GetConversation(ctx context.Context, ownerID, id int64) (*Conversation, error) {
	return s.convs.Get(ctx, ownerID, id)
}

// DeleteConversation 删除会话及其消息；ES 副本由 outbox 异步清理。
func (s *Service) DeleteConversation(ctx context.Context, ownerID, id int64) error {
	return s.convs.DeleteConversation(ctx, ownerID, id)
}

// ListMessages 返回会话最近的消息，按时间顺序。
// 先校验会话归属，避免把别人的消息列表读出来。
func (s *Service) ListMessages(ctx context.Context, ownerID, conversationID int64, limit, beforeSeq int) ([]Message, error) {
	if _, err := s.convs.Get(ctx, ownerID, conversationID); err != nil {
		return nil, err
	}
	return s.convs.ListMessages(ctx, ownerID, conversationID, limit, beforeSeq)
}
