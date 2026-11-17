package chatmodel

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// chatModelColumns 显式列出列名。
//
// 不写 SELECT *：以后加列时，读取路径不会连带把新列搬进内存
// （比如真加上"加密前的备份"这种字段，绝不能顺手读出来）。
var chatModelColumns = []string{
	"id", "owner_id", "name", "base_url", "model", "api_key_cipher", "api_key_hint",
	"temperature", "max_tokens", "is_default", "last_used_at",
	"created_at", "updated_at",
}

// errDuplicate 表示唯一索引冲突（重名，或第二个默认）。
//
// 内部错误，由 service 翻成"重名"这类可读消息：同一个 ErrDuplicate 在
// 两种场景下含义不同，repository 不该猜是哪一个。
var errDuplicate = errors.New("chat model row conflicts with an existing one")

// Repository 是 chat_models 的数据访问层。
type Repository struct {
	db *gorm.DB
}

// NewRepository 创建仓储。db 由 cmd/server 注入，仓储内部不建连接。
func NewRepository(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

// List 返回全部配置，默认项排最前，其余按名字排序。
//
// 排序而不是按创建时间：下拉框里名字有序，人才能快速找到要的那条；
// 默认项置顶则是"打开就能点发送"的前提。
func (r *Repository) List(ctx context.Context, ownerID int64) ([]ChatModel, error) {
	items := make([]ChatModel, 0, 4)
	err := r.db.WithContext(ctx).
		Select(chatModelColumns).
		Where("owner_id = ?", ownerID).
		Order("is_default DESC, name ASC, id ASC").
		Find(&items).Error
	if err != nil {
		return nil, translateError("list chat models", err)
	}
	return items, nil
}

// Get 按 id 读取一条配置。
func (r *Repository) Get(ctx context.Context, ownerID, id int64) (*ChatModel, error) {
	var item ChatModel
	err := r.db.WithContext(ctx).
		Select(chatModelColumns).
		Where("owner_id = ? AND id = ?", ownerID, id).
		Take(&item).Error
	if err != nil {
		return nil, translateError("get chat model", err)
	}
	return &item, nil
}

// Count 返回配置总数，用于"第一条自动成为默认"的判断。
func (r *Repository) Count(ctx context.Context, ownerID int64) (int64, error) {
	var total int64
	if err := r.db.WithContext(ctx).Model(&ChatModel{}).
		Where("owner_id = ?", ownerID).Count(&total).Error; err != nil {
		return 0, translateError("count chat models", err)
	}
	return total, nil
}

// Create 插入一条配置，自增 id 由数据库回填到 m.ID。
func (r *Repository) Create(ctx context.Context, m *ChatModel) error {
	if err := r.db.WithContext(ctx).Create(m).Error; err != nil {
		return translateError("create chat model", err)
	}
	return nil
}

// Update 用给定行的字段整体覆盖一条配置。
//
// 为什么用 map 而不是 Save：Save 会把没填的字段写成零值（包括 is_default），
// 而"改一个名字"不该顺手把默认标记清掉。map 里显式列出要改的列，
// 意图明确；temperature / max_tokens 传 nil 就写 NULL，正是"不设置"的语义。
func (r *Repository) Update(ctx context.Context, m *ChatModel) error {
	res := r.db.WithContext(ctx).Model(&ChatModel{}).Where("owner_id = ? AND id = ?", m.OwnerID, m.ID).
		Updates(map[string]any{
			"name":           m.Name,
			"base_url":       m.BaseURL,
			"model":          m.Model,
			"api_key_cipher": m.APIKeyCipher,
			"api_key_hint":   m.APIKeyHint,
			"temperature":    m.Temperature,
			"max_tokens":     m.MaxTokens,
			"updated_at":     time.Now().UTC(),
		})
	if res.Error != nil {
		return translateError("update chat model", res.Error)
	}
	if res.RowsAffected == 0 {
		// 条件更新命中 0 行有两种可能：id 不存在，或字段值与原来完全一样
		// （PostgreSQL 的 UPDATE 仍会返回 1，只有行不存在才是 0）。
		return ErrNotFound
	}
	return nil
}

// Delete 删除一条配置。历史问答记录不会受影响——qa_runs 只存模型名快照。
func (r *Repository) Delete(ctx context.Context, ownerID, id int64) error {
	res := r.db.WithContext(ctx).Where("owner_id = ? AND id = ?", ownerID, id).Delete(&ChatModel{})
	if res.Error != nil {
		return translateError("delete chat model", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// SetDefault 把某条设为默认，同时把其它条取消。
//
// 必须在事务里"先全清、再置一"：chat_models_default_uniq 是部分唯一索引，
// 直接置 true 会在旧默认还在时撞索引。清理与置位分开写而不是一条
// `SET is_default = (id = ?)`，是因为后者在"两行同时为真的中间状态"上
// 依赖 PostgreSQL 的更新顺序，不保证索引不被触发。
func (r *Repository) SetDefault(ctx context.Context, ownerID, id int64) error {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		// 只清自己那一条默认：别人的默认项与他无关，动它就是越权。
		if err := tx.Model(&ChatModel{}).Where("owner_id = ? AND is_default = ?", ownerID, true).
			Updates(map[string]any{"is_default": false, "updated_at": now}).Error; err != nil {
			return err
		}
		res := tx.Model(&ChatModel{}).Where("owner_id = ? AND id = ?", ownerID, id).
			Updates(map[string]any{"is_default": true, "updated_at": now})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrNotFound
		}
		return nil
	})
	if err != nil {
		return translateError("set default chat model", err)
	}
	return nil
}

// Touch 记录"最近使用时间"。
//
// 用 UpdateColumn 而不是 Update：它跳过钩子、也不改 updated_at。
// "用过一次"不是"配置被修改"，混在一起会让界面上"更新时间"失去意义。
func (r *Repository) Touch(ctx context.Context, ownerID, id int64) error {
	err := r.db.WithContext(ctx).Model(&ChatModel{}).Where("owner_id = ? AND id = ?", ownerID, id).
		UpdateColumn("last_used_at", time.Now().UTC()).Error
	if err != nil {
		return translateError("touch chat model", err)
	}
	return nil
}

// Resolve 选出本次请求要用哪条配置。
//
// 规则顺序（docs/design/rag.md §7.1）：
//  1. 调用方指定了 id → 就用它，不存在则报错；
//  2. 有 is_default → 用它；
//  3. 只有一条 → 用它（唯一一条就是默认，不必让用户多点一次）；
//  4. 多条且无默认 → ErrNotSelected，让用户先选。
//
// 绝不"取最新的那条"：静默选错模型，用户会以为是回答质量问题，
// 而真正的原因是系统替他做了决定。
func (r *Repository) Resolve(ctx context.Context, ownerID int64, id *int64) (*ChatModel, error) {
	if id != nil {
		return r.Get(ctx, ownerID, *id)
	}
	items, err := r.List(ctx, ownerID)
	if err != nil {
		return nil, err
	}
	switch len(items) {
	case 0:
		return nil, ErrNotConfigured
	case 1:
		return &items[0], nil
	}
	for i := range items {
		if items[i].IsDefault {
			return &items[i], nil
		}
	}
	return nil, ErrNotSelected
}

// translateError 把驱动层错误翻成模块内可判断的错误。
func translateError(op string, err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		return ErrNotFound
	case errors.Is(err, gorm.ErrDuplicatedKey):
		return errDuplicate
	default:
		return fmt.Errorf("%s: %w", op, err)
	}
}
