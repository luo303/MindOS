// 本文件是认证的 HTTP 入口：绑定参数、写 Cookie、把哨兵错误翻成统一错误码。
//
// 它不判断"密码对不对""邮箱是否已注册"——那些都在 service 里。
// 这样同一套规则将来接命令行或测试时不需要重写。
package auth

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/luo303/mindos/backend/internal/httpx"
)

// Handler 提供 /auth/* 接口。
type Handler struct {
	svc    *Service
	secure bool
	// 两个限流器按客户端 IP 计数。登录 10 次/分钟、注册 5 次/分钟：
	// 正常用户不可能点到这个量级，脚本一定超。
	loginLimiter    *Limiter
	registerLimiter *Limiter
}

// NewHandler 创建 handler。secure 决定 Cookie 是否带 Secure（prod 为 true）。
func NewHandler(svc *Service, secure bool) *Handler {
	return &Handler{
		svc:             svc,
		secure:          secure,
		loginLimiter:    NewLimiter(10, time.Minute),
		registerLimiter: NewLimiter(5, time.Minute),
	}
}

type registerRequest struct {
	Email       string `json:"email" binding:"required"`
	Password    string `json:"password" binding:"required"`
	DisplayName string `json:"display_name"`
}

type loginRequest struct {
	Email    string `json:"email" binding:"required"`
	Password string `json:"password" binding:"required"`
}

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password" binding:"required"`
	NewPassword     string `json:"new_password" binding:"required"`
}

// userView 是对外的账号形状。不含 password_hash，也不含会话 token。
type userView struct {
	ID          int64      `json:"id"`
	Email       string     `json:"email"`
	DisplayName string     `json:"display_name"`
	CreatedAt   time.Time  `json:"created_at"`
	LastLoginAt *time.Time `json:"last_login_at"`
}

