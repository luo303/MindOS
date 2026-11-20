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

// SummaryTopic 是滚动摘要的 outbox topic。
const SummaryTopic = "conversation.summarize"

// summaryClaimTTL 是认领的过期时间；worker 崩溃后最多等这么久再被别人接管。
const summaryClaimTTL = 5 * time.Minute

// SummaryClaim 是一次认领的结果：批次范围、上一版摘要、本批原文。
type SummaryClaim struct {
	ConversationID int64
	OwnerID        int64
	Version        int
	BatchFromSeq   int
	BatchToSeq     int
	Previous       string
	Messages       []Message
}

// ClaimSummary 在短事务里认领一批未摘要消息，不调模型。
func (r *ConversationRepository) ClaimSummary(ctx context.Context, ownerID, conversationID int64) (*SummaryClaim, error) {
	var claim *SummaryClaim
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

		hotStart := conv.MessageCount - historyLimit + 1
		if hotStart < 1 {
			hotStart = 1
		}
		from := conv.SummarizedUntilSeq + 1
		to := from + 11
		if to > hotStart-1 {
			to = hotStart - 1
		}
		if to-from+1 < 12 {
			return nil
		}
		if conv.SummaryClaimedUntil > 0 && conv.SummaryClaimedAt != nil &&
			time.Since(*conv.SummaryClaimedAt) < summaryClaimTTL {
			return nil
		}

		if err := tx.Model(&Conversation{}).
			Where("owner_id = ? AND id = ?", ownerID, conversationID).
			Updates(map[string]any{
				"summary_claimed_until": to,
				"summary_claimed_at":    time.Now().UTC(),
			}).Error; err != nil {
			return err
		}

		previous := ""
		if conv.SummaryVersion > 0 {
			var summary ConversationSummary
			if err := tx.Where("owner_id = ? AND conversation_id = ? AND version = ?",
				ownerID, conversationID, conv.SummaryVersion).Take(&summary).Error; err != nil {
				return err
			}
			previous = summary.Content
		}

		var messages []Message
		if err := tx.Where("owner_id = ? AND conversation_id = ? AND seq BETWEEN ? AND ? AND error_code IS NULL",
			ownerID, conversationID, from, to).
			Order("seq ASC").Find(&messages).Error; err != nil {
			return err
		}

		claim = &SummaryClaim{
			ConversationID: conversationID,
			OwnerID:        ownerID,
			Version:        conv.SummaryVersion + 1,
			BatchFromSeq:   from,
			BatchToSeq:     to,
			Previous:       previous,
			Messages:       messages,
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("claim summary: %w", err)
	}
	return claim, nil
}

// CommitSummary 把模型输出写成新版本，并推进摘要水位。
func (r *ConversationRepository) CommitSummary(ctx context.Context, claim *SummaryClaim, summary string, tokenCount int, model string) error {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var conv Conversation
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("owner_id = ? AND id = ?", claim.OwnerID, claim.ConversationID).
			Take(&conv).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrConversationNotFound
		}
		if err != nil {
			return err
		}
		if conv.SummaryClaimedUntil != claim.BatchToSeq || conv.SummarizedUntilSeq >= claim.BatchToSeq {
			return fmt.Errorf("summary claim no longer valid")
		}

		record := ConversationSummary{
			ConversationID: claim.ConversationID,
			OwnerID:        claim.OwnerID,
			Version:        claim.Version,
			CoveredFromSeq: 1,
			CoveredToSeq:   claim.BatchToSeq,
			BatchFromSeq:   claim.BatchFromSeq,
			BatchToSeq:     claim.BatchToSeq,
			Content:        summary,
			TokenCount:     tokenCount,
			Model:          &model,
		}
		if err := tx.Create(&record).Error; err != nil {
			return err
		}
		return tx.Model(&Conversation{}).
			Where("owner_id = ? AND id = ?", claim.OwnerID, claim.ConversationID).
			Updates(map[string]any{
				"summarized_until_seq":  claim.BatchToSeq,
				"summary_version":       claim.Version,
				"summary_claimed_until": 0,
				"summary_claimed_at":    nil,
			}).Error
	})
	if err != nil {
		return fmt.Errorf("commit summary: %w", err)
	}
	return nil
}

// ReleaseSummary 在模型失败后释放认领，让下次重试能再拿到。
func (r *ConversationRepository) ReleaseSummary(ctx context.Context, conversationID, batchToSeq int64) error {
	return r.db.WithContext(ctx).Model(&Conversation{}).
		Where("id = ? AND summary_claimed_until = ?", conversationID, batchToSeq).
		Updates(map[string]any{
			"summary_claimed_until": 0,
			"summary_claimed_at":    nil,
		}).Error
}

// MaybeEnqueueSummary 在 assistant 消息落库后判断是否够 12 条积压。
//
// 事件可以重复：真正的互斥在 ClaimSummary 的认领标记上。
func (r *ConversationRepository) MaybeEnqueueSummary(ctx context.Context, ownerID, conversationID int64) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var conv Conversation
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("owner_id = ? AND id = ?", ownerID, conversationID).
			Take(&conv).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		hotStart := conv.MessageCount - historyLimit + 1
		if hotStart < 1 {
			hotStart = 1
		}
		backlog := hotStart - conv.SummarizedUntilSeq - 1
		if backlog < 12 {
			return nil
		}
		msg := outbox.New(SummaryTopic, strconv.FormatInt(conversationID, 10), map[string]any{
			"conversation_id": conversationID,
			"owner_id":        ownerID,
		})
		return outbox.AppendTx(ctx, tx, msg)
	})
}
