package knowledge

import (
	"context"
	"errors"
	"testing"
)

// fakeSearchStore 是两条 SQL 的替身。
//
// 记录收到的参数而不是只返回数据：这样"模型名有没有传给向量路""候选条数
// 是不是 30"也能被断言——它们不影响返回值，但会影响真实查询的正确性。
type fakeSearchStore struct {
	vectorHits []Hit
	textHits   []Hit
	vectorErr  error
	textErr    error

	gotModel      string
	gotVectorLen  int
	gotVectorTop  int
	gotTextQuery  string
	gotTextTop    int
	vectorQueries int
	textQueries   int
}

func (f *fakeSearchStore) SearchVector(_ context.Context, vector []float32, model string, limit int, _ DocumentFilter) ([]Hit, error) {
	f.vectorQueries++
	f.gotModel = model
	f.gotVectorLen = len(vector)
	f.gotVectorTop = limit
	return f.vectorHits, f.vectorErr
}

func (f *fakeSearchStore) SearchText(_ context.Context, query string, limit int, _ DocumentFilter) ([]Hit, error) {
	f.textQueries++
	f.gotTextQuery = query
	f.gotTextTop = limit
	return f.textHits, f.textErr
}

func hit(chunkID int64, similarity float64) Hit {
	return Hit{ChunkID: chunkID, DocumentID: 1, Seq: int(chunkID), Similarity: similarity}
}

func TestFuseRRFRanksMultiPathHitsHigher(t *testing.T) {
	// 分块 2 在两路都是第 2 名，分块 1 只在向量路第 1 名。
	// RRF 的结论应该是 2 更高：2*(1/62) > 1/61。这条正是"多路共识优先"的核心。
	vectorHits := []Hit{hit(1, 0.9), hit(2, 0.8), hit(3, 0.7)}
	// 9 号只在全文路出现，需要显式给 text_rank：否则"保留原文排名"这条断言
	// 会因为值本来就是 0 而永远成立，测不出任何东西。
	textHits := []Hit{{ChunkID: 9, DocumentID: 1, Seq: 9, TextRank: 0.4}, hit(2, 0)}

	got := fuseRRF(vectorHits, textHits, 10)

	if len(got) != 4 {
		t.Fatalf("merged hits = %d, want 4", len(got))
	}
	if got[0].ChunkID != 2 {
		t.Fatalf("top hit = %d, want 2 (hit by both paths)", got[0].ChunkID)
	}
	if got[0].Similarity != 0.8 {
		t.Errorf("top hit similarity = %v, want 0.8 from the vector path", got[0].Similarity)
	}
	if got[0].RRFScore <= got[1].RRFScore {
		t.Errorf("dual-path hit must outrank single-path: %v <= %v", got[0].RRFScore, got[1].RRFScore)
	}
	// 只在全文路出现的分块要保留，相似度留 0——上层靠它区分"有向量依据"与"纯字面命中"。
	var lone *Hit
	for i := range got {
		if got[i].ChunkID == 9 {
			lone = &got[i]
		}
	}
	if lone == nil {
		t.Fatal("full-text-only hit 9 was dropped")
	}
	if lone.Similarity != 0 {
		t.Errorf("text-only hit similarity = %v, want 0", lone.Similarity)
	}
	if lone.TextRank == 0 {
		t.Error("text-only hit should keep its text_rank")
	}
}

func TestFuseRRFTruncatesAndStaysDeterministic(t *testing.T) {
	// 构造两个"同分"的分块：1 号在向量路第 2、全文路第 1，2 号反之。
	// 两边的 1/61 + 1/62 完全相等，于是排序必须落到下一个键（chunk id）上。
	// 用同一路的第 1/2 名来构造是错的——那样分数本来就不同，测不到兜底逻辑。
	vectorHits := []Hit{hit(2, 0.5), hit(1, 0.5)}
	textHits := []Hit{hit(1, 0), hit(2, 0)}

	first := fuseRRF(vectorHits, textHits, 2)
	second := fuseRRF(vectorHits, textHits, 2)

	if len(first) != 2 {
		t.Fatalf("limit not applied: got %d", len(first))
	}
	if first[0].RRFScore != first[1].RRFScore {
		t.Fatalf("expected a tie, got %v vs %v", first[0].RRFScore, first[1].RRFScore)
	}
	for i := range first {
		if first[i].ChunkID != second[i].ChunkID {
			t.Fatalf("order changed between runs: %d vs %d", first[i].ChunkID, second[i].ChunkID)
		}
	}
	if first[0].ChunkID != 1 || first[1].ChunkID != 2 {
		t.Errorf("ties should break by chunk id, got %d,%d", first[0].ChunkID, first[1].ChunkID)
	}
}

func TestFuseRRFEmptyInputs(t *testing.T) {
	if got := fuseRRF(nil, nil, 8); len(got) != 0 {
		t.Fatalf("empty input returned %d hits", len(got))
	}
}

