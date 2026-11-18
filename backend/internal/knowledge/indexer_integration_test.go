package knowledge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/elastic/go-elasticsearch/v8"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/luo303/mindos/backend/internal/outbox"
	"github.com/luo303/mindos/backend/internal/testsupport"
)

// TestSearchIndexerIntegration 需要真实 ES：
//
//	MINDOS_TEST_ES_URL=http://127.0.0.1:9200 \
//	MINDOS_TEST_DSN='host=... port=... user=... password=... dbname=postgres sslmode=disable' \
//	go test -race -run TestSearchIndexerIntegration ./internal/knowledge
//
// 覆盖：bulk 写入、owner 过滤、中文关键词命中、删除清理。
func TestSearchIndexerIntegration(t *testing.T) {
	esURL := os.Getenv("MINDOS_TEST_ES_URL")
	if esURL == "" {
		t.Skip("MINDOS_TEST_ES_URL not set")
	}
	client, err := elasticsearch.NewClient(elasticsearch.Config{Addresses: []string{esURL}})
	if err != nil {
		t.Fatalf("new es client: %v", err)
	}
	ping, err := client.Ping(client.Ping.WithContext(context.Background()))
	if err != nil {
		t.Fatalf("ping es: %v", err)
	}
	_ = ping.Body.Close()

	prefix := fmt.Sprintf("mindos-test-%d", time.Now().UnixNano())
	ctx := context.Background()
	if err := EnsureSearchIndexes(ctx, client, prefix); err != nil {
		t.Fatalf("ensure indexes: %v", err)
	}
	t.Cleanup(func() {
		for _, index := range []string{prefix + "-chunks-v1", prefix + "-messages-v1"} {
			_, _ = esRequest(ctx, client, http.MethodDelete, "/"+index, "")
		}
	})

	db, owner := setupKnowledgeDB(t)
	repo := NewRepository(db)
	doc := newIntegrationDocument(owner, "退款流程：提交申请后 3-5 个工作日到账。")
	if err := repo.CreateWithChunks(ctx, doc, chunkFixture(2)); err != nil {
		t.Fatalf("create document: %v", err)
	}
	sources, err := repo.ListChunkSources(ctx, doc.ID)
	if err != nil {
		t.Fatalf("list chunks: %v", err)
	}
	vectors := make([]ChunkVector, 0, len(sources))
	for i, source := range sources {
		vector := make([]float32, EmbeddingDimensions)
		vector[0] = float32(i+1) / 10
		vectors = append(vectors, ChunkVector{ID: source.ID, Vector: vector})
	}
	if err := repo.SaveEmbeddings(ctx, doc.ID, "test-model", EmbeddingDimensions, vectors); err != nil {
		t.Fatalf("save embeddings: %v", err)
	}
	assertOutboxEvent(t, db, SearchTopicDocumentUpsert, doc.ID)

	indexer := NewSearchIndexer(db, client, prefix, zap.NewNop())
	if err := indexer.UpsertDocument(ctx, doc.ID); err != nil {
		t.Fatalf("index document: %v", err)
	}

	if got := esCount(t, ctx, client, prefix+"-chunks", map[string]any{
		"query": map[string]any{"term": map[string]any{"owner_id": owner}},
	}); got != 2 {
		t.Errorf("owner hits = %d, want 2", got)
	}
	if got := esCount(t, ctx, client, prefix+"-chunks", map[string]any{
		"query": map[string]any{"match": map[string]any{"content": "正文"}},
	}); got != 2 {
		t.Errorf("keyword hits = %d, want 2", got)
	}

	other := testsupport.NewUser(t, db, fmt.Sprintf("other-%d@test.local", time.Now().UnixNano()))
	if got := esCount(t, ctx, client, prefix+"-chunks", map[string]any{
		"query": map[string]any{"term": map[string]any{"owner_id": other}},
	}); got != 0 {
		t.Errorf("other owner hits = %d, want 0", got)
	}

	if err := repo.Delete(ctx, owner, doc.ID); err != nil {
		t.Fatalf("delete document: %v", err)
	}
	assertOutboxEvent(t, db, SearchTopicDocumentDelete, doc.ID)
	if err := indexer.DeleteDocument(ctx, doc.ID); err != nil {
		t.Fatalf("delete from es: %v", err)
	}
	if got := esCount(t, ctx, client, prefix+"-chunks", map[string]any{
		"query": map[string]any{"term": map[string]any{"document_id": doc.ID}},
	}); got != 0 {
		t.Errorf("after delete hits = %d, want 0", got)
	}
}

func assertOutboxEvent(t *testing.T, db *gorm.DB, topic string, documentID int64) {
	t.Helper()
	var count int64
	err := db.Model(&outbox.Message{}).
		Where("topic = ? AND payload->>'document_id' = ?", topic, strconv.FormatInt(documentID, 10)).
		Count(&count).Error
	if err != nil {
		t.Fatalf("count outbox %s: %v", topic, err)
	}
	if count == 0 {
		t.Errorf("no outbox event %s for document %d", topic, documentID)
	}
}

func esCount(t *testing.T, ctx context.Context, client *elasticsearch.Client, index string, body map[string]any) int {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal query: %v", err)
	}
	res, err := esRequest(ctx, client, http.MethodPost, "/"+index+"/_search", string(raw))
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if res.status >= 300 {
		t.Fatalf("search status %d: %s", res.status, res.body)
	}
	var parsed struct {
		Hits struct {
			Total struct {
				Value int `json:"value"`
			} `json:"total"`
		} `json:"hits"`
	}
	if err := json.Unmarshal([]byte(res.body), &parsed); err != nil {
		t.Fatalf("decode search: %v", err)
	}
	return parsed.Hits.Total.Value
}
