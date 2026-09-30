// Package registry 封装基于 etcd 的服务注册与发现。
//
// 需求要求用 etcd 做注册发现（doc.md §8）。本包把 etcd 客户端与 kratos
// registry 的组装收拢到一处，各服务只传配置，不重复写连接与租约细节。
//
// 返回类型是 kratos 的 Registrar / Discovery 接口而非 etcd 具体类型，
// 因此日后若换 consul 或 nacos，只需在此处换实现，服务代码不动。
package registry

import (
	"errors"
	"fmt"
	"time"

	etcdregistry "github.com/go-kratos/kratos/contrib/registry/etcd/v3"
	"github.com/go-kratos/kratos/v3/registry"
	clientv3 "go.etcd.io/etcd/client/v3"
)

// Config etcd 连接与注册配置。
type Config struct {
	// Endpoints etcd 地址列表。
	Endpoints []string
	// DialTimeout 建连超时，默认 5s。
	DialTimeout time.Duration
	// Username / Password etcd 认证凭据，开启鉴权时必填。
	Username string
	Password string
	// Namespace key 前缀，用于多环境隔离，如 "/fleamarket/prod"。
	//
	// 必须按环境隔离：若 dev 与 prod 共用前缀，开发机启动的实例
	// 会被生产流量发现并调用，后果严重。
	Namespace string
	// TTL 服务实例的租约时长，默认 15s。
	//
	// 这个值决定了「实例崩溃后多久被摘除」。设得过短会让网络抖动
	// 误判为实例下线，过长则故障实例会持续接收流量。
	TTL time.Duration
	// MaxRetry 注册失败的重试次数，默认 5。
	MaxRetry int
}

func (c *Config) setDefaults() error {
	if len(c.Endpoints) == 0 {
		return errors.New("registry: empty etcd endpoints")
	}
	if c.DialTimeout <= 0 {
		c.DialTimeout = 5 * time.Second
	}
	if c.TTL <= 0 {
		c.TTL = 15 * time.Second
	}
	if c.MaxRetry <= 0 {
		c.MaxRetry = 5
	}
	if c.Namespace == "" {
		c.Namespace = "/microservices"
	}
	return nil
}

// Registry 同时实现 kratos 的 Registrar 与 Discovery。
type Registry struct {
	*etcdregistry.Registry
	client *clientv3.Client
}

// New 创建 etcd 注册中心。
//
// 返回的 cleanup 会关闭 etcd 连接，需在进程退出前调用。
func New(c Config) (*Registry, func(), error) {
	if err := c.setDefaults(); err != nil {
		return nil, nil, err
	}

	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   c.Endpoints,
		DialTimeout: c.DialTimeout,
		Username:    c.Username,
		Password:    c.Password,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("registry: connect etcd: %w", err)
	}

	r := etcdregistry.New(cli,
		etcdregistry.Namespace(c.Namespace),
		etcdregistry.RegisterTTL(c.TTL),
		etcdregistry.MaxRetry(c.MaxRetry),
	)

	cleanup := func() { _ = cli.Close() }
	return &Registry{Registry: r, client: cli}, cleanup, nil
}

// Client 暴露底层 etcd 客户端。
//
// 刻意开放：除注册发现外，etcd 还适合承担分布式配置与
// Snowflake 节点号的顺序租约分配，复用同一连接比新建更合理。
func (r *Registry) Client() *clientv3.Client { return r.client }

// 编译期确认接口实现，避免 kratos 版本升级后接口变化却未被发现。
var (
	_ registry.Registrar = (*Registry)(nil)
	_ registry.Discovery = (*Registry)(nil)
)

// Endpoint 构造 kratos 的 discovery 端点字符串。
//
// 形如 "discovery:///user-service"，kratos 的 gRPC 客户端会据此
// 走 Discovery 解析而非直连 IP。做成函数是为了避免各处手写字符串拼错。
func Endpoint(serviceName string) string {
	return "discovery:///" + serviceName
}
