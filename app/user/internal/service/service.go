// Package service 是 user-service 的传输适配层。
//
// 只做 DTO ↔ 领域对象的转换与身份提取，不含业务规则。
// 敏感字段的脱敏在本层完成——这里是数据离开服务的最后一道关口。
package service

import "github.com/google/wire"

// ProviderSet 服务层的 wire provider 集合。
var ProviderSet = wire.NewSet(
	NewAuthService,
	NewUserService,
	NewCampusAuthService,
)
