package approval

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
)

var ErrNotFound = errors.New("approval not found")

// ErrPending 表示该会话已有一条待审批动作；同一会话同时最多一条。
var ErrPending = errors.New("approval already pending for conversation")

type Repository struct{ db *gorm.DB }

func NewRepository(db *gorm.DB) *Repository { return &Repository{db: db} }

func (r *Repository) Create(ctx context.Context, req *Request) error {
	if err := r.db.WithContext(ctx).Create(req).Error; err != nil {
		return fmt.Errorf("create approval: %w", err)
	}
	return nil
}

func (r *Repository) Get(ctx context.Context, ownerID, id int64) (*Request, error) {
	var req Request
	err := r.db.WithContext(ctx).
		Where("owner_id = ? AND id = ?", ownerID, id).
		Take(&req).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get approval %d: %w", id, err)
	}
	return &req, nil
}

// GetByCheckpoint 按 checkpoint 找审批；Agent 中断时用它关联。
func (r *Repository) GetByCheckpoint(ctx context.Context, checkpointID string) (*Request, error) {
	var req Request
	err := r.db.WithContext(ctx).Where("checkpoint_id = ?", checkpointID).Take(&req).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get approval by checkpoint %s: %w", checkpointID, err)
	}
	return &req, nil
}

// ListByConversation 返回会话的审批历史；status 为空或 all 时不过滤。
// HasPending 在创建新审批前做门禁；唯一索引负责并发兜底。
func (r *Repository) HasPending(ctx context.Context, ownerID, conversationID int64) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&Request{}).
		Where("owner_id = ? AND conversation_id = ? AND status = ?", ownerID, conversationID, StatusPending).
		Count(&count).Error
	if err != nil {
		return false, fmt.Errorf("count pending approvals: %w", err)
	}
	return count > 0, nil
}

func (r *Repository) ListByConversation(ctx context.Context, ownerID, conversationID int64, status string) ([]Request, error) {
	query := r.db.WithContext(ctx).Where("owner_id = ? AND conversation_id = ?", ownerID, conversationID)
	if status != "" && status != "all" {
		query = query.Where("status = ?", status)
	}
	var out []Request
	if err := query.Order("created_at ASC").Limit(200).Find(&out).Error; err != nil {
		return nil, fmt.Errorf("list conversation approvals: %w", err)
	}
	return out, nil
}

func (r *Repository) ListPending(ctx context.Context, ownerID int64, conversationID int64) ([]Request, error) {
	query := r.db.WithContext(ctx).
		Where("owner_id = ? AND status = ? AND expires_at > now()", ownerID, StatusPending)
	if conversationID > 0 {
		query = query.Where("conversation_id = ?", conversationID)
	}
	var out []Request
	if err := query.Order("created_at DESC").Limit(100).Find(&out).Error; err != nil {
		return nil, fmt.Errorf("list pending approvals: %w", err)
	}
	return out, nil
}

// SetInterrupt 记录 Eino 的 interrupt ID；审批后恢复需要它。
func (r *Repository) SetInterrupt(ctx context.Context, checkpointID, interruptID string) error {
	return r.db.WithContext(ctx).Model(&Request{}).
		Where("checkpoint_id = ?", checkpointID).
		Updates(map[string]any{"interrupt_id": interruptID, "updated_at": time.Now().UTC()}).Error
}

// Decide 条件更新 pending -> approved/rejected，只有一个请求能抢到。
func (r *Repository) Decide(ctx context.Context, ownerID, id int64, status string) (*Request, error) {
	now := time.Now().UTC()
	res := r.db.WithContext(ctx).Model(&Request{}).
		Where("owner_id = ? AND id = ? AND status = ? AND expires_at > now()", ownerID, id, StatusPending).
		Updates(map[string]any{"status": status, "decided_at": now, "updated_at": now})
	if res.Error != nil {
		return nil, fmt.Errorf("decide approval %d: %w", id, res.Error)
	}
	if res.RowsAffected == 0 {
		return nil, ErrNotFound
	}
	return r.Get(ctx, ownerID, id)
}

func (r *Repository) Complete(ctx context.Context, ownerID, id int64, status string, result JSON) error {
	return r.db.WithContext(ctx).Model(&Request{}).
		Where("owner_id = ? AND id = ?", ownerID, id).
		Updates(map[string]any{
			"status":     status,
			"result":     result,
			"updated_at": time.Now().UTC(),
		}).Error
}
