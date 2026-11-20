package todo

import (
	"errors"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/luo303/mindos/backend/internal/auth"
	"github.com/luo303/mindos/backend/internal/httpx"
)

type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

type createRequest struct {
	Title       string     `json:"title" binding:"required"`
	Description string     `json:"description"`
	Priority    int        `json:"priority"`
	DueAt       *time.Time `json:"due_at"`
}

func (h *Handler) Create(c *gin.Context) {
	p, ok := auth.PrincipalFrom(c)
	if !ok {
		httpx.Fail(c, httpx.ErrAuthRequired)
		return
	}
	var req createRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Fail(c, httpx.ErrInvalidArgument.WithMessage("title is required"))
		return
	}
	t, err := h.svc.Create(c.Request.Context(), p.UserID, CreateInput{
		Title:       req.Title,
		Description: req.Description,
		Priority:    req.Priority,
		DueAt:       req.DueAt,
		Source:      SourceManual,
	})
	if err != nil {
		httpx.Fail(c, mapError(err))
		return
	}
	httpx.Created(c, t)
}

func (h *Handler) List(c *gin.Context) {
	p, ok := auth.PrincipalFrom(c)
	if !ok {
		httpx.Fail(c, httpx.ErrAuthRequired)
		return
	}
	limit, _ := strconv.Atoi(c.Query("limit"))
	filter := Filter{Status: c.Query("status"), Limit: limit}
	if raw := c.Query("due_before"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			httpx.Fail(c, httpx.ErrInvalidArgument.WithMessage("invalid due_before"))
			return
		}
		filter.DueBefore = &parsed
	}
	items, err := h.svc.List(c.Request.Context(), p.UserID, filter)
	if err != nil {
		httpx.Fail(c, mapError(err))
		return
	}
	httpx.OK(c, gin.H{"items": items})
}

type updateRequest struct {
	Title       *string    `json:"title"`
	Description *string    `json:"description"`
	Status      *string    `json:"status"`
	Priority    *int       `json:"priority"`
	DueAt       *time.Time `json:"due_at"`
	ClearDue    bool       `json:"clear_due"`
}

func (h *Handler) Update(c *gin.Context) {
	p, ok := auth.PrincipalFrom(c)
	if !ok {
		httpx.Fail(c, httpx.ErrAuthRequired)
		return
	}
	id, err := todoID(c)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	var req updateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Fail(c, httpx.ErrInvalidArgument.WithMessage("invalid body"))
		return
	}
	t, svcErr := h.svc.Update(c.Request.Context(), p.UserID, id, UpdateInput{
		Title:       req.Title,
		Description: req.Description,
		Status:      req.Status,
		Priority:    req.Priority,
		DueAt:       req.DueAt,
		ClearDue:    req.ClearDue,
	})
	if svcErr != nil {
		httpx.Fail(c, mapError(svcErr))
		return
	}
	httpx.OK(c, t)
}

func (h *Handler) Delete(c *gin.Context) {
	p, ok := auth.PrincipalFrom(c)
	if !ok {
		httpx.Fail(c, httpx.ErrAuthRequired)
		return
	}
	id, err := todoID(c)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	if err := h.svc.Delete(c.Request.Context(), p.UserID, id); err != nil {
		httpx.Fail(c, mapError(err))
		return
	}
	httpx.OK(c, gin.H{"deleted": true})
}

func todoID(c *gin.Context) (int64, error) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, httpx.ErrInvalidArgument.WithMessage("invalid todo id")
	}
	return id, nil
}

func mapError(err error) *httpx.Error {
	switch {
	case errors.Is(err, ErrNotFound):
		return httpx.ErrNotFound.WithMessage("todo not found")
	case errors.Is(err, ErrInvalidTitle), errors.Is(err, ErrTitleTooLong),
		errors.Is(err, ErrDescriptionTooLong), errors.Is(err, ErrInvalidPriority),
		errors.Is(err, ErrInvalidStatus):
		return httpx.ErrInvalidArgument.WithMessage("%s", err.Error())
	default:
		return httpx.ErrInternal.Wrap(err)
	}
}
