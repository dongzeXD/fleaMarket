//go:build wireinject
// +build wireinject

package main

import (
	"log/slog"

	"github.com/fleamarket/app/user/internal/biz"
	"github.com/fleamarket/app/user/internal/conf"
	"github.com/fleamarket/app/user/internal/data"
	"github.com/fleamarket/app/user/internal/server"
	"github.com/fleamarket/app/user/internal/service"

	"github.com/go-kratos/kratos/v3"
	"github.com/go-kratos/kratos/v3/registry"
	"github.com/google/wire"
)

func wireApp(
	*conf.Server,
	*conf.Data,
	*conf.Auth,
	*conf.Crypto,
	*conf.Storage,
	*conf.Sms,
	*conf.Email,
	*conf.Wechat,
	*conf.Business,
	registry.Registrar,
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
