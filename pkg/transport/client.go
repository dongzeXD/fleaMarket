// Package transport 提供跨服务 gRPC 客户端的统一构造。
//
// 收拢到公共库的价值：超时、熔断、重试、链路透传这些横切关注点
// 若由各服务各写一遍，必然出现「A 服务忘了设超时」这类不一致，
// 而这类问题往往在故障时才暴露。
package transport

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/go-kratos/kratos/v3/middleware"
	"github.com/go-kratos/kratos/v3/middleware/circuitbreaker"
	"github.com/go-kratos/kratos/v3/middleware/metadata"
	"github.com/go-kratos/kratos/v3/middleware/recovery"
	"github.com/go-kratos/kratos/v3/registry"
	kgrpc "github.com/go-kratos/kratos/v3/transport/grpc"
	"google.golang.org/grpc"
)

// PropagatedPrefix 需要跨服务透传的 metadata 前缀。
//
// 网关把鉴权结果（用户 ID、角色、校区）放进以此为前缀的 metadata，
// 下游服务据此判断调用者身份，避免每个服务各自解一遍 JWT。
const PropagatedPrefix = "x-md-global-"

// ClientOptions gRPC 客户端配置。
type ClientOptions struct {
	// Endpoint 目标地址。跨服务调用应使用 registry.Endpoint("xxx-service")
	// 产出的 "discovery:///xxx-service"，由 etcd 解析实例列表。
	Endpoint string
	// Discovery 服务发现实例。Endpoint 使用 discovery scheme 时必填。
	Discovery registry.Discovery
	// Timeout 单次调用超时，默认 2s。
	//
	// 必须有默认值且不宜过长：调用链上任一环节没有超时，
	// 都会让上游线程被无限期占用，最终演变为雪崩。
	Timeout time.Duration
	// Middlewares 追加的中间件，接在内置中间件之后。
	Middlewares []middleware.Middleware
	// IdempotentOperations 可安全重试的方法全名集合，
	// 形如 "/user.v1.UserService/BatchGetUserBrief"。
	//
	// 为什么要显式声明而不是「全部重试」：重试非幂等方法会造成重复扣款、
	// 重复下单这类数据错误。默认不重试、由调用方逐个声明，
	// 是把「这个方法真的幂等吗」这个判断交给最清楚的人。
	IdempotentOperations []string
	// MaxRetries 幂等方法的最大重试次数，默认 2（即最多请求 3 次）。
	MaxRetries int
	// GRPCOptions 透传给 grpc-go 的原生选项。
	GRPCOptions []grpc.DialOption
}

func (o *ClientOptions) setDefaults() error {
	if o.Endpoint == "" {
		return errors.New("transport: empty endpoint")
	}
	if o.Timeout <= 0 {
		o.Timeout = 2 * time.Second
	}
	if o.MaxRetries <= 0 {
		o.MaxRetries = 2
	}
	return nil
}

// NewGRPCConn 创建到指定服务的 gRPC 连接。
//
// 内置中间件链（按执行顺序）：
//  1. recovery —— 客户端侧的 panic 兜底，避免一次编解码异常打挂整个进程。
//  2. metadata.Client —— 透传全局 metadata（用户身份、traceID）。
//  3. circuitbreaker.Client —— 下游持续失败时快速失败，避免拖垮自己。
//  4. retry —— 仅对显式声明为幂等的方法重试。
//
// 返回的 cleanup 需在进程退出前调用。
func NewGRPCConn(ctx context.Context, o ClientOptions) (*grpc.ClientConn, func(), error) {
	if err := o.setDefaults(); err != nil {
		return nil, nil, err
	}

	ms := []middleware.Middleware{
		recovery.Recovery(),
		metadata.Client(metadata.WithPropagatedPrefix(PropagatedPrefix)),
		circuitbreaker.Client(),
	}
	if len(o.IdempotentOperations) > 0 {
		ms = append(ms, Retry(o.IdempotentOperations, o.MaxRetries))
	}
	ms = append(ms, o.Middlewares...)

	opts := []kgrpc.ClientOption{
		kgrpc.WithEndpoint(o.Endpoint),
		kgrpc.WithTimeout(o.Timeout),
		kgrpc.WithMiddleware(ms...),
	}
	if o.Discovery != nil {
		opts = append(opts, kgrpc.WithDiscovery(o.Discovery))
	}
	if len(o.GRPCOptions) > 0 {
		opts = append(opts, kgrpc.WithOptions(o.GRPCOptions...))
	}

	conn, err := kgrpc.NewClient(ctx, opts...)
	if err != nil {
		return nil, nil, fmt.Errorf("transport: dial %s: %w", o.Endpoint, err)
	}
	return conn, func() { _ = conn.Close() }, nil
}
