#!/usr/bin/env bash
# ============================================================================
# 由 scripts/new-service.sh 调用，生成服务骨架中需要替换的文件。
#
# 拆成独立脚本是因为这里包含大段 heredoc 模板，混在主脚本里会让流程难以阅读。
#
# 用法: gen-service-files.sh <svc> <module> <target-dir>
# ============================================================================
set -euo pipefail

SVC="${1:?}"
MODULE="${2:?}"
TARGET="${3:?}"

mkdir -p "${TARGET}/entity"

# ---------------------------------------------------------------------------
# 配置定义：在模板的 Server/Data 之外补齐 Registry、Trace、Log、Auth
# ---------------------------------------------------------------------------
cat > "${TARGET}/internal/conf/conf.proto" <<EOF
syntax = "proto3";
package kratos.api;

option go_package = "${MODULE}/internal/conf;conf";

import "google/protobuf/duration.proto";

message Bootstrap {
  Server server = 1;
  Data data = 2;
  Registry registry = 3;
  Observability observability = 4;
  Auth auth = 5;
}

message Server {
  message HTTP {
    string network = 1;
    string addr = 2;
    google.protobuf.Duration timeout = 3;
  }
  message GRPC {
    string network = 1;
    string addr = 2;
    google.protobuf.Duration timeout = 3;
  }
  HTTP http = 1;
  GRPC grpc = 2;
}

message Data {
  message Database {
    // DSN。凭据由环境变量注入，不写入配置文件。
    string source = 1;
    int32 max_open_conns = 2;
    int32 max_idle_conns = 3;
    google.protobuf.Duration conn_max_lifetime = 4;
    google.protobuf.Duration slow_threshold = 5;
    // 打印全部 SQL，仅开发环境开启。
    bool debug = 6;
  }
  message Redis {
    string addr = 1;
    string password = 2;
    int32 db = 3;
    int32 pool_size = 4;
    google.protobuf.Duration read_timeout = 5;
    google.protobuf.Duration write_timeout = 6;
    // key 前缀，多环境共用实例时用于隔离。
    string key_prefix = 7;
  }
  Database database = 1;
  Redis redis = 2;
}

// Registry etcd 服务注册发现配置。
message Registry {
  message Etcd {
    repeated string endpoints = 1;
    google.protobuf.Duration dial_timeout = 2;
    string username = 3;
    string password = 4;
    // key 前缀，必须按环境隔离，否则开发实例会被生产流量发现。
    string namespace = 5;
    // 实例租约时长，决定崩溃后多久被摘除。
    google.protobuf.Duration ttl = 6;
  }
  Etcd etcd = 1;
}

// Observability 日志与链路追踪。
message Observability {
  message Log {
    // debug / info / warn / error
    string level = 1;
    // text / json
    string format = 2;
    bool add_source = 3;
  }
  message Trace {
    bool enabled = 1;
    // OTLP/HTTP 端点，如 localhost:4318
    string endpoint = 2;
    double sample_ratio = 3;
    bool insecure = 4;
  }
  Log log = 1;
  Trace trace = 2;
  string environment = 3;
}

// Auth JWT 校验配置。下游服务只需校验，签发仅 user-service 使用。
message Auth {
  // 签名密钥，必须由环境变量注入。
  string jwt_secret = 1;
  google.protobuf.Duration access_ttl = 2;
  google.protobuf.Duration refresh_ttl = 3;
}
EOF

# ---------------------------------------------------------------------------
# 数据层：GORM + Redis，替换模板的 Ent 版本
# ---------------------------------------------------------------------------
cat > "${TARGET}/internal/data/data.go" <<EOF
package data

import (
	"log/slog"
	"time"

	"${MODULE}/internal/conf"

	"github.com/fleamarket/pkg/cache"
	"github.com/fleamarket/pkg/orm"
	"github.com/google/wire"
	"gorm.io/gorm"
)

// ProviderSet 数据层的 wire provider 集合。
// 新增 Repo 实现后在此登记。
var ProviderSet = wire.NewSet(NewData)

