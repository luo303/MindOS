// 本文件是账号与会话的业务层：注册、登录、校验、改密码。
//
// 三条不变量，改这个文件时别破坏：
//  1. 密码只以 bcrypt 哈希出现，任何返回值/日志都不带明文；
//  2. 会话 token 只以 SHA-256 哈希落库，明文只在响应里出现一次；
//  3. "邮箱不存在"与"密码错"对外是同一个错误，且耗时接近。
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"go.uber.org/zap"
)

// 模块级哨兵错误，handler 用 errors.Is 判断后翻成 HTTP 错误码。
var (
	// ErrUserNotFound 表示账号不存在（只在内部使用，对外一律是"凭据不对"）。
	ErrUserNotFound = errors.New("user not found")
	// ErrSessionInvalid 表示 Cookie 里的 token 不存在、已过期或已被撤销。
	ErrSessionInvalid = errors.New("session is invalid or expired")
	// ErrInvalidCredentials 表示邮箱或密码不对。
	ErrInvalidCredentials = errors.New("invalid email or password")
	// ErrEmailTaken 表示注册邮箱已被占用。
	ErrEmailTaken = errors.New("email is already registered")
	// ErrAccountLocked 表示连续失败触发了临时锁定。
	ErrAccountLocked = errors.New("account is temporarily locked")
	// ErrAccountDisabled 表示账号被停用。
	ErrAccountDisabled = errors.New("account is disabled")
	// ErrRegistrationClosed 表示部署关闭了自助注册。
	ErrRegistrationClosed = errors.New("registration is closed")
	// ErrPasswordMismatch 表示改密码时旧密码不对。
	ErrPasswordMismatch = errors.New("current password is incorrect")
	// ErrInvalidEmail 表示邮箱格式不合法。
	ErrInvalidEmail = errors.New("invalid email address")
)

const (
	// maxLoginFailures 是触发锁定的连续失败次数。
	maxLoginFailures = 5
	// lockDuration 是锁定时长。太短挡不住爆破，太长会让忘记密码的人以为账号坏了。
	lockDuration = 15 * time.Minute
	// sessionTouchInterval 是"最近活动时间"的写入节流间隔。
	// 每个请求都写会让认证路径变成写热点，而这个字段只用于展示。
	sessionTouchInterval = 5 * time.Minute
	// defaultSessionTTL 是配置缺省时的会话有效期。
	defaultSessionTTL = 30 * 24 * time.Hour
	// maxDisplayNameRunes 是昵称长度上限，避免一个超长昵称把侧栏撑坏。
	maxDisplayNameRunes = 64
	// maxUserAgentBytes 是 UA 落库上限，防止一条畸形请求把列撑大。
	maxUserAgentBytes = 512
)

// Service 编排账号与会话。依赖全部由 cmd/server 注入。
type Service struct {
	repo             *Repository
	ttl              time.Duration
	registrationOpen bool
	log              *zap.Logger
}

// NewService 组装业务层。ttl <= 0 时回退到 30 天。
func NewService(repo *Repository, ttl time.Duration, registrationOpen bool, log *zap.Logger) *Service {
	if ttl <= 0 {
		ttl = defaultSessionTTL
	}
	if log == nil {
		log = zap.NewNop()
	}
	return &Service{repo: repo, ttl: ttl, registrationOpen: registrationOpen, log: log}
}

// RegisterInput 是注册请求。
type RegisterInput struct {
	Email       string
	Password    string
	DisplayName string
}

// LoginInput 是登录请求。UserAgent / ClientIP 只用于排查"这个会话是哪来的"。
type LoginInput struct {
	Email     string
	Password  string
	UserAgent string
	ClientIP  string
}

// SessionResult 是"登录成功"要交给 HTTP 层的东西。
//
// Token 是明文 token，只在这一次响应里出现；ExpiresAt 决定 Cookie 的 Max-Age。
type SessionResult struct {
	User      *User
	Token     string
	ExpiresAt time.Time
}

