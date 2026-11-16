// 本文件是对话模型配置的持久化模型。
//
// 表名叫 chat_models 而不是 model_configs：这张表只存对话模型，
// 向量模型仍在 .env（它的维度绑在 vector(1024) 列上，属于表结构）。
// 用中性泛称会让人以为它还能存别的，见 docs/design/rag.md §7.1。
package chatmodel

import "time"

// ChatModel 对应 chat_models 表的一行，也就是"一条用户配置的对话模型"。
//
// APIKeyCipher 直接映射 bytea：GORM 对 []byte 走 binary 协议，不需要自定义类型
// （documents.tags 那种 text[] 才需要，见 knowledge.StringArray）。
type ChatModel struct {
	ID           int64      `gorm:"column:id;primaryKey"`
	Name         string     `gorm:"column:name"`
	BaseURL      string     `gorm:"column:base_url"`
	Model        string     `gorm:"column:model"`
	APIKeyCipher []byte     `gorm:"column:api_key_cipher"`
	APIKeyHint   string     `gorm:"column:api_key_hint"`
	Temperature  *float32   `gorm:"column:temperature"`
	MaxTokens    *int       `gorm:"column:max_tokens"`
	IsDefault    bool       `gorm:"column:is_default"`
	LastUsedAt   *time.Time `gorm:"column:last_used_at"`
	CreatedAt    time.Time  `gorm:"column:created_at"`
	UpdatedAt    time.Time  `gorm:"column:updated_at"`
}

// TableName 显式写出来：改结构体名时不会悄悄换表。
func (ChatModel) TableName() string { return "chat_models" }
