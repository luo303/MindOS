// Package chat 是问答模块：一次 ask 请求的编排、SSE 事件、运行记录。
//
// 它不认识 Eino（模型与会话由 internal/agent 提供），也不认识检索细节
// （检索发生在 agent 的 search_knowledge 工具里）。这样 SSE 协议、
// 引用映射、落库这几件事可以脱离模型单独测试。
package chat

import (
	"database/sql/driver"
	"fmt"
	"time"
)

// 裁决结果。与迁移里的 CHECK 约束一一对应。
//
// 2026-11-18 起只有 answered / failed 会被写入：judge 已删除，
// 没有引用或引用越界都不再改判。refused / ungrounded 只用于读旧记录，
// 保留常量是因为 CHECK 约束与历史数据还在。
const (
	// VerdictAnswered 表示这次 run 正常结束（不代表回答一定正确）。
	VerdictAnswered = "answered"
	// VerdictRefused / VerdictUngrounded 是 2026-11-18 之前的旧裁决值。
	VerdictRefused    = "refused"
	VerdictUngrounded = "ungrounded"
	// VerdictFailed 表示这次没有产出结果：模型 429/超时、客户端中途断开、
	// token 超预算。
	VerdictFailed = "failed"
)

// 裁决原因。写进 verdict_reason。
//
// 前四个是旧裁决（2026-11-18 前）的取值，只用于读历史记录；
// 新代码只写后四个（执行失败的原因）。
const (
	ReasonNoEvidence      = "no_evidence"      // 旧：检索低于阈值，没调模型
	ReasonNoCitation      = "no_citation"      // 旧：模型输出里没有 [n]
	ReasonUnknownCitation = "unknown_citation" // 旧：引用了未登记的编号
	ReasonModelRefused    = "model_refused"    // 旧：模型自己说无法回答

	// 下面四个与 VerdictFailed 配套：verdict 说"没有结果"，这里说"为什么"。
	ReasonUpstreamError = "upstream_error"
	ReasonCancelled     = "cancelled"
	ReasonTimeout       = "timeout"
	ReasonBudget        = "budget_exceeded"
)

// Run 对应 qa_runs 表的一行，也就是"一次问答"。
type Run struct {
	ID int64 `gorm:"column:id;primaryKey"`
	// OwnerID 是这次问答属于谁。会话记录按人隔离，复盘统计也按它分组。
	OwnerID int64 `gorm:"column:owner_id"`
	// ConversationID 为 nil 表示一次性 /ask，不属于任何会话。
	ConversationID *int64 `gorm:"column:conversation_id"`
	Question       string `gorm:"column:question"`
	// Answer 是最终返回给用户的文本。现在原样保存模型输出，不再撤回。
	Answer string `gorm:"column:answer"`
	// RawAnswer 是模型原始输出，只有这里还能看到"模型到底写了什么"。
	RawAnswer     *string `gorm:"column:raw_answer"`
	Verdict       string  `gorm:"column:verdict"`
	VerdictReason *string `gorm:"column:verdict_reason"`
	// Citations 与 Retrieved 是 JSON 数组，用 RawJSON 在 []byte 与 jsonb 之间桥接。
	Citations    RawJSON `gorm:"column:citations;type:jsonb"`
	Retrieved    RawJSON `gorm:"column:retrieved;type:jsonb"`
	ChatModelID  *int64  `gorm:"column:chat_model_id"`
	Model        *string `gorm:"column:model"`
	PromptTokens int64   `gorm:"column:prompt_tokens"`
	// CompletionTokens 用 int64 记账：token 数是整数，绝不引入 float64。
	CompletionTokens int64     `gorm:"column:completion_tokens"`
	LatencyMS        int       `gorm:"column:latency_ms"`
	ErrorCode        *string   `gorm:"column:error_code"`
	CreatedAt        time.Time `gorm:"column:created_at"`
}

// TableName 显式写出来：改结构体名时不会悄悄换表。
func (Run) TableName() string { return "qa_runs" }

// RawJSON 是 jsonb 列与 []byte 之间的桥。
//
// 为什么需要它：pgx 把 jsonb 当文本传输，GORM 又没有内置的 jsonb 类型
// （引入 gorm.io/datatypes 只为一个字段不值）。实现 driver.Valuer 与
// sql.Scanner 就够了，与 knowledge.StringArray 处理 text[] 是同一个思路。
type RawJSON []byte

// Value 把 []byte 交给驱动。空值写成 '[]' 而不是 NULL：
// 两列都有 NOT NULL DEFAULT '[]'，写 NULL 会直接违反约束。
func (j RawJSON) Value() (driver.Value, error) {
	if len(j) == 0 {
		return "[]", nil
	}
	return string(j), nil
}

// Scan 从驱动读回 jsonb。
//
// 两种类型都要处理：pgx 在不同路径下可能给 []byte 或 string，
// 只认一种会在某个版本升级后突然报 "cannot scan"。
func (j *RawJSON) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		*j = nil
	case []byte:
		*j = append((*j)[:0], v...)
	case string:
		*j = RawJSON(v)
	default:
		return fmt.Errorf("chat: cannot scan %T into RawJSON", src)
	}
	return nil
}
