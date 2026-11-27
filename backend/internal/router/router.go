// Package router 负责装配 gin 引擎：404/405 统一处理 + 业务路由分组。
//
// 它不认识任何 service 实现，只接收已经构造好的 handler，
// 这样"依赖从哪里来"完全由 cmd/server 决定，router 只关心"路由怎么挂"。
//
// 分组只有两层：公开（登录/注册/探针）与需登录（其余全部）。
// 没有"部分公开"的第三层——那需要逐个接口论证为什么可以匿名访问，
// 而这个系统里没有一个业务接口能匿名。
package router

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/luo303/mindos/backend/internal/auth"
	"github.com/luo303/mindos/backend/internal/chat"
	"github.com/luo303/mindos/backend/internal/chatmodel"
	"github.com/luo303/mindos/backend/internal/handler"
	"github.com/luo303/mindos/backend/internal/httpx"
	"github.com/luo303/mindos/backend/internal/knowledge"
	"github.com/luo303/mindos/backend/internal/memory"
	"github.com/luo303/mindos/backend/internal/todo"
)

// Deps 收集构建路由需要的依赖。
// 用结构体而不是一长串参数：以后新增 handler 时调用方不必修改函数签名。
type Deps struct {
	Health   *handler.HealthHandler
	System   *handler.SystemHandler
	Settings *handler.SettingsHandler

	// Auth 是注册/登录/登出/改密码。
	Auth *auth.Handler
	// RequireSession 是会话校验中间件。由 auth 包构造，router 只负责挂在哪。
	RequireSession gin.HandlerFunc
	// CheckOrigin 是写请求的 CSRF 同源校验。可信来源列表由 main 从配置读出后注入，
	// router 不读配置。
	CheckOrigin gin.HandlerFunc

	// Knowledge 是知识模块（文档入库与分块）。
	Knowledge *knowledge.Handler

	// ChatModels 是用户配置的对话模型（存库、加密、可切换）。
	ChatModels *chatmodel.Handler

	// Ask 是单轮流式问答。
	Ask *chat.Handler

	// Todos 是用户待办。
	Todos *todo.Handler
	// Memories 管理按账号隔离的长期偏好。
	Memories *memory.Handler

	// 后续模块在这里扩展，例如：
	//   Chat *chat.Handler
}

