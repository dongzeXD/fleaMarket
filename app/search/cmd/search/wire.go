//go:build wireinject
// +build wireinject

package main

import (
	"log/slog"

	"github.com/fleamarket/app/search/internal/biz"
	"github.com/fleamarket/app/search/internal/conf"
	"github.com/fleamarket/app/search/internal/data"
	"github.com/fleamarket/app/search/internal/server"
	"github.com/fleamarket/app/search/internal/service"

	"github.com/go-kratos/kratos/v3"
	"github.com/go-kratos/kratos/v3/registry"
	"github.com/google/wire"
)

func wireApp(
	*conf.Server,
	*conf.Data,
	*conf.Meilisearch,
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
