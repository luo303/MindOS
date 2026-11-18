package knowledge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/elastic/go-elasticsearch/v8"
)

// SearcherAPI 是 search_knowledge 依赖的检索能力。
//
// *Searcher（PG 原生）与 *ESSearcher（ES 读模型）都满足它；
// cmd/server 按 SEARCH_BACKEND 选择实现，agent/chat 不需要知道区别。
type SearcherAPI interface {
	Search(ctx context.Context, ownerID int64, query string, limit int, filter DocumentFilter) (*SearchResult, error)
}

var _ SearcherAPI = (*Searcher)(nil)
var _ SearcherAPI = (*ESSearcher)(nil)

// ESSearcher 用 Elasticsearch 做 BM25 + kNN + 应用侧 RRF。
//
// ES 不是事实库：它可能滞后、可能有孤儿；调用方拿到 hit 后仍要回 PG
// 校验引用。这里只负责"给出候选与名次"。
type ESSearcher struct {
	client   *elasticsearch.Client
	embedder Embedder
	index    string // 读别名，例如 mindos-chunks
}

// NewESSearcher 组装 ES 检索器。indexPrefix 为空时用 mindos。
func NewESSearcher(client *elasticsearch.Client, embedder Embedder, indexPrefix string) *ESSearcher {
	if indexPrefix == "" {
		indexPrefix = "mindos"
	}
	return &ESSearcher{client: client, embedder: embedder, index: indexPrefix + "-chunks"}
}

// Search 执行一次混合检索，只在该拥有者的 ready 文档里找。
func (s *ESSearcher) Search(ctx context.Context, ownerID int64, query string, limit int, filter DocumentFilter) (*SearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, ErrEmptyQuery
	}
	if s.embedder == nil {
		return nil, ErrEmbeddingNotConfigured
	}
	if limit <= 0 {
		limit = defaultSearchLimit
	}
	if limit > maxSearchLimit {
		limit = maxSearchLimit
	}

	vectors, _, err := s.embedder.Embed(ctx, []string{query})
	if err != nil {
		return nil, mapEmbedError(err)
	}
	if len(vectors) != 1 {
		return nil, fmt.Errorf("knowledge: embedding returned %d vectors for 1 query", len(vectors))
	}

	// 两路各取 defaultCandidates 条，与原生实现保持一致。
	semantic, err := s.searchKNN(ctx, ownerID, vectors[0], s.embedder.Model(), filter)
	if err != nil {
		return nil, err
	}
	keyword, err := s.searchBM25(ctx, ownerID, query, filter)
	if err != nil {
		return nil, err
	}

	return fuseESHits(semantic, keyword, limit), nil
}

type esSource struct {
	ChunkID     int64  `json:"chunk_id"`
	DocumentID  int64  `json:"document_id"`
	Seq         int    `json:"seq"`
	StartOffset int    `json:"start_offset"`
	EndOffset   int    `json:"end_offset"`
	Title       string `json:"title"`
	Heading     string `json:"heading"`
	Content     string `json:"content"`
	EmbedModel  string `json:"embed_model"`
}

type esHit struct {
	Score  float64  `json:"_score"`
	Source esSource `json:"_source"`
}

type esResponseHits struct {
	Hits struct {
		Hits []esHit `json:"hits"`
	} `json:"hits"`
}

var esSourceFields = []string{
	"chunk_id", "document_id", "seq", "start_offset", "end_offset",
	"title", "heading", "content", "embed_model",
}

func (s *ESSearcher) searchKNN(ctx context.Context, ownerID int64, vector []float32, model string, filter DocumentFilter) ([]esHit, error) {
	filters := esFilters(ownerID, filter, model)
	body := map[string]any{
		"size":    defaultCandidates,
		"_source": esSourceFields,
		"knn": map[string]any{
			"field":          "embedding",
			"query_vector":   vector,
			"k":              defaultCandidates,
			"num_candidates": 100,
			"filter":         filters,
		},
	}
	return s.doSearch(ctx, body)
}

func (s *ESSearcher) searchBM25(ctx context.Context, ownerID int64, query string, filter DocumentFilter) ([]esHit, error) {
	body := map[string]any{
		"size":    defaultCandidates,
		"_source": esSourceFields,
		"query": map[string]any{
			"bool": map[string]any{
				"filter": esFilters(ownerID, filter, ""),
				"must": []any{
					map[string]any{
						"multi_match": map[string]any{
							"query":  query,
							"fields": []string{"title^2", "content"},
						},
					},
				},
			},
		},
	}
	return s.doSearch(ctx, body)
}

