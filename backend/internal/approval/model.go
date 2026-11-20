package approval

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"
)

const (
	StatusPending   = "pending"
	StatusApproved  = "approved"
	StatusRejected  = "rejected"
	StatusCompleted = "completed"
	StatusExpired   = "expired"
	StatusFailed    = "failed"
)

// JSON 是 jsonb 与 map 之间的桥。
type JSON map[string]any

func (j JSON) Value() (driver.Value, error) {
	if j == nil {
		return "{}", nil
	}
	raw, err := json.Marshal(map[string]any(j))
	if err != nil {
		return nil, fmt.Errorf("marshal approval json: %w", err)
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
		return fmt.Errorf("scan approval json: unsupported type %T", src)
	}
	if len(raw) == 0 {
		*j = JSON{}
		return nil
	}
	return json.Unmarshal(raw, (*map[string]any)(j))
}

// Request 是一条待审批的 AI 写操作。
type Request struct {
	ID             int64      `gorm:"column:id;primaryKey" json:"id"`
	OwnerID        int64      `gorm:"column:owner_id" json:"-"`
	ConversationID *int64     `gorm:"column:conversation_id" json:"conversation_id,omitempty"`
	UserMessageID  *int64     `gorm:"column:user_message_id" json:"user_message_id,omitempty"`
	ChatModelID    *int64     `gorm:"column:chat_model_id" json:"chat_model_id,omitempty"`
	ToolName       string     `gorm:"column:tool_name" json:"tool_name"`
	Arguments      JSON       `gorm:"column:arguments;type:jsonb" json:"arguments"`
	Summary        string     `gorm:"column:summary" json:"summary"`
	Status         string     `gorm:"column:status" json:"status"`
	ExpiresAt      time.Time  `gorm:"column:expires_at" json:"expires_at"`
	DecidedAt      *time.Time `gorm:"column:decided_at" json:"decided_at,omitempty"`
	Result         JSON       `gorm:"column:result;type:jsonb" json:"result"`
	CreatedAt      time.Time  `gorm:"column:created_at" json:"created_at"`
	UpdatedAt      time.Time  `gorm:"column:updated_at" json:"updated_at"`
}

func (Request) TableName() string { return "agent_approvals" }
