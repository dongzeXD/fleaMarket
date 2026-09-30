package proxy

import (
	"context"

	"github.com/fleamarket/app/gateway/internal/conf"

	userv1 "github.com/fleamarket/api/user/v1"
	"github.com/fleamarket/pkg/registry"
	"github.com/fleamarket/pkg/transport"
	kratosregistry "github.com/go-kratos/kratos/v3/registry"
	"github.com/google/wire"
	"google.golang.org/grpc"
)

// ProviderSet 转发层的 wire provider 集合。
var ProviderSet = wire.NewSet(
	NewJWTManager,
	NewUserConn,
	NewAuthServiceClient,
	NewUserServiceClient,
	NewCampusAuthServiceClient,
	NewUserProxy,
)

// UserConn 是到 user-service 的连接。
//
// 定义具名类型而非直接用 *grpc.ClientConn：网关会连接 8 个下游服务，
// 若都是同一类型，wire 无法区分该把哪个连接注入给哪个客户端。
type UserConn struct {
	conn *grpc.ClientConn
}

// NewUserConn 建立到 user-service 的连接。
//
// 幂等方法清单在此显式声明：只有明确幂等的查询类接口才允许重试。
// 登录、拉黑这类会改变状态的方法不在列内——重试它们会产生重复副作用。
func NewUserConn(d kratosregistry.Discovery, c *conf.Downstream) (*UserConn, func(), error) {
	svc := c.GetUser()
	name := svc.GetName()
	if name == "" {
		name = "user-service"
	}

	conn, cleanup, err := transport.NewGRPCConn(context.Background(), transport.ClientOptions{
		Endpoint:  registry.Endpoint(name),
		Discovery: d,
		Timeout:   svc.GetTimeout().AsDuration(),
		IdempotentOperations: []string{
			"/user.v1.UserService/GetMe",
			"/user.v1.UserService/GetUserHomepage",
			"/user.v1.UserService/ListFollowing",
			"/user.v1.UserService/ListBlocked",
			"/user.v1.CampusAuthService/ListSchools",
			"/user.v1.CampusAuthService/ListCampuses",
			"/user.v1.CampusAuthService/GetMyCampusAuth",
		},
	})
	if err != nil {
		return nil, nil, err
	}
	return &UserConn{conn: conn}, cleanup, nil
}

// NewAuthServiceClient 构造 AuthService 客户端。
func NewAuthServiceClient(c *UserConn) userv1.AuthServiceClient {
	return userv1.NewAuthServiceClient(c.conn)
}

// NewUserServiceClient 构造 UserService 客户端。
func NewUserServiceClient(c *UserConn) userv1.UserServiceClient {
	return userv1.NewUserServiceClient(c.conn)
}

// NewCampusAuthServiceClient 构造 CampusAuthService 客户端。
func NewCampusAuthServiceClient(c *UserConn) userv1.CampusAuthServiceClient {
	return userv1.NewCampusAuthServiceClient(c.conn)
}
