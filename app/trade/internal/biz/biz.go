// Package biz 是 trade-service 的领域层。
//
// 本平台不做资金托管（doc.md §1.3），因此「订单」的实质是
// 预订凭证 + 面交约定，而非支付单据。领域逻辑的重点在于：
// 并发抢占的正确性、状态流转的合法性、超时的兜底处理。
package biz

import "github.com/google/wire"

// ProviderSet 领域层的 wire provider 集合。
var ProviderSet = wire.NewSet(
	NewOrderUsecase,
	NewReviewUsecase,
)
