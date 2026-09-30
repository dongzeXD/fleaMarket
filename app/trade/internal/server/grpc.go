package server

import (
	"github.com/fleamarket/app/trade/internal/conf"
	"github.com/fleamarket/app/trade/internal/service"

	tradev1 "github.com/fleamarket/api/trade/v1"
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
	orderSvc *service.OrderService,
	reviewSvc *service.ReviewService,
) (*grpc.Server, error) {
	validator, err := validate.Middleware()
	if err != nil {
		return nil, err
	}

	ms := []middleware.Middleware{
		recovery.Recovery(),
		metadata.Server(),
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

	tradev1.RegisterOrderServiceServer(srv, orderSvc)
	tradev1.RegisterReviewServiceServer(srv, reviewSvc)

	return srv, nil
}
