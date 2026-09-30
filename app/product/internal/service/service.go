// Package service 是 product-service 的传输适配层。
//
// 只做 DTO ↔ 领域对象转换与身份提取，不含业务规则。
package service

import "github.com/google/wire"

// ProviderSet 服务层的 wire provider 集合。
var ProviderSet = wire.NewSet(
	NewProductService,
	NewCategoryService,
	NewFavoriteService,
	NewWantedService,
)
