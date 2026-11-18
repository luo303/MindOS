package chat

import (
	"errors"
	"time"
)

// ErrConversationNotFound 表示会话不存在或不属于当前用户。
var ErrConversationNotFound = errors.New("conversation not found")

// 消息角色。数据库 CHECK 约束与此一一对应。
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

// Conversation 是一个多轮会话。
//
// 标题、消息数只用于列表展示与分配 seq；真正的对话内容在 messages 里。
type Conversation struct {
	ID      int64  `gorm:"column:id;primaryKey" json:"id"`
	OwnerID int64  `gorm:"column:owner_id" json:"-"`
	Title   string `gorm:"column:title" json:"title"`
	// MessageCount 同时充当"下一条消息的 seq - 1"。
	// 追加消息时用条件更新自增，靠行锁保证同一会话内 seq 不重复。
	MessageCount int       `gorm:"column:message_count" json:"message_count"`
	CreatedAt    time.Time `gorm:"column:created_at" json:"created_at"`
	UpdatedAt    time.Time `gorm:"column:updated_at" json:"updated_at"`
}

// TableName 显式写出来：改结构体名时不会悄悄换表。
func (Conversation) TableName() string { return "conversations" }

// Message 是一条对话消息。角色只能是 user / assistant。
type Message struct {
	ID             int64     `gorm:"column:id;primaryKey" json:"id"`
	ConversationID int64     `gorm:"column:conversation_id" json:"conversation_id"`
	OwnerID        int64     `gorm:"column:owner_id" json:"-"`
	Role           string    `gorm:"column:role" json:"role"`
	Content        string    `gorm:"column:content" json:"content"`
	Citations      RawJSON   `gorm:"column:citations;type:jsonb" json:"citations"`
	Seq            int       `gorm:"column:seq" json:"seq"`
	CreatedAt      time.Time `gorm:"column:created_at" json:"created_at"`
}

// TableName 见 Conversation.TableName 的说明。
func (Message) TableName() string { return "messages" }
