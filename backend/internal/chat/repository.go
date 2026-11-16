package chat

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

// Repository 是 qa_runs 的数据访问层。
//
// 本期只有写入与按 id 读取：问答记录不参与回答（那是 messages 的职责），
// 只被复盘与统计查询。加一个用不到的 List 只会是死代码。
type Repository struct {
	db *gorm.DB
}

// NewRepository 创建仓储。db 由 cmd/server 注入，仓储内部不建连接。
func NewRepository(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

// Create 写入一行运行记录，自增 id 回填到 run.ID。
func (r *Repository) Create(ctx context.Context, run *Run) error {
	if err := r.db.WithContext(ctx).Create(run).Error; err != nil {
		return fmt.Errorf("create qa run: %w", err)
	}
	return nil
}

// Get 按 id 读取一行，主要用于集成测试与事后复盘。
func (r *Repository) Get(ctx context.Context, id int64) (*Run, error) {
	var run Run
	err := r.db.WithContext(ctx).Where("id = ?", id).Take(&run).Error
	if err != nil {
		return nil, fmt.Errorf("get qa run %d: %w", id, err)
	}
	return &run, nil
}