// sessionView 是登录/注册成功后的响应：账号 + 过期时间。
//
// 不回 token 本身：它已经在 HttpOnly Cookie 里，回一份到 JSON 里
// 等于给 XSS 多开一条拿走它的路。
type sessionView struct {
	User      userView  `json:"user"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Register 处理 POST /auth/register。
func (h *Handler) Register(c *gin.Context) {
	if !h.registerLimiter.Allow(c.ClientIP()) {
		httpx.Fail(c, httpx.ErrAccountLocked.WithMessage("注册请求过于频繁，请稍后再试"))
		return
	}
	var req registerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Fail(c, httpx.ErrInvalidArgument.WithMessage("email and password are required"))
		return
	}
	res, err := h.svc.Register(c.Request.Context(), RegisterInput{
		Email:       req.Email,
		Password:    req.Password,
		DisplayName: req.DisplayName,
	})
	if err != nil {
		httpx.Fail(c, mapError(err))
		return
	}
	h.setCookie(c, res)
	httpx.Created(c, sessionView{User: viewOf(res.User), ExpiresAt: res.ExpiresAt})
}

// Login 处理 POST /auth/login。
func (h *Handler) Login(c *gin.Context) {
	if !h.loginLimiter.Allow(c.ClientIP()) {
		httpx.Fail(c, httpx.ErrAccountLocked.WithMessage("登录尝试过于频繁，请稍后再试"))
		return
	}
	var req loginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Fail(c, httpx.ErrInvalidArgument.WithMessage("email and password are required"))
		return
	}
	res, err := h.svc.Login(c.Request.Context(), LoginInput{
		Email:     req.Email,
		Password:  req.Password,
		UserAgent: c.Request.UserAgent(),
		ClientIP:  c.ClientIP(),
	})
	if err != nil {
		httpx.Fail(c, mapError(err))
		return
	}
	h.setCookie(c, res)
	httpx.OK(c, sessionView{User: viewOf(res.User), ExpiresAt: res.ExpiresAt})
}

// Logout 处理 POST /auth/logout：删当前会话并清 Cookie。
func (h *Handler) Logout(c *gin.Context) {
	p, ok := PrincipalFrom(c)
	if !ok {
		httpx.Fail(c, httpx.ErrAuthRequired)
		return
	}
	if err := h.svc.Logout(c.Request.Context(), p.SessionID); err != nil {
		httpx.Fail(c, mapError(err))
		return
	}
	h.clearCookie(c)
	httpx.OK(c, gin.H{"logged_out": true})
}

// LogoutAll 处理 POST /auth/logout-all：踢掉其他设备上的会话。
func (h *Handler) LogoutAll(c *gin.Context) {
	p, ok := PrincipalFrom(c)
	if !ok {
		httpx.Fail(c, httpx.ErrAuthRequired)
		return
	}
	if err := h.svc.LogoutAll(c.Request.Context(), p.UserID, p.SessionID); err != nil {
		httpx.Fail(c, mapError(err))
		return
	}
	httpx.OK(c, gin.H{"logged_out_others": true})
}

// Me 处理 GET /auth/me。
func (h *Handler) Me(c *gin.Context) {
	p, ok := PrincipalFrom(c)
	if !ok {
		httpx.Fail(c, httpx.ErrAuthRequired)
		return
	}
	user, err := h.svc.Profile(c.Request.Context(), p.UserID)
	if err != nil {
		httpx.Fail(c, mapError(err))
		return
	}
	httpx.OK(c, viewOf(user))
}

// ChangePassword 处理 PUT /auth/password。
func (h *Handler) ChangePassword(c *gin.Context) {
	p, ok := PrincipalFrom(c)
	if !ok {
		httpx.Fail(c, httpx.ErrAuthRequired)
		return
	}
	var req changePasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Fail(c, httpx.ErrInvalidArgument.WithMessage("current_password and new_password are required"))
		return
	}
	if err := h.svc.ChangePassword(c.Request.Context(), p.UserID, p.SessionID, req.CurrentPassword, req.NewPassword); err != nil {
		httpx.Fail(c, mapError(err))
		return
	}
	httpx.OK(c, gin.H{"changed": true})
}

// setCookie 写会话 Cookie。
//
// HttpOnly：JS 读不到，XSS 也拿不走；SameSite=Lax：跨站 POST 不带 Cookie。
// Secure 只在 prod 打开，否则本地 http 开发时浏览器会直接丢掉它。
func (h *Handler) setCookie(c *gin.Context, res *SessionResult) {
	maxAge := int(time.Until(res.ExpiresAt).Seconds())
	if maxAge < 1 {
		maxAge = 1
	}
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     CookieName,
		Value:    res.Token,
		Path:     "/",
		MaxAge:   maxAge,
		Expires:  res.ExpiresAt,
		HttpOnly: true,
		Secure:   h.secure,
		SameSite: http.SameSiteLaxMode,
	})
}

func (h *Handler) clearCookie(c *gin.Context) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   h.secure,
		SameSite: http.SameSiteLaxMode,
	})
}

func viewOf(u *User) userView {
	return userView{
		ID:          u.ID,
		Email:       u.Email,
		DisplayName: u.DisplayName,
		CreatedAt:   u.CreatedAt,
		LastLoginAt: u.LastLoginAt,
	}
}

// mapError 把认证哨兵错误翻成对外错误码。
//
// 默认分支原样返回：数据库不可用等意外错误交给 httpx.Fail 兜底成 500，
// 原始原因只进日志。
func mapError(err error) error {
	switch {
	case errors.Is(err, ErrInvalidCredentials):
		return httpx.ErrInvalidCredentials.WithMessage("邮箱或密码不正确")
	case errors.Is(err, ErrAccountLocked):
		return httpx.ErrAccountLocked.WithMessage("连续失败次数过多，请 15 分钟后再试")
	case errors.Is(err, ErrAccountDisabled):
		return httpx.ErrAccountDisabled.WithMessage("该账号已被停用")
	case errors.Is(err, ErrEmailTaken):
		return httpx.ErrEmailTaken.WithMessage("这个邮箱已经注册过了")
	case errors.Is(err, ErrWeakPassword):
		return httpx.ErrWeakPassword.WithMessage("%s", err.Error())
	case errors.Is(err, ErrInvalidEmail):
		return httpx.ErrInvalidArgument.WithMessage("邮箱格式不正确")
	case errors.Is(err, ErrRegistrationClosed):
		return httpx.ErrRegistrationClosed.WithMessage("本部署未开放注册")
	case errors.Is(err, ErrPasswordMismatch):
		return httpx.ErrPasswordMismatch.WithMessage("当前密码不正确")
	case errors.Is(err, ErrSessionInvalid), errors.Is(err, ErrUserNotFound):
		// 会话指向的账号被删了，等价于"登录态失效"。
		return httpx.ErrAuthRequired.WithMessage("登录已失效，请重新登录")
	default:
		return err
	}
}