// Register 创建账号并直接登录（不做"注册后再登录"两步）。
func (s *Service) Register(ctx context.Context, in RegisterInput) (*SessionResult, error) {
	if !s.registrationOpen {
		return nil, ErrRegistrationClosed
	}
	email, err := normalizeEmail(in.Email)
	if err != nil {
		return nil, err
	}
	hash, err := HashPassword(in.Password)
	if err != nil {
		return nil, err
	}
	name := normalizeDisplayName(in.DisplayName, email)

	user := &User{Email: email, PasswordHash: hash, DisplayName: name, Status: StatusActive}
	if err := s.repo.CreateUser(ctx, user); err != nil {
		if errors.Is(err, errDuplicate) {
			return nil, ErrEmailTaken
		}
		return nil, err
	}
	s.log.Info("user registered", zap.Int64("user_id", user.ID))
	return s.startSession(ctx, user, "", "")
}

// Login 校验凭据并新建会话。
func (s *Service) Login(ctx context.Context, in LoginInput) (*SessionResult, error) {
	email, err := normalizeEmail(in.Email)
	if err != nil {
		// 邮箱格式不对也算"凭据不对"：登录口不需要教人邮箱怎么写，
		// 那会变成另一种账号探测（格式对/错的两条耗时不同）。
		return nil, ErrInvalidCredentials
	}

	user, err := s.repo.GetUserByEmail(ctx, email)
	if errors.Is(err, ErrUserNotFound) {
		// 不存在的邮箱也跑一次 bcrypt，让两条路径的耗时接近。
		compareDummy(in.Password)
		return nil, ErrInvalidCredentials
	}
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	if user.LockedUntil != nil && user.LockedUntil.After(now) {
		return nil, ErrAccountLocked
	}
	if user.Status != StatusActive {
		return nil, ErrAccountDisabled
	}
	if err := VerifyPassword(user.PasswordHash, in.Password); err != nil {
		// 锁定由数据库原子累加：并发尝试不会漏计。
		if recErr := s.repo.RecordLoginFailure(ctx, user.ID, maxLoginFailures, now.Add(lockDuration)); recErr != nil {
			s.log.Warn("record login failure", zap.Int64("user_id", user.ID), zap.Error(recErr))
		}
		if user.FailedLogins+1 >= maxLoginFailures {
			return nil, ErrAccountLocked
		}
		return nil, ErrInvalidCredentials
	}

	// 登录成功后的记录失败不该让登录失败：用户已经通过了校验，
	// 这只是一次记账。下一次请求会再算一次。
	if err := s.repo.RecordLoginSuccess(ctx, user.ID, now); err != nil {
		s.log.Warn("record login success", zap.Int64("user_id", user.ID), zap.Error(err))
	}
	user.FailedLogins = 0
	user.LockedUntil = nil
	user.LastLoginAt = &now
	return s.startSession(ctx, user, in.UserAgent, in.ClientIP)
}

// Authenticate 校验 token 并返回本次请求的身份。
//
// 过期会话顺手删掉：留在表里只会让清理任务多扫一行。
func (s *Service) Authenticate(ctx context.Context, token string) (Principal, error) {
	if token == "" {
		return Principal{}, ErrSessionInvalid
	}
	row, err := s.repo.SessionAuthByHash(ctx, hashToken(token))
	if err != nil {
		return Principal{}, err
	}
	now := time.Now().UTC()
	if !row.ExpiresAt.After(now) {
		_ = s.repo.DeleteSessionByID(ctx, row.SessionID)
		return Principal{}, ErrSessionInvalid
	}
	if row.Status != StatusActive {
		return Principal{}, ErrAccountDisabled
	}
	if now.Sub(row.LastSeenAt) >= sessionTouchInterval {
		if err := s.repo.TouchSession(ctx, row.SessionID, now); err != nil {
			// 只影响"最近活动"这个展示字段，不能因此让请求 401。
			s.log.Warn("touch session", zap.Int64("session_id", row.SessionID), zap.Error(err))
		}
	}
	return Principal{
		UserID:      row.UserID,
		SessionID:   row.SessionID,
		Email:       row.Email,
		DisplayName: row.DisplayName,
	}, nil
}

