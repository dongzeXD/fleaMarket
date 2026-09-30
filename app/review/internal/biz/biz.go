package biz

import "github.com/google/wire"

// ProviderSet 领域层的 wire provider 集合。
// 新增 Usecase 后在此登记。
var ProviderSet = wire.NewSet()
