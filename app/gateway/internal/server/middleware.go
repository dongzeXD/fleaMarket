package server

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fleamarket/app/gateway/internal/conf"

	"github.com/fleamarket/pkg/auth"
	"github.com/fleamarket/pkg/errs"
	"github.com/go-kratos/kratos/v3/middleware"
	"github.com/go-kratos/kratos/v3/transport"
	khttp "github.com/go-kratos/kratos/v3/transport/http"
	"golang.org/x/time/rate"
)

// --- 限流 ---

// bucketSet 按维度键持有一组令牌桶，并定期回收空闲桶。
//
// 为何需要回收：以客户端 IP 为键时，键空间等于互联网的 IP 空间。
// 若只增不减，一次扫描式攻击就能让网关内存无界增长——
// 限流组件本身反而成了拒绝服务的入口。
type bucketSet struct {
	mu      sync.Mutex
	buckets map[string]*bucketEntry
	rate    rate.Limit
	burst   int
	idleTTL time.Duration
	lastGC  time.Time
}

type bucketEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

func newBucketSet(b *conf.RateLimit_Bucket, idleTTL time.Duration, defRate float64, defBurst int) *bucketSet {
	r := b.GetRate()
	if r <= 0 {
		r = defRate
	}
	burst := int(b.GetBurst())
	if burst <= 0 {
		burst = defBurst
	}
	if idleTTL <= 0 {
		idleTTL = 10 * time.Minute
	}
	return &bucketSet{
		buckets: make(map[string]*bucketEntry),
		rate:    rate.Limit(r),
		burst:   burst,
		idleTTL: idleTTL,
		lastGC:  time.Now(),
	}
}

// allow 判断该键是否还有配额。
func (s *bucketSet) allow(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	// 惰性回收：借每次请求顺带清理，避免额外的后台 goroutine。
	if now.Sub(s.lastGC) > s.idleTTL {
		for k, e := range s.buckets {
			if now.Sub(e.lastSeen) > s.idleTTL {
				delete(s.buckets, k)
			}
		}
		s.lastGC = now
	}

	e, ok := s.buckets[key]
	if !ok {
		e = &bucketEntry{limiter: rate.NewLimiter(s.rate, s.burst)}
		s.buckets[key] = e
	}
	e.lastSeen = now
	return e.limiter.Allow()
}

// writeOperations 需要额外配额的写操作。
//
// 选取标准是「滥用会产生外部成本或骚扰他人」：
// 发短信要花钱、发布与举报会占用审核人力、发消息会骚扰他人。
// 纯读接口不在此列，它们由 per_ip / per_user 的通用配额覆盖。
var writeOperations = map[string]struct{}{
	"/user.v1.AuthService/SendSmsCode":                 {},
	"/user.v1.CampusAuthService/StartEmailAuth":        {},
	"/user.v1.CampusAuthService/SubmitStudentCardAuth": {},
	"/user.v1.UserService/BlockUser":                   {},
}

// RateLimit 返回三维限流中间件：IP、用户、写操作。
//
// 三者是叠加关系而非择一：
//   - per_ip 防同一台机器批量注册；
//   - per_user 防已登录账号高频调用；
//   - write_ops 防对高成本接口的定向滥用。
//
// 未登录请求只受 IP 维度约束，因为此时没有可靠的用户标识。
func RateLimit(c *conf.RateLimit) middleware.Middleware {
	idle := c.GetIdleTtl().AsDuration()
	perIP := newBucketSet(c.GetPerIp(), idle, 50, 100)
	perUser := newBucketSet(c.GetPerUser(), idle, 20, 40)
	writeOps := newBucketSet(c.GetWriteOps(), idle, 1, 5)

	return func(handler middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req any) (any, error) {
			tr, ok := transport.FromServerContext(ctx)
			if !ok {
				return handler(ctx, req)
			}

			if ip := clientIP(ctx); ip != "" && !perIP.allow(ip) {
				return nil, errs.RateLimited("请求过于频繁，请稍后再试")
			}

			op := tr.Operation()
			if uid := auth.MustUserID(ctx); uid > 0 {
				key := strconv.FormatInt(uid, 10)
				if !perUser.allow(key) {
					return nil, errs.RateLimited("操作过于频繁，请稍后再试")
				}
				if _, isWrite := writeOperations[op]; isWrite && !writeOps.allow(key) {
					return nil, errs.RateLimited("该操作过于频繁，请稍后再试")
				}
			} else if _, isWrite := writeOperations[op]; isWrite {
				// 未登录的写操作（如发短信）按 IP 计入写配额，
				// 否则注册前的接口就完全没有写维度保护。
				if ip := clientIP(ctx); ip != "" && !writeOps.allow("ip:"+ip) {
					return nil, errs.RateLimited("该操作过于频繁，请稍后再试")
				}
			}

			return handler(ctx, req)
		}
	}
}

