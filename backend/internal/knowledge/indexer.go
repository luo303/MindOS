package knowledge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/elastic/go-elasticsearch/v8"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// SearchIndexer 把 PG 里的文档/分块写进 ES 读模型。
//
// 只由 outbox 驱动，不参与 HTTP 请求；失败由 dispatch 重试。
type SearchIndexer struct {
	db          *gorm.DB
	client      *elasticsearch.Client
	chunksIndex string // 读别名，例如 mindos-chunks
	log         *zap.Logger
}

// NewSearchIndexer 组装索引器。indexPrefix 为空时用 mindos。
func NewSearchIndexer(db *gorm.DB, client *elasticsearch.Client, indexPrefix string, log *zap.Logger) *SearchIndexer {
	if indexPrefix == "" {
		indexPrefix = "mindos"
	}
	if log == nil {
		log = zap.NewNop()
	}
	return &SearchIndexer{db: db, client: client, chunksIndex: indexPrefix + "-chunks", log: log}
}

// indexChunk 依赖 GORM 的命名策略把列名映射到字段（id/seq/content...），
// 不写 struct tag。
type indexChunk struct {
	ID            int64
	Seq           int
	Content       string
	Heading       *string
	StartOffset   int
	EndOffset     int
	EmbedModel    *string
	EmbeddingText *string
}

// UpsertDocument 重建一个文档在 ES 里的全部分块。
//
// 先按 document_id 清掉旧分块，再 bulk 写当前分块：重索引后 chunk_id 会变，
// 不清理会留下指向旧正文的孤儿。
func (s *SearchIndexer) UpsertDocument(ctx context.Context, documentID int64) error {
	var doc Document
	err := s.db.WithContext(ctx).Where("id = ?", documentID).Take(&doc).Error
	if errors.Is(err, gorm.ErrRecordNotFound) || (err == nil && doc.Status != StatusReady) {
		return s.DeleteDocument(ctx, documentID)
	}
	if err != nil {
		return fmt.Errorf("load document %d: %w", documentID, err)
	}

	var chunks []indexChunk
	err = s.db.WithContext(ctx).Raw(
		"SELECT id, seq, content, heading, start_offset, end_offset, "+
			"embed_model, embedding::text AS embedding_text "+
			"FROM document_chunks WHERE document_id = ? ORDER BY seq ASC", documentID).Scan(&chunks).Error
	if err != nil {
		return fmt.Errorf("load chunks for document %d: %w", documentID, err)
	}
	if len(chunks) == 0 {
		return s.DeleteDocument(ctx, documentID)
	}

	if err := s.deleteByDocument(ctx, documentID); err != nil {
		return err
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	for _, chunk := range chunks {
		meta := map[string]any{
			"index": map[string]any{
				"_index": s.chunksIndex,
				"_id":    strconv.FormatInt(chunk.ID, 10),
			},
		}
		if err := enc.Encode(meta); err != nil {
			return fmt.Errorf("encode bulk meta: %w", err)
		}
		source := map[string]any{
			"chunk_id":     chunk.ID,
			"document_id":  doc.ID,
			"owner_id":     doc.OwnerID,
			"seq":          chunk.Seq,
			"start_offset": chunk.StartOffset,
			"end_offset":   chunk.EndOffset,
			"title":        doc.Title,
			"content":      chunk.Content,
			"heading":      derefString(chunk.Heading),
			"collection":   derefString(doc.Collection),
			"tags":         []string(doc.Tags),
			"status":       doc.Status,
			"updated_at":   doc.UpdatedAt.UTC().Format(time.RFC3339),
		}
		if chunk.EmbedModel != nil {
			source["embed_model"] = *chunk.EmbedModel
		}
		if chunk.EmbeddingText != nil && *chunk.EmbeddingText != "" {
			var vector []float32
			if err := json.Unmarshal([]byte(*chunk.EmbeddingText), &vector); err == nil {
				source["embedding"] = vector
			}
		}
		if err := enc.Encode(source); err != nil {
			return fmt.Errorf("encode bulk source: %w", err)
		}
	}

	res, err := s.bulk(ctx, buf.Bytes())
	if err != nil {
		return err
	}
	var parsed struct {
		Errors bool
	}
	if err := json.Unmarshal([]byte(res.body), &parsed); err != nil {
		return fmt.Errorf("decode bulk response: %w", err)
	}
	if parsed.Errors {
		return fmt.Errorf("bulk index document %d: %s", documentID, clip(res.body, 500))
	}
	s.log.Debug("indexed document", zap.Int64("document_id", documentID), zap.Int("chunks", len(chunks)))
	return nil
}

// DeleteDocument 按 document_id 删除 ES 里的全部分块。
func (s *SearchIndexer) DeleteDocument(ctx context.Context, documentID int64) error {
	return s.deleteByDocument(ctx, documentID)
}

func (s *SearchIndexer) deleteByDocument(ctx context.Context, documentID int64) error {
	body, err := json.Marshal(map[string]any{
		"query": map[string]any{"term": map[string]any{"document_id": documentID}},
	})
	if err != nil {
		return err
	}
	res, err := esRequest(ctx, s.client, http.MethodPost,
		"/"+s.chunksIndex+"/_delete_by_query?conflicts=proceed&refresh=true", string(body))
	if err != nil {
		return fmt.Errorf("delete document %d from es: %w", documentID, err)
	}
	// 404 说明索引还没建；启动时 EnsureSearchIndexes 会处理，这里不当作失败。
	if res.status >= 300 && res.status != http.StatusNotFound {
		return fmt.Errorf("delete document %d from es: status %d: %s", documentID, res.status, clip(res.body, 300))
	}
	return nil
}

// bulk 发送 NDJSON bulk 请求。esRequest 固定 JSON Content-Type，这里需要单独实现。
func (s *SearchIndexer) bulk(ctx context.Context, body []byte) (esResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "/_bulk?refresh=wait_for", bytes.NewReader(body))
	if err != nil {
		return esResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-ndjson")
	res, err := s.client.Perform(req)
	if err != nil {
		return esResponse{}, fmt.Errorf("bulk request: %w", err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return esResponse{}, err
	}
	if res.StatusCode >= 300 {
		return esResponse{}, fmt.Errorf("bulk request: status %d: %s", res.StatusCode, clip(string(raw), 500))
	}
	return esResponse{status: res.StatusCode, body: string(raw)}, nil
}

func derefString(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

// clip 只用于错误信息，避免把整段 bulk 响应塞进日志/重试记录。
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
