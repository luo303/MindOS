package memory

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Repository 管理偏好、候选和修订的持久化。
type Repository struct{ db *gorm.DB }

// NewRepository 创建偏好仓储。
func NewRepository(db *gorm.DB) *Repository { return &Repository{db: db} }

const memoryColumns = "id, owner_id, key, value, status, version, updated_at"
const candidateColumns = "id, owner_id, conversation_id, message_id, key, value, evidence, base_version, status, created_at"

func (repo *Repository) list(ctx context.Context, ownerID int64) ([]Memory, error) {
	items := make([]Memory, 0, 3)
	err := repo.db.WithContext(ctx).Select(memoryColumns).Where("owner_id = ?", ownerID).Order("key").Find(&items).Error
	return items, err
}

func (repo *Repository) versions(ctx context.Context, ownerID, id int64, before int) ([]Version, error) {
	var existing int64
	if err := repo.db.WithContext(ctx).Model(&Memory{}).Where("owner_id = ? AND id = ?", ownerID, id).Count(&existing).Error; err != nil {
		return nil, err
	}
	if existing == 0 {
		return nil, ErrNotFound
	}
	rows := make([]Version, 0)
	query := repo.db.WithContext(ctx).Select("memory_id, version, value, status, source, message_id, conversation_id, created_at").
		Where("owner_id = ? AND memory_id = ?", ownerID, id)
	if before > 0 {
		query = query.Where("version < ?", before)
	}
	err := query.Order("version DESC").Limit(50).Find(&rows).Error
	return rows, err
}

func (repo *Repository) candidates(ctx context.Context, ownerID, conversationID int64) ([]Candidate, error) {
	rows := make([]Candidate, 0)
	query := repo.db.WithContext(ctx).Select(candidateColumns).Where("owner_id = ? AND status = 'pending'", ownerID)
	if conversationID > 0 {
		query = query.Where("conversation_id = ?", conversationID)
	}
	err := query.Order("id DESC").Limit(50).Find(&rows).Error
	return rows, err
}

func (repo *Repository) addCandidate(ctx context.Context, candidate Candidate, sourceAt time.Time) error {
	var current Memory
	err := repo.db.WithContext(ctx).Select(memoryColumns).
		Where("owner_id = ? AND key = ?", candidate.OwnerID, candidate.Key).Take(&current).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if err == nil {
		// 旧消息即使排队很久，也不能基于用户在此之后手动提交的值提议覆盖。
		if current.UpdatedAt.After(sourceAt) || (current.Status == active && current.Value == candidate.Value) {
			return nil
		}
		candidate.BaseVersion = current.Version
	}
	err = repo.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&candidate).Error
	if errors.Is(err, gorm.ErrForeignKeyViolated) {
		return nil // 消费时原会话已被删除，不重试、不保留候选原文。
	}
	return err
}

func (repo *Repository) accept(ctx context.Context, ownerID, candidateID int64, accept bool, editedValue *string) error {
	return repo.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var candidate Candidate
		err := tx.Select(candidateColumns).Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("owner_id = ? AND id = ?", ownerID, candidateID).Take(&candidate).Error
		if err != nil {
			return lookupError(err)
		}
		if candidate.Status != pending {
			return ErrConflict
		}
		status := "rejected"
		if accept {
			value := candidate.Value
			if editedValue != nil {
				value = *editedValue
			}
			if aliases(candidate.Key, value) == nil {
				return ErrInvalid
			}
			origin := Version{Source: "chat", MessageID: &candidate.MessageID, ConversationID: &candidate.ConversationID}
			change := mutation{key: candidate.Key, value: &value, status: active, expected: candidate.BaseVersion}
			if _, err := changeMemory(tx, ownerID, change, origin); err != nil {
				return err
			}
			status = "accepted"
		}
		return tx.Model(&Candidate{}).Where("owner_id = ? AND id = ?", ownerID, candidateID).Update("status", status).Error
	})
}

type mutation struct {
	id       int64
	key      string
	value    *string
	status   string
	expected int
}

func (repo *Repository) manual(ctx context.Context, ownerID int64, change mutation) (Memory, error) {
	var item Memory
	err := repo.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		item, err = changeMemory(tx, ownerID, change, Version{Source: "manual"})
		return err
	})
	return item, err
}

// changeMemory 与版本快照共事务：行锁保护已存在的键，唯一约束保护首次并发创建。
func changeMemory(tx *gorm.DB, ownerID int64, change mutation, origin Version) (Memory, error) {
	if ownerID <= 0 {
		return Memory{}, ErrNotFound
	}
	query := tx.Select(memoryColumns).Clauses(clause.Locking{Strength: "UPDATE"})
	if change.id != 0 {
		query = query.Where("owner_id = ? AND id = ?", ownerID, change.id)
	} else {
		query = query.Where("owner_id = ? AND key = ?", ownerID, change.key)
	}
	var item Memory
	err := query.Take(&item).Error
	if errors.Is(err, gorm.ErrRecordNotFound) && change.id == 0 && change.expected == 0 && change.value != nil {
		item = Memory{OwnerID: ownerID, Key: change.key, Value: *change.value, Status: active, Version: 1, UpdatedAt: time.Now().UTC()}
		insert := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&item)
		if insert.Error != nil {
			return Memory{}, insert.Error
		}
		if insert.RowsAffected == 0 {
			return Memory{}, ErrConflict
		}
	} else if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) && change.id == 0 && change.expected != 0 {
			return Memory{}, ErrConflict
		}
		return Memory{}, lookupError(err)
	} else {
		if item.Version != change.expected {
			return Memory{}, ErrConflict
		}
		value, status := item.Value, item.Status
		if change.value != nil {
			value = *change.value
		}
		if change.status != "" {
			status = change.status
		}
		if aliases(item.Key, value) == nil {
			return Memory{}, ErrInvalid
		}
		if item.Value == value && item.Status == status {
			return item, nil
		}
		item.Value, item.Status, item.Version, item.UpdatedAt = value, status, item.Version+1, time.Now().UTC()
		if err := tx.Model(&Memory{}).Where("owner_id = ? AND id = ?", ownerID, item.ID).
			Updates(map[string]any{"value": value, "status": status, "version": item.Version, "updated_at": item.UpdatedAt}).Error; err != nil {
			return Memory{}, err
		}
	}
	origin.OwnerID, origin.MemoryID, origin.Version = ownerID, item.ID, item.Version
	origin.Value, origin.Status, origin.CreatedAt = item.Value, item.Status, item.UpdatedAt
	if err := tx.Create(&origin).Error; err != nil {
		return Memory{}, fmt.Errorf("append memory version: %w", err)
	}
	return item, nil
}

func lookupError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotFound
	}
	return err
}