// clientIP 取客户端真实 IP。
//
// 优先读 X-Forwarded-For 的第一段：网关通常部署在负载均衡之后，
// RemoteAddr 会是 LB 的地址，用它限流等于把所有用户算作同一个键。
//
// 安全前提：X-Forwarded-For 可被客户端伪造，因此只有在确信前置
// 代理会覆写该头时才可信任。若网关直接暴露在公网，应改用 RemoteAddr。
func clientIP(ctx context.Context) string {
	req, ok := khttp.RequestFromServerContext(ctx)
	if !ok || req == nil {
		return ""
	}
	if xff := req.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i > 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	if xr := req.Header.Get("X-Real-IP"); xr != "" {
		return strings.TrimSpace(xr)
	}
	host, _, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil {
		return req.RemoteAddr
	}
	return host
}

// --- 鉴权白名单 ---

// publicOperations 无需登录即可访问的操作。
//
// 采用白名单而非黑名单：新增接口时若忘记配置，默认是「需要登录」，
// 失败模式是用户报错而非数据泄漏。反之用黑名单，漏配就意味着接口裸奔。
var publicOperations = map[string]struct{}{
	"/user.v1.AuthService/SendSmsCode":        {},
	"/user.v1.AuthService/LoginByPhone":       {},
	"/user.v1.AuthService/LoginByWechat":      {},
	"/user.v1.AuthService/RefreshToken":       {},
	"/user.v1.AuthService/Logout":             {},
	"/user.v1.CampusAuthService/ListSchools":  {},
	"/user.v1.CampusAuthService/ListCampuses": {},
	// 认证链接可能在另一台设备的邮件客户端里被点开，那里没有登录态。
	// 安全性由一次性签名令牌保证。
	"/user.v1.CampusAuthService/VerifyEmailAuth": {},
	// 他人主页需可被未注册用户打开，否则分享链接失去获客价值。
	"/user.v1.UserService/GetUserHomepage": {},
}

func isPublic(operation string) bool {
	_, ok := publicOperations[operation]
	return ok
}

// AdminOnly 对 admin 域的操作强制管理员身份。
//
// 网关按 operation 前缀判定，admin-service 内部仍会二次校验（doc.md §13）——
// 不把安全性寄托在「网关路由一定配对了」这个假设上。
func AdminOnly() middleware.Middleware {
	return func(handler middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req any) (any, error) {
			tr, ok := transport.FromServerContext(ctx)
			if !ok {
				return handler(ctx, req)
			}
			if !strings.HasPrefix(tr.Operation(), "/admin.v1.") {
				return handler(ctx, req)
			}
			id, ok := auth.FromContext(ctx)
			if !ok {
				return nil, auth.ErrUnauthenticated("请先登录")
			}
			if !id.IsAdmin() {
				return nil, auth.ErrPermissionDenied("需要管理员权限")
			}
			return handler(ctx, req)
		}
	}
}

// --- HTTP 过滤器 ---

// corsFilter 返回跨域过滤器。
func corsFilter(c *conf.Server_CORS) khttp.FilterFunc {
	origins := c.GetAllowOrigins()
	allowed := make(map[string]struct{}, len(origins))
	for _, o := range origins {
		allowed[o] = struct{}{}
	}
	methods := strings.Join(orDefaultSlice(c.GetAllowMethods(),
		[]string{"GET", "POST", "PATCH", "PUT", "DELETE", "OPTIONS"}), ", ")
	headers := strings.Join(orDefaultSlice(c.GetAllowHeaders(),
		[]string{"Content-Type", "Authorization", "X-Client", "X-Requested-With"}), ", ")
	maxAge := strconv.Itoa(int(c.GetMaxAge().AsDuration().Seconds()))

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			// 只回显白名单内的来源，不回显 "*"：
			// 带 credentials 的请求在 "*" 下会被浏览器拒绝，
			// 且回显任意来源等于放弃同源保护。
			if _, ok := allowed[origin]; ok && origin != "" {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Vary", "Origin")
				if c.GetAllowCredentials() {
					w.Header().Set("Access-Control-Allow-Credentials", "true")
				}
				w.Header().Set("Access-Control-Allow-Methods", methods)
				w.Header().Set("Access-Control-Allow-Headers", headers)
				w.Header().Set("Access-Control-Max-Age", maxAge)
			}
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// bodyLimitFilter 限制请求体大小。
//
// 图片走对象存储直传，因此网关只需承载 JSON 请求体，
// 默认 1MB 已相当宽松。没有上限则一个构造的大请求就能打爆内存。
func bodyLimitFilter(maxBytes int64) khttp.FilterFunc {
	if maxBytes <= 0 {
		maxBytes = 1 << 20
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ContentLength > maxBytes {
				http.Error(w, `{"code":413,"reason":"PAYLOAD_TOO_LARGE","message":"请求体过大"}`,
					http.StatusRequestEntityTooLarge)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			next.ServeHTTP(w, r)
		})
	}
}

func orDefaultSlice(v, def []string) []string {
	if len(v) == 0 {
		return def
	}
	return v
}
