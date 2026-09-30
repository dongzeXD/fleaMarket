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
	"github.com/go-kratos/kratos/v3/transport/http"
)

// NewHTTPServer 创建 HTTP 服务端。
//
// 除 api-gateway 外，各业务服务的 HTTP 端口只用于健康检查与内部调试，
// 对外流量统一经网关进入（doc.md §3）。
func NewHTTPServer(c *conf.Server,
	orderSvc *service.OrderService,
	reviewSvc *service.ReviewService,
) (*http.Server, error) {
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

	opts := []http.ServerOption{http.Middleware(ms...)}
	if c.GetHttp().GetNetwork() != "" {
		opts = append(opts, http.Network(c.GetHttp().GetNetwork()))
	}
	if c.GetHttp().GetAddr() != "" {
		opts = append(opts, http.Address(c.GetHttp().GetAddr()))
	}
	if c.GetHttp().GetTimeout() != nil {
		opts = append(opts, http.Timeout(c.GetHttp().GetTimeout().AsDuration()))
	}

	srv := http.NewServer(opts...)

	tradev1.RegisterOrderServiceHTTPServer(srv, orderSvc)
	tradev1.RegisterReviewServiceHTTPServer(srv, reviewSvc)

	return srv, nil
}