// Data 持有各 Repo 共享的长生命周期客户端。
type Data struct {
	DB    *gorm.DB
	Cache *cache.RedisCache
}

// NewData 建立数据库与缓存连接。
//
// 注意此处不做建表：schema 变更统一由 cmd/dbctl 负责（doc.md §7），
// 服务启动时自动建表会让「谁在什么时候改了表结构」变得不可追溯。
func NewData(c *conf.Data, logger *slog.Logger) (*Data, func(), error) {
	db, dbCleanup, err := orm.Open(orm.Config{
		DSN:             c.GetDatabase().GetSource(),
		MaxOpenConns:    int(c.GetDatabase().GetMaxOpenConns()),
		MaxIdleConns:    int(c.GetDatabase().GetMaxIdleConns()),
		ConnMaxLifetime: c.GetDatabase().GetConnMaxLifetime().AsDuration(),
		SlowThreshold:   c.GetDatabase().GetSlowThreshold().AsDuration(),
		Debug:           c.GetDatabase().GetDebug(),
		Logger:          logger,
	})
	if err != nil {
		return nil, nil, err
	}

	rc, cacheCleanup, err := cache.NewRedis(cache.RedisConfig{
		Addr:         c.GetRedis().GetAddr(),
		Password:     c.GetRedis().GetPassword(),
		DB:           int(c.GetRedis().GetDb()),
		PoolSize:     int(c.GetRedis().GetPoolSize()),
		ReadTimeout:  orDefault(c.GetRedis().GetReadTimeout().AsDuration(), 200*time.Millisecond),
		WriteTimeout: orDefault(c.GetRedis().GetWriteTimeout().AsDuration(), 200*time.Millisecond),
		KeyPrefix:    c.GetRedis().GetKeyPrefix(),
	})
	if err != nil {
		dbCleanup()
		return nil, nil, err
	}

	cleanup := func() {
		cacheCleanup()
		dbCleanup()
	}
	return &Data{DB: db, Cache: rc}, cleanup, nil
}

func orDefault(v, def time.Duration) time.Duration {
	if v <= 0 {
		return def
	}
	return v
}
EOF

cat > "${TARGET}/internal/biz/biz.go" <<EOF
package biz

import "github.com/google/wire"

// ProviderSet 领域层的 wire provider 集合。
// 新增 Usecase 后在此登记。
var ProviderSet = wire.NewSet()
EOF

cat > "${TARGET}/internal/service/service.go" <<EOF
package service

import "github.com/google/wire"

// ProviderSet 服务层的 wire provider 集合。
// 新增 proto service 实现后在此登记。
var ProviderSet = wire.NewSet()
EOF

# ---------------------------------------------------------------------------
# 实体包：刻意放在 internal 之外（doc.md §7.1）
# ---------------------------------------------------------------------------
cat > "${TARGET}/entity/entity.go" <<EOF
// Package entity 定义本服务的数据库表实体。
//
// 为何放在 internal 之外（见 doc.md §7.1）：cmd/dbctl 需要 import 各服务的
// 实体才能做统一建表，而 internal 会阻止跨模块导入。
//
// 因此这里只允许出现「纯数据结构」——字段、GORM tag、TableName()。
// Repo 实现、领域逻辑、service 一律留在 internal 下，其他服务即使
// import 了本包也拿不到任何可执行的业务能力，不构成边界泄漏。
package entity

// Group 本服务的实体分组名，供 dbctl 的 --group 参数使用。
const Group = "${SVC}"
EOF

# ---------------------------------------------------------------------------
# 传输层：模板的 grpc.go / http.go 引用了已删除的 todo 示例契约，需重写。
#
# 中间件链的顺序有讲究：recovery 必须最外层，否则后续中间件 panic 无人兜底；
# validate 放在业务 handler 之前，避免非法参数进入领域逻辑。
# ---------------------------------------------------------------------------
cat > "${TARGET}/internal/server/grpc.go" <<EOF
package server

