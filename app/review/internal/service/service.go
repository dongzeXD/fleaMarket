package service

import "github.com/google/wire"

// ProviderSet 服务层的 wire provider 集合。
// 新增 proto service 实现后在此登记。
var ProviderSet = wire.NewSet()
