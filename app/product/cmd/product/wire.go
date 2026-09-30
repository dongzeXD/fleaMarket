//go:build wireinject
// +build wireinject

package main

import (
	"log/slog"

	"github.com/fleamarket/app/product/internal/biz"
	"github.com/fleamarket/app/product/internal/conf"
	"github.com/fleamarket/app/product/internal/data"
	"github.com/fleamarket/app/product/internal/server"
	"github.com/fleamarket/app/product/internal/service"

	"github.com/go-kratos/kratos/v3"
	"github.com/go-kratos/kratos/v3/registry"
	"github.com/google/wire"
)

func wireApp(
	*conf.Server,
	*conf.Data,
	*conf.Storage,
	*conf.Downstream,
	*conf.Business,
	registry.Registrar,
	// Discovery 与 Registrar 由同一 etcd 实例实现，但 wire 按类型注入，
	// 故需分别声明。本服务既注册自身，也要发现 user-service。
	registry.Discovery,
	*slog.Logger,
) (*kratos.App, func(), error) {
	panic(wire.Build(
		server.ProviderSet,
		data.ProviderSet,
		biz.ProviderSet,
		service.ProviderSet,
		newApp,
	))
}
