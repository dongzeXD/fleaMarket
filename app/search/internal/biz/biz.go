// Package biz 是 search-service 的领域层。
//
// 本服务不拥有数据库表：索引在 Meilisearch，权威数据在 product-service。
// 因此这里的「仓储」全是对外部的网关。
package biz

import "github.com/google/wire"

// ProviderSet 领域层的 wire provider 集合。
var ProviderSet = wire.NewSet(NewSearchUsecase)
