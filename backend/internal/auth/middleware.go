// 本文件是认证的 gin 中间件：会话校验与 CSRF 的同源校验。
//
// 两个中间件都不认识业务模块：一个只产出 Principal，一个只在写请求上比对 Origin。
package auth

import (
	"context"
	"errors"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/luo303/mindos/backend/internal/httpx"
)

// CookieName 是会话 Cookie 的名字。
//
// 不用 __Host- 前缀：那要求 Secure + Path=/ + 无 Domain，本地 http 开发会直接失效。
// 生产（APP_ENV=prod）会带上 Secure，安全性由它保证。
const CookieName = "mindos_session"

// principalKey 是 Principal 在 gin.Context 里的键。
//
// 用带包名前缀的字符串而不是裸 "user"：gin 的键空间是全应用共享的，
// 裸名字迟早和别的中间件撞上。
const principalKey = "auth.principal"

// ctxKeyPrincipal 是标准库 context 的键类型（未导出，避免跨包冲突）。
type ctxKeyPrincipal struct{}

// WithPrincipal 把身份放进标准库 context，供不持有 *gin.Context 的层记日志。
//
// 注意：**数据隔离不靠它**。repository 的 owner 条件是显式参数，
// 因为"从 context 取值"允许缺失，漏传会静默变成 owner_id=0 而不是编译错误。
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKeyPrincipal{}, p)
}

// PrincipalFromContext 读取身份（不存在返回 false）。
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ctxKeyPrincipal{}).(Principal)
	return p, ok
}

// SetPrincipal 把身份写进 gin.Context 与标准库 context。
//
// 生产路径由 NewSessionMiddleware 调用。导出它是因为 principalKey 不导出，
// httptest 里的 handler 测试无法自己写这个键；将来加 API Key 认证时，
// 那条中间件也应该走同一个写入点，而不是各自 c.Set 一个私有键。
func SetPrincipal(c *gin.Context, p Principal) {
	c.Set(principalKey, p)
	c.Request = c.Request.WithContext(WithPrincipal(c.Request.Context(), p))
}

// PrincipalFrom 从 gin.Context 取身份。只在挂了 NewSessionMiddleware 的路由里有效。
func PrincipalFrom(c *gin.Context) (Principal, bool) {
	v, ok := c.Get(principalKey)
	if !ok {
		return Principal{}, false
	}
	p, ok := v.(Principal)
	return p, ok
}

// NewSessionMiddleware 校验 Cookie 里的会话，并把 Principal 写进 gin.Context 与 context。
//
// 失败一律中断：没有身份就没有"这是谁的数据"可言，继续走下去只能靠默认值猜。
func NewSessionMiddleware(svc *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		token, err := c.Cookie(CookieName)
		if err != nil || token == "" {
			httpx.Abort(c, httpx.ErrAuthRequired.WithMessage("请先登录"))
			return
		}
		p, err := svc.Authenticate(c.Request.Context(), token)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				c.Status(499)
				c.Abort()
				return
			}
			httpx.Abort(c, mapError(err))
			return
		}
		// http.Request 是不可变的：SetPrincipal 内部会把新请求赋回去，
		// 否则 ctx 里的身份立刻丢失。
		SetPrincipal(c, p)
		c.Next()
	}
}

// CheckOrigin 是 CSRF 的第二道闸（第一道是 SameSite=Lax）。
//
// 规则：非 GET/HEAD/OPTIONS 请求如果带了 Origin，必须满足下面之一：
//  1. Origin 的 host 与请求 Host 相同（浏览器直连 API，或代理保留原始 Host）；
//  2. Origin 在 allowedOrigins 里（反向代理改写 Host 的部署）。
//
// 为什么需要第 2 条：CSRF 校验拿 Origin 比 Host，前提是两者是同一个地址。
// Vite 的 changeOrigin=true、nginx 默认的 proxy_set_header 都会把 Host 改成
// 上游地址，此时 Origin=http://localhost:5173、Host=127.0.0.1:8080，
// 合法请求会被当成攻击——这是实测踩到的误判。
//
// 为什么不读 X-Forwarded-Host：那个头由请求方提供，攻击者可以把它写成自己的域名，
// 于是"Origin 与 X-Forwarded-Host 一致"这条规则形同虚设。可信来源只能是配置。
//
// 为什么"不带 Origin 就放行"：curl、服务端调用、老客户端不会带这个头，
// 一律要求会让 AGENTS.md 里的真实 curl 验证无法进行。而浏览器对同源与跨源的
// POST 都会带 Origin，所以跨站表单/脚本一定命中检查。
func CheckOrigin(allowedOrigins []string, log *zap.Logger) gin.HandlerFunc {
	if log == nil {
		log = zap.NewNop()
	}
	allowed := make(map[string]struct{}, len(allowedOrigins))
	for _, origin := range allowedOrigins {
		if key, ok := originKey(origin); ok {
			allowed[key] = struct{}{}
		}
	}

	return func(c *gin.Context) {
		switch c.Request.Method {
		case "GET", "HEAD", "OPTIONS":
			c.Next()
			return
		}
		origin := c.GetHeader("Origin")
		if origin == "" {
			c.Next()
			return
		}
		if u, err := url.Parse(origin); err == nil && strings.EqualFold(u.Host, c.Request.Host) {
			c.Next()
			return
		}
		if key, ok := originKey(origin); ok {
			if _, trusted := allowed[key]; trusted {
				c.Next()
				return
			}
		}

		// 记下两端地址：运维看到"前端 5173、后端 8080"就能立刻明白是代理改写了 Host。
		log.Warn("cross-site request rejected",
			zap.String("origin", origin),
			zap.String("host", c.Request.Host),
			zap.String("path", c.Request.URL.Path))
		httpx.Abort(c, httpx.ErrOriginMismatch.WithMessage(
			"请求来源校验未通过（CSRF 防护）。本地开发请确认前端代理保留原始 Host，"+
				"或在 AUTH_ALLOWED_ORIGINS 中登记前端地址"))
	}
}

// originKey 把 Origin 规整成 "scheme://host" 作为比较键。
//
// 去掉默认端口：http://x 与 http://x:80 是同一个来源，配置时不该逼人写端口。
func originKey(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return "", false
	}
	host := strings.ToLower(u.Host)
	if (u.Scheme == "http" && strings.HasSuffix(host, ":80")) ||
		(u.Scheme == "https" && strings.HasSuffix(host, ":443")) {
		if idx := strings.LastIndex(host, ":"); idx > 0 {
			host = host[:idx]
		}
	}
	return strings.ToLower(u.Scheme) + "://" + host, true
}
