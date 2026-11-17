package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

type envelope struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func TestCheckOrigin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(CheckOrigin())
	e.POST("/x", func(c *gin.Context) { c.Status(http.StatusOK) })
	e.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })

	cases := []struct {
		name   string
		method string
		origin string
		want   int
	}{
		// curl / 服务端调用不带 Origin：必须放行，否则 AGENTS.md 要求的真实 curl 验证做不了。
		{"no origin", http.MethodPost, "", http.StatusOK},
		{"same host", http.MethodPost, "http://example.com", http.StatusOK},
		// 跨站写请求：SameSite 之外的第二道闸。
		{"cross site", http.MethodPost, "http://evil.example.net", http.StatusForbidden},
		// GET 不产生副作用，不受这条限制。
		{"get with cross origin", http.MethodGet, "http://evil.example.net", http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "http://example.com/x", nil)
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
			if tc.want == http.StatusForbidden {
				var env envelope
				if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
					t.Fatalf("decode envelope: %v", err)
				}
				if env.Code != "AUTH_ORIGIN_MISMATCH" {
					t.Errorf("code = %q, want AUTH_ORIGIN_MISMATCH", env.Code)
				}
			}
		})
	}
}

func TestSessionMiddlewareRejectsMissingCookie(t *testing.T) {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	// repo 传 nil 是安全的：没有 Cookie 时中间件在访问数据库之前就中断了。
	e.Use(NewSessionMiddleware(NewService(nil, 0, false, nil)))
	e.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	var env envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if env.Code != "AUTH_REQUIRED" {
		t.Errorf("code = %q, want AUTH_REQUIRED", env.Code)
	}
}

func TestSessionMiddlewareRejectsForgedToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc, _ := setupAuthService(t, true)

	e := gin.New()
	e.Use(NewSessionMiddleware(svc))
	e.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: "not-a-real-token"})
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}
