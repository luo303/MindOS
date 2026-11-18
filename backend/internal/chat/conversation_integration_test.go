package chat

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/luo303/mindos/backend/internal/chatmodel"
	"gorm.io/gorm"

	"github.com/luo303/mindos/backend/internal/migrate"
	"github.com/luo303/mindos/backend/internal/testsupport"
	"github.com/luo303/mindos/backend/migrations"
)

// newConversationDB 建一个迁移到最新的测试库，并返回仓储、拥有者 id。
func newConversationDB(t *testing.T) (*ConversationRepository, *gorm.DB, int64) {
	t.Helper()
	db := testsupport.NewDatabase(t, "chatconv")
	if err := migrate.New(db, migrations.FS).Up(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	gdb := testsupport.NewGorm(t, db)
	owner := testsupport.NewUser(t, gdb, "conv@example.com")
	return NewConversationRepository(gdb), gdb, owner
}

func TestConversationRepositoryIntegration(t *testing.T) {
	repo, gdb, owner := newConversationDB(t)
	ctx := context.Background()

	conv := &Conversation{OwnerID: owner, Title: "新会话"}
	if err := repo.Create(ctx, conv); err != nil {
		t.Fatalf("create: %v", err)
	}

	userMsg, err := repo.AppendMessage(ctx, owner, conv.ID, RoleUser, "第一个问题", RawJSON("[]"))
	if err != nil {
		t.Fatalf("append user: %v", err)
	}
	if userMsg.Seq != 1 {
		t.Errorf("user seq = %d, want 1", userMsg.Seq)
	}
	assistantMsg, err := repo.AppendMessage(ctx, owner, conv.ID, RoleAssistant, "第一个回答", RawJSON("[]"))
	if err != nil {
		t.Fatalf("append assistant: %v", err)
	}
	if assistantMsg.Seq != 2 {
		t.Errorf("assistant seq = %d, want 2", assistantMsg.Seq)
	}

	// 首条用户消息把占位标题替换掉。
	got, err := repo.Get(ctx, owner, conv.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Title != "第一个问题" {
		t.Errorf("title = %q, want 第一个问题", got.Title)
	}
	if got.MessageCount != 2 {
		t.Errorf("message_count = %d, want 2", got.MessageCount)
	}

	before, err := repo.MessagesBefore(ctx, owner, conv.ID, 3, 10)
	if err != nil {
		t.Fatalf("messages before: %v", err)
	}
	if len(before) != 2 || before[0].Role != RoleUser || before[1].Role != RoleAssistant {
		t.Fatalf("history = %+v", before)
	}

	list, err := repo.List(ctx, owner, 10)
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %+v, err = %v", list, err)
	}

	// 跨租户：读、写、列消息都必须被挡住。
	other := testsupport.NewUser(t, gdb, "other@example.com")
	if _, err := repo.Get(ctx, other, conv.ID); !errors.Is(err, ErrConversationNotFound) {
		t.Errorf("cross-tenant get = %v, want ErrConversationNotFound", err)
	}
	if _, err := repo.AppendMessage(ctx, other, conv.ID, RoleUser, "越权", RawJSON("[]")); !errors.Is(err, ErrConversationNotFound) {
		t.Errorf("cross-tenant append = %v, want ErrConversationNotFound", err)
	}
}

func TestAskInConversationIntegration(t *testing.T) {
	repo, gdb, owner := newConversationDB(t)
	ctx := context.Background()

	runner := &fakeRunner{answer: "这是回答。"}
	runs := &fakeRunStore{}
	svc := NewService(
		&fakeResolver{resolved: &chatmodel.Resolved{ID: 1, Model: "fake", BaseURL: "https://x/v1"}},
		runs,
		&fakeBuilder{runner: runner},
		repo,
		Settings{Timeout: 10 * time.Second},
		nil,
	)

	conv, err := svc.CreateConversation(ctx, owner, "")
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	if _, err := repo.AppendMessage(ctx, owner, conv.ID, RoleUser, "旧问题", RawJSON("[]")); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := repo.AppendMessage(ctx, owner, conv.ID, RoleAssistant, "旧回答", RawJSON("[]")); err != nil {
		t.Fatalf("seed assistant: %v", err)
	}

	emit := &fakeEmitter{}
	if err := svc.AskInConversation(ctx, owner, conv.ID, AskInput{Question: "新问题"}, emit); err != nil {
		t.Fatalf("ask: %v", err)
	}

	// 热层历史：旧 user、旧 assistant、当前 user。
	if len(runner.gotHistory) != 3 {
		t.Fatalf("history = %+v, want 3", runner.gotHistory)
	}
	wantRoles := []string{RoleUser, RoleAssistant, RoleUser}
	for i, want := range wantRoles {
		if runner.gotHistory[i].Role != want {
			t.Errorf("history[%d].role = %q, want %q", i, runner.gotHistory[i].Role, want)
		}
	}
	if runner.gotHistory[2].Content != "新问题" {
		t.Errorf("current question = %q", runner.gotHistory[2].Content)
	}

	if len(runs.runs) != 1 || runs.runs[0].ConversationID == nil || *runs.runs[0].ConversationID != conv.ID {
		t.Fatalf("run = %+v", runs.runs)
	}
	if emit.done.AssistantMessageID == 0 {
		t.Errorf("done assistant message id = 0")
	}

	msgs, err := svc.ListMessages(ctx, owner, conv.ID, 10, 0)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(msgs) != 4 {
		t.Fatalf("messages = %d, want 4", len(msgs))
	}
	if msgs[3].Role != RoleAssistant || msgs[3].Content != "这是回答。" {
		t.Errorf("last message = %+v", msgs[3])
	}

	// 跨租户：同一个库里另一个用户不能往这个会话里发消息。
	other := testsupport.NewUser(t, gdb, "other2@example.com")
	otherSvc := NewService(
		&fakeResolver{resolved: &chatmodel.Resolved{ID: 1, Model: "fake", BaseURL: "https://x/v1"}},
		&fakeRunStore{},
		&fakeBuilder{runner: &fakeRunner{answer: "x"}},
		repo,
		Settings{Timeout: 10 * time.Second},
		nil,
	)
	if err := otherSvc.AskInConversation(ctx, other, conv.ID, AskInput{Question: "越权"}, &fakeEmitter{}); !errors.Is(err, ErrConversationNotFound) {
		t.Fatalf("cross-tenant ask = %v, want ErrConversationNotFound", err)
	}
}
