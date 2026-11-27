package chat

import (
	"context"
	"strings"
	"testing"

	"github.com/luo303/mindos/backend/internal/chatmodel"
	"github.com/luo303/mindos/backend/internal/memory"
	"github.com/luo303/mindos/backend/internal/outbox"
)

func TestAskPreferencesAndOutboxIntegration(t *testing.T) {
	repo, db, owner := newConversationDB(t)
	ctx := context.Background()
	preferences := memory.NewService(memory.NewRepository(db))
	if _, err := preferences.Create(ctx, owner, "answer_detail", "concise"); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{answer: "知道了。"}
	svc := NewService(&fakeResolver{resolved: &chatmodel.Resolved{ID: 1, Model: "fake"}}, &fakeRunStore{},
		&fakeBuilder{runner: runner}, repo, nil, nil, nil, nil).WithPreferences(preferences)
	conv, err := svc.CreateConversation(ctx, owner, "")
	if err != nil {
		t.Fatal(err)
	}
	emit := &fakeEmitter{}
	// 不启动 RabbitMQ 或提炼模型。只要聊天等待候选完成，这次调用就不会拿到 done。
	if err := svc.AskInConversation(ctx, owner, conv.ID, AskInput{Question: "记住，以后代码示例默认用 Go"}, emit); err != nil {
		t.Fatal(err)
	}
	if emit.done.AssistantMessageID == 0 || emit.done.UserMessageID == 0 {
		t.Fatalf("done: %+v", emit.done)
	}
	if len(runner.gotHistory) != 2 || runner.gotHistory[0].Role != "system" ||
		!strings.Contains(runner.gotHistory[0].Content, "简洁") || !strings.Contains(runner.gotHistory[0].Content, "不要在聊天中声称已经保存") {
		t.Fatalf("runner input: %+v", runner.gotHistory)
	}
	if err := svc.AskInConversation(ctx, owner, conv.ID, AskInput{Question: "这次用 Python"}, &fakeEmitter{}); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.Model(&outbox.Message{}).Where("topic = ?", memory.ExtractTopic).Count(&count).Error; err != nil || count != 2 {
		t.Fatalf("extraction jobs = %d %v", count, err)
	}
}
