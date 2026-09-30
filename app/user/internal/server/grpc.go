package server

import (
	"github.com/fleamarket/app/user/internal/conf"
	"github.com/fleamarket/app/user/internal/service"

	userv1 "github.com/fleamarket/api/user/v1"
	"github.com/fleamarket/pkg/auth"
	"github.com/fleamarket/pkg/validate"
	"github.com/go-kratos/kratos/v3/middleware"
	"github.com/go-kratos/kratos/v3/middleware/metadata"
	"github.com/go-kratos/kratos/v3/middleware/recovery"
	"github.com/go-kratos/kratos/v3/transport/grpc"
)

// NewGRPCServer 创建 gRPC 服务端。
//
// 服务间调用只走 gRPC，故此处不挂鉴权中间件：调用方身份由网关校验后
// 经 metadata 透传，下游服务信任 metadata 但仍需在 biz 层校验资源归属
// （doc.md §13），不依赖网关单点。
func NewGRPCServer(c *conf.Server,
	authSvc *service.AuthService,
	userSvc *service.UserService,
	campusSvc *service.CampusAuthService,
) (*grpc.Server, error) {
	validator, err := validate.Middleware()
	if err != nil {
		return nil, err
	}

	ms := []middleware.Middleware{
		recovery.Recovery(),
		metadata.Server(),
		// 从网关注入的 metadata 还原调用者身份。
		auth.ServerFromMetadata(),
		validator,
	}

	opts := []grpc.ServerOption{grpc.Middleware(ms...)}
	if c.GetGrpc().GetNetwork() != "" {
		opts = append(opts, grpc.Network(c.GetGrpc().GetNetwork()))
	}
	if c.GetGrpc().GetAddr() != "" {
		opts = append(opts, grpc.Address(c.GetGrpc().GetAddr()))
	}
	if c.GetGrpc().GetTimeout() != nil {
		opts = append(opts, grpc.Timeout(c.GetGrpc().GetTimeout().AsDuration()))
	}

	srv := grpc.NewServer(opts...)

	userv1.RegisterAuthServiceServer(srv, authSvc)
	userv1.RegisterUserServiceServer(srv, userSvc)
	userv1.RegisterCampusAuthServiceServer(srv, campusSvc)

	// 不需要显式注册 reflection：kratos v3 的 grpc.NewServer 默认已注册
	// （可用 grpc.DisableReflection() 关闭）。重复注册会在启动时
	// 以 "duplicate service registration" 致命错误退出。
	// 反射是 doc.md §6.3 路径 B 的前提，因此保持默认开启。

	return srv, nil
}
