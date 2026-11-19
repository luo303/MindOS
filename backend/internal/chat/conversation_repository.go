package chat

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/luo303/mindos/backend/internal/outbox"
)

// ConversationRepository 是会话与消息的数据访问层。
//
// 所有查询都带 owner_id：跨租户访问返回 ErrConversationNotFound，
// 不区分"不存在"和"不属于你"。
type ConversationRepository struct {
	db *gorm.DB
}

// NewConversationRepository 创建仓储。db 由 cmd/server 注入。
func NewConversationRepository(db *gorm.DB) *ConversationRepository {
	return &ConversationRepository{db: db}
}

// Create 写入一个空会话。
func (r *ConversationRepository) Create(ctx context.Context, conv *Conversation) error {
	if err := r.db.WithContext(ctx).Create(conv).Error; err != nil {
		return fmt.Errorf("create conversation: %w", err)
	}
	return nil
}

// List 按最近更新时间倒序列出会话，最多 100 条。
func (r *ConversationRepository) List(ctx context.Context, ownerID int64, limit int) ([]Conversation, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	var out []Conversation
	err := r.db.WithContext(ctx).
		Select("id", "owner_id", "title", "message_count", "created_at", "updated_at").
		Where("owner_id = ?", ownerID).
		Order("updated_at DESC").
		Limit(limit).
		Find(&out).Error
	if err != nil {
		return nil, fmt.Errorf("list conversations: %w", err)
	}
	return out, nil
}

// Get 按"拥有者 + id"读取会话。
func (r *ConversationRepository) Get(ctx context.Context, ownerID, id int64) (*Conversation, error) {
	var conv Conversation
	err := r.db.WithContext(ctx).
		Where("owner_id = ? AND id = ?", ownerID, id).
		Take(&conv).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrConversationNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get conversation %d: %w", id, err)
	}
	return &conv, nil
}

// AppendMessage 原子地追加一条消息并分配 seq。
//
// 锁住会话行再读 message_count：两个请求同时发消息时，seq 不会重复，
// 也不会出现"先查再改"的竞态。
func (r *ConversationRepository) AppendMessage(
	ctx context.Context, ownerID, conversationID int64,
	role, content string, citations RawJSON,
) (*Message, error) {
	var msg Message
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var conv Conversation
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("owner_id = ? AND id = ?", ownerID, conversationID).
			Take(&conv).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrConversationNotFound
		}
		if err != nil {
			return err
		}

		seq := conv.MessageCount + 1
		title := conv.Title
		// "新会话" 是 CreateConversation 的占位标题：第一条用户消息到了就替换。
		if (title == "" || title == "新会话") && role == RoleUser {
			title = titleFrom(content)
		}
		if len(citations) == 0 {
			citations = RawJSON("[]")
		}
		msg = Message{
			ConversationID: conversationID,
			OwnerID:        ownerID,
			Role:           role,
			Content:        content,
			Citations:      citations,
			Seq:            seq,
			TokenCount:     estimateTokens(content),
		}
		if err := tx.Create(&msg).Error; err != nil {
			return err
		}
		if err := tx.Model(&Conversation{}).
			Where("owner_id = ? AND id = ?", ownerID, conversationID).
			Updates(map[string]any{
				"message_count": seq,
				"title":         title,
				"updated_at":    time.Now().UTC(),
			}).Error; err != nil {
			return err
		}
		// 先索引正文（关键词立即可用），再异步补向量。
		payload := map[string]any{
			"message_id":      msg.ID,
			"conversation_id": conversationID,
			"owner_id":        ownerID,
		}
		msgKey := strconv.FormatInt(msg.ID, 10)
		if err := outbox.AppendTx(ctx, tx, outbox.New(SearchMessageUpsert, msgKey, payload)); err != nil {
			return err
		}
		return outbox.AppendTx(ctx, tx, outbox.New(SearchMessageEmbed, msgKey, payload))
	})
	if err != nil {
		if errors.Is(err, ErrConversationNotFound) {
			return nil, err
		}
		return nil, fmt.Errorf("append message: %w", err)
	}
	return &msg, nil
}

// MessagesBefore 返回 seq < beforeSeq 的最近 limit 条消息，按 seq 升序。
//
// 热层历史用它：传入当前 user 消息的 seq，拿到它之前的对话。
func (r *ConversationRepository) MessagesBefore(
	ctx context.Context, ownerID, conversationID int64, beforeSeq, limit int,
) ([]Message, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	var out []Message
	err := r.db.WithContext(ctx).
		Where("owner_id = ? AND conversation_id = ? AND seq < ?", ownerID, conversationID, beforeSeq).
		Order("seq DESC").
		Limit(limit).
		Find(&out).Error
	if err != nil {
		return nil, fmt.Errorf("list messages: %w", err)
	}
	reverseMessages(out)
	return out, nil
}

// LatestSummary 读最新一版摘要；没有摘要时返回 (nil, nil)。
func (r *ConversationRepository) LatestSummary(ctx context.Context, ownerID, conversationID int64) (*ConversationSummary, error) {
	var summary ConversationSummary
	err := r.db.WithContext(ctx).
		Where("owner_id = ? AND conversation_id = ?", ownerID, conversationID).
		Order("version DESC").
		Take(&summary).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("latest summary: %w", err)
	}
	return &summary, nil
}

// MessagesBySeqs 按 seq 批量取消息，只返回属于该会话/用户的行。
//
// 冷层命中单条消息后用它补 seq±1，不依赖派生轮次表。
func (r *ConversationRepository) MessagesBySeqs(ctx context.Context, ownerID, conversationID int64, seqs []int) ([]Message, error) {
	if len(seqs) == 0 {
		return nil, nil
	}
	var out []Message
	err := r.db.WithContext(ctx).
		Where("owner_id = ? AND conversation_id = ? AND seq IN ?", ownerID, conversationID, seqs).
		Order("seq ASC").
		Find(&out).Error
	if err != nil {
		return nil, fmt.Errorf("list messages by seqs: %w", err)
	}
	return out, nil
}

// ListMessages 返回最近的 limit 条消息（可按 beforeSeq 翻页），按 seq 升序。
func (r *ConversationRepository) ListMessages(
	ctx context.Context, ownerID, conversationID int64, limit, beforeSeq int,
) ([]Message, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	q := r.db.WithContext(ctx).
		Where("owner_id = ? AND conversation_id = ?", ownerID, conversationID)
	if beforeSeq > 0 {
		q = q.Where("seq < ?", beforeSeq)
	}
	var out []Message
	if err := q.Order("seq DESC").Limit(limit).Find(&out).Error; err != nil {
		return nil, fmt.Errorf("list messages: %w", err)
	}
	reverseMessages(out)
	return out, nil
}

// reverseMessages 把"倒序取最近 N 条"翻回时间顺序。
func reverseMessages(msgs []Message) {
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
}

// titleFrom 用首条用户消息生成会话标题。
func titleFrom(content string) string {
	runes := []rune(content)
	const max = 40
	if len(runes) > max {
		return string(runes[:max]) + "…"
	}
	return content
}
