package memory

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/luo303/mindos/backend/internal/auth"
	"github.com/luo303/mindos/backend/internal/httpx"
)

// Handler 把登录账号传给偏好服务，不接受客户端指定 owner。
type Handler struct{ service *Service }

// NewHandler 创建长期记忆接口。
func NewHandler(service *Service) *Handler { return &Handler{service: service} }

// List 返回当前偏好。
func (handler *Handler) List(c *gin.Context) {
	handler.run(c, func(ctx context.Context, ownerID int64) error {
		items, err := handler.service.List(ctx, ownerID)
		if err == nil {
			httpx.OK(c, gin.H{"items": items})
		}
		return err
	})
}

// Create 创建新的默认偏好。
func (handler *Handler) Create(c *gin.Context) {
	handler.run(c, func(ctx context.Context, ownerID int64) error {
		var body struct {
			Key   string `json:"key"`
			Value string `json:"value" binding:"required"`
			Metadata
		}
		if c.ShouldBindJSON(&body) != nil {
			return ErrInvalid
		}
		var item Memory
		var err error
		if body.Key != "" && body.Category == "" {
			item, err = handler.service.Create(ctx, ownerID, body.Key, body.Value)
		} else {
			item, err = handler.service.CreateEntry(ctx, ownerID, body.Value, body.Metadata)
		}
		if err == nil {
			httpx.Created(c, item)
		}
		return err
	})
}

// Update 编辑、归档或恢复已有偏好。
func (handler *Handler) Update(c *gin.Context) {
	handler.run(c, func(ctx context.Context, ownerID int64) error {
		id, err := positiveID(c.Param("id"))
		if err != nil {
			return err
		}
		var body struct {
			Expected int       `json:"expected_version" binding:"required"`
			Value    *string   `json:"value"`
			Status   string    `json:"status"`
			Metadata *Metadata `json:"metadata"`
		}
		if c.ShouldBindJSON(&body) != nil {
			return ErrInvalid
		}
		item, err := handler.service.Update(ctx, ownerID, id, body.Expected, body.Value, body.Status, body.Metadata)
		if err == nil {
			httpx.OK(c, item)
		}
		return err
	})
}

// Evidence 返回来源记录。
func (handler *Handler) Evidence(c *gin.Context) {
	handler.run(c, func(ctx context.Context, ownerID int64) error {
		id, err := positiveID(c.Param("id"))
		if err != nil {
			return err
		}
		items, err := handler.service.Evidence(ctx, ownerID, id)
		if err == nil {
			httpx.OK(c, gin.H{"items": items})
		}
		return err
	})
}

// Settings 返回当前模式与可选的会话设置。
func (handler *Handler) Settings(c *gin.Context) {
	handler.run(c, func(ctx context.Context, ownerID int64) error {
		var id int64
		var err error
		if raw := c.Query("conversation_id"); raw != "" {
			id, err = positiveID(raw)
			if err != nil {
				return err
			}
		}
		item, err := handler.service.Settings(ctx, ownerID, id)
		if err == nil {
			httpx.OK(c, item)
		}
		return err
	})
}

// SetSettings 编辑记忆模式或会话开关。
func (handler *Handler) SetSettings(c *gin.Context) {
	handler.run(c, func(ctx context.Context, ownerID int64) error {
		var body struct {
			Mode           string `json:"mode"`
			ConversationID int64  `json:"conversation_id"`
			Off            *bool  `json:"conversation_off"`
		}
		if c.ShouldBindJSON(&body) != nil {
			return ErrInvalid
		}
		item, err := handler.service.SetSettings(ctx, ownerID, body.ConversationID, body.Mode, body.Off)
		if err == nil {
			httpx.OK(c, item)
		}
		return err
	})
}

// Versions 返回指定偏好的修订历史。
func (handler *Handler) Versions(c *gin.Context) {
	handler.run(c, func(ctx context.Context, ownerID int64) error {
		id, err := positiveID(c.Param("id"))
		if err != nil {
			return err
		}
		before := 0
		if raw := c.Query("before_version"); raw != "" {
			before, err = strconv.Atoi(raw)
			if err != nil || before <= 0 {
				return ErrInvalid
			}
		}
		items, err := handler.service.Versions(ctx, ownerID, id, before)
		if err == nil {
			httpx.OK(c, gin.H{"items": items})
		}
		return err
	})
}

// Candidates 列出账号或指定会话的待确认项。
func (handler *Handler) Candidates(c *gin.Context) {
	handler.run(c, func(ctx context.Context, ownerID int64) error {
		var conversationID int64
		if raw := c.Query("conversation_id"); raw != "" {
			var err error
			conversationID, err = positiveID(raw)
			if err != nil {
				return err
			}
		}
		items, err := handler.service.Candidates(ctx, ownerID, conversationID)
		if err == nil {
			httpx.OK(c, gin.H{"items": items})
		}
		return err
	})
}

// Decide 确认或拒绝候选；不信任客户端传来的基础版本。
func (handler *Handler) Decide(c *gin.Context) {
	handler.run(c, func(ctx context.Context, ownerID int64) error {
		id, err := positiveID(c.Param("id"))
		if err != nil {
			return err
		}
		var body struct {
			Decision string  `json:"decision" binding:"required"`
			Value    *string `json:"value"`
		}
		if c.ShouldBindJSON(&body) != nil {
			return ErrInvalid
		}
		if err := handler.service.Decide(ctx, ownerID, id, body.Decision, body.Value); err != nil {
			return err
		}
		httpx.OK(c, gin.H{"decision": body.Decision})
		return nil
	})
}

func (handler *Handler) run(c *gin.Context, fn func(context.Context, int64) error) {
	principal, ok := auth.PrincipalFrom(c)
	if !ok {
		httpx.Fail(c, httpx.ErrAuthRequired)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()
	err := fn(ctx, principal.UserID)
	switch {
	case err == nil:
	case errors.Is(err, ErrInvalid):
		httpx.Fail(c, httpx.ErrInvalidArgument.WithMessage("无效的记忆内容、范围或操作"))
	case errors.Is(err, ErrNotFound):
		httpx.Fail(c, httpx.ErrNotFound)
	case errors.Is(err, ErrConflict):
		httpx.Fail(c, httpx.ErrConflict.WithMessage("记忆或建议状态已变化，请核对当前值"))
	default:
		httpx.Fail(c, err)
	}
}

func positiveID(raw string) (int64, error) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, ErrInvalid
	}
	return id, nil
}
