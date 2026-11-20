package chat

import (
	"context"
	"testing"
	"time"

	"github.com/luo303/mindos/backend/internal/chatmodel"
)

func TestAskFailurePersistsAssistantMessage(t *testing.T) {
	repo, gdb, owner := newConversationDB(t)
	ctx := context.Background()
	svc := NewService(
		&fakeResolver{resolved: &chatmodel.Resolved{ID: 1, Model: "fake", BaseURL: "https://x/v1"}},
		&fakeRunStore{},
		&fakeBuilder{runner: &fakeRunner{err: context.DeadlineExceeded}},
		repo,
		nil,
		Settings{Timeout: 10 * time.Second},
		nil,
	)
	conv, err := svc.CreateConversation(ctx, owner, "")
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	if err := svc.AskInConversation(ctx, owner, conv.ID, AskInput{Question: "会失败的问题"}, &fakeEmitter{}); err != nil {
		t.Fatalf("ask: %v", err)
	}

	var messages []Message
	if err := gdb.Where("conversation_id = ?", conv.ID).Order("seq ASC").Find(&messages).Error; err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(messages))
	}
	failed := messages[1]
	if failed.Role != RoleAssistant {
		t.Errorf("role = %s, want assistant", failed.Role)
	}
	if failed.ErrorCode == nil || *failed.ErrorCode != ReasonTimeout {
		t.Errorf("error code = %v, want %s", failed.ErrorCode, ReasonTimeout)
	}
	if failed.ErrorMessage == nil || *failed.ErrorMessage == "" {
		t.Error("error message is empty")
	}
}

func TestStopPersistsNormalAssistantMessage(t *testing.T) {
	repo, gdb, owner := newConversationDB(t)
	ctx := context.Background()
	svc := NewService(
		&fakeResolver{resolved: &chatmodel.Resolved{ID: 1, Model: "fake", BaseURL: "https://x/v1"}},
		&fakeRunStore{},
		&fakeBuilder{runner: &fakeRunner{err: context.Canceled}},
		repo,
		nil,
		Settings{Timeout: 10 * time.Second},
		nil,
	)
	conv, err := svc.CreateConversation(ctx, owner, "")
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	if err := svc.AskInConversation(ctx, owner, conv.ID, AskInput{Question: "中途停止"}, &fakeEmitter{}); err != nil {
		t.Fatalf("ask: %v", err)
	}

	var messages []Message
	if err := gdb.Where("conversation_id = ?", conv.ID).Order("seq ASC").Find(&messages).Error; err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(messages))
	}
	stopped := messages[1]
	if stopped.ErrorCode != nil {
		t.Errorf("stopped message should not be failed, error_code=%v", *stopped.ErrorCode)
	}
	if stopped.Content != stoppedAnswer {
		t.Errorf("content = %q, want %q", stopped.Content, stoppedAnswer)
	}
}
