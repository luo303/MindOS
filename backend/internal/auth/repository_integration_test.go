package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/luo303/mindos/backend/internal/migrate"
	"github.com/luo303/mindos/backend/internal/testsupport"
	"github.com/luo303/mindos/backend/migrations"
)

// 账号与会话的语义一半落在数据库上：唯一索引、过期会话的删除、锁定的原子累加、
// 改密码后其他会话被吊销。这些用替身演不出来，必须对着真库跑。
func setupAuthService(t *testing.T, registrationOpen bool) (*Service, *gorm.DB) {
	t.Helper()
	db := testsupport.NewDatabase(t, "auth")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := migrate.New(db, migrations.FS).Up(ctx); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	gdb := testsupport.NewGorm(t, db)
	return NewService(NewRepository(gdb), 30*24*time.Hour, registrationOpen, nil), gdb
}

func TestRegisterAndAuthenticateIntegration(t *testing.T) {
	svc, db := setupAuthService(t, true)
	ctx := context.Background()

	res, err := svc.Register(ctx, RegisterInput{
		Email:    " Alice@Example.COM ",
		Password: "password-123",
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if res.User.Email != "alice@example.com" {
		t.Errorf("email = %q, want lowercased", res.User.Email)
	}
	// 没填昵称时用邮箱前缀，界面上总要有个称呼。
	if res.User.DisplayName != "alice" {
		t.Errorf("display_name = %q, want alice", res.User.DisplayName)
	}
	if res.Token == "" || !res.ExpiresAt.After(time.Now()) {
		t.Fatalf("session = %q expires %v", res.Token, res.ExpiresAt)
	}

	p, err := svc.Authenticate(ctx, res.Token)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if p.UserID != res.User.ID || p.SessionID == 0 || p.Email != "alice@example.com" {
		t.Fatalf("principal = %+v, user = %d", p, res.User.ID)
	}

	// 密码必须是 bcrypt 哈希，不是明文也不是 sha256。
	var stored string
	if err := db.Raw("SELECT password_hash FROM users WHERE id = ?", res.User.ID).Scan(&stored).Error; err != nil {
		t.Fatalf("read password hash: %v", err)
	}
	if !strings.HasPrefix(stored, "$2a$") || strings.Contains(stored, "password-123") {
		t.Fatalf("password_hash = %q, want a bcrypt hash", stored)
	}

	// 库里只能有 token 的哈希：dump 被拿走也不能直接当 Cookie 用。
	var sessionRow struct {
		TokenHash []byte
	}
	if err := db.Raw("SELECT token_hash FROM sessions WHERE id = ?", p.SessionID).Scan(&sessionRow).Error; err != nil {
		t.Fatalf("read token hash: %v", err)
	}
	tokenHash := sessionRow.TokenHash
	if string(tokenHash) == res.Token {
		t.Fatal("session token stored in plaintext")
	}
	if len(tokenHash) != 32 {
		t.Errorf("token_hash length = %d, want 32", len(tokenHash))
	}

	// 同一邮箱（大小写不同）不能注册第二遍。
	if _, err := svc.Register(ctx, RegisterInput{Email: "alice@example.com", Password: "password-456"}); !errors.Is(err, ErrEmailTaken) {
		t.Fatalf("duplicate register err = %v, want ErrEmailTaken", err)
	}

	// 登录成功也会新建会话；旧会话仍然有效（多设备）。
	login, err := svc.Login(ctx, LoginInput{Email: "alice@example.com", Password: "password-123"})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if login.Token == res.Token {
		t.Fatal("login reused the existing token")
	}
	if _, err := svc.Authenticate(ctx, res.Token); err != nil {
		t.Fatalf("old session should still work: %v", err)
	}
}

func TestLoginLockoutIntegration(t *testing.T) {
	svc, db := setupAuthService(t, true)
	ctx := context.Background()

	reg, err := svc.Register(ctx, RegisterInput{Email: "bob@example.com", Password: "password-123"})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	// 前 4 次是普通失败，第 5 次触发锁定。
	for i := 1; i <= maxLoginFailures; i++ {
		_, err := svc.Login(ctx, LoginInput{Email: "bob@example.com", Password: "wrong-password"})
		if i < maxLoginFailures && !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("attempt %d err = %v, want ErrInvalidCredentials", i, err)
		}
		if i == maxLoginFailures && !errors.Is(err, ErrAccountLocked) {
			t.Fatalf("attempt %d err = %v, want ErrAccountLocked", i, err)
		}
	}

	// 锁定期间即使密码正确也不放行。
	if _, err := svc.Login(ctx, LoginInput{Email: "bob@example.com", Password: "password-123"}); !errors.Is(err, ErrAccountLocked) {
		t.Fatalf("locked login err = %v, want ErrAccountLocked", err)
	}

	var lockState struct {
		FailedLogins int
		LockedUntil  *time.Time
	}
	if err := db.Raw("SELECT failed_logins, locked_until FROM users WHERE id = ?", reg.User.ID).
		Scan(&lockState).Error; err != nil {
		t.Fatalf("read lock state: %v", err)
	}
	failed := lockState.FailedLogins
	lockedUntil := lockState.LockedUntil
	if failed != maxLoginFailures {
		t.Errorf("failed_logins = %d, want %d", failed, maxLoginFailures)
	}
	if lockedUntil == nil || !lockedUntil.After(time.Now()) {
		t.Errorf("locked_until = %v, want a future time", lockedUntil)
	}

	// 模拟 15 分钟过去：锁定解除后正确密码能登录，并清零计数。
	if err := db.Exec("UPDATE users SET locked_until = now() - interval '1 minute' WHERE id = ?", reg.User.ID).Error; err != nil {
		t.Fatalf("unlock: %v", err)
	}
	res, err := svc.Login(ctx, LoginInput{Email: "bob@example.com", Password: "password-123"})
	if err != nil {
		t.Fatalf("login after lock expiry: %v", err)
	}
	if res.User.FailedLogins != 0 {
		t.Errorf("failed_logins after success = %d, want 0", res.User.FailedLogins)
	}
}

func TestSessionExpiryAndLogoutIntegration(t *testing.T) {
	svc, db := setupAuthService(t, true)
	ctx := context.Background()

	reg, err := svc.Register(ctx, RegisterInput{Email: "carol@example.com", Password: "password-123"})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	p, err := svc.Authenticate(ctx, reg.Token)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}

	// 过期会话：校验失败，并且顺手删掉那一行。
	if err := db.Exec("UPDATE sessions SET expires_at = now() - interval '1 minute' WHERE id = ?", p.SessionID).Error; err != nil {
		t.Fatalf("expire session: %v", err)
	}
	if _, err := svc.Authenticate(ctx, reg.Token); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("expired session err = %v, want ErrSessionInvalid", err)
	}
	var count int64
	if err := db.Raw("SELECT count(*) FROM sessions WHERE id = ?", p.SessionID).Scan(&count).Error; err != nil {
		t.Fatalf("count session: %v", err)
	}
	if count != 0 {
		t.Error("expired session row was not deleted")
	}

	// 主动登出：删除后立即失效，重复登出不报错。
	login, err := svc.Login(ctx, LoginInput{Email: "carol@example.com", Password: "password-123"})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	p2, err := svc.Authenticate(ctx, login.Token)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if err := svc.Logout(ctx, p2.SessionID); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if _, err := svc.Authenticate(ctx, login.Token); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("after logout err = %v, want ErrSessionInvalid", err)
	}
	if err := svc.Logout(ctx, p2.SessionID); err != nil {
		t.Fatalf("repeated logout should be a no-op: %v", err)
	}
}

