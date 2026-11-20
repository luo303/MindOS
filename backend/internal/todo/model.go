package todo

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"
)

// 待办状态。与迁移里的 CHECK 约束一致。
const (
	StatusOpen       = "open"
	StatusInProgress = "in_progress"
	StatusDone       = "done"
	StatusCancelled  = "cancelled"
)

// 来源。manual 是用户自己创建，ai 是审批通过的 Agent 创建。
const (
	SourceManual = "manual"
	SourceAI     = "ai"
)

// JSON 是 jsonb 列与 map 之间的桥。
//
// 与 chat.RawJSON 同理：只实现 Scan/Value 时，encoding/json 不会出错；
// 这里直接用 map 作为 API 形状，前端拿到的就是对象。
type JSON map[string]any

func (j JSON) Value() (driver.Value, error) {
	if j == nil {
		return "{}", nil
	}
	raw, err := json.Marshal(map[string]any(j))
	if err != nil {
		return nil, fmt.Errorf("marshal todo json: %w", err)
	}
	return string(raw), nil
}

func (j *JSON) Scan(src any) error {
	var raw []byte
	switch v := src.(type) {
	case nil:
		*j = JSON{}
		return nil
	case []byte:
		raw = v
	case string:
		raw = []byte(v)
	default:
		return fmt.Errorf("scan todo json: unsupported type %T", src)
	}
	if len(raw) == 0 {
		*j = JSON{}
		return nil
	}
	return json.Unmarshal(raw, (*map[string]any)(j))
}

// Todo 是用户待办。tasks 表是文档异步作业，不是这张表。
type Todo struct {
	ID          int64      `gorm:"column:id;primaryKey" json:"id"`
	OwnerID     int64      `gorm:"column:owner_id" json:"-"`
	Title       string     `gorm:"column:title" json:"title"`
	Description string     `gorm:"column:description" json:"description"`
	Status      string     `gorm:"column:status" json:"status"`
	Priority    int        `gorm:"column:priority" json:"priority"`
	DueAt       *time.Time `gorm:"column:due_at" json:"due_at,omitempty"`
	Source      string     `gorm:"column:source" json:"source"`
	SourceRef   JSON       `gorm:"column:source_ref;type:jsonb" json:"source_ref"`
	ApprovalID  *int64     `gorm:"column:approval_id" json:"approval_id,omitempty"`
	CreatedAt   time.Time  `gorm:"column:created_at" json:"created_at"`
	UpdatedAt   time.Time  `gorm:"column:updated_at" json:"updated_at"`
	CompletedAt *time.Time `gorm:"column:completed_at" json:"completed_at,omitempty"`
	DeletedAt   *time.Time `gorm:"column:deleted_at" json:"-"`
}

func (Todo) TableName() string { return "todos" }

// Terminal 表示待办是否已结束（完成或取消）。
func (t Todo) Terminal() bool {
	return t.Status == StatusDone || t.Status == StatusCancelled
}
