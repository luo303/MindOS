package knowledge

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFuseESHits(t *testing.T) {
	semantic := []esHit{
		{Score: 0.8, Source: esSource{ChunkID: 1, DocumentID: 10, Content: "a"}},
		{Score: 0.7, Source: esSource{ChunkID: 2, DocumentID: 10, Content: "b"}},
	}
	keyword := []esHit{
		{Score: 12.5, Source: esSource{ChunkID: 2, DocumentID: 10, Content: "b"}},
		{Score: 3.0, Source: esSource{ChunkID: 3, DocumentID: 11, Content: "c"}},
	}

	result := fuseESHits(semantic, keyword, 5)
	if result.VectorHits != 2 || result.TextHits != 2 {
		t.Fatalf("hits = %d/%d, want 2/2", result.VectorHits, result.TextHits)
	}
	if result.Degraded {
		t.Error("with semantic hits, Degraded must be false")
	}
	if len(result.Hits) != 3 {
		t.Fatalf("fused = %d, want 3", len(result.Hits))
	}
	// chunk 2 同时命中两路（1/62 + 1/61），应排第一。
	if result.Hits[0].ChunkID != 2 {
		t.Errorf("top = %d, want 2", result.Hits[0].ChunkID)
	}
	// 0.7 的 kNN 分数换算回余弦是 0.4。
	if got := result.Hits[0].Similarity; got < 0.39 || got > 0.41 {
		t.Errorf("similarity = %v, want ~0.4", got)
	}
	if result.Hits[0].TextRank != 12.5 {
		t.Errorf("text rank = %v, want 12.5", result.Hits[0].TextRank)
	}
	// 关键词独有的一条，余弦保持 0（只能算字面命中）。
	for _, hit := range result.Hits {
		if hit.ChunkID == 3 && hit.Similarity != 0 {
			t.Errorf("keyword-only similarity = %v, want 0", hit.Similarity)
		}
	}
}

func TestFuseESHitsDegraded(t *testing.T) {
	result := fuseESHits(nil, []esHit{
		{Score: 1.0, Source: esSource{ChunkID: 1}},
	}, 5)
	if !result.Degraded {
		t.Error("empty semantic path must be Degraded")
	}
	if result.MaxSimilarity != 0 {
		t.Errorf("max similarity = %v, want 0", result.MaxSimilarity)
	}
	if len(result.Hits) != 1 || result.Hits[0].Similarity != 0 {
		t.Errorf("hits = %+v", result.Hits)
	}
}

func TestESFilters(t *testing.T) {
	collection := "notes"
	tag := "go"
	raw, err := json.Marshal(esFilters(7, DocumentFilter{Collection: &collection, Tag: &tag}, "bge-m3"))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	body := string(raw)
	for _, want := range []string{
		`"owner_id":7`,
		`"status":"ready"`,
		`"embed_model":"bge-m3"`,
		`"collection":"notes"`,
		`"tags":"go"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("filters missing %s: %s", want, body)
		}
	}
}
