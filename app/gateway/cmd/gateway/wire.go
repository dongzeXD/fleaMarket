//go:build wireinject
// +build wireinject

package main

import (
	"log/slog"

	"github.com/fleamarket/app/gateway/internal/conf"
	"github.com/fleamarket/app/gateway/internal/proxy"
	"github.com/fleamarket/app/gateway/internal/server"

	"github.com/go-kratos/kratos/v3"
	"github.com/go-kratos/kratos/v3/registry"
	"github.com/google/wire"
)

func wireApp(
	*conf.Server,
	*conf.Auth,
	*conf.Downstream,
	*conf.RateLimit,
	*conf.Docs,
	registry.Registrar,
	// Discovery 与 Registrar 由同一个 etcd 实例实现，但 wire 按类型注入，
	// 故需分别声明。网关既要注册自身，也要发现下游。
	registry.Discovery,
	*slog.Logger,
) (*kratos.App, func(), error) {
	panic(wire.Build(
		server.ProviderSet,
		proxy.ProviderSet,
		newApp,
	))
}
