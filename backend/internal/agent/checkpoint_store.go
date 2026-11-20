package agent

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
)

type CheckpointStore struct{ db *gorm.DB }

func NewCheckpointStore(db *gorm.DB) *CheckpointStore { return &CheckpointStore{db: db} }

type checkpointRow struct {
	CheckpointID string
	OwnerID      int64
	Payload      []byte
	ExpiresAt    time.Time
}

func (checkpointRow) TableName() string { return "agent_checkpoints" }

func (s *CheckpointStore) Get(ctx context.Context, checkpointID string) ([]byte, bool, error) {
	var row checkpointRow
	err := s.db.WithContext(ctx).Where("checkpoint_id = ? AND expires_at > now()", checkpointID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("get checkpoint %s: %w", checkpointID, err)
	}
	return row.Payload, true, nil
}

func (s *CheckpointStore) Set(ctx context.Context, checkpointID string, payload []byte) error {
	meta := RunMetaFrom(ctx)
	now := time.Now().UTC()
	expires := now.Add(7 * 24 * time.Hour)
	updates := map[string]any{"payload": payload, "expires_at": expires, "updated_at": now}
	if meta.OwnerID > 0 {
		updates["owner_id"] = meta.OwnerID
	}
	if meta.ConversationID > 0 {
		updates["conversation_id"] = meta.ConversationID
	}
	res := s.db.WithContext(ctx).Model(&checkpointRow{}).Where("checkpoint_id = ?", checkpointID).Updates(updates)
	if res.Error != nil {
		return fmt.Errorf("update checkpoint %s: %w", checkpointID, res.Error)
	}
	if res.RowsAffected == 0 {
		if err := s.db.WithContext(ctx).Create(&checkpointRow{CheckpointID: checkpointID, OwnerID: meta.OwnerID, Payload: payload, ExpiresAt: expires}).Error; err != nil {
			return fmt.Errorf("insert checkpoint %s: %w", checkpointID, err)
		}
	}
	return nil
}

func (s *CheckpointStore) Delete(ctx context.Context, checkpointID string) error {
	return s.db.WithContext(ctx).Where("checkpoint_id = ?", checkpointID).Delete(&checkpointRow{}).Error
}
