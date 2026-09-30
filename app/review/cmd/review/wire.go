//go:build wireinject
// +build wireinject

package main

import (
	"log/slog"

	"github.com/fleamarket/app/review/internal/conf"
	"github.com/fleamarket/app/review/internal/server"

	"github.com/go-kratos/kratos/v3"
	"github.com/go-kratos/kratos/v3/registry"
	"github.com/google/wire"
)

func wireApp(*conf.Server, *conf.Data, registry.Registrar, *slog.Logger) (*kratos.App, func(), error) {
	panic(wire.Build(
		server.ProviderSet,
		// data / biz / service 的 ProviderSet 在本服务尚无 Repo 与 Usecase 时
		// 是空集或无消费者，wire 会以 "unused provider set" 报错。
		// 添加首个 Repo/Usecase/Service 后取消下面三行注释并重新执行 make wire-review。
		// data.ProviderSet,
		// biz.ProviderSet,
		// service.ProviderSet,
		newApp,
	))
}
