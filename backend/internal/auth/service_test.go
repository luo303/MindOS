package auth

import (
	"strings"
	"testing"
	"time"
)

func TestNormalizeEmail(t *testing.T) {
	valid := map[string]string{
		" Alice@Example.COM ": "alice@example.com",
		"bob+tag@sub.example.cn": "bob+tag@sub.example.cn",
	}
	for in, want := range valid {
		got, err := normalizeEmail(in)
		if err != nil || got != want {
			t.Errorf("normalizeEmail(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	invalid := []string{"", "no-at-sign", "a@b", "a@.com", "@example.com", "a b@example.com", "Name <a@example.com>"}
	for _, in := range invalid {
		if got, err := normalizeEmail(in); err == nil {
			t.Errorf("normalizeEmail(%q) = %q, want error", in, got)
		}
	}
}

func TestNormalizeDisplayNameFallsBackToEmail(t *testing.T) {
	if got := normalizeDisplayName("", "alice@example.com"); got != "alice" {
		t.Errorf("display name = %q, want alice", got)
	}
	long := strings.Repeat("名", maxDisplayNameRunes+10)
	if got := normalizeDisplayName(long, "a@b.com"); len([]rune(got)) != maxDisplayNameRunes {
		t.Errorf("display name runes = %d, want %d", len([]rune(got)), maxDisplayNameRunes)
	}
}

func TestNewTokenIsRandomAndHashed(t *testing.T) {
	token, hash, err := newToken()
	if err != nil {
		t.Fatalf("newToken: %v", err)
	}
	if len(token) != 43 {
		t.Errorf("token length = %d, want 43 (32 bytes base64url)", len(token))
	}
	if len(hash) != 32 {
		t.Errorf("hash length = %d, want 32 (sha256)", len(hash))
	}
	if string(hash) == token {
		t.Fatal("token hash equals the token itself")
	}
	other, otherHash, err := newToken()
	if err != nil {
		t.Fatalf("newToken: %v", err)
	}
	if token == other {
		t.Fatal("two tokens are identical")
	}
	if string(hash) == string(otherHash) {
		t.Fatal("two different tokens hash to the same value")
	}
	if string(hashToken(token)) != string(hash) {
		t.Fatal("hashToken does not match newToken's hash")
	}
}

func TestLimiterAllowsUpToLimitWindow(t *testing.T) {
	l := NewLimiter(2, time.Minute)
	if !l.Allow("1.2.3.4") || !l.Allow("1.2.3.4") {
		t.Fatal("first two attempts should pass")
	}
	if l.Allow("1.2.3.4") {
		t.Fatal("third attempt within the window should be rejected")
	}
	// 另一个 IP 不受影响。
	if !l.Allow("5.6.7.8") {
		t.Fatal("a different key should have its own bucket")
	}
	// limit <= 0 表示关闭限流（测试与排障用）。
	off := NewLimiter(0, time.Minute)
	for i := 0; i < 100; i++ {
		if !off.Allow("x") {
			t.Fatal("limit 0 should never block")
		}
	}
}

func TestTruncateBytesKeepsValidUTF8(t *testing.T) {
	// 5 个汉字 = 15 字节；截到 4 字节会切坏第二个字，必须退回到 3 字节。
	got := truncateBytes("一二三四五", 4)
	if got != "一" {
		t.Fatalf("truncateBytes = %q, want 一", got)
	}
	if truncateBytes("abc", 10) != "abc" {
		t.Fatal("short string changed")
	}
}
