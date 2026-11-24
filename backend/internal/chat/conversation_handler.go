package chat

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/luo303/mindos/backend/internal/auth"
	"github.com/luo303/mindos/backend/internal/httpx"
)

// CreateConversation 是 POST /conversations。
type createConversationRequest struct {
	Title string `json:"title"`
}

func (h *Handler) CreateConversation(c *gin.Context) {
	p, ok := auth.PrincipalFrom(c)
	if !ok {
		httpx.Fail(c, httpx.ErrAuthRequired)
		return
	}
	var req createConversationRequest
	// 标题可以完全省略，所以不用 binding:"required"。
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Fail(c, httpx.ErrInvalidArgument.WithMessage("invalid body"))
		return
	}
	conv, err := h.svc.CreateConversation(c.Request.Context(), p.UserID, req.Title)
	if err != nil {
		httpx.Fail(c, httpx.ErrInternal.Wrap(err))
		return
	}
	httpx.Created(c, conv)
}

// ListConversations 是 GET /conversations。
func (h *Handler) ListConversations(c *gin.Context) {
	p, ok := auth.PrincipalFrom(c)
	if !ok {
		httpx.Fail(c, httpx.ErrAuthRequired)
		return
	}
	items, err := h.svc.ListConversations(c.Request.Context(), p.UserID)
	if err != nil {
		httpx.Fail(c, httpx.ErrInternal.Wrap(err))
		return
	}
	httpx.OK(c, gin.H{"items": items})
}

// GetConversation 是 GET /conversations/:id。
func (h *Handler) GetConversation(c *gin.Context) {
	p, ok := auth.PrincipalFrom(c)
	if !ok {
		httpx.Fail(c, httpx.ErrAuthRequired)
		return
	}
	id, ok := conversationID(c)
	if !ok {
		return
	}
	conv, err := h.svc.GetConversation(c.Request.Context(), p.UserID, id)
	if err != nil {
		httpx.Fail(c, mapAskError(err))
		return
	}
	httpx.OK(c, conv)
}

// DeleteConversation 是 DELETE /conversations/:id。
func (h *Handler) DeleteConversation(c *gin.Context) {
	p, ok := auth.PrincipalFrom(c)
	if !ok {
		httpx.Fail(c, httpx.ErrAuthRequired)
		return
	}
	id, ok := conversationID(c)
	if !ok {
		return
	}
	if err := h.svc.DeleteConversation(c.Request.Context(), p.UserID, id); err != nil {
		httpx.Fail(c, mapAskError(err))
		return
	}
	httpx.OK(c, gin.H{"deleted": true})
}

// ListApprovals 是 GET /approvals?status=pending&conversation_id=。
func (h *Handler) ListApprovals(c *gin.Context) {
	p, ok := auth.PrincipalFrom(c)
	if !ok {
		httpx.Fail(c, httpx.ErrAuthRequired)
		return
	}
	conversationID, _ := strconv.ParseInt(c.Query("conversation_id"), 10, 64)
	items, err := h.svc.ListApprovals(c.Request.Context(), p.UserID, conversationID, c.Query("status"))
	if err != nil {
		httpx.Fail(c, httpx.ErrInternal.Wrap(err))
		return
	}
	httpx.OK(c, gin.H{"items": items})
}

type approvalDecisionRequest struct {
	Decision string `json:"decision" binding:"required"`
}

// DecideApproval 是 POST /approvals/:id/decision。
//
// 响应是 SSE：批准后 Resume 的每个 delta 直接推给前端，不再等整段回答。
func (h *Handler) DecideApproval(c *gin.Context) {
	p, ok := auth.PrincipalFrom(c)
	if !ok {
		httpx.Fail(c, httpx.ErrAuthRequired)
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		httpx.Fail(c, httpx.ErrInvalidArgument.WithMessage("invalid approval id"))
		return
	}
	var req approvalDecisionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Fail(c, httpx.ErrInvalidArgument.WithMessage("decision is required"))
		return
	}
	if req.Decision != "approve" && req.Decision != "reject" {
		httpx.Fail(c, httpx.ErrInvalidArgument.WithMessage("decision must be approve or reject"))
		return
	}
	writer := &sseWriter{c: c}
	_, err = h.svc.DecideApproval(c.Request.Context(), p.UserID, id, req.Decision == "approve", writer)
	if err == nil {
		return
	}
	if writer.started {
		code, message, retryable := classifyRunError(err)
		_ = writer.ErrorDetail(code, message, errorDetail(err), retryable)
		return
	}
	httpx.Fail(c, mapAskError(err))
}

// ListMessages 是 GET /conversations/:id/messages。
func (h *Handler) ListMessages(c *gin.Context) {
	p, ok := auth.PrincipalFrom(c)
	if !ok {
		httpx.Fail(c, httpx.ErrAuthRequired)
		return
	}
	id, ok := conversationID(c)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(c.Query("limit"))
	before, _ := strconv.Atoi(c.Query("before_seq"))
	items, err := h.svc.ListMessages(c.Request.Context(), p.UserID, id, limit, before)
	if err != nil {
		httpx.Fail(c, mapAskError(err))
		return
	}
	httpx.OK(c, gin.H{"items": items})
}

// PostMessage 是 POST /conversations/:id/messages，SSE 流式回答。
func (h *Handler) PostMessage(c *gin.Context) {
	p, ok := auth.PrincipalFrom(c)
	if !ok {
		httpx.Fail(c, httpx.ErrAuthRequired)
		return
	}
	id, ok := conversationID(c)
	if !ok {
		return
	}
	var req askRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Fail(c, httpx.ErrInvalidArgument.WithMessage("question is required"))
		return
	}

	writer := &sseWriter{c: c}
	err := h.svc.AskInConversation(c.Request.Context(), p.UserID, id, AskInput{
		Question:    req.Question,
		ChatModelID: req.ChatModelID,
	}, writer)
	if err == nil {
		return
	}
	if writer.started {
		_ = writer.Error("internal", "回答过程中出错，请重试", true)
		return
	}
	httpx.Fail(c, mapAskError(err))
}

// conversationID 解析路径参数；非法时直接写 400 并返回 false。
func conversationID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		httpx.Fail(c, httpx.ErrInvalidArgument.WithMessage("invalid conversation id"))
		return 0, false
	}
	return id, true
}
