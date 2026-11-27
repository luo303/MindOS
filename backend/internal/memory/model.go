// Package memory 管理有证据、可修订的长期记忆。
package memory

import "time"

const (
	active   = "active"
	archived = "archived"
	pending  = "pending"
)

// Metadata 描述记忆的整理类别、适用范围和有效期；类别不限制正文内容。
type Metadata struct {
	Category  string     `json:"category"`
	Scope     string     `json:"scope"`
	ExpiresAt *time.Time `json:"expires_at"`
	Pinned    bool       `json:"pinned"`
}

// Memory 是独立条目的当前值。归档后仍使用同一个 ID 和版本链。
type Memory struct {
	ID        int64     `json:"id"`
	OwnerID   int64     `json:"-"`
	Key       string    `json:"key"`
	Value     string    `json:"value"`
	Status    string    `json:"status"`
	Version   int       `json:"version"`
	UpdatedAt time.Time `json:"updated_at"`
	Metadata  `gorm:"embedded"`
}

// TableName 显式映射 GORM 表名。
func (Memory) TableName() string { return "memories" }

// Version 是一次修改后的快照；消息删除后只保留结构化偏好。
type Version struct {
	OwnerID        int64     `json:"-"`
	MemoryID       int64     `json:"memory_id" gorm:"primaryKey;autoIncrement:false"`
	Version        int       `json:"version" gorm:"primaryKey;autoIncrement:false"`
	Value          string    `json:"value"`
	Status         string    `json:"status"`
	Source         string    `json:"source"`
	MessageID      *int64    `json:"message_id,omitempty"`
	ConversationID *int64    `json:"conversation_id,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	Metadata       `gorm:"embedded"`
	Automatic      bool `json:"automatic"`
}

// TableName 显式映射 GORM 表名。
func (Version) TableName() string { return "memory_versions" }

// Candidate 是尚未确认的模型提议，永远不参与对话偏好读取。
type Candidate struct {
	ID             int64     `json:"id"`
	OwnerID        int64     `json:"-"`
	MessageID      int64     `json:"message_id"`
	ConversationID int64     `json:"conversation_id"`
	Key            string    `json:"key"`
	Value          string    `json:"value"`
	Evidence       string    `json:"evidence"`
	BaseVersion    int       `json:"base_version"`
	Status         string    `json:"status"`
	CreatedAt      time.Time `json:"created_at"`
	Metadata       `gorm:"embedded"`
	TargetID       *int64 `json:"target_id,omitempty"`
	Action         string `json:"action"`
}

// TableName 显式映射 GORM 表名。
func (Candidate) TableName() string { return "memory_candidates" }

// Evidence 关联一个版本和真实的用户消息；删除来源消息即清理证据。
type Evidence struct {
	OwnerID        int64  `json:"-"`
	MemoryID       int64  `json:"memory_id"`
	Version        int    `json:"version"`
	MessageID      int64  `json:"message_id"`
	ConversationID int64  `json:"conversation_id"`
	Excerpt        string `json:"excerpt"`
}

// TableName 显式映射证据表。
func (Evidence) TableName() string { return "memory_evidence" }
