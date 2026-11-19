package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/elastic/go-elasticsearch/v8"
)

// MessageSearchHit 是冷层召回的一条消息。
type MessageSearchHit struct {
	MessageID  int64
	Seq        int
	Role       string
	Content    string
	Score      float64
	Similarity float64
}

// MessageSearcher 在 mindos-messages 上做 BM25 + kNN + 应用侧 RRF。
//
// embedder 为 nil 或调用失败时退化为纯关键词，不影响对话主流程。
type MessageSearcher struct {
	client   *elasticsearch.Client
	embedder MessageEmbedder
	index    string
}

// NewMessageSearcher 组装冷层检索器。
func NewMessageSearcher(client *elasticsearch.Client, embedder MessageEmbedder, indexPrefix string) *MessageSearcher {
	if indexPrefix == "" {
		indexPrefix = "mindos"
	}
	return &MessageSearcher{client: client, embedder: embedder, index: indexPrefix + "-messages"}
}

type esMessageSource struct {
	MessageID int64  `json:"message_id"`
	Seq       int    `json:"seq"`
	Role      string `json:"role"`
	Content   string `json:"content"`
}

type esMessageHit struct {
	Score  float64         `json:"_score"`
	Source esMessageSource `json:"_source"`
}

type esMessageResponse struct {
	Hits struct {
		Hits []esMessageHit `json:"hits"`
	} `json:"hits"`
}

// Search 召回 seq < beforeSeq 的消息，返回 RRF 排序后的 limit 条。
func (s *MessageSearcher) Search(ctx context.Context, ownerID, conversationID int64, beforeSeq int, query string, limit int) ([]MessageSearchHit, error) {
	query = strings.TrimSpace(query)
	if query == "" || beforeSeq <= 1 {
		return nil, nil
	}
	if limit <= 0 {
		limit = 20
	}

	keyword, err := s.searchKeyword(ctx, ownerID, conversationID, beforeSeq, query)
	if err != nil {
		return nil, err
	}
	semantic, err := s.searchVector(ctx, ownerID, conversationID, beforeSeq, query)
	if err != nil {
		// 向量失败不阻断：关键词已经能召回。
		semantic = nil
	}
	return fuseMessageHits(semantic, keyword, limit), nil
}

func (s *MessageSearcher) searchKeyword(ctx context.Context, ownerID, conversationID int64, beforeSeq int, query string) ([]esMessageHit, error) {
	body := map[string]any{
		"size":    20,
		"_source": []string{"message_id", "seq", "role", "content"},
		"query": map[string]any{
			"bool": map[string]any{
				"filter": messageFilters(ownerID, conversationID, beforeSeq),
				"must": []any{
					map[string]any{"match": map[string]any{"content": query}},
				},
			},
		},
	}
	return s.doSearch(ctx, body)
}

func (s *MessageSearcher) searchVector(ctx context.Context, ownerID, conversationID int64, beforeSeq int, query string) ([]esMessageHit, error) {
	if s.embedder == nil {
		return nil, nil
	}
	vectors, _, err := s.embedder.Embed(ctx, []string{query})
	if err != nil {
		return nil, err
	}
	if len(vectors) != 1 {
		return nil, fmt.Errorf("message search: got %d vectors", len(vectors))
	}
	body := map[string]any{
		"size":    20,
		"_source": []string{"message_id", "seq", "role", "content"},
		"knn": map[string]any{
			"field":          "embedding",
			"query_vector":   vectors[0],
			"k":              20,
			"num_candidates": 100,
			"filter":         messageVectorFilters(ownerID, conversationID, beforeSeq, s.embedder.Model()),
		},
	}
	return s.doSearch(ctx, body)
}

func messageVectorFilters(ownerID, conversationID int64, beforeSeq int, embedModel string) []any {
	filters := messageFilters(ownerID, conversationID, beforeSeq)
	if embedModel != "" {
		filters = append(filters, map[string]any{"term": map[string]any{"embed_model": embedModel}})
	}
	return filters
}

func messageFilters(ownerID, conversationID int64, beforeSeq int) []any {
	return []any{
		map[string]any{"term": map[string]any{"owner_id": ownerID}},
		map[string]any{"term": map[string]any{"conversation_id": conversationID}},
		map[string]any{"range": map[string]any{"seq": map[string]any{"lt": beforeSeq}}},
	}
}

func (s *MessageSearcher) doSearch(ctx context.Context, body map[string]any) ([]esMessageHit, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	status, resp, err := esCall(ctx, s.client, http.MethodPost, "/"+s.index+"/_search", "application/json", raw)
	if err != nil {
		return nil, err
	}
	if status >= 300 {
		return nil, fmt.Errorf("message search: status %d: %s", status, clipText(resp, 300))
	}
	var parsed esMessageResponse
	if err := json.Unmarshal(resp, &parsed); err != nil {
		return nil, err
	}
	return parsed.Hits.Hits, nil
}

// fuseMessageHits 与知识库一样用 RRF k=60；同分时按 seq 升序（更早优先）。
func fuseMessageHits(semantic, keyword []esMessageHit, limit int) []MessageSearchHit {
	scores := make(map[int64]float64)
	merged := make(map[int64]MessageSearchHit)
	maxSimilarity := make(map[int64]float64)

	for i, item := range semantic {
		id := item.Source.MessageID
		cosine := 2*item.Score - 1
		if cosine < -1 {
			cosine = -1
		}
		if cosine > 1 {
			cosine = 1
		}
		hit := MessageSearchHit{
			MessageID:  id,
			Seq:        item.Source.Seq,
			Role:       item.Source.Role,
			Content:    item.Source.Content,
			Similarity: cosine,
		}
		merged[id] = hit
		maxSimilarity[id] = cosine
		scores[id] += 1.0 / (60.0 + float64(i+1))
	}
	for i, item := range keyword {
		id := item.Source.MessageID
		hit, ok := merged[id]
		if !ok {
			hit = MessageSearchHit{
				MessageID: id,
				Seq:       item.Source.Seq,
				Role:      item.Source.Role,
				Content:   item.Source.Content,
			}
			merged[id] = hit
		}
		hit.Score = item.Score
		merged[id] = hit
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
		return merged[ids[i]].Seq < merged[ids[j]].Seq
	})
	if len(ids) > limit {
		ids = ids[:limit]
	}
	out := make([]MessageSearchHit, 0, len(ids))
	for _, id := range ids {
		hit := merged[id]
		hit.Similarity = maxSimilarity[id]
		out = append(out, hit)
	}
	return out
}