import (
	"${MODULE}/internal/conf"

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
func NewGRPCServer(c *conf.Server) (*grpc.Server, error) {
	validator, err := validate.Middleware()
	if err != nil {
		return nil, err
	}

	ms := []middleware.Middleware{
		recovery.Recovery(),
		metadata.Server(),
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

	// TODO: 在此注册本服务的 gRPC service 实现，例如
	// userv1.RegisterUserServiceServer(srv, userSvc)

	return srv, nil
}
EOF

cat > "${TARGET}/internal/server/http.go" <<EOF
package server

import (
	"${MODULE}/internal/conf"

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
func NewHTTPServer(c *conf.Server) (*http.Server, error) {
	validator, err := validate.Middleware()
	if err != nil {
		return nil, err
	}

	ms := []middleware.Middleware{
		recovery.Recovery(),
		metadata.Server(),
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

	// TODO: 在此注册本服务的 HTTP service 实现，例如
	// userv1.RegisterUserServiceHTTPServer(srv, userSvc)

	return srv, nil
}
EOF

# ---------------------------------------------------------------------------
# 进程入口：接入 pkg/log、pkg/trace、pkg/registry
# ---------------------------------------------------------------------------
cat > "${TARGET}/cmd/${SVC}/main.go" <<'MAINEOF'
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"

	"MODULE_PLACEHOLDER/internal/conf"

	pkglog "github.com/fleamarket/pkg/log"
	pkgreg "github.com/fleamarket/pkg/registry"
	pkgtrace "github.com/fleamarket/pkg/trace"
	"github.com/go-kratos/kratos/contrib/otel/v3/tracing"
	"github.com/go-kratos/kratos/v3"
	"github.com/go-kratos/kratos/v3/config"
	"github.com/go-kratos/kratos/v3/config/env"
	"github.com/go-kratos/kratos/v3/config/file"
	"github.com/go-kratos/kratos/v3/registry"
	"github.com/go-kratos/kratos/v3/transport/grpc"
	"github.com/go-kratos/kratos/v3/transport/http"

	_ "go.uber.org/automaxprocs"
)

// 通过 -ldflags "-X main.Version=x.y.z" 注入。
var (
	Name     = "SVC_PLACEHOLDER-service"
	Version  string
	flagconf string

	id, _ = os.Hostname()
)

func init() {
	flag.StringVar(&flagconf, "conf", "../../configs", "配置文件路径")
}

func newApp(logger *slog.Logger, gs *grpc.Server, hs *http.Server, r registry.Registrar) *kratos.App {
	return kratos.New(
		kratos.ID(id),
		kratos.Name(Name),
		kratos.Version(Version),
		kratos.Metadata(map[string]string{}),
		kratos.Logger(logger),
		kratos.Server(gs, hs),
		// 注册到 etcd，并在优雅停机时自动注销（doc.md §8.2）。
		kratos.Registrar(r),
	)
}

func main() {
	flag.Parse()

	c := config.New(config.WithSource(
		file.NewSource(flagconf),
		// 环境变量优先级高于文件，使密钥可以不落盘。
		env.NewSource("KRATOS"),
	))
	defer c.Close()

	if err := c.Load(); err != nil {
		panic(err)
	}
	var bc conf.Bootstrap
	if err := c.Scan(&bc); err != nil {
		panic(err)
	}

	logger := pkglog.New(pkglog.Config{
		ServiceID:      id,
		ServiceName:    Name,
		ServiceVersion: Version,
		Level:          bc.GetObservability().GetLog().GetLevel(),
		Format:         logFormat(bc.GetObservability().GetLog().GetFormat()),
		AddSource:      bc.GetObservability().GetLog().GetAddSource(),
		// TraceAttrs 把 traceID 注入每条日志，实现日志与链路互查。
		Extractors: []pkglog.Extractor{tracing.TraceAttrs},
	})
	pkglog.SetDefault(logger)

	ctx := context.Background()
	shutdownTrace, err := pkgtrace.Init(ctx, pkgtrace.Config{
		Enabled:        bc.GetObservability().GetTrace().GetEnabled(),
		Endpoint:       bc.GetObservability().GetTrace().GetEndpoint(),
		ServiceName:    Name,
		ServiceVersion: Version,
		Environment:    bc.GetObservability().GetEnvironment(),
		SampleRatio:    bc.GetObservability().GetTrace().GetSampleRatio(),
		Insecure:       bc.GetObservability().GetTrace().GetInsecure(),
	})
	if err != nil {
		panic(err)
	}
	defer func() { _ = shutdownTrace(ctx) }()

	reg, regCleanup, err := pkgreg.New(pkgreg.Config{
		Endpoints:   bc.GetRegistry().GetEtcd().GetEndpoints(),
		DialTimeout: bc.GetRegistry().GetEtcd().GetDialTimeout().AsDuration(),
		Username:    bc.GetRegistry().GetEtcd().GetUsername(),
		Password:    bc.GetRegistry().GetEtcd().GetPassword(),
		Namespace:   bc.GetRegistry().GetEtcd().GetNamespace(),
		TTL:         bc.GetRegistry().GetEtcd().GetTtl().AsDuration(),
	})
	if err != nil {
		panic(err)
	}
	defer regCleanup()

	app, cleanup, err := wireApp(bc.Server, bc.Data, reg, logger)
	if err != nil {
		panic(err)
	}
	defer cleanup()

	if err := app.Run(); err != nil {
		panic(err)
	}
}

func logFormat(s string) pkglog.Format {
	if s == "json" {
		return pkglog.FormatJSON
	}
	return pkglog.FormatText
}
MAINEOF
sed -i '' -e "s#MODULE_PLACEHOLDER#${MODULE}#g" -e "s#SVC_PLACEHOLDER#${SVC}#g" \
  "${TARGET}/cmd/${SVC}/main.go"

cat > "${TARGET}/cmd/${SVC}/wire.go" <<EOF
//go:build wireinject
// +build wireinject

package main

import (
	"log/slog"

	"${MODULE}/internal/conf"
	"${MODULE}/internal/server"

	"github.com/go-kratos/kratos/v3"
	"github.com/go-kratos/kratos/v3/registry"
	"github.com/google/wire"
)

func wireApp(*conf.Server, *conf.Data, registry.Registrar, *slog.Logger) (*kratos.App, func(), error) {
	panic(wire.Build(
		server.ProviderSet,
		// data / biz / service 的 ProviderSet 在本服务尚无 Repo 与 Usecase 时
		// 是空集或无消费者，wire 会以 "unused provider set" 报错。
		// 添加首个 Repo/Usecase/Service 后取消下面三行注释并重新执行 make wire-SVC_PLACEHOLDER。
		// data.ProviderSet,
		// biz.ProviderSet,
		// service.ProviderSet,
		newApp,
	))
}
EOF
sed -i '' -e "s#SVC_PLACEHOLDER#${SVC}#g" "${TARGET}/cmd/${SVC}/wire.go"
rm -f "${TARGET}/cmd/${SVC}/wire_gen.go"

# ---------------------------------------------------------------------------
# 本地模块的 replace 指令
#
# go.work 让 go build 能解析 pkg / api，但 go mod tidy 不感知 workspace
# （它按单模块视角解析依赖），会把 github.com/fleamarket/pkg 当成需要从
# 远端拉取的模块而失败。因此每个服务的 go.mod 必须显式 replace。
# ---------------------------------------------------------------------------
if ! grep -q "github.com/fleamarket/pkg => " "${TARGET}/go.mod"; then
  cat >> "${TARGET}/go.mod" <<'EOF'

replace (
	github.com/fleamarket/api => ../../api
	github.com/fleamarket/pkg => ../../pkg
)
EOF
fi
# require 条目由 go mod tidy 补全，此处先声明依赖以便 tidy 能找到它们。
(cd "${TARGET}" && go get github.com/fleamarket/pkg github.com/fleamarket/api >/dev/null 2>&1 || true)
