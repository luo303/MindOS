package todo

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/luo303/mindos/backend/internal/migrate"
	"github.com/luo303/mindos/backend/internal/testsupport"
	"github.com/luo303/mindos/backend/migrations"
)

func TestTodoServiceIntegration(t *testing.T) {
	db := testsupport.NewDatabase(t, "todo")
	if err := migrate.New(db, migrations.FS).Up(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	gdb := testsupport.NewGorm(t, db)
	owner := testsupport.NewUser(t, gdb, "todo-owner@example.com")
	other := testsupport.NewUser(t, gdb, "todo-other@example.com")
	svc := NewService(NewRepository(gdb))
	ctx := context.Background()

	due := time.Now().UTC().Add(24 * time.Hour)
	created, err := svc.Create(ctx, owner, CreateInput{
		Title: "整理 Redis 笔记", Description: "补充集群章节", Priority: 1, DueAt: &due,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.Source != SourceManual || created.Status != StatusOpen {
		t.Fatalf("created = %+v", created)
	}

	items, err := svc.List(ctx, owner, Filter{})
	if err != nil || len(items) != 1 {
		t.Fatalf("owner list = %v, %v", items, err)
	}
	otherItems, err := svc.List(ctx, other, Filter{})
	if err != nil || len(otherItems) != 0 {
		t.Fatalf("other list = %v, %v", otherItems, err)
	}

	done := StatusDone
	updated, err := svc.Update(ctx, owner, created.ID, UpdateInput{Status: &done})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Status != StatusDone || updated.CompletedAt == nil {
		t.Fatalf("updated = %+v", updated)
	}

	if err := svc.Delete(ctx, other, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant delete = %v, want ErrNotFound", err)
	}
	if err := svc.Delete(ctx, owner, created.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := svc.Get(ctx, owner, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get after delete = %v, want ErrNotFound", err)
	}
}