// esFilters 是所有 ES 查询的租户/可见性边界。
//
// 这个函数是唯一允许构造 owner 过滤的地方；新增查询时必须调用它。
func esFilters(ownerID int64, filter DocumentFilter, embedModel string) []any {
	filters := []any{
		map[string]any{"term": map[string]any{"owner_id": ownerID}},
		// 检索永远只看 ready 文档，不交给调用方决定。
		map[string]any{"term": map[string]any{"status": "ready"}},
	}
	if embedModel != "" {
		filters = append(filters, map[string]any{"term": map[string]any{"embed_model": embedModel}})
	}
	if filter.Collection != nil && *filter.Collection != "" {
		filters = append(filters, map[string]any{"term": map[string]any{"collection": *filter.Collection}})
	}
	if filter.Tag != nil && *filter.Tag != "" {
		filters = append(filters, map[string]any{"term": map[string]any{"tags": *filter.Tag}})
	}
	return filters
}

func (s *ESSearcher) doSearch(ctx context.Context, body map[string]any) ([]esHit, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal es query: %w", err)
	}
	res, err := esRequest(ctx, s.client, http.MethodPost, "/"+s.index+"/_search", string(raw))
	if err != nil {
		return nil, fmt.Errorf("search elasticsearch: %w", err)
	}
	if res.status >= 300 {
		return nil, fmt.Errorf("search elasticsearch: status %d: %s", res.status, res.body)
	}
	var parsed esResponseHits
	if err := json.Unmarshal([]byte(res.body), &parsed); err != nil {
		return nil, fmt.Errorf("decode es response: %w", err)
	}
	return parsed.Hits.Hits, nil
}

// fuseESHits 在 Go 侧做 RRF，保持与 PG 原生实现相同的排序语义。
//
// kNN 的 _score 是 (1+cosine)/2，换算回 cosine 供前端展示；
// BM25 没有余弦，Similarity 保持 0，前端显示"仅字面命中"。
func fuseESHits(semantic, keyword []esHit, limit int) *SearchResult {
	scores := make(map[int64]float64, len(semantic)+len(keyword))
	merged := make(map[int64]*Hit, len(semantic)+len(keyword))
	maxSimilarity := 0.0

	for i, item := range semantic {
		id := item.Source.ChunkID
		cosine := 2*item.Score - 1
		if cosine < -1 {
			cosine = -1
		}
		if cosine > 1 {
			cosine = 1
		}
		if cosine > maxSimilarity {
			maxSimilarity = cosine
		}
		hit := toKnowledgeHit(item.Source)
		hit.Similarity = cosine
		merged[id] = &hit
		scores[id] += 1.0 / (60.0 + float64(i+1))
	}
	for i, item := range keyword {
		id := item.Source.ChunkID
		hit, ok := merged[id]
		if !ok {
			copied := toKnowledgeHit(item.Source)
			hit = &copied
			merged[id] = hit
		}
		hit.TextRank = item.Score
		scores[id] += 1.0 / (60.0 + float64(i+1))
	}

	ids := make([]int64, 0, len(scores))
	for id := range scores {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if scores[ids[i]] != scores[ids[j]] {
			return scores[ids[i]] > scores[ids[j]]
		}
		return ids[i] < ids[j]
	})
	if len(ids) > limit {
		ids = ids[:limit]
	}

	out := make([]Hit, 0, len(ids))
	for _, id := range ids {
		hit := merged[id]
		hit.RRFScore = scores[id]
		out = append(out, *hit)
	}
	return &SearchResult{
		Hits:          out,
		VectorHits:    len(semantic),
		TextHits:      len(keyword),
		MaxSimilarity: maxSimilarity,
		Degraded:      len(semantic) == 0,
	}
}

func toKnowledgeHit(src esSource) Hit {
	return Hit{
		DocumentID:  src.DocumentID,
		ChunkID:     src.ChunkID,
		Seq:         src.Seq,
		Title:       src.Title,
		Heading:     src.Heading,
		Content:     src.Content,
		StartOffset: src.StartOffset,
		EndOffset:   src.EndOffset,
	}
}
