package todo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// ErrNotFound 表示待办不存在或不属于当前用户。
var ErrNotFound = errors.New("todo not found")

// Repository 是 todos 的数据访问层。
type Repository struct {
	db *gorm.DB
}

func NewRepository(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

// Filter 是列表筛选条件。
type Filter struct {
	Status    string
	DueBefore *time.Time
	Limit     int
}

func (r *Repository) List(ctx context.Context, ownerID int64, f Filter) ([]Todo, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 100
	}
	query := r.db.WithContext(ctx).
		Select("id", "owner_id", "title", "description", "status", "priority",
			"due_at", "source", "source_ref", "approval_id", "created_at",
			"updated_at", "completed_at").
		Where("owner_id = ? AND deleted_at IS NULL", ownerID)
	if f.Status != "" {
		query = query.Where("status = ?", f.Status)
	}
	if f.DueBefore != nil {
		query = query.Where("due_at IS NOT NULL AND due_at <= ?", *f.DueBefore)
	}
	var out []Todo
	if err := query.Order("status = 'done', due_at NULLS LAST, created_at DESC").Limit(f.Limit).Find(&out).Error; err != nil {
		return nil, fmt.Errorf("list todos: %w", err)
	}
	return out, nil
}

func (r *Repository) Get(ctx context.Context, ownerID, id int64) (*Todo, error) {
	var t Todo
	err := r.db.WithContext(ctx).
		Where("owner_id = ? AND id = ? AND deleted_at IS NULL", ownerID, id).
		Take(&t).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get todo %d: %w", id, err)
	}
	return &t, nil
}

func (r *Repository) Create(ctx context.Context, t *Todo) error {
	if err := r.db.WithContext(ctx).Create(t).Error; err != nil {
		return fmt.Errorf("create todo: %w", err)
	}
	return nil
}

// Update 只更新传入的列；调用方负责校验 owner 与字段。
func (r *Repository) Update(ctx context.Context, ownerID, id int64, updates map[string]any) (*Todo, error) {
	if len(updates) > 0 {
		updates["updated_at"] = time.Now().UTC()
		res := r.db.WithContext(ctx).Model(&Todo{}).
			Where("owner_id = ? AND id = ? AND deleted_at IS NULL", ownerID, id).
			Updates(updates)
		if res.Error != nil {
			return nil, fmt.Errorf("update todo %d: %w", id, res.Error)
		}
		if res.RowsAffected == 0 {
			return nil, ErrNotFound
		}
	}
	return r.Get(ctx, ownerID, id)
}

// SoftDelete 软删除，保留审批/审计关联。
func (r *Repository) SoftDelete(ctx context.Context, ownerID, id int64) error {
	res := r.db.WithContext(ctx).Model(&Todo{}).
		Where("owner_id = ? AND id = ? AND deleted_at IS NULL", ownerID, id).
		Updates(map[string]any{
			"deleted_at": time.Now().UTC(),
			"updated_at": time.Now().UTC(),
		})
	if res.Error != nil {
		return fmt.Errorf("delete todo %d: %w", id, res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}