func TestDisabledAccountIntegration(t *testing.T) {
	svc, db := setupAuthService(t, true)
	ctx := context.Background()

	reg, err := svc.Register(ctx, RegisterInput{Email: "dave@example.com", Password: "password-123"})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := db.Exec("UPDATE users SET status = 'disabled' WHERE id = ?", reg.User.ID).Error; err != nil {
		t.Fatalf("disable user: %v", err)
	}

	// 停用后：已有会话立即失效，登录也拒绝。不需要逐个删会话。
	if _, err := svc.Authenticate(ctx, reg.Token); !errors.Is(err, ErrAccountDisabled) {
		t.Fatalf("authenticate disabled err = %v, want ErrAccountDisabled", err)
	}
	if _, err := svc.Login(ctx, LoginInput{Email: "dave@example.com", Password: "password-123"}); !errors.Is(err, ErrAccountDisabled) {
		t.Fatalf("login disabled err = %v, want ErrAccountDisabled", err)
	}
}

func TestChangePasswordRevokesOtherSessionsIntegration(t *testing.T) {
	svc, _ := setupAuthService(t, true)
	ctx := context.Background()

	first, err := svc.Register(ctx, RegisterInput{Email: "erin@example.com", Password: "password-123"})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	second, err := svc.Login(ctx, LoginInput{Email: "erin@example.com", Password: "password-123"})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	p, err := svc.Authenticate(ctx, first.Token)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}

	if err := svc.ChangePassword(ctx, p.UserID, p.SessionID, "wrong-password", "new-password-456"); !errors.Is(err, ErrPasswordMismatch) {
		t.Fatalf("wrong current err = %v, want ErrPasswordMismatch", err)
	}
	if err := svc.ChangePassword(ctx, p.UserID, p.SessionID, "password-123", "new-password-456"); err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}

	// 当前会话保留，其他设备被踢。
	if _, err := svc.Authenticate(ctx, first.Token); err != nil {
		t.Fatalf("current session should survive: %v", err)
	}
	if _, err := svc.Authenticate(ctx, second.Token); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("other session err = %v, want ErrSessionInvalid", err)
	}

	if _, err := svc.Login(ctx, LoginInput{Email: "erin@example.com", Password: "password-123"}); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("old password err = %v, want ErrInvalidCredentials", err)
	}
	if _, err := svc.Login(ctx, LoginInput{Email: "erin@example.com", Password: "new-password-456"}); err != nil {
		t.Fatalf("new password login: %v", err)
	}
}

func TestRegistrationClosedIntegration(t *testing.T) {
	svc, _ := setupAuthService(t, false)
	if _, err := svc.Register(context.Background(), RegisterInput{
		Email:    "frank@example.com",
		Password: "password-123",
	}); !errors.Is(err, ErrRegistrationClosed) {
		t.Fatalf("err = %v, want ErrRegistrationClosed", err)
	}
}

func TestSessionSurvivesConcurrentAuthenticate(t *testing.T) {
	svc, _ := setupAuthService(t, true)
	ctx := context.Background()

	reg, err := svc.Register(ctx, RegisterInput{Email: "gina@example.com", Password: "password-123"})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	// 并发校验同一 token：只读 + 节流的 last_seen 更新，不该出现互相覆盖的错。
	const workers = 8
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		go func() {
			_, err := svc.Authenticate(ctx, reg.Token)
			errs <- err
		}()
	}
	for i := 0; i < workers; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent authenticate: %v", err)
		}
	}
}

