package chat

import (
	"context"
	"testing"

	"github.com/luo303/mindos/backend/internal/outbox"
)

func TestSummaryClaimAndCommitIntegration(t *testing.T) {
	repo, gdb, owner := newConversationDB(t)
	ctx := context.Background()
	conv := &Conversation{OwnerID: owner, Title: "摘要测试"}
	if err := repo.Create(ctx, conv); err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	for i := 1; i <= 30; i++ {
		role := RoleUser
		if i%2 == 0 {
			role = RoleAssistant
		}
		if _, err := repo.AppendMessage(ctx, owner, conv.ID, role, "消息内容", RawJSON("[]")); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	if err := repo.MaybeEnqueueSummary(ctx, owner, conv.ID); err != nil {
		t.Fatalf("enqueue summary: %v", err)
	}
	var events int64
	if err := gdb.Model(&outbox.Message{}).Where("topic = ?", SummaryTopic).Count(&events).Error; err != nil {
		t.Fatalf("count summary events: %v", err)
	}
	if events == 0 {
		t.Fatal("no summary event enqueued")
	}

	claim, err := repo.ClaimSummary(ctx, owner, conv.ID)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if claim == nil {
		t.Fatal("claim is nil")
	}
	if claim.BatchFromSeq != 1 || claim.BatchToSeq != 12 {
		t.Fatalf("batch = %d..%d, want 1..12", claim.BatchFromSeq, claim.BatchToSeq)
	}
	if len(claim.Messages) != 12 {
		t.Fatalf("messages = %d, want 12", len(claim.Messages))
	}

	summary := "用户在测试滚动摘要。"
	if err := repo.CommitSummary(ctx, claim, summary, estimateTokens(summary), "fake"); err != nil {
		t.Fatalf("commit: %v", err)
	}
	latest, err := repo.LatestSummary(ctx, owner, conv.ID)
	if err != nil || latest == nil {
		t.Fatalf("latest summary = %v, %v", latest, err)
	}
	if latest.Version != 1 || latest.CoveredToSeq != 12 || latest.Content != summary {
		t.Fatalf("latest = %+v", latest)
	}
	convAfter, err := repo.Get(ctx, owner, conv.ID)
	if err != nil {
		t.Fatalf("get conversation: %v", err)
	}
	if convAfter.SummarizedUntilSeq != 12 || convAfter.SummaryVersion != 1 {
		t.Fatalf("conversation watermark = %+v", convAfter)
	}

	// 只剩 2 条未摘要（13..14），不够 12 条，不再认领。
	next, err := repo.ClaimSummary(ctx, owner, conv.ID)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if next != nil {
		t.Fatalf("second claim = %+v, want nil", next)
	}
}
