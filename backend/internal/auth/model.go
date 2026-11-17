// Package auth 负责账号与会话：注册、登录、登出、会话校验与改密码。
//
// 边界：这里只认识 users / sessions 两张表，不认识任何业务模块。
// 业务模块拿到的是 Principal（用户 id 与展示信息），再按 owner_id 收口自己的数据。
//
// 为什么会话落库而不是发 JWT：撤销必须立刻生效。删一行 vs 维护黑名单，
// 前者是这个量级的系统里唯一说得清的做法；本服务每条请求本来就要查库，
// 多一次带唯一索引的查找可以忽略。
package auth

import "time"

// 账号状态。与迁移 0012 的 CHECK 约束一一对应。
const (
	// StatusActive 是正常账号，可以登录。
	StatusActive = "active"
	// StatusDisabled 是停用账号：会话校验也会拒绝它，
	// 所以"停用一个人"只需要改这一列，不需要挨个删会话。
	StatusDisabled = "disabled"
)

// User 对应 users 表。
//
// PasswordHash 是 bcrypt 输出（60 字符），**永远不出现在响应与日志里**。
type User struct {
	ID           int64      `gorm:"column:id;primaryKey"`
	Email        string     `gorm:"column:email"`
	PasswordHash string     `gorm:"column:password_hash"`
	DisplayName  string     `gorm:"column:display_name"`
	Status       string     `gorm:"column:status"`
	FailedLogins int        `gorm:"column:failed_logins"`
	LockedUntil  *time.Time `gorm:"column:locked_until"`
	LastLoginAt  *time.Time `gorm:"column:last_login_at"`
	CreatedAt    time.Time  `gorm:"column:created_at"`
	UpdatedAt    time.Time  `gorm:"column:updated_at"`
}

// TableName 显式声明，避免改结构体名时悄悄换表。
func (User) TableName() string { return "users" }

// Session 对应 sessions 表。
//
// TokenHash 是 token 的 SHA-256（32 字节原始值）。token 本身只在响应里出现一次，
// 库里存哈希：dump 被拿走时攻击者也无法反推出可用的 Cookie。
type Session struct {
	ID         int64     `gorm:"column:id;primaryKey"`
	UserID     int64     `gorm:"column:user_id"`
	TokenHash  []byte    `gorm:"column:token_hash"`
	UserAgent  string    `gorm:"column:user_agent"`
	ClientIP   string    `gorm:"column:client_ip"`
	CreatedAt  time.Time `gorm:"column:created_at"`
	LastSeenAt time.Time `gorm:"column:last_seen_at"`
	ExpiresAt  time.Time `gorm:"column:expires_at"`
}

// TableName 见 User.TableName 的说明。
func (Session) TableName() string { return "sessions" }

// Principal 是"这次请求是谁"的最小集合。
//
// 不放 PasswordHash、不放 User 全量：中间件每请求都会造一个，
// 让业务层拿不到密码哈希，就少了一条"顺手打日志把哈希带出去"的路径。
type Principal struct {
	UserID      int64
	SessionID   int64
	Email       string
	DisplayName string
}
