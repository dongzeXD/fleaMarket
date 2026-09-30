package auth

import (
	"context"
	goerrors "errors"
	"strconv"
	"strings"

	"github.com/go-kratos/kratos/v3/errors"
	"github.com/go-kratos/kratos/v3/metadata"
	"github.com/go-kratos/kratos/v3/middleware"
	"github.com/go-kratos/kratos/v3/transport"
)

// metadata 中承载身份的键。
//
// 使用 kratos 的全局 metadata 前缀 x-md-global-，使其能自动跨服务透传：
// 网关解一次 JWT，整条调用链都能读到调用者身份。
const (
	MDUserID = "x-md-global-user-id"
	MDRole   = "x-md-global-role"
	// MDExtraPrefix 业务附加属性的键前缀，如 x-md-global-ext-campus-id。
	MDExtraPrefix = "x-md-global-ext-"
)

// 角色常量。角色是通用的鉴权概念，故放在公共库。
const (
	RoleUser  = "user"
	RoleAdmin = "admin"
)

// identityKey 是 context 中存放身份的键类型。
// 用私有类型防止外部包用字符串键覆盖。
type identityKey struct{}

// Identity 请求发起者的身份。
type Identity struct {
	UserID int64
	Role   string
	Extra  map[string]string
}

// IsAdmin 是否管理员。
func (i *Identity) IsAdmin() bool { return i != nil && i.Role == RoleAdmin }

// NewContext 把身份写入 context。
func NewContext(ctx context.Context, id *Identity) context.Context {
	return context.WithValue(ctx, identityKey{}, id)
}

// FromContext 读出身份。未登录时返回 nil, false。
func FromContext(ctx context.Context) (*Identity, bool) {
	id, ok := ctx.Value(identityKey{}).(*Identity)
	return id, ok && id != nil
}

// MustUserID 读出用户 ID，未登录返回 0。
//
// 提供这个便捷方法是因为业务代码里「取当前用户 ID」极其高频，
// 每次都写三行类型断言会淹没真正的逻辑。
func MustUserID(ctx context.Context) int64 {
	if id, ok := FromContext(ctx); ok {
		return id.UserID
	}
	return 0
}

// ErrUnauthenticated 未认证的 kratos 错误。
func ErrUnauthenticated(msg string) error {
	return errors.Unauthorized("UNAUTHENTICATED", msg)
}

// ErrPermissionDenied 无权限的 kratos 错误。
func ErrPermissionDenied(msg string) error {
	return errors.Forbidden("PERMISSION_DENIED", msg)
}

// ServerFromMetadata 返回从 metadata 还原身份的服务端中间件。
//
// 供业务服务使用：它们不解 JWT，只信任网关注入的 metadata。
// 注意「信任」的边界——metadata 只用于确定「调用者是谁」，
// 而「这个人能否操作这个资源」必须在 biz 层按 owner_id 再校验一次（doc.md §13）。
// 否则一旦有服务被直接暴露，整个鉴权体系就被绕过了。
func ServerFromMetadata() middleware.Middleware {
	return func(handler middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req any) (any, error) {
			md, ok := metadata.FromServerContext(ctx)
			if !ok {
				return handler(ctx, req)
			}
			uid, err := strconv.ParseInt(md.Get(MDUserID), 10, 64)
			if err != nil || uid <= 0 {
				// 无身份不等于错误：公开接口（学校列表、商品浏览）本就无需登录。
				// 是否必须登录由各接口自行判断。
				return handler(ctx, req)
			}
			id := &Identity{
				UserID: uid,
				Role:   md.Get(MDRole),
				Extra:  map[string]string{},
			}
			md.Range(func(k string, _ []string) bool {
				if strings.HasPrefix(k, MDExtraPrefix) {
					id.Extra[strings.TrimPrefix(k, MDExtraPrefix)] = md.Get(k)
				}
				return true
			})
			if id.Role == "" {
				id.Role = RoleUser
			}
			return handler(NewContext(ctx, id), req)
		}
	}
}

// ServerFromToken 返回自行校验 JWT 的服务端中间件。
//
// 供 api-gateway 使用。skipper 返回 true 的操作跳过校验（登录、学校字典等公开接口）。
func ServerFromToken(m *Manager, skipper func(operation string) bool) middleware.Middleware {
	return func(handler middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req any) (any, error) {
			tr, ok := transport.FromServerContext(ctx)
			if !ok {
				return nil, ErrUnauthenticated("无法识别请求来源")
			}
			if skipper != nil && skipper(tr.Operation()) {
				return handler(ctx, req)
			}

			token := bearerToken(tr.RequestHeader().Get("Authorization"))
			claims, err := m.Verify(ctx, token, TypeAccess)
			if err != nil {
				switch {
				case goerrors.Is(err, ErrTokenExpired):
					return nil, errors.Unauthorized("TOKEN_EXPIRED", "登录已过期，请重新登录")
				case goerrors.Is(err, ErrTokenRevoked):
					return nil, errors.Unauthorized("TOKEN_REVOKED", "登录状态已失效")
				case goerrors.Is(err, ErrTokenMissing):
					return nil, ErrUnauthenticated("请先登录")
				default:
					return nil, ErrUnauthenticated("登录凭证无效")
				}
			}

			id := &Identity{UserID: claims.UserID, Role: claims.Role, Extra: claims.Extra}
			if id.Role == "" {
				id.Role = RoleUser
			}
			ctx = NewContext(ctx, id)
			ctx = injectMetadata(ctx, id)
			return handler(ctx, req)
		}
	}
}

// RequireLogin 返回强制登录的中间件，用于业务服务中必须登录的接口分组。
func RequireLogin() middleware.Middleware {
	return func(handler middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req any) (any, error) {
			if _, ok := FromContext(ctx); !ok {
				return nil, ErrUnauthenticated("请先登录")
			}
			return handler(ctx, req)
		}
	}
}

// RequireAdmin 返回强制管理员的中间件。
//
// 网关按路由前缀挂载它，但 admin-service 内部仍应二次校验（doc.md §13）——
// 不把安全性寄托在「路由一定配对了」这个假设上。
func RequireAdmin() middleware.Middleware {
	return func(handler middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req any) (any, error) {
			id, ok := FromContext(ctx)
			if !ok {
				return nil, ErrUnauthenticated("请先登录")
			}
			if !id.IsAdmin() {
				return nil, ErrPermissionDenied("需要管理员权限")
			}
			return handler(ctx, req)
		}
	}
}

// injectMetadata 把身份写入 metadata，使其随 gRPC 调用透传到下游。
func injectMetadata(ctx context.Context, id *Identity) context.Context {
	md, ok := metadata.FromClientContext(ctx)
	if !ok {
		md = metadata.New()
	}
	md.Set(MDUserID, strconv.FormatInt(id.UserID, 10))
	md.Set(MDRole, id.Role)
	for k, v := range id.Extra {
		md.Set(MDExtraPrefix+k, v)
	}
	return metadata.NewClientContext(ctx, md)
}

func bearerToken(header string) string {
	const prefix = "Bearer "
	if len(header) > len(prefix) && strings.EqualFold(header[:len(prefix)], prefix) {
		return header[len(prefix):]
	}
	// 也接受不带 Bearer 前缀的裸令牌：部分客户端（尤其小程序）
	// 的请求库会自行处理 Authorization，容错可省去一类联调问题。
	return header
}
