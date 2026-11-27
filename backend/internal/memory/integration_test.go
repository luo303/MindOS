package memory

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/luo303/mindos/backend/internal/migrate"
	"github.com/luo303/mindos/backend/internal/testsupport"
	"github.com/luo303/mindos/backend/migrations"
	"gorm.io/gorm"
)

func setupMemory(t *testing.T) (*Service, *gorm.DB, int64, int64) {
	t.Helper()
	sqlDB := testsupport.NewDatabase(t, "longterm_memory")
	if err := migrate.New(sqlDB, migrations.FS).Up(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	db := testsupport.NewGorm(t, sqlDB)
	return NewService(NewRepository(db)), db, testsupport.NewUser(t, db, "memory@example.com"), testsupport.NewUser(t, db, "other@example.com")
}

func seedProposal(t *testing.T, service *Service, db *gorm.DB, owner int64, display, value string) int64 {
	t.Helper()
	var conversationID, messageID int64
	text := "记住，以后代码示例默认用 " + display
	if err := db.Raw("INSERT INTO conversations (owner_id) VALUES (?) RETURNING id", owner).Scan(&conversationID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Raw("INSERT INTO messages (owner_id, conversation_id, role, seq, content) VALUES (?, ?, 'user', 1, ?) RETURNING id", owner, conversationID, text).Scan(&messageID).Error; err != nil {
		t.Fatal(err)
	}
	event := ExtractEvent{OwnerID: owner, ConversationID: conversationID, MessageID: messageID, ModelID: 1}
	output := `{"key":"code_example_language","value":"` + value + `","evidence":"` + text + `"}`
	for range 2 {
		if err := service.StoreProposal(context.Background(), event, time.Now().UTC(), text, output); err != nil {
			t.Fatal(err)
		}
	}
	return conversationID
}

func TestPreferenceLifecycleIntegration(t *testing.T) {
	service, db, owner, other := setupMemory(t)
	ctx := context.Background()
	conversationID := seedProposal(t, service, db, owner, "Go", "go")
	if block, err := service.Context(ctx, owner); err != nil || block != "" {
		t.Fatalf("pending proposal affected context: %q %v", block, err)
	}
	items, err := service.Candidates(ctx, owner, conversationID)
	if err != nil || len(items) != 1 {
		t.Fatalf("expected one idempotent proposal: %+v %v", items, err)
	}
	if foreign, err := service.Candidates(ctx, other, conversationID); err != nil || len(foreign) != 0 {
		t.Fatalf("another account listed proposal: %+v %v", foreign, err)
	}
	if err := service.Decide(ctx, other, items[0].ID, "accept", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another account decided proposal: %v", err)
	}
	if err := service.Decide(ctx, owner, items[0].ID, "accept", nil); err != nil {
		t.Fatal(err)
	}
	if block, err := service.Context(ctx, owner); err != nil || !strings.Contains(block, "Go") {
		t.Fatalf("confirmed Go missing: %q %v", block, err)
	}
	staleConversation := seedProposal(t, service, db, owner, "Rust", "rust")
	proposals, err := service.Candidates(ctx, owner, staleConversation)
	if err != nil || len(proposals) != 1 || proposals[0].BaseVersion != 1 {
		t.Fatalf("replacement proposal = %+v %v", proposals, err)
	}
	memories, err := service.List(ctx, owner)
	if err != nil || len(memories) != 1 {
		t.Fatalf("memory list = %+v %v", memories, err)
	}
	python := "python"
	item, err := service.Update(ctx, owner, memories[0].ID, 1, &python, "")
	if err != nil || item.Version != 2 {
		t.Fatalf("edit = %+v %v", item, err)
	}
	if err := service.Decide(ctx, owner, proposals[0].ID, "accept", nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale proposal should conflict: %v", err)
	}
	if _, err := service.Update(ctx, other, item.ID, item.Version, nil, archived); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another account updated memory: %v", err)
	}
	if _, err := service.Versions(ctx, other, item.ID, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another account read history: %v", err)
	}
	if err := db.Exec("DELETE FROM conversations WHERE owner_id = ? AND id IN (?, ?)", owner, conversationID, staleConversation).Error; err != nil {
		t.Fatal(err)
	}
	if list, err := service.Candidates(ctx, owner, 0); err != nil || len(list) != 0 {
		t.Fatalf("deleted source proposals remain: %+v %v", list, err)
	}
	versions, err := service.Versions(ctx, owner, item.ID, 0)
	if err != nil || len(versions) != 2 || versions[1].Source != "chat" || versions[1].MessageID != nil {
		t.Fatalf("deleted source retained text/id or lost history: %+v %v", versions, err)
	}
	if _, err := service.Update(ctx, owner, item.ID, item.Version, nil, archived); err != nil {
		t.Fatal(err)
	}
	if block, err := service.Context(ctx, owner); err != nil || block != "" {
		t.Fatalf("archived memory affected context: %q %v", block, err)
	}
}

func TestConcurrentMemoryConfirmationIntegration(t *testing.T) {
	service, db, owner, _ := setupMemory(t)
	ctx := context.Background()
	seedProposal(t, service, db, owner, "Go", "go")
	seedProposal(t, service, db, owner, "Rust", "rust")
	items, err := service.Candidates(ctx, owner, 0)
	if err != nil || len(items) != 2 {
		t.Fatalf("proposals = %+v %v", items, err)
	}
	results := make(chan error, len(items))
	for _, item := range items {
		go func() { results <- service.Decide(ctx, owner, item.ID, "accept", nil) }()
	}
	success, conflict := 0, 0
	for range items {
		switch err := <-results; {
		case err == nil:
			success++
		case errors.Is(err, ErrConflict):
			conflict++
		default:
			t.Fatalf("unexpected decision error: %v", err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("success = %d, conflict = %d", success, conflict)
	}
}
