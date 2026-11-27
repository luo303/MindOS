package memory

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestOpenMemoryModesIntegration(t *testing.T) {
	service, db, owner, other := setupMemory(t)
	ctx := context.Background()
	var conversationID int64
	if err := db.Raw("INSERT INTO conversations (owner_id) VALUES (?) RETURNING id", owner).Scan(&conversationID).Error; err != nil {
		t.Fatal(err)
	}
	sequence := 0
	store := func(text, raw string) {
		t.Helper()
		sequence++
		var messageID int64
		if err := db.Raw("INSERT INTO messages (owner_id, conversation_id, role, seq, content) VALUES (?, ?, 'user', ?, ?) RETURNING id", owner, conversationID, sequence, text).Scan(&messageID).Error; err != nil {
			t.Fatal(err)
		}
		event := ExtractEvent{OwnerID: owner, ConversationID: conversationID, MessageID: messageID, ModelID: 1}
		for range 2 {
			if err := service.repo.storeProposals(ctx, event, time.Now().UTC(), decodeProposals(text, raw), nil); err != nil {
				t.Fatal(err)
			}
		}
	}
	settings, err := service.Settings(ctx, owner, conversationID)
	if err != nil || settings.Mode != "auto" {
		t.Fatalf("default settings: %+v %v", settings, err)
	}
	store("我在做 MindOS，平时周日跑步", `[{"action":"create","category":"project","value":"开发 MindOS","evidence":"我在做 MindOS","certainty":"explicit"},{"action":"create","category":"habit","value":"周日跑步","evidence":"平时周日跑步","certainty":"explicit"}]`)
	store("我在做 MindOS", `[{"action":"create","category":"project","value":"开发 MindOS","evidence":"我在做 MindOS","certainty":"explicit"}]`)
	items, err := service.List(ctx, owner)
	if err != nil || len(items) != 2 {
		t.Fatalf("auto items: %+v %v", items, err)
	}
	for _, item := range items {
		if item.Value == "开发 MindOS" {
			rows, err := service.Evidence(ctx, owner, item.ID)
			if err != nil || len(rows) != 2 {
				t.Fatalf("evidence: %+v %v", rows, err)
			}
		}
	}
	if _, err := service.SetSettings(ctx, owner, 0, "suggest", nil); err != nil {
		t.Fatal(err)
	}
	store("我喜欢简洁的回答", `[{"action":"create","category":"preference","value":"喜欢简洁回答","evidence":"我喜欢简洁的回答","certainty":"explicit"}]`)
	candidates, err := service.Candidates(ctx, owner, conversationID)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("suggestion: %+v %v", candidates, err)
	}
	if err := service.Decide(ctx, other, candidates[0].ID, "accept", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign decision: %v", err)
	}
	off := true
	if _, err := service.SetSettings(ctx, owner, conversationID, "", &off); err != nil {
		t.Fatal(err)
	}
	store("我养了一只猫", `[{"action":"create","category":"profile","value":"养猫","evidence":"我养了一只猫","certainty":"explicit"}]`)
	candidates, err = service.Candidates(ctx, owner, conversationID)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("disabled conversation wrote a candidate: %+v %v", candidates, err)
	}
	if foreign, err := service.List(ctx, other); err != nil || len(foreign) != 0 {
		t.Fatalf("foreign memory: %+v %v", foreign, err)
	}
}

func TestOpenMemoryExtraction(t *testing.T) {
	source := "我在做 MindOS，平时周日晚上做周计划。"
	raw := `[{"action":"create","category":"project","value":"正在开发 MindOS","evidence":"我在做 MindOS","certainty":"explicit"},{"action":"create","category":"habit","value":"周日晚上做周计划","evidence":"平时周日晚上做周计划","certainty":"explicit"}]`
	items := decodeProposals(source, raw)
	if len(items) != 2 || items[0].Key == items[1].Key || !items[0].autoEligible() {
		t.Fatalf("ordinary messages should yield independent memories: %+v", items)
	}
	if len(decodeProposals(source, `[{"action":"create","category":"profile","value":"猜测用户身份","evidence":"不存在的事实","certainty":"explicit"}]`)) != 0 {
		t.Fatal("invented evidence accepted")
	}
	if len(decodeProposals("我的密码是 abc123", `[{"action":"create","category":"profile","value":"密码是 abc123","evidence":"我的密码是 abc123","certainty":"explicit"}]`)) != 0 {
		t.Fatal("credential accepted")
	}
}

func TestContextSelection(t *testing.T) {
	now := time.Now().UTC()
	expired := now.Add(-time.Hour)
	items := []Memory{
		{ID: 1, Key: "answer_detail", Value: "concise", Status: active},
		{ID: 2, Metadata: Metadata{Category: "project"}, Value: "MindOS 后端使用 Go", Status: active},
		{ID: 3, Metadata: Metadata{Category: "state", ExpiresAt: &expired}, Value: "MindOS 出差", Status: active},
		{ID: 4, Metadata: Metadata{Category: "habit"}, Value: "周日跑步", Status: active},
		{ID: 5, Metadata: Metadata{Category: "project"}, Value: "MindOS 私有项目", Status: archived},
	}
	got := relevantContext(items, "MindOS 后端怎么做", now)
	if !strings.Contains(got, "简洁") || !strings.Contains(got, "MindOS 后端使用 Go") || strings.Contains(got, "出差") || strings.Contains(got, "跑步") || strings.Contains(got, "私有项目") {
		t.Fatalf("unexpected context: %s", got)
	}
}