// Register 把路由注册到已经挂好全局中间件的 engine 上。
// 中间件在 cmd/server 中统一 Use，这里只关心路由表。
func Register(e *gin.Engine, deps Deps) {
	// 健康检查放在根路径，容器和负载均衡可以直接访问。
	// 它们不返回任何业务数据，也不需要登录：探针变 401 会让编排系统误判服务已死。
	e.GET("/healthz", deps.Health.Live)
	e.GET("/readyz", deps.Health.Ready)

	// 404：路由不存在时也返回统一格式，而不是 gin 默认的纯文本。
	e.NoRoute(func(c *gin.Context) {
		httpx.Abort(c, httpx.ErrNotFound.WithMessage(
			"route %s %s not found", c.Request.Method, c.Request.URL.Path,
		))
	})

	// 405：路径存在但方法不对。需要显式打开 HandleMethodNotAllowed。
	e.HandleMethodNotAllowed = true
	e.NoMethod(func(c *gin.Context) {
		httpx.Abort(c, &httpx.Error{
			Code:       "METHOD_NOT_ALLOWED",
			Message:    "method " + c.Request.Method + " not allowed",
			HTTPStatus: http.StatusMethodNotAllowed,
		})
	})

	// 业务路由统一挂在 /api/v1 下，方便以后做版本演进。
	v1 := e.Group("/api/v1")
	// 所有写请求先过同源校验（CSRF 的第二道闸，第一道是 SameSite=Lax）。
	// 挂在组上而不是逐个接口：漏挂一个写接口就等于少一道防线。
	v1.Use(deps.CheckOrigin)

	// —— 公开接口 ——
	// 注册受 AUTH_REGISTRATION 控制；登录本身有限流与账号锁定。
	v1.POST("/auth/register", deps.Auth.Register)
	v1.POST("/auth/login", deps.Auth.Login)

	// —— 以下全部需要登录 ——
	authed := v1.Group("", deps.RequireSession)

	authed.GET("/auth/me", deps.Auth.Me)
	authed.POST("/auth/logout", deps.Auth.Logout)
	authed.POST("/auth/logout-all", deps.Auth.LogoutAll)
	authed.PUT("/auth/password", deps.Auth.ChangePassword)

	// 系统与设置：部署级信息，登录后可见。本轮不区分管理员。
	authed.GET("/system/status", deps.System.Status)
	authed.GET("/settings", deps.Settings.Get)
	// 连通性测试会真发一次模型请求，所以是 POST：它不是"读状态"，
	// 有副作用（计费）且不该被浏览器或代理缓存。
	authed.POST("/settings/embedding/test", deps.Settings.TestEmbedding)

	// 知识：导入与重索引都是异步受理，返回 202 + message_id；
	// 同内容重复导入返回 200 + existed=true（同一用户内）。
	authed.POST("/documents", deps.Knowledge.CreateFile)
	authed.POST("/documents/note", deps.Knowledge.CreateNote)
	// 静态段放在 :id 之前：gin 允许同一层同时有静态与参数路由，
	// 顺序只影响可读性，不影响匹配（静态优先）。
	authed.POST("/documents/embeddings/backfill", deps.Knowledge.EmbedBackfill)
	authed.GET("/documents", deps.Knowledge.List)
	authed.GET("/documents/:id", deps.Knowledge.Get)
	authed.GET("/documents/:id/chunks", deps.Knowledge.ListChunks)
	authed.PATCH("/documents/:id", deps.Knowledge.Update)
	authed.DELETE("/documents/:id", deps.Knowledge.Delete)
	authed.POST("/documents/:id/reindex", deps.Knowledge.Reindex)
	authed.POST("/documents/:id/embed", deps.Knowledge.Embed)
	authed.POST("/documents/:id/cancel", deps.Knowledge.Cancel)

	// 对话模型：每人一套。设默认是独立动作——它要先清掉自己的旧默认，
	// 一次性有副作用的原子操作，不适合塞进"整体替换字段"的 PATCH。
	authed.GET("/chat-models", deps.ChatModels.List)
	authed.POST("/chat-models", deps.ChatModels.Create)
	authed.PATCH("/chat-models/:id", deps.ChatModels.Update)
	authed.DELETE("/chat-models/:id", deps.ChatModels.Delete)
	authed.POST("/chat-models/:id/default", deps.ChatModels.SetDefault)
	authed.POST("/chat-models/:id/test", deps.ChatModels.Test)

	// 问答：SSE 流式。用 POST 而不是 GET，因为问题在 body 里，
	// 而且它不该被浏览器或代理缓存。
	authed.POST("/ask", deps.Ask.Ask)

	// 多轮会话：会话 CRUD + 按会话发消息（SSE）。
	authed.POST("/conversations", deps.Ask.CreateConversation)
	authed.GET("/conversations", deps.Ask.ListConversations)
	authed.GET("/conversations/:id", deps.Ask.GetConversation)
	authed.DELETE("/conversations/:id", deps.Ask.DeleteConversation)
	authed.GET("/conversations/:id/messages", deps.Ask.ListMessages)
	authed.POST("/conversations/:id/messages", deps.Ask.PostMessage)

	// AI 写操作的持久化审批。
	authed.GET("/approvals", deps.Ask.ListApprovals)
	authed.POST("/approvals/:id/decision", deps.Ask.DecideApproval)

	// 用户待办：手动 CRUD；AI 写工具复用 service，但先过审批。
	authed.GET("/todos", deps.Todos.List)
	authed.POST("/todos", deps.Todos.Create)
	authed.POST("/todos/batch-delete", deps.Todos.BatchDelete)
	authed.PATCH("/todos/:id", deps.Todos.Update)
	authed.DELETE("/todos/:id", deps.Todos.Delete)

	authed.GET("/memories", deps.Memories.List)
	authed.POST("/memories", deps.Memories.Create)
	authed.PATCH("/memories/:id", deps.Memories.Update)
	authed.GET("/memories/:id/versions", deps.Memories.Versions)
	authed.GET("/memories/:id/evidence", deps.Memories.Evidence)
	authed.GET("/memory-settings", deps.Memories.Settings)
	authed.PATCH("/memory-settings", deps.Memories.SetSettings)
	authed.GET("/memory-candidates", deps.Memories.Candidates)
	authed.POST("/memory-candidates/:id/decision", deps.Memories.Decide)
}
