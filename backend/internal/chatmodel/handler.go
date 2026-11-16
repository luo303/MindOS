// 本文件是对话模型配置的 HTTP 入口：绑定参数、把哨兵错误翻成统一错误码。
//
// 它不做业务判断——"名字不能重""Key 要加密"都在 service 里，
// 这样同一套规则将来被别处复用时不需要重写。
package chatmodel

import (
	"errors"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/luo303/mindos/backend/internal/httpx"
)

// Handler 提供 /chat-models 相关接口。
type Handler struct {
	svc *Service
}

// NewHandler 创建 handler。依赖由 cmd/server 装配，router 只负责挂路由。
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// createRequest 是新增配置的请求体。
//
// binding:"required" 只拦"字段缺失或为空串"，格式校验交给 service：
// 校验规则只写一份，否则 handler 与 service 迟早给出不一致的结论。
type createRequest struct {
	Name        string   `json:"name" binding:"required"`
	BaseURL     string   `json:"base_url" binding:"required"`
	Model       string   `json:"model" binding:"required"`
	APIKey      string   `json:"api_key"`
	Temperature *float32 `json:"temperature"`
	MaxTokens   *int     `json:"max_tokens"`
	IsDefault   bool     `json:"is_default"`
}

// updateRequest 是全量替换 + Key 三态。
//
// APIKey 用 *string 而不是 string：JSON 里没有这个字段时是 nil（不改），
// 传 "" 时是指向空串的指针（清除）。这两种意图不能合并。
type updateRequest struct {
	Name        string   `json:"name" binding:"required"`
	BaseURL     string   `json:"base_url" binding:"required"`
	Model       string   `json:"model" binding:"required"`
	APIKey      *string  `json:"api_key"`
	Temperature *float32 `json:"temperature"`
	MaxTokens   *int     `json:"max_tokens"`
}

// probeView 是连通性测试的响应体。
type probeView struct {
	OK        bool   `json:"ok"`
	Model     string `json:"model"`
	LatencyMS int64  `json:"latency_ms"`
	Reply     string `json:"reply"`
}

// List 返回全部配置：GET /chat-models。
//
// 不返回明文 Key，只给末四位与"是否已配置"。浏览器、终端历史、
// 日志抓取都有可能把响应带出去。
func (h *Handler) List(c *gin.Context) {
	items, err := h.svc.List(c.Request.Context())
	if err != nil {
		httpx.Fail(c, mapError(err))
		return
	}
	httpx.OK(c, gin.H{"items": items})
}

// Create 新增一条配置：POST /chat-models。
func (h *Handler) Create(c *gin.Context) {
	var req createRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Fail(c, httpx.ErrInvalidArgument.WithMessage("name, base_url and model are required"))
		return
	}
	view, err := h.svc.Create(c.Request.Context(), CreateInput{
		Name:        req.Name,
		BaseURL:     req.BaseURL,
		Model:       req.Model,
		APIKey:      req.APIKey,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
		IsDefault:   req.IsDefault,
	})
	if err != nil {
		httpx.Fail(c, mapError(err))
		return
	}
	httpx.Created(c, view)
}

// Update 修改一条配置：PATCH /chat-models/:id。
func (h *Handler) Update(c *gin.Context) {
	id, err := parseID(c.Param("id"))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	var req updateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Fail(c, httpx.ErrInvalidArgument.WithMessage("name, base_url and model are required"))
		return
	}
	view, err := h.svc.Update(c.Request.Context(), id, UpdateInput{
		Name:        req.Name,
		BaseURL:     req.BaseURL,
		Model:       req.Model,
		APIKey:      req.APIKey,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
	})
	if err != nil {
		httpx.Fail(c, mapError(err))
		return
	}
	httpx.OK(c, view)
}

// Delete 删除一条配置：DELETE /chat-models/:id。
func (h *Handler) Delete(c *gin.Context) {
	id, err := parseID(c.Param("id"))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	if err := h.svc.Delete(c.Request.Context(), id); err != nil {
		httpx.Fail(c, mapError(err))
		return
	}
	httpx.OK(c, gin.H{"id": id})
}

// SetDefault 指定默认配置：POST /chat-models/:id/default。
//
// 用独立的动作而不是 PATCH 一个 is_default 字段：设默认要先清掉旧默认，
// 是一次有副作用的原子操作，放进"整体替换字段"的 PATCH 里会让人以为
// 它只是又一个可改属性。路径风格与 /documents/:id/reindex 一致。
func (h *Handler) SetDefault(c *gin.Context) {
	id, err := parseID(c.Param("id"))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	view, err := h.svc.SetDefault(c.Request.Context(), id)
	if err != nil {
		httpx.Fail(c, mapError(err))
		return
	}
	httpx.OK(c, view)
}

// Test 用指定配置真发一条消息：POST /chat-models/:id/test。
func (h *Handler) Test(c *gin.Context) {
	id, err := parseID(c.Param("id"))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	probe, err := h.svc.Test(c.Request.Context(), id)
	if err != nil {
		httpx.Fail(c, mapError(err))
		return
	}
	httpx.OK(c, probeView{
		OK:        true,
		Model:     probe.Model,
		LatencyMS: probe.Latency.Milliseconds(),
		Reply:     probe.Reply,
	})
}

// parseID 解析路径里的数字 id。
//
// 与非数字 id 的请求返回 400 而不是交给下游：`/chat-models/abc` 是调用方
// 拼错了，不该变成一次无意义的数据库查询。
func parseID(raw string) (int64, error) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, httpx.ErrInvalidArgument.WithMessage("id must be a positive integer")
	}
	return id, nil
}

// mapError 把模块哨兵错误翻成对外错误码。
//
// 只有 ErrInvalidInput 会把原始文案带给客户端：它的内容是我们自己写的
// "name is required" 这类提示，对用户有用且不含内部细节。
func mapError(err error) error {
	switch {
	case errors.Is(err, ErrNotFound):
		return httpx.ErrChatModelNotFound
	case errors.Is(err, ErrNameConflict):
		return httpx.ErrChatModelNameConflict
	case errors.Is(err, ErrNotConfigured):
		return httpx.ErrModelNotConfigured
	case errors.Is(err, ErrNotSelected):
		return httpx.ErrModelNotSelected
	case errors.Is(err, ErrInvalidInput):
		// 用 %s 而不是直接传 err.Error()：WithMessage 的第一个参数是格式串，
		// 传变量会被 vet 拦下（变量里出现 % 时还会输出乱码）。
		return httpx.ErrInvalidArgument.WithMessage("%s", err.Error())
	case errors.Is(err, ErrDecryptFailed):
		return httpx.ErrMasterKeyMismatch
	default:
		return httpx.ErrInternal.Wrap(err)
	}
}
