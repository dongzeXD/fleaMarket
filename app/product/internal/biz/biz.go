// Package biz 是 product-service 的领域层。
//
// 只依赖本包声明的接口（见 port.go），因此整层单测可脱离中间件运行。
package biz

import "github.com/google/wire"

// ProviderSet 领域层的 wire provider 集合。
var ProviderSet = wire.NewSet(
	NewProductUsecase,
	NewFavoriteUsecase,
	NewWantedUsecase,
	NewCatalogUsecase,
)