func TestSearchRejectsEmptyQuery(t *testing.T) {
	searcher := NewSearcher(&fakeSearchStore{}, newFakeEmbedder())
	if _, err := searcher.Search(context.Background(), "   ", 8, DocumentFilter{}); !errors.Is(err, ErrEmptyQuery) {
		t.Fatalf("err = %v, want ErrEmptyQuery", err)
	}
}

func TestSearchWithoutEmbedderFailsLoudly(t *testing.T) {
	// 未配置向量模型时不能悄悄退化成全文检索：中文全文在 simple 配置下
	// 基本召回不了东西，退化等于给用户"能答但不准"的假象。
	searcher := NewSearcher(&fakeSearchStore{}, nil)
	if _, err := searcher.Search(context.Background(), "怎么配置", 8, DocumentFilter{}); !errors.Is(err, ErrEmbeddingNotConfigured) {
		t.Fatalf("err = %v, want ErrEmbeddingNotConfigured", err)
	}
}

func TestSearchReportsThresholdInputs(t *testing.T) {
	store := &fakeSearchStore{
		vectorHits: []Hit{hit(1, 0.42), hit(2, 0.71), hit(3, 0.55)},
		textHits:   []Hit{hit(4, 0)},
	}
	embedder := newFakeEmbedder()
	searcher := NewSearcher(store, embedder)

	got, err := searcher.Search(context.Background(), "多端同步怎么实现", 8, DocumentFilter{})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got.MaxSimilarity != 0.71 {
		t.Errorf("MaxSimilarity = %v, want 0.71 (max of vector path only)", got.MaxSimilarity)
	}
	if got.Degraded {
		t.Error("Degraded must be false when the vector path returned hits")
	}
	if got.VectorHits != 3 || got.TextHits != 1 {
		t.Errorf("counts = %d/%d, want 3/1", got.VectorHits, got.TextHits)
	}
	// 模型名必须透传给向量路：漏传会让 SQL 用空串比对 embed_model，一条都查不到。
	if store.gotModel != embedder.Model() {
		t.Errorf("vector path model = %q, want %q", store.gotModel, embedder.Model())
	}
	if store.gotVectorLen != EmbeddingDimensions {
		t.Errorf("query vector len = %d, want %d", store.gotVectorLen, EmbeddingDimensions)
	}
	if store.gotVectorTop != defaultCandidates || store.gotTextTop != defaultCandidates {
		t.Errorf("candidate limits = %d/%d, want %d", store.gotVectorTop, store.gotTextTop, defaultCandidates)
	}
	// 嵌入只调一次：两路共用同一个查询向量。
	if embedder.calls != 1 {
		t.Errorf("embedding calls = %d, want 1", embedder.calls)
	}
}

func TestSearchMarksDegradedWhenVectorPathEmpty(t *testing.T) {
	store := &fakeSearchStore{textHits: []Hit{hit(7, 0)}}
	got, err := NewSearcher(store, newFakeEmbedder()).Search(context.Background(), "redis 集群", 8, DocumentFilter{})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if !got.Degraded {
		t.Error("Degraded must be true when only the text path matched")
	}
	if got.MaxSimilarity != 0 {
		t.Errorf("MaxSimilarity = %v, want 0", got.MaxSimilarity)
	}
}

func TestSearchClampsLimit(t *testing.T) {
	// 候选数固定 30，limit 只影响最后截断，所以必须让替身真的返回 30 条
	// 才能看出压缩效果——返回空切片时这个用例会静默通过，等于没测。
	candidates := make([]Hit, defaultCandidates)
	for i := range candidates {
		candidates[i] = hit(int64(i+1), 0.5)
	}
	store := &fakeSearchStore{vectorHits: candidates}
	searcher := NewSearcher(store, newFakeEmbedder())

	got, err := searcher.Search(context.Background(), "q", 0, DocumentFilter{})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got.Hits) != defaultSearchLimit {
		t.Errorf("limit 0 -> %d hits, want the default %d", len(got.Hits), defaultSearchLimit)
	}

	got, err = searcher.Search(context.Background(), "q", 999, DocumentFilter{})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got.Hits) != maxSearchLimit {
		t.Errorf("limit 999 -> %d hits, want the cap %d", len(got.Hits), maxSearchLimit)
	}
}

func TestSearchPropagatesStoreErrors(t *testing.T) {
	wantErr := errors.New("db down")
	store := &fakeSearchStore{vectorErr: wantErr}
	if _, err := NewSearcher(store, newFakeEmbedder()).Search(context.Background(), "q", 8, DocumentFilter{}); !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want the store error", err)
	}
	// 向量路失败时不能继续跑全文路：那会返回一份看似正常但缺了一半的结果。
	if store.textQueries != 0 {
		t.Errorf("text path ran %d times after vector path failed", store.textQueries)
	}
}
