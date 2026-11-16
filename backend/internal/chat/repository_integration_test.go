package chat

import (
	"context"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/luo303/mindos/backend/internal/migrate"
	"github.com/luo303/mindos/backend/internal/testsupport"
	"github.com/luo303/mindos/backend/migrations"
)

// qa_runs 的形状由数据库保证：verdict 的取值、jsonb 的往返、token 的非负。
// 这些都不是内存里能验证的事。
func setupChatDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := testsupport.NewDatabase(t, "chat")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := migrate.New(db, migrations.FS).Up(ctx); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	return testsupport.NewGorm(t, db)
}

func TestQARunIntegration(t *testing.T) {
	db := setupChatDB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	reason := ReasonNoEvidence
	raw := "根据现有知识库无法回答该问题"
	modelName := "spark-x2.6"
	var modelID int64 = 7

	run := &Run{
		Question:         "EasyV 的多端共享是怎么做同步的？",
		Answer:           "这个问题在知识库里没有依据。",
		RawAnswer:        &raw,
		Verdict:          VerdictRefused,
		VerdictReason:    &reason,
		Citations:        RawJSON(`[{"n":1,"chunk_id":42,"title":"EasyV 多端"}]`),
		Retrieved:        RawJSON(`[{"n":1,"chunk_id":42,"similarity":0.21,"rrf_score":0.016}]`),
		ChatModelID:      &modelID,
		Model:            &modelName,
		PromptTokens:     1200,
		CompletionTokens: 34,
		LatencyMS:        860,
	}
	if err := repo.Create(ctx, run); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if run.ID == 0 {
		t.Fatal("id should be assigned by the database")
	}

	got, err := repo.Get(ctx, run.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Question != run.Question || got.Verdict != VerdictRefused {
		t.Fatalf("round trip mismatch: %+v", got)
	}
	if got.VerdictReason == nil || *got.VerdictReason != ReasonNoEvidence {
		t.Errorf("verdict_reason = %v", got.VerdictReason)
	}
	if string(got.Citations) == "" || !strings.Contains(string(got.Citations), "chunk_id") {
		t.Errorf("citations did not survive the jsonb round trip: %s", got.Citations)
	}
	if !strings.Contains(string(got.Retrieved), "rrf_score") {
		t.Errorf("retrieved damaged: %s", got.Retrieved)
	}
	if got.PromptTokens != 1200 || got.CompletionTokens != 34 || got.LatencyMS != 860 {
		t.Errorf("usage fields = %d/%d/%d", got.PromptTokens, got.CompletionTokens, got.LatencyMS)
	}
	if got.ChatModelID == nil || *got.ChatModelID != modelID {
		t.Errorf("chat_model_id = %v", got.ChatModelID)
	}

	t.Run("empty json defaults to an empty array", func(t *testing.T) {
		minimal := &Run{Question: "q", Answer: "a", Verdict: VerdictAnswered}
		if err := repo.Create(ctx, minimal); err != nil {
			t.Fatalf("Create: %v", err)
		}
		readBack, err := repo.Get(ctx, minimal.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		// 两列都是 NOT NULL DEFAULT '[]'，写 NULL 会直接违反约束；
		// 这里确认 RawJSON 的空值走的是 '[]' 而不是 NULL。
		if string(readBack.Citations) != "[]" {
			t.Errorf("citations = %q, want []", readBack.Citations)
		}
	})

	t.Run("verdict is constrained by the database", func(t *testing.T) {
		bad := &Run{Question: "q", Answer: "a", Verdict: "maybe"}
		if err := repo.Create(ctx, bad); err == nil {
			t.Fatal("database accepted an unknown verdict")
		} else if !strings.Contains(err.Error(), "qa_runs_verdict_check") {
			t.Logf("note: constraint error text = %v", err)
		}
	})

	t.Run("blank question is rejected", func(t *testing.T) {
		blank := &Run{Question: "   ", Answer: "a", Verdict: VerdictAnswered}
		if err := repo.Create(ctx, blank); err == nil {
			t.Fatal("database accepted an empty question")
		}
	})
}
