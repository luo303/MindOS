package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/luo303/mindos/backend/internal/config"
)

func TestMaskSecret(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},                                // 未配置
		{"short", "****"},                       // 短密钥全遮
		{"sk-1234567890abcdef", "sk-1****cdef"}, // 常规：前 4 后 4
		{"12345678", "****"},                    // 恰好 8 位也全遮（信息量低）
		{"123456789", "1234****6789"},           // 9 位起才露片段
	}
	for _, tc := range cases {
		if got := maskSecret(tc.in); got != tc.want {
			t.Errorf("maskSecret(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestSettingsGetMasksSecrets 是安全红线测试：响应里任何地方都不能出现完整密钥。
//
// 对话模型已从 .env 迁到数据库（/chat-models），所以这里守的是向量模型的密钥。
func TestSettingsGetMasksSecrets(t *testing.T) {
	const apiKey = "sk-live-0123456789abcdef"
	cfg := &config.Config{
		App: config.AppConfig{Env: "dev", HTTPAddr: ":8080"},
		Log: config.LogConfig{Level: "debug", Format: "console"},
		DB:  config.DBConfig{Host: "localhost", Port: 5432, Name: "mindos", User: "mindos", Password: "db-password"},
		Embedding: config.EmbeddingConfig{
			APIBase: "https://api.example.com/v1", APIKey: apiKey, Model: "BAAI/bge-m3",
		},
		Agent: config.AgentConfig{MaxSteps: 30, TokenBudget: 200000, RunTimeout: 5 * time.Minute},
	}
	h := NewSettingsHandler(cfg, nil)

	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/settings", nil)
	h.Get(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status code = %d, want 200", rec.Code)
	}
	body := rec.Body.String()

	if strings.Contains(body, apiKey) {
		t.Fatalf("response leaks the full API key: %s", body)
	}
	if strings.Contains(body, "db-password") {
		t.Fatalf("response leaks the DB password: %s", body)
	}

	var parsed struct {
		Data struct {
			Embedding struct {
				APIKeyMasked     string `json:"api_key_masked"`
				APIKeyConfigured bool   `json:"api_key_configured"`
				Model            string `json:"model"`
			} `json:"embedding"`
			Agent struct {
				RunTimeoutSecond int64 `json:"run_timeout_seconds"`
			} `json:"agent"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !parsed.Data.Embedding.APIKeyConfigured {
		t.Error("embedding.api_key_configured should be true")
	}
	if parsed.Data.Embedding.APIKeyMasked == "" {
		t.Error("embedding.api_key_masked should not be empty")
	}
	if parsed.Data.Embedding.Model != "BAAI/bge-m3" {
		t.Errorf("embedding.model = %q", parsed.Data.Embedding.Model)
	}
	if parsed.Data.Agent.RunTimeoutSecond != 300 {
		t.Errorf("agent.run_timeout_seconds = %d, want 300", parsed.Data.Agent.RunTimeoutSecond)
	}
}

// TestSettingsOmitsChatModel 确认对话模型不再出现在 /settings。
//
// 它现在由用户配置并存库，接口在 /chat-models。这里保留一个"字段不存在"
// 的断言，是为了防止有人后来为了兼容而把它加回来——加回来的结果一定是
// 前端读到一个永远为空的区块，然后有人去查为什么"配了却读不到"。
func TestSettingsOmitsChatModel(t *testing.T) {
	cfg := &config.Config{
		App: config.AppConfig{Env: "dev", HTTPAddr: ":8080"},
		Log: config.LogConfig{Level: "debug", Format: "console"},
		DB:  config.DBConfig{Host: "localhost", Port: 5432, Name: "mindos", User: "mindos", Password: "pw"},
	}
	h := NewSettingsHandler(cfg, nil)

	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/settings", nil)
	h.Get(c)

	var parsed struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := parsed.Data["llm"]; ok {
		t.Error("/settings must not expose an llm block anymore")
	}
	if _, ok := parsed.Data["embedding"]; !ok {
		t.Error("/settings should still expose the embedding block")
	}
}

// TestSettingsGetUnconfiguredEmbedding 确认未配置时如实返回，不伪造成功状态。
func TestSettingsGetUnconfiguredEmbedding(t *testing.T) {
	cfg := &config.Config{
		App: config.AppConfig{Env: "dev", HTTPAddr: ":8080"},
		Log: config.LogConfig{Level: "debug", Format: "console"},
		DB:  config.DBConfig{Host: "localhost", Port: 5432, Name: "mindos", User: "mindos", Password: "pw"},
	}
	h := NewSettingsHandler(cfg, nil)

	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/settings", nil)
	h.Get(c)

	var parsed struct {
		Data struct {
			Embedding struct {
				APIKeyMasked     string `json:"api_key_masked"`
				APIKeyConfigured bool   `json:"api_key_configured"`
			} `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if parsed.Data.Embedding.APIKeyConfigured {
		t.Error("embedding.api_key_configured should be false when unset")
	}
	if parsed.Data.Embedding.APIKeyMasked != "" {
		t.Errorf("embedding.api_key_masked should be empty when unset, got %q", parsed.Data.Embedding.APIKeyMasked)
	}
}
