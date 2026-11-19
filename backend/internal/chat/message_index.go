package chat

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
	"github.com/pgvector/pgvector-go"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// MessageEmbedder 是消息向量化需要的最小能力；*platform.Embedding 满足它。
type MessageEmbedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, int64, error)
	Model() string
}

// MessageIndexer 把 messages 写进 ES 读模型，并负责写入时补向量。
type MessageIndexer struct {
	db       *gorm.DB
	client   *elasticsearch.Client
	index    string
	embedder MessageEmbedder
	log      *zap.Logger
}

// NewMessageIndexer 组装消息索引器。indexPrefix 为空时用 mindos。
func NewMessageIndexer(db *gorm.DB, client *elasticsearch.Client, indexPrefix string, embedder MessageEmbedder, log *zap.Logger) *MessageIndexer {
	if indexPrefix == "" {
		indexPrefix = "mindos"
	}
	if log == nil {
		log = zap.NewNop()
	}
	return &MessageIndexer{db: db, client: client, index: indexPrefix + "-messages", embedder: embedder, log: log}
}

type indexedMessage struct {
	ID             int64
	ConversationID int64
	OwnerID        int64
	Role           string
	Content        string
	Seq            int
	EmbeddingModel *string
	EmbeddingText  *string
	CreatedAt      time.Time
}

// Upsert 重建一条消息的 ES 文档；消息不存在（会话已删除）时清掉索引。
func (m *MessageIndexer) Upsert(ctx context.Context, messageID int64) error {
	var msg indexedMessage
	err := m.db.WithContext(ctx).Raw(
		"SELECT id, conversation_id, owner_id, role, content, seq, "+
			"embedding_model, embedding::text AS embedding_text, created_at "+
			"FROM messages WHERE id = ?", messageID).Scan(&msg).Error
	if errors.Is(err, gorm.ErrRecordNotFound) || (err == nil && msg.ID == 0) {
		return m.Delete(ctx, messageID)
	}
	if err != nil {
		return fmt.Errorf("load message %d: %w", messageID, err)
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(map[string]any{
		"index": map[string]any{"_index": m.index, "_id": strconv.FormatInt(msg.ID, 10)},
	}); err != nil {
		return err
	}
	source := map[string]any{
		"message_id":      msg.ID,
		"conversation_id": msg.ConversationID,
		"owner_id":        msg.OwnerID,
		"role":            msg.Role,
		"content":         msg.Content,
		"seq":             msg.Seq,
		"created_at":      msg.CreatedAt.UTC().Format(time.RFC3339),
	}
	if msg.EmbeddingModel != nil {
		source["embed_model"] = *msg.EmbeddingModel
	}
	if msg.EmbeddingText != nil && *msg.EmbeddingText != "" {
		var vector []float32
		if err := json.Unmarshal([]byte(*msg.EmbeddingText), &vector); err == nil {
			source["embedding"] = vector
		}
	}
	if err := enc.Encode(source); err != nil {
		return err
	}

	status, body, err := esCall(ctx, m.client, http.MethodPost, "/_bulk?refresh=wait_for", "application/x-ndjson", buf.Bytes())
	if err != nil {
		return err
	}
	if status >= 300 {
		return fmt.Errorf("bulk index message %d: status %d: %s", messageID, status, clipText(body, 300))
	}
	var parsed struct{ Errors bool }
	if err := json.Unmarshal(body, &parsed); err != nil {
		return fmt.Errorf("decode message bulk response: %w", err)
	}
	if parsed.Errors {
		return fmt.Errorf("bulk index message %d: %s", messageID, clipText(body, 300))
	}
	return nil
}

// Delete 按 _id 删除一条消息。
func (m *MessageIndexer) Delete(ctx context.Context, messageID int64) error {
	status, body, err := esCall(ctx, m.client, http.MethodDelete,
		"/"+m.index+"/_doc/"+strconv.FormatInt(messageID, 10)+"?refresh=true", "application/json", nil)
	if err != nil {
		return err
	}
	if status >= 300 && status != http.StatusNotFound {
		return fmt.Errorf("delete message %d from es: status %d: %s", messageID, status, clipText(body, 300))
	}
	return nil
}

// Embed 计算一条消息的向量并写回 PG，然后刷新 ES 文档。
//
// 失败只返回错误，messages.embedding 保持 NULL；关键词路仍然可用。
func (m *MessageIndexer) Embed(ctx context.Context, messageID int64) error {
	if m.embedder == nil {
		return nil
	}
	var msg Message
	err := m.db.WithContext(ctx).Where("id = ?", messageID).Take(&msg).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load message %d for embed: %w", messageID, err)
	}
	vectors, _, err := m.embedder.Embed(ctx, []string{msg.Content})
	if err != nil {
		return fmt.Errorf("embed message %d: %w", messageID, err)
	}
	if len(vectors) != 1 {
		return fmt.Errorf("embed message %d: got %d vectors", messageID, len(vectors))
	}
	if err := m.db.WithContext(ctx).Exec(
		"UPDATE messages SET embedding = CAST(? AS vector), embedding_model = ?, embedded_at = ? WHERE id = ?",
		pgvector.NewVector(vectors[0]).String(), m.embedder.Model(), time.Now().UTC(), messageID).Error; err != nil {
		return fmt.Errorf("save message %d embedding: %w", messageID, err)
	}
	return m.Upsert(ctx, messageID)
}

func esCall(ctx context.Context, client *elasticsearch.Client, method, path, contentType string, body []byte) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, path, reader)
	if err != nil {
		return 0, nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	res, err := client.Perform(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return 0, nil, err
	}
	return res.StatusCode, raw, nil
}

func clipText(raw []byte, n int) string {
	if len(raw) <= n {
		return string(raw)
	}
	return string(raw[:n]) + "..."
}
