package chatmodel

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestNormalizeModel(t *testing.T) {
	cases := []struct {
		name    string
		inName  string
		inURL   string
		inModel string
		wantErr bool
		// wantURL 只在成功时需要检查；尾部斜杠必须被去掉，
		// 否则"看起来一样的两条配置"在界面上无法区分。
		wantURL string
	}{
		{name: "ok", inName: "  DeepSeek  ", inURL: "https://api.deepseek.com/v1/", inModel: "deepseek-chat",
			wantURL: "https://api.deepseek.com/v1"},
		{name: "http allowed", inName: "ollama", inURL: "http://localhost:11434/v1", inModel: "qwen",
			wantURL: "http://localhost:11434/v1"},
		{name: "empty name", inName: "  ", inURL: "https://a/v1", inModel: "m", wantErr: true},
		{name: "empty url", inName: "n", inURL: "  ", inModel: "m", wantErr: true},
		{name: "url without scheme", inName: "n", inURL: "api.example.com/v1", inModel: "m", wantErr: true},
		{name: "empty model", inName: "n", inURL: "https://a/v1", inModel: "   ", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			Name, URL, Model, err := normalizeModel(tc.inName, tc.inURL, tc.inModel)
			if tc.wantErr {
				if !errors.Is(err, ErrInvalidInput) {
					t.Fatalf("err = %v, want ErrInvalidInput", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if URL != tc.wantURL {
				t.Errorf("base_url = %q, want %q", URL, tc.wantURL)
			}
			if Name == "" || Model == "" {
				t.Errorf("name/model trimmed to empty: %q %q", Name, Model)
			}
		})
	}
}

func TestValidateOptions(t *testing.T) {
	tooHot := float32(2.5)
	ok := float32(0.3)
	zeroTokens := 0
	tokens := 4096

	if err := validateOptions(&tooHot, nil); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("temperature 2.5 err = %v, want ErrInvalidInput", err)
	}
	if err := validateOptions(&ok, &zeroTokens); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("max_tokens 0 err = %v, want ErrInvalidInput", err)
	}
	// 两个字段都为空是合法的：表示"用厂商默认值"，不是"没填"。
	if err := validateOptions(nil, nil); err != nil {
		t.Errorf("nil options err = %v, want nil", err)
	}
	if err := validateOptions(&ok, &tokens); err != nil {
		t.Errorf("valid options err = %v, want nil", err)
	}
}

func TestHintOf(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"short", ""},    // 8 位以内不显示尾号
		{"12345678", ""}, // 恰好 8 位
		{"sk-1234567890abcdef", "cdef"},
	}
	for _, tc := range cases {
		if got := hintOf(tc.in); got != tc.want {
			t.Errorf("hintOf(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestViewOfHidesPlaintextKey(t *testing.T) {
	item := &ChatModel{
		ID:      1,
		Name:    "n",
		BaseURL: "https://a/v1",
		Model:   "m",
		// 故意把"明文"塞进密文字段：如果 View 哪天加了 cipher 字段，
		// 这条断言会立刻抓到——界面拿到的 JSON 里绝不能出现它。
		APIKeyCipher: []byte("sk-plaintext-should-never-leak"),
		APIKeyHint:   "cdef",
	}
	view := viewOf(item)
	if !view.APIKeyConfigured {
		t.Error("APIKeyConfigured should be true when ciphertext exists")
	}
	if view.APIKeyHint != "cdef" {
		t.Errorf("APIKeyHint = %q", view.APIKeyHint)
	}

	payload, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("marshal view: %v", err)
	}
	if strings.Contains(string(payload), "sk-plaintext") {
		t.Fatalf("view leaks the credential: %s", payload)
	}
}
