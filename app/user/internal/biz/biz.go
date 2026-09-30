// Package biz 是 user-service 的领域层。
//
// 分层约定（kratos 官方布局）：
//   - service：DTO ↔ 领域对象的转换，不含业务规则
//   - biz：全部业务规则，只依赖本包声明的接口（见 port.go / repo.go）
//   - data：接口的具体实现（MySQL、Redis、外部 HTTP）
//
// 这样 biz 的单测可以完全脱离中间件，用内存假实现覆盖全部分支。
package biz

import "github.com/google/wire"

// ProviderSet 领域层的 wire provider 集合。
var ProviderSet = wire.NewSet(
	NewAuthUsecase,
	NewUserUsecase,
	NewCampusAuthUsecase,
)
