package auth

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// TestMain 把哈希成本降到最低跑用例：这里验证的是"能哈希、能校验、盐随机"，
// 不是"哈希有多慢"。生产值 12 由 TestDefaultBcryptCost 单独守着。
func TestMain(m *testing.M) {
	bcryptCost = bcrypt.MinCost
	os.Exit(m.Run())
}

func TestDefaultBcryptCost(t *testing.T) {
	if defaultBcryptCost != 12 {
		t.Fatalf("defaultBcryptCost = %d, want 12", defaultBcryptCost)
	}
}

func TestHashPasswordRoundTrip(t *testing.T) {
	hash, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	// bcrypt 的哈希以成本参数开头，换成本时能一眼看出来。
	if !strings.HasPrefix(hash, fmt.Sprintf("$2a$%02d$", bcryptCost)) {
		t.Fatalf("hash = %q, want bcrypt cost %d", hash, bcryptCost)
	}
	if err := VerifyPassword(hash, "correct horse battery"); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if err := VerifyPassword(hash, "wrong password"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("wrong password err = %v, want ErrInvalidCredentials", err)
	}
}

func TestHashPasswordRejectsBadLength(t *testing.T) {
	cases := []string{
		"",
		"short",
		strings.Repeat("a", 73),
	}
	for _, pw := range cases {
		if _, err := HashPassword(pw); !errors.Is(err, ErrWeakPassword) {
			t.Errorf("HashPassword(%d bytes) err = %v, want ErrWeakPassword", len(pw), err)
		}
	}
	// 边界值必须通过：8 与 72 字节都是合法的。
	for _, pw := range []string{"12345678", strings.Repeat("a", 72)} {
		if _, err := HashPassword(pw); err != nil {
			t.Errorf("HashPassword(%d bytes) should be valid: %v", len(pw), err)
		}
	}
}

func TestPasswordPolicyRejectsBlank(t *testing.T) {
	// 长度够但全是空白：能过长度检查，不该被当成密码。
	if _, err := HashPassword("        "); !errors.Is(err, ErrWeakPassword) {
		t.Fatalf("blank password err = %v, want ErrWeakPassword", err)
	}
}

// 同一个密码两次哈希必须不同：bcrypt 每次生成随机盐。
// 相同就说明盐是固定的，那等于全库共用一个彩虹表。
func TestHashPasswordUsesFreshSalt(t *testing.T) {
	first, err := HashPassword("same-password")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	second, err := HashPassword("same-password")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if first == second {
		t.Fatal("identical hashes: salt is not random")
	}
}
