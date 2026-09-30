package proxy

import (
	"github.com/fleamarket/app/gateway/internal/conf"

	"github.com/fleamarket/pkg/auth"
)

// NewJWTManager 构造网关的令牌校验器。
//
// 这里刻意传 nil 作为 Revoker，与 user-service 的做法不同，原因是：
// 网关只校验 access_token，而按设计 access_token 不做吊销（doc.md §9.1）——
// 为 2 小时有效期的令牌维护吊销名单意味着每个请求都要查一次 Redis，代价过高。
// refresh_token 的吊销检查发生在 user-service 内部，那里有 Redis 连接。
//
// 因此网关不需要 Redis，这也让它保持为一个无状态组件，可随意水平扩展。
func NewJWTManager(c *conf.Auth) (*auth.Manager, error) {
	return auth.NewManager(auth.Config{
		Secret:     c.GetJwtSecret(),
		Issuer:     "fleamarket",
		AccessTTL:  c.GetAccessTtl().AsDuration(),
		RefreshTTL: c.GetRefreshTtl().AsDuration(),
	}, nil)
}
