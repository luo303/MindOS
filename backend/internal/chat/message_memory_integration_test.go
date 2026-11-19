package chat

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/elastic/go-elasticsearch/v8"
	"go.uber.org/zap"

	"github.com/luo303/mindos/backend/internal/knowledge"
)

type fakeMessageEmbedder struct {
	model  string
	vector []float32
}

func (f *fakeMessageEmbedder) Embed(_ context.Context, texts []string) ([][]float32, int64, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = f.vector
	}
	return out, 0, nil
}

func (f *fakeMessageEmbedder) Model() string { return f.model }

func TestMessageMemoryIntegration(t *testing.T) {
	esURL := os.Getenv("MINDOS_TEST_ES_URL")
	if esURL == "" {
		t.Skip("MINDOS_TEST_ES_URL not set")
	}
	client, err := elasticsearch.NewClient(elasticsearch.Config{Addresses: []string{esURL}})
	if err != nil {
		t.Fatalf("new es client: %v", err)
	}
	prefix := fmt.Sprintf("mindos-chat-test-%d", time.Now().UnixNano())
	ctx := context.Background()
	if err := knowledge.EnsureSearchIndexes(ctx, client, prefix); err != nil {
		t.Fatalf("ensure indexes: %v", err)
	}
	t.Cleanup(func() {
		for _, index := range []string{prefix + "-chunks-v1", prefix + "-messages-v1"} {
			_, _, _ = esCall(ctx, client, http.MethodDelete, "/"+index, "", nil)
		}
	})

	repo, gdb, owner := newConversationDB(t)
	conv := &Conversation{OwnerID: owner, Title: "冷层测试"}
	if err := repo.Create(ctx, conv); err != nil {
		t.Fatalf("create conversation: %v", err)
	}

	vector := make([]float32, 1024)
	vector[0] = 0.1
	embedder := &fakeMessageEmbedder{model: "test-model", vector: vector}
	indexer := NewMessageIndexer(gdb, client, prefix, embedder, zap.NewNop())
	for i := 1; i <= 20; i++ {
		role := RoleUser
		if i%2 == 0 {
			role = RoleAssistant
		}
		content := fmt.Sprintf("第 %d 条普通消息", i)
		if i == 1 {
			content = "退款流程需要 3-5 个工作日"
		}
		msg, err := repo.AppendMessage(ctx, owner, conv.ID, role, content, RawJSON("[]"))
		if err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
		if err := indexer.Embed(ctx, msg.ID); err != nil {
			t.Fatalf("embed %d: %v", i, err)
		}
		if err := indexer.Upsert(ctx, msg.ID); err != nil {
			t.Fatalf("index %d: %v", i, err)
		}
	}

	searcher := NewMessageSearcher(client, embedder, prefix)
	hits, err := searcher.Search(ctx, owner, conv.ID, 5, "退款", 5)
	if err != nil {
		t.Fatalf("search cold: %v", err)
	}
	if len(hits) == 0 || hits[0].Seq != 1 {
		t.Fatalf("cold hits = %+v, want seq 1 first", hits)
	}

	// 摘要：只读最新一版，且和冷层一起进入 system 文本。
	if err := gdb.Create(&ConversationSummary{
		ConversationID: conv.ID,
		OwnerID:        owner,
		Version:        1,
		CoveredFromSeq: 1,
		CoveredToSeq:   4,
		BatchFromSeq:   1,
		BatchToSeq:     4,
		Content:        "用户在咨询退款时效。",
		TokenCount:     12,
	}).Error; err != nil {
		t.Fatalf("create summary: %v", err)
	}
	memory := NewConversationMemory(repo, searcher, zap.NewNop())
	block := memory.Context(ctx, owner, conv.ID, 5, "退款要多久")
	if !strings.Contains(block, "退款时效") || !strings.Contains(block, "相关历史原文") {
		t.Fatalf("memory block missing summary/cold: %s", block)
	}
}
