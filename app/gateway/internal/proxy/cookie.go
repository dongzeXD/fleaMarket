package proxy

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/fleamarket/app/gateway/internal/conf"

	userv1 "github.com/fleamarket/api/user/v1"
	"github.com/go-kratos/kratos/v3/transport"
	khttp "github.com/go-kratos/kratos/v3/transport/http"
)

// refreshCookie 负责 refresh_token 在 Cookie 与响应体之间的取舍。
//
// 策略（doc.md §10.4）：
//   - Web 端：refresh_token 写入 httpOnly Cookie，并从响应体中清空。
//     这样它对 JS 不可见，XSS 无法窃取长期凭证。
//   - 小程序端：无 Cookie 机制，仍在响应体中明文返回，由客户端自行保存。
//
// 客户端类型通过 X-Client 头区分，缺省按 Web 处理——
// 默认走更安全的那条路径，漏传头部的后果是小程序拿不到令牌（明显可见的故障），
// 而不是 Web 端的长期凭证暴露在 JS 里（隐蔽的安全问题）。
type refreshCookie struct {
	cfg *conf.Auth_RefreshCookie
	ttl time.Duration
}

func newRefreshCookie(c *conf.Auth) *refreshCookie {
	ttl := c.GetRefreshTtl().AsDuration()
	if ttl <= 0 {
		ttl = 30 * 24 * time.Hour
	}
	return &refreshCookie{cfg: c.GetRefreshCookie(), ttl: ttl}
}

func (rc *refreshCookie) name() string {
	if n := rc.cfg.GetName(); n != "" {
		return n
	}
	return "fm_refresh"
}

func (rc *refreshCookie) sameSite() http.SameSite {
	switch strings.ToLower(rc.cfg.GetSameSite()) {
	case "strict":
		return http.SameSiteStrictMode
	case "none":
		return http.SameSiteNoneMode
	default:
		// Lax 是默认：Strict 会导致从邮件里点开的认证链接丢失登录态，
		// None 则要求 Secure 且放宽了 CSRF 保护。
		return http.SameSiteLaxMode
	}
}

// isMiniApp 判断是否为小程序客户端。
func isMiniApp(ctx context.Context) bool {
	req, ok := khttp.RequestFromServerContext(ctx)
	if !ok || req == nil {
		return false
	}
	return strings.EqualFold(req.Header.Get("X-Client"), "miniapp")
}

// apply 按客户端类型处理登录响应。
func (rc *refreshCookie) apply(ctx context.Context, reply *userv1.LoginReply) *userv1.LoginReply {
	if reply == nil || reply.GetRefreshToken() == "" {
		return reply
	}
	if isMiniApp(ctx) {
		return reply
	}

	if w, ok := responseWriterFrom(ctx); ok {
		http.SetCookie(w, &http.Cookie{
			Name:     rc.name(),
			Value:    reply.GetRefreshToken(),
			Path:     orDefault(rc.cfg.GetPath(), "/"),
			Domain:   rc.cfg.GetDomain(),
			MaxAge:   int(rc.ttl.Seconds()),
			HttpOnly: true,
			Secure:   rc.cfg.GetSecure(),
			SameSite: rc.sameSite(),
		})
		// 从响应体清空，使其只存在于 httpOnly Cookie 中。
		reply.RefreshToken = ""
	}
	return reply
}

// clear 登出时删除 Cookie。
func (rc *refreshCookie) clear(ctx context.Context) {
	w, ok := responseWriterFrom(ctx)
	if !ok {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     rc.name(),
		Value:    "",
		Path:     orDefault(rc.cfg.GetPath(), "/"),
		Domain:   rc.cfg.GetDomain(),
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   rc.cfg.GetSecure(),
		SameSite: rc.sameSite(),
	})
}

// read 从 Cookie 中取 refresh_token。
//
// 请求体中的值优先：小程序把令牌放在body里，没有 Cookie 可读。
func (rc *refreshCookie) read(ctx context.Context, fromBody string) string {
	if fromBody != "" {
		return fromBody
	}
	req, ok := khttp.RequestFromServerContext(ctx)
	if !ok || req == nil {
		return ""
	}
	ck, err := req.Cookie(rc.name())
	if err != nil {
		return ""
	}
	return ck.Value
}

// responseWriterFrom 取出底层 ResponseWriter 以写入 Set-Cookie。
//
// kratos v3 把 Response() 拆到了 ResponseTransporter 接口上，
// 这是在 service 方法内部操作 HTTP 响应头的正规途径——
// 比在 proto 里为 Cookie 额外定义字段干净得多。
func responseWriterFrom(ctx context.Context) (http.ResponseWriter, bool) {
	tr, ok := transport.FromServerContext(ctx)
	if !ok {
		return nil, false
	}
	ht, ok := tr.(khttp.ResponseTransporter)
	if !ok {
		return nil, false
	}
	w := ht.Response()
	if w == nil {
		return nil, false
	}
	return w, true
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
