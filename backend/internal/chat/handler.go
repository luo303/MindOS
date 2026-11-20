// 本文件是问答的 HTTP 入口：把 Emitter 实现成 SSE 写出器，并翻译启动期错误。
//
// 它只做协议：不判断该不该拒答、不解析引用、不碰数据库。
// 那些都在 service 里，这样同一套规则换成 WebSocket 或命令行时不用重写。
package chat

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/luo303/mindos/backend/internal/auth"
	"github.com/luo303/mindos/backend/internal/chatmodel"
	"github.com/luo303/mindos/backend/internal/httpx"
	"github.com/luo303/mindos/backend/internal/knowledge"
)

// Handler 提供 POST /ask。
type Handler struct {
	svc *Service
}

// NewHandler 创建 handler。依赖由 cmd/server 装配。
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// askRequest 是问答请求体。
//
// ChatModelID 可省：省了按"默认 → 唯一一条 → 报错"的顺序解析。
type askRequest struct {
	Question    string `json:"question" binding:"required"`
	ChatModelID *int64 `json:"chat_model_id"`
}

// Ask 是流式问答：POST /ask。
//
// 错误分两段处理：
//   - 第一个事件之前失败 → 正常的 JSON 错误（409/503/400），前端能按状态码处理；
//   - 已经开始推事件 → 只能补一个 error 事件，因为 HTTP 状态码已经发出去了。
//
// 这个分界由 sseWriter.started 判断，不靠猜"大概写到哪了"。
func (h *Handler) Ask(c *gin.Context) {
	p, ok := auth.PrincipalFrom(c)
	if !ok {
		httpx.Fail(c, httpx.ErrAuthRequired)
		return
	}
	var req askRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Fail(c, httpx.ErrInvalidArgument.WithMessage("question is required"))
		return
	}

	writer := &sseWriter{c: c}
	err := h.svc.Ask(c.Request.Context(), p.UserID, AskInput{
		Question:    req.Question,
		ChatModelID: req.ChatModelID,
	}, writer)
	if err == nil {
		return
	}
	if writer.started {
		// 流已经开始，改不了状态码。发一个收尾错误，让前端停在"失败"而不是"成功"。
		_ = writer.Error("internal", "回答过程中出错，请重试", true)
		return
	}
	httpx.Fail(c, mapAskError(err))
}

// sseWriter 把 Emitter 实现成 SSE。
//
// 事件体是 {"type","seq","payload"}：与项目既有 SSE 约定一致，
// 前端按 seq 排序即可，不需要自己维护顺序。
type sseWriter struct {
	c   *gin.Context
	seq int
	// started 表示响应头已经写出：此后不能再改状态码，也不能再返回 JSON。
	started bool
}

type sseEvent struct {
	Type    string `json:"type"`
	Seq     int    `json:"seq"`
	Payload any    `json:"payload"`
}

func (w *sseWriter) Retrieval(items []RetrievalItem) error {
	return w.send("retrieval", gin.H{"hits": items})
}

func (w *sseWriter) Delta(text string) error {
	return w.send("delta", gin.H{"text": text})
}

func (w *sseWriter) Verdict(status, reason string) error {
	return w.send("verdict", gin.H{"status": status, "reason": reason})
}

func (w *sseWriter) Citation(items []CitationItem) error {
	return w.send("citation", gin.H{"items": items})
}

func (w *sseWriter) Done(payload DonePayload) error {
	return w.send("done", payload)
}

func (w *sseWriter) Error(code, message string, retryable bool) error {
	return w.send("error", gin.H{"code": code, "message": message, "retryable": retryable})
}

func (w *sseWriter) send(eventType string, payload any) error {
	w.seq++
	// 先序列化再写：编码失败时不能把半条 JSON 推出去，
	// 前端按空行切分，半条会让后面所有事件错位。
	data, err := json.Marshal(sseEvent{Type: eventType, Seq: w.seq, Payload: payload})
	if err != nil {
		return fmt.Errorf("encode sse event %s: %w", eventType, err)
	}

	w.ensureStarted()

	if _, err := fmt.Fprintf(w.c.Writer, "data: %s\n\n", data); err != nil {
		return err
	}
	// 必须显式 Flush：gin 默认会缓冲，不刷就变成"全部算完再一次性吐出来"，
	// 流式就白做了。
	w.c.Writer.Flush()
	return nil
}

func (w *sseWriter) ensureStarted() {
	if w.started {
		return
	}
	// http.Server.WriteTimeout 是整条响应的硬上限（从读完请求头到响应写完）。
	// 普通接口 20s 足够，但流式问答轻松超过它：到期后内核写缓冲直接报
	// i/o timeout，现象就是"发了 retrieval 之后连接被砍"。
	// SSE 的存活由 RAG_TIMEOUT 与客户端断开负责，所以把这个请求的写截止清掉——
	// http.NewResponseController + SetWriteDeadline 是 Go 1.20 起官方支持的写法。
	// 只有这一个接口需要，普通接口仍受全局 WriteTimeout 保护。
	if err := http.NewResponseController(w.c.Writer).SetWriteDeadline(time.Time{}); err != nil {
		// 清不掉只是回到"有写超时"的旧行为，不该因此让整个问答失败。
		w.c.Error(fmt.Errorf("clear write deadline: %w", err))
	}
	header := w.c.Writer.Header()
	header.Set("Content-Type", "text/event-stream; charset=utf-8")
	header.Set("Cache-Control", "no-cache")
	header.Set("Connection", "keep-alive")
	// 关掉反向代理的响应缓冲，否则事件会攒着一起发，看起来像"卡了十秒然后瞬间出完"。
	header.Set("X-Accel-Buffering", "no")
	w.c.Writer.WriteHeader(http.StatusOK)
	w.started = true
}

// mapAskError 把启动期错误翻成 HTTP 错误码。
//
// 只覆盖"流开始之前"能遇到的错误：模型配置、向量配置、参数。
// 执行期错误在 service 里已经通过 Error 事件给出，不走这里。
func mapAskError(err error) error {
	switch {
	case errors.Is(err, ErrEmptyQuestion), errors.Is(err, ErrQuestionTooLong):
		return httpx.ErrInvalidArgument.WithMessage("%s", err.Error())
	case errors.Is(err, ErrApprovalPending):
		return httpx.ErrConflict.WithMessage("当前会话有待审批操作，请先处理审批")
	case errors.Is(err, ErrConversationNotFound):
		return httpx.ErrNotFound.WithMessage("会话不存在")
	case errors.Is(err, chatmodel.ErrNotConfigured):
		return httpx.ErrModelNotConfigured.WithMessage("先在设置页添加一条对话模型配置")
	case errors.Is(err, chatmodel.ErrNotSelected):
		return httpx.ErrModelNotSelected.WithMessage("有多条对话模型配置，请先在设置页选一条默认")
	case errors.Is(err, chatmodel.ErrNotFound):
		return httpx.ErrChatModelNotFound
	case errors.Is(err, chatmodel.ErrDecryptFailed):
		return httpx.ErrMasterKeyMismatch
	case errors.Is(err, knowledge.ErrEmbeddingNotConfigured):
		return httpx.ErrEmbeddingNotConfigured.WithMessage("先在 .env 配置 EMBEDDING_* 并补齐向量")
	default:
		// 检索失败（数据库不可用等）走到这里。原始错误只进日志，
		// 不返回给客户端——它可能带 SQL 片段。
		return httpx.ErrInternal.Wrap(err)
	}
}
