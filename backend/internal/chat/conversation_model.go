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
	MessageCount int `gorm:"column:message_count" json:"message_count"`
	// 摘要水位：covered_to_seq 已经写进 conversation_summaries 的最高 seq。
	SummarizedUntilSeq  int        `gorm:"column:summarized_until_seq" json:"-"`
	SummaryVersion      int        `gorm:"column:summary_version" json:"-"`
	SummaryClaimedUntil int        `gorm:"column:summary_claimed_until" json:"-"`
	SummaryClaimedAt    *time.Time `gorm:"column:summary_claimed_at" json:"-"`
	CreatedAt           time.Time  `gorm:"column:created_at" json:"created_at"`
	UpdatedAt           time.Time  `gorm:"column:updated_at" json:"updated_at"`
}

// TableName 显式写出来：改结构体名时不会悄悄换表。
func (Conversation) TableName() string { return "conversations" }

// Message 是一条对话消息。角色只能是 user / assistant。
type Message struct {
	ID             int64   `gorm:"column:id;primaryKey" json:"id"`
	ConversationID int64   `gorm:"column:conversation_id" json:"conversation_id"`
	OwnerID        int64   `gorm:"column:owner_id" json:"-"`
	Role           string  `gorm:"column:role" json:"role"`
	Content        string  `gorm:"column:content" json:"content"`
	Citations      RawJSON `gorm:"column:citations;type:jsonb" json:"citations"`
	Seq            int     `gorm:"column:seq" json:"seq"`
	// TokenCount 是写入时的启发式估算，用于热层预算与摘要输入预算。
	TokenCount int `gorm:"column:token_count" json:"-"`
	// Embedding/Model 只用于冷层与 ES 重建，不返回给前端。
	EmbeddingModel *string    `gorm:"column:embedding_model" json:"-"`
	EmbeddedAt     *time.Time `gorm:"column:embedded_at" json:"-"`
	// ErrorCode/ErrorMessage 只在 assistant 失败时写入，刷新后用来还原失败卡片。
	ErrorCode    *string   `gorm:"column:error_code" json:"error_code,omitempty"`
	ErrorMessage *string   `gorm:"column:error_message" json:"error_message,omitempty"`
	CreatedAt    time.Time `gorm:"column:created_at" json:"created_at"`
}

// TableName 见 Conversation.TableName 的说明。
func (Message) TableName() string { return "messages" }

// ConversationSummary 是某一版滚动摘要。
//
// 只读最新版本参与上下文；旧版本保留用于审计/回滚。
type ConversationSummary struct {
	ID             int64     `gorm:"column:id;primaryKey" json:"id"`
	ConversationID int64     `gorm:"column:conversation_id" json:"conversation_id"`
	OwnerID        int64     `gorm:"column:owner_id" json:"-"`
	Version        int       `gorm:"column:version" json:"version"`
	CoveredFromSeq int       `gorm:"column:covered_from_seq" json:"covered_from_seq"`
	CoveredToSeq   int       `gorm:"column:covered_to_seq" json:"covered_to_seq"`
	BatchFromSeq   int       `gorm:"column:batch_from_seq" json:"batch_from_seq"`
	BatchToSeq     int       `gorm:"column:batch_to_seq" json:"batch_to_seq"`
	Content        string    `gorm:"column:content" json:"content"`
	TokenCount     int       `gorm:"column:token_count" json:"token_count"`
	Model          *string   `gorm:"column:model" json:"model,omitempty"`
	CreatedAt      time.Time `gorm:"column:created_at" json:"created_at"`
}

// TableName 显式写出来，避免结构体改名时换表。
func (ConversationSummary) TableName() string { return "conversation_summaries" }
