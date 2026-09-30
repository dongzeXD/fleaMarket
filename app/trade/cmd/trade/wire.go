//go:build wireinject
// +build wireinject

package main

import (
	"log/slog"

	"github.com/fleamarket/app/trade/internal/biz"
	"github.com/fleamarket/app/trade/internal/conf"
	"github.com/fleamarket/app/trade/internal/data"
	"github.com/fleamarket/app/trade/internal/server"
	"github.com/fleamarket/app/trade/internal/service"

	"github.com/go-kratos/kratos/v3"
	"github.com/go-kratos/kratos/v3/registry"
	"github.com/google/wire"
)

func wireApp(
	*conf.Server,
	*conf.Data,
	*conf.Downstream,
	*conf.Business,
	registry.Registrar,
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
