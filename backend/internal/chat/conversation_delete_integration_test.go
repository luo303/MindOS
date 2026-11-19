package chat

import (
	"context"
	"errors"
	"testing"

	"github.com/luo303/mindos/backend/internal/outbox"
)

func TestDeleteConversationCascadesIntegration(t *testing.T) {
	repo, gdb, owner := newConversationDB(t)
	ctx := context.Background()
	conv := &Conversation{OwnerID: owner, Title: "待删除"}
	if err := repo.Create(ctx, conv); err != nil {
		t.Fatalf("create: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := repo.AppendMessage(ctx, owner, conv.ID, RoleUser, "消息", RawJSON("[]")); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	if err := repo.DeleteConversation(ctx, owner, conv.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := repo.Get(ctx, owner, conv.ID); !errors.Is(err, ErrConversationNotFound) {
		t.Fatalf("get after delete = %v, want ErrConversationNotFound", err)
	}
	var messages int64
	if err := gdb.Model(&Message{}).Where("conversation_id = ?", conv.ID).Count(&messages).Error; err != nil {
		t.Fatalf("count messages: %v", err)
	}
	if messages != 0 {
		t.Fatalf("messages after delete = %d, want 0", messages)
	}
	var events int64
	if err := gdb.Model(&outbox.Message{}).Where("topic = ?", SearchConversationDelete).Count(&events).Error; err != nil {
		t.Fatalf("count delete event: %v", err)
	}
	if events != 1 {
		t.Fatalf("delete events = %d, want 1", events)
	}
}
