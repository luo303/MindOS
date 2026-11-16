package chatmodel

import (
	"bytes"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testKey(fill byte) []byte { return bytes.Repeat([]byte{fill}, masterKeySize) }

func TestCipherRoundTrip(t *testing.T) {
	c, err := NewCipher(testKey(7))
	if err != nil {
		t.Fatalf("NewCipher: %v", err)
	}
	// 空串也要能往返：某些兼容端点不需要 Key，那条配置会存空字符串。
	// 中文用来确认走的是字节而不是被某种字符集截断。
	secrets := []string{"sk-1234567890abcdef", "", strings.Repeat("密钥", 500)}
	for _, want := range secrets {
		blob, err := c.Encrypt(want)
		if err != nil {
			t.Fatalf("Encrypt(%d chars): %v", len(want), err)
		}
		got, err := c.Decrypt(blob)
		if err != nil {
			t.Fatalf("Decrypt: %v", err)
		}
		if got != want {
			t.Fatalf("round trip mismatch: got %q, want %q", got, want)
		}
	}
}

func TestCipherUsesFreshNonce(t *testing.T) {
	c, err := NewCipher(testKey(7))
	if err != nil {
		t.Fatalf("NewCipher: %v", err)
	}
	first, err := c.Encrypt("same-plaintext")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	second, err := c.Encrypt("same-plaintext")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	// 同一明文两次加密必须得到不同密文：nonce 复用是 GCM 最致命的误用，
	// 这条断言就是那条规则的可执行版本。
	if bytes.Equal(first, second) {
		t.Fatal("same plaintext produced identical ciphertext: nonce is being reused")
	}
}

func TestNewCipherRejectsWrongKeySize(t *testing.T) {
	for _, size := range []int{0, 16, 24, 31, 33} {
		key := bytes.Repeat([]byte{1}, size)
		if _, err := NewCipher(key); !errors.Is(err, ErrMasterKeyInvalid) {
			t.Errorf("NewCipher(%d bytes) err = %v, want ErrMasterKeyInvalid", size, err)
		}
	}
}

func TestDecryptFailsWithAnotherKey(t *testing.T) {
	writer, err := NewCipher(testKey(1))
	if err != nil {
		t.Fatalf("NewCipher: %v", err)
	}
	reader, err := NewCipher(testKey(2))
	if err != nil {
		t.Fatalf("NewCipher: %v", err)
	}
	blob, err := writer.Encrypt("sk-secret")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	// 换主密钥后必须明确失败，不能返回空串——空串会让上层构造出一个
	// "没有 Key 的请求"，厂商回 401，人去查 Key 过期，而真因是密钥不匹配。
	if _, err := reader.Decrypt(blob); !errors.Is(err, ErrDecryptFailed) {
		t.Fatalf("err = %v, want ErrDecryptFailed", err)
	}
}

func TestDecryptRejectsTruncatedBlob(t *testing.T) {
	c, err := NewCipher(testKey(3))
	if err != nil {
		t.Fatalf("NewCipher: %v", err)
	}
	for _, blob := range [][]byte{nil, {}, {1, 2, 3}} {
		if _, err := c.Decrypt(blob); !errors.Is(err, ErrDecryptFailed) {
			t.Errorf("Decrypt(%d bytes) err = %v, want ErrDecryptFailed", len(blob), err)
		}
	}
}

func TestLoadMasterKeyPrefersEnv(t *testing.T) {
	key := testKey(9)
	dir := t.TempDir()
	got, err := LoadMasterKey(base64.StdEncoding.EncodeToString(key), dir)
	if err != nil {
		t.Fatalf("LoadMasterKey: %v", err)
	}
	if !bytes.Equal(got, key) {
		t.Fatal("env key was not used")
	}
	// 环境变量优先时不该顺手生成文件：否则同一个目录里会躺着一把用不到的密钥，
	// 以后误把环境变量删掉就会换一把，已存的密文全部解不开。
	if _, err := os.Stat(filepath.Join(dir, masterKeyFile)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("master.key should not be created when the env var is set, err = %v", err)
	}
}

func TestLoadMasterKeyGeneratesAndReuses(t *testing.T) {
	dir := t.TempDir()
	first, err := LoadMasterKey("", dir)
	if err != nil {
		t.Fatalf("LoadMasterKey: %v", err)
	}
	if len(first) != masterKeySize {
		t.Fatalf("generated key length = %d, want %d", len(first), masterKeySize)
	}
	second, err := LoadMasterKey("", dir)
	if err != nil {
		t.Fatalf("LoadMasterKey: %v", err)
	}
	// 第二次必须读回同一个密钥：每次启动都换一把，等于所有已存 Key 立即失效。
	if !bytes.Equal(first, second) {
		t.Fatal("second load generated a different key")
	}
	info, err := os.Stat(filepath.Join(dir, masterKeyFile))
	if err != nil {
		t.Fatalf("stat master.key: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("master.key permission = %o, want 600", perm)
	}
}

func TestLoadMasterKeyRejectsBadEncoding(t *testing.T) {
	if _, err := LoadMasterKey("not-base64!!", t.TempDir()); err == nil {
		t.Error("invalid base64 should fail")
	}
	short := base64.StdEncoding.EncodeToString([]byte("too-short"))
	if _, err := LoadMasterKey(short, t.TempDir()); !errors.Is(err, ErrMasterKeyInvalid) {
		t.Errorf("err = %v, want ErrMasterKeyInvalid", err)
	}
}
