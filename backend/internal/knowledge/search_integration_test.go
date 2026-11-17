package knowledge

import (
	"context"
	"math"
	"strings"
	"testing"
)

// 检索的 SQL 只能对着真 PostgreSQL + pgvector 验证：
// `<=>` 的排序、`1 - distance` 的量纲、`embed_model` 过滤、以及
// websearch_to_tsquery 对英文词的实际行为，假仓储一个都演不出来。
func TestSearchIntegration(t *testing.T) {
	db, owner := setupKnowledgeDB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	tech := "技术"
	doc := sampleDocument(owner, strings.Repeat("a", 64))
	doc.Collection = &tech
	// 三条内容刻意分工：0 号两路都能命中（验证 RRF 加分）、
	// 1 号只有向量能命中（验证排序）、2 号两路都不该命中（验证没有垃圾召回）。
	chunks := []Chunk{
		{Seq: 0, Content: "redis cluster failover 原理", StartOffset: 0, EndOffset: 24, TokenCount: 6},
		{Seq: 1, Content: "easyv 大屏的多端同步机制", StartOffset: 24, EndOffset: 40, TokenCount: 6},
		{Seq: 2, Content: "今天没有任何相关内容", StartOffset: 40, EndOffset: 52, TokenCount: 5},
	}
	if err := repo.CreateWithChunks(ctx, doc, chunks); err != nil {
		t.Fatalf("CreateWithChunks: %v", err)
	}

	life := "生活"
	other := sampleDocument(owner, strings.Repeat("b", 64))
	other.Collection = &life
	if err := repo.CreateWithChunks(ctx, other, []Chunk{
		{Seq: 0, Content: "番茄炒蛋的做法", StartOffset: 0, EndOffset: 10, TokenCount: 4},
	}); err != nil {
		t.Fatalf("CreateWithChunks(other): %v", err)
	}

	stored, err := repo.ListChunks(ctx, owner, doc.ID, 10, 0)
	if err != nil || len(stored) != len(chunks) {
		t.Fatalf("ListChunks = %d, %v", len(stored), err)
	}
	otherChunks, err := repo.ListChunks(ctx, owner, other.ID, 10, 0)
	if err != nil || len(otherChunks) != 1 {
		t.Fatalf("ListChunks(other) = %d, %v", len(otherChunks), err)
	}

	axis := func(pos int, value float32) []float32 {
		v := make([]float32, EmbeddingDimensions)
		if pos >= 0 {
			v[pos] = value
		}
		return v
	}
	// 查询向量只落在第 0 轴上：0 号分块同向（相似度 1），1 号正交（0），
	// 2 号反向（-1）。这样排序结果是确定的，不依赖具体数值大小。
	query := axis(0, 1)
	vectors := []ChunkVector{
		{ID: stored[0].ID, Vector: axis(0, 1)},
		{ID: stored[1].ID, Vector: axis(1, 1)},
		{ID: stored[2].ID, Vector: axis(0, -1)},
	}
	if err := repo.SaveEmbeddings(ctx, doc.ID, "fake-embed", EmbeddingDimensions, vectors); err != nil {
		t.Fatalf("SaveEmbeddings: %v", err)
	}
	if err := repo.SaveEmbeddings(ctx, other.ID, "fake-embed", EmbeddingDimensions, []ChunkVector{
		{ID: otherChunks[0].ID, Vector: axis(1, 1)},
	}); err != nil {
		t.Fatalf("SaveEmbeddings(other): %v", err)
	}

	t.Run("vector path orders by cosine similarity", func(t *testing.T) {
		hits, err := repo.SearchVector(ctx, owner, query, "fake-embed", 10, DocumentFilter{})
		if err != nil {
			t.Fatalf("SearchVector: %v", err)
		}
		if len(hits) != 4 {
			t.Fatalf("hits = %d, want 4 (both documents are ready)", len(hits))
		}
		if hits[0].ChunkID != stored[0].ID {
			t.Fatalf("top hit = %d, want %d", hits[0].ChunkID, stored[0].ID)
		}
		if math.Abs(hits[0].Similarity-1) > 1e-6 {
			t.Errorf("identical vector similarity = %v, want 1", hits[0].Similarity)
		}
		// 反向向量的相似度必须为负：如果 SQL 写成 `<=>` 而没做 1-x，
		// 或者把距离当相似度用，这个断言会立刻炸。
		if hits[len(hits)-1].Similarity >= 0 {
			t.Errorf("opposite vector similarity = %v, want negative", hits[len(hits)-1].Similarity)
		}
	})

	t.Run("vector path ignores chunks from another model", func(t *testing.T) {
		hits, err := repo.SearchVector(ctx, owner, query, "some-other-model", 10, DocumentFilter{})
		if err != nil {
			t.Fatalf("SearchVector: %v", err)
		}
		if len(hits) != 0 {
			t.Fatalf("hits = %d, want 0: vectors from another model must not be compared", len(hits))
		}
	})

	t.Run("text path matches the english token", func(t *testing.T) {
		// 中文在 simple 配置下整句是一个词，所以这里只验证英文词——
		// 中文召回靠向量路，这条限制写在 docs/design/rag.md 的已知限制里。
		hits, err := repo.SearchText(ctx, owner, "redis", 10, DocumentFilter{})
		if err != nil {
			t.Fatalf("SearchText: %v", err)
		}
		if len(hits) != 1 || hits[0].ChunkID != stored[0].ID {
			t.Fatalf("hits = %+v, want exactly chunk %d", hits, stored[0].ID)
		}
		if hits[0].TextRank <= 0 {
			t.Errorf("text rank = %v, want positive", hits[0].TextRank)
		}
	})

	t.Run("collection filter applies to both paths", func(t *testing.T) {
		collection := life
		filter := DocumentFilter{Collection: &collection}
		hits, err := repo.SearchText(ctx, owner, "redis", 10, filter)
		if err != nil {
			t.Fatalf("SearchText: %v", err)
		}
		if len(hits) != 0 {
			t.Fatalf("hits = %d, want 0 outside the collection", len(hits))
		}
		vectorHits, err := repo.SearchVector(ctx, owner, query, "fake-embed", 10, filter)
		if err != nil {
			t.Fatalf("SearchVector: %v", err)
		}
		if len(vectorHits) != 1 || vectorHits[0].DocumentID != other.ID {
			t.Fatalf("vector hits = %+v, want only the collection's document", vectorHits)
		}
	})

	t.Run("searcher fuses both paths", func(t *testing.T) {
		embedder := &fakeEmbedder{model: "fake-embed", dim: EmbeddingDimensions, vectors: [][]float32{query}}
		searcher := NewSearcher(repo, embedder)

		res, err := searcher.Search(ctx, owner, "redis", 8, DocumentFilter{})
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if res.Degraded {
			t.Error("Degraded must be false: the vector path returned hits")
		}
		if math.Abs(res.MaxSimilarity-1) > 1e-6 {
			t.Errorf("MaxSimilarity = %v, want 1", res.MaxSimilarity)
		}
		if len(res.Hits) == 0 || res.Hits[0].ChunkID != stored[0].ID {
			t.Fatalf("top hit = %+v, want chunk %d", res.Hits, stored[0].ID)
		}
		// 0 号两路都命中，分数必须高于只在向量路出现的 1 号。
		var dual, single float64
		for _, hit := range res.Hits {
			switch hit.ChunkID {
			case stored[0].ID:
				dual = hit.RRFScore
			case stored[1].ID:
				single = hit.RRFScore
			}
		}
		if dual <= single {
			t.Errorf("dual-path score %v should exceed single-path %v", dual, single)
		}
	})

	t.Run("searcher reports degraded when model changed", func(t *testing.T) {
		embedder := &fakeEmbedder{model: "brand-new-model", dim: EmbeddingDimensions, vectors: [][]float32{query}}
		res, err := NewSearcher(repo, embedder).Search(ctx, owner, "redis", 8, DocumentFilter{})
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		// 换了模型但没重建时：向量路空、全文路有，必须明确标成降级，
		// 否则界面会显示"语义检索已启用"而实际上只剩字面命中。
		if !res.Degraded {
			t.Error("Degraded must be true when the vector path is empty")
		}
		if res.MaxSimilarity != 0 {
			t.Errorf("MaxSimilarity = %v, want 0", res.MaxSimilarity)
		}
		if len(res.Hits) == 0 {
			t.Fatal("text path should still return the redis chunk")
		}
	})
}