// Profile 返回账号详情，供 /auth/me 使用。
func (s *Service) Profile(ctx context.Context, userID int64) (*User, error) {
	return s.repo.GetUserByID(ctx, userID)
}

// Logout 删除当前会话。已经不存在时视为成功（重复登出不是错误）。
func (s *Service) Logout(ctx context.Context, sessionID int64) error {
	return s.repo.DeleteSessionByID(ctx, sessionID)
}

// LogoutAll 删除该用户除当前会话外的全部会话。
func (s *Service) LogoutAll(ctx context.Context, userID, keepSessionID int64) error {
	return s.repo.DeleteOtherSessions(ctx, userID, keepSessionID)
}

// ChangePassword 校验旧密码后写入新哈希，并吊销其他设备上的会话。
//
// 保留当前会话：用户刚改完密码就被踢出去，会以为改失败了。
func (s *Service) ChangePassword(ctx context.Context, userID, sessionID int64, current, next string) error {
	user, err := s.repo.GetUserByID(ctx, userID)
	if err != nil {
		return err
	}
	if err := VerifyPassword(user.PasswordHash, current); err != nil {
		return ErrPasswordMismatch
	}
	hash, err := HashPassword(next)
	if err != nil {
		return err
	}
	if err := s.repo.UpdatePassword(ctx, user.ID, hash); err != nil {
		return err
	}
	if err := s.repo.DeleteOtherSessions(ctx, user.ID, sessionID); err != nil {
		return err
	}
	s.log.Info("password changed", zap.Int64("user_id", user.ID))
	return nil
}

// startSession 生成 token 并落库。
func (s *Service) startSession(ctx context.Context, user *User, userAgent, clientIP string) (*SessionResult, error) {
	token, hash, err := newToken()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	session := &Session{
		UserID:     user.ID,
		TokenHash:  hash,
		UserAgent:  truncateBytes(userAgent, maxUserAgentBytes),
		ClientIP:   clientIP,
		CreatedAt:  now,
		LastSeenAt: now,
		ExpiresAt:  now.Add(s.ttl),
	}
	if err := s.repo.CreateSession(ctx, session); err != nil {
		return nil, err
	}
	return &SessionResult{User: user, Token: token, ExpiresAt: session.ExpiresAt}, nil
}

// newToken 生成 32 字节随机 token 与它的 SHA-256。
//
// 为什么用 base64url 而不是 hex：43 个字符 vs 64 个，Cookie 小一点；
// 且 url-safe 字符集不需要额外转义。
func newToken() (string, []byte, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", nil, fmt.Errorf("generate session token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(buf)
	return token, hashToken(token), nil
}

// hashToken 与会话校验必须用同一个算法，两处只留一份实现。
func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// normalizeEmail 统一小写并做一次语法校验。
//
// 用 net/mail 而不是手写正则：邮箱的合法形态比看起来复杂，
// 但这里只接受"纯地址"（带 display name 的直接拒绝），够用且不会误放行。
func normalizeEmail(raw string) (string, error) {
	email := strings.ToLower(strings.TrimSpace(raw))
	if email == "" || len(email) > 254 {
		return "", ErrInvalidEmail
	}
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email {
		return "", ErrInvalidEmail
	}
	local, domain, ok := strings.Cut(email, "@")
	if !ok || local == "" || !strings.Contains(domain, ".") || strings.HasPrefix(domain, ".") {
		return "", ErrInvalidEmail
	}
	return email, nil
}

// normalizeDisplayName 空昵称用邮箱前缀兜底：界面上总要有个称呼。
func normalizeDisplayName(raw, email string) string {
	name := strings.TrimSpace(raw)
	if name == "" {
		local, _, _ := strings.Cut(email, "@")
		name = local
	}
	if utf8.RuneCountInString(name) > maxDisplayNameRunes {
		runes := []rune(name)
		name = string(runes[:maxDisplayNameRunes])
	}
	return name
}

// truncateBytes 按字节截断，用于 UA 这种纯展示字段。
// 截断处可能切坏一个 UTF-8 字符，所以退回到最近的分界点。
func truncateBytes(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := s[:limit]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut
}
