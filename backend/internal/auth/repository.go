package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// errDuplicate 表示唯一索引冲突（邮箱已注册）。
// 内部错误，由 service 翻成"邮箱已被占用"：仓储不该猜上层要说什么话。
var errDuplicate = errors.New("account row conflicts with an existing one")

// userColumns 显式列出列名，不写 SELECT *。
var userColumns = []string{
	"id", "email", "password_hash", "display_name", "status",
	"failed_logins", "locked_until", "last_login_at", "created_at", "updated_at",
}

// Repository 是 users / sessions 的数据访问。
type Repository struct {
	db *gorm.DB
}

// NewRepository 创建仓储。db 由 cmd/server 注入。
func NewRepository(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

// CreateUser 插入一个账号，自增 id 回填到 u.ID。
func (r *Repository) CreateUser(ctx context.Context, u *User) error {
	if err := r.db.WithContext(ctx).Create(u).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return errDuplicate
		}
		return fmt.Errorf("auth: create user: %w", err)
	}
	return nil
}

// GetUserByEmail 按小写邮箱读取账号。
func (r *Repository) GetUserByEmail(ctx context.Context, email string) (*User, error) {
	var u User
	err := r.db.WithContext(ctx).Select(userColumns).Where("email = ?", email).Take(&u).Error
	if err != nil {
		return nil, translateUserError("get user by email", err)
	}
	return &u, nil
}

// GetUserByID 按主键读取账号。
func (r *Repository) GetUserByID(ctx context.Context, id int64) (*User, error) {
	var u User
	err := r.db.WithContext(ctx).Select(userColumns).Where("id = ?", id).Take(&u).Error
	if err != nil {
		return nil, translateUserError("get user by id", err)
	}
	return &u, nil
}

// UpdatePassword 写入新哈希。
func (r *Repository) UpdatePassword(ctx context.Context, id int64, hash string) error {
	res := r.db.WithContext(ctx).Model(&User{}).Where("id = ?", id).
		Updates(map[string]any{"password_hash": hash, "updated_at": time.Now().UTC()})
	if res.Error != nil {
		return fmt.Errorf("auth: update password: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrUserNotFound
	}
	return nil
}

// RecordLoginFailure 原子地累加失败次数，达到阈值时写入锁定截止时间。
//
// 为什么用一条 SQL 而不是"先读 count 再写"：两个并发登录请求会各自读到同一个
// 计数、各自写回 count+1，实际只记了一次。这一条语句把判断与自增放在数据库里，
// 不需要锁，也不会丢计数。
func (r *Repository) RecordLoginFailure(ctx context.Context, id int64, threshold int, lockUntil time.Time) error {
	err := r.db.WithContext(ctx).Exec(
		`UPDATE users
		    SET failed_logins = failed_logins + 1,
		        locked_until  = CASE WHEN failed_logins + 1 >= ? THEN ? ELSE locked_until END,
		        updated_at    = now()
		  WHERE id = ?`,
		threshold, lockUntil, id).Error
	if err != nil {
		return fmt.Errorf("auth: record login failure: %w", err)
	}
	return nil
}

// RecordLoginSuccess 清空失败计数与锁定，并记录最近登录时间。
func (r *Repository) RecordLoginSuccess(ctx context.Context, id int64, at time.Time) error {
	err := r.db.WithContext(ctx).Model(&User{}).Where("id = ?", id).
		Updates(map[string]any{
			"failed_logins": 0,
			"locked_until":  nil,
			"last_login_at": at,
			"updated_at":    at,
		}).Error
	if err != nil {
		return fmt.Errorf("auth: record login success: %w", err)
	}
	return nil
}

// CreateSession 插入一条会话，自增 id 回填到 s.ID。
func (r *Repository) CreateSession(ctx context.Context, s *Session) error {
	if err := r.db.WithContext(ctx).Create(s).Error; err != nil {
		return fmt.Errorf("auth: create session: %w", err)
	}
	return nil
}

// SessionAuth 是"按 token 校验登录态"需要的一行。
//
// 一次 JOIN 取全会话与账号状态：分成两次查询会让每个请求多一次往返，
// 而这两张表本来就是一对一（按 session.user_id）。
type SessionAuth struct {
	SessionID   int64     `gorm:"column:session_id"`
	UserID      int64     `gorm:"column:user_id"`
	Email       string    `gorm:"column:email"`
	DisplayName string    `gorm:"column:display_name"`
	Status      string    `gorm:"column:status"`
	ExpiresAt   time.Time `gorm:"column:expires_at"`
	LastSeenAt  time.Time `gorm:"column:last_seen_at"`
}

// SessionAuthByHash 按 token 哈希读会话与账号状态。
func (r *Repository) SessionAuthByHash(ctx context.Context, tokenHash []byte) (*SessionAuth, error) {
	var row SessionAuth
	err := r.db.WithContext(ctx).
		Table("sessions AS s").
		Select(`s.id AS session_id, s.user_id, s.expires_at, s.last_seen_at,
		        u.email, u.display_name, u.status`).
		Joins("JOIN users u ON u.id = s.user_id").
		Where("s.token_hash = ?", tokenHash).
		Take(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrSessionInvalid
		}
		return nil, fmt.Errorf("auth: get session: %w", err)
	}
	return &row, nil
}

// TouchSession 更新最近活动时间。
//
// 单独一个方法而不是每请求都写：会话表是认证路径上的表，
// 每个请求一行 UPDATE 的写放大没有必要，服务层会按节流间隔调用它。
func (r *Repository) TouchSession(ctx context.Context, id int64, at time.Time) error {
	err := r.db.WithContext(ctx).Model(&Session{}).Where("id = ?", id).
		UpdateColumn("last_seen_at", at).Error
	if err != nil {
		return fmt.Errorf("auth: touch session: %w", err)
	}
	return nil
}

// DeleteSessionByID 删除一条会话（登出）。
func (r *Repository) DeleteSessionByID(ctx context.Context, id int64) error {
	err := r.db.WithContext(ctx).Where("id = ?", id).Delete(&Session{}).Error
	if err != nil {
		return fmt.Errorf("auth: delete session: %w", err)
	}
	return nil
}

// DeleteOtherSessions 删除该用户除 keepID 之外的全部会话（改密码、全部登出）。
func (r *Repository) DeleteOtherSessions(ctx context.Context, userID, keepID int64) error {
	err := r.db.WithContext(ctx).
		Where("user_id = ? AND id <> ?", userID, keepID).
		Delete(&Session{}).Error
	if err != nil {
		return fmt.Errorf("auth: delete other sessions: %w", err)
	}
	return nil
}

// DeleteExpiredSessions 清理过期会话，返回删除行数。
//
// 由 reaper 周期调用。limit 防止一次删除把连接占太久：
// 积压很多时分多轮删干净，而不是让一条 DELETE 卡住整个 reaper。
func (r *Repository) DeleteExpiredSessions(ctx context.Context, before time.Time, limit int) (int64, error) {
	res := r.db.WithContext(ctx).Exec(
		`DELETE FROM sessions WHERE id IN (
		    SELECT id FROM sessions WHERE expires_at < ? ORDER BY expires_at LIMIT ?
		 )`, before, limit)
	if res.Error != nil {
		return 0, fmt.Errorf("auth: delete expired sessions: %w", res.Error)
	}
	return res.RowsAffected, nil
}

func translateUserError(op string, err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrUserNotFound
	}
	return fmt.Errorf("auth: %s: %w", op, err)
}
