package biz

import (
	"context"
	"time"
)

// 领域对象与端口声明。

// 订单状态。与 api/trade/v1 的 OrderStatus 同值。
const (
	StatusPendingSeller uint8 = 1
	StatusAccepted      uint8 = 2
	StatusAwaitingPeer  uint8 = 3
	StatusCompleted     uint8 = 4
	StatusRejected      uint8 = 5
	StatusCancelled     uint8 = 6
	StatusExpired       uint8 = 7
)

// PayChannelOffline 线下面交。当前唯一允许的取值（doc.md §1.3）。
const PayChannelOffline uint8 = 1

// 评价角色。
const (
	ReviewRoleBuyer  uint8 = 1
	ReviewRoleSeller uint8 = 2
)

// 各状态的超时时长。
const (
	// PendingSellerTTL 卖家确认超时。
	//
	// 24 小时：太短会让卖家因上课或睡觉而错过；太长则商品被长期锁定，
	// 其他买家看着「已预订」却永远等不到释放。
	PendingSellerTTL = 24 * time.Hour
	// AcceptedTTL 接受后的面交时限。
	//
	// 7 天：校园内约面交通常几天内能成，超过说明双方已失去兴趣，
	// 此时应释放商品而非无限期占用。
	AcceptedTTL = 7 * 24 * time.Hour
	// AwaitingPeerTTL 单方确认后等待对方的时限。
	//
	// 72 小时后自动完成：线下交易平台无法验证货物是否真的交付，
	// 只能依赖双方确认。自动完成兜住「一方忘记点」的常见情况，
	// 否则订单会永久卡在中间态，双方都无法评价。
	AwaitingPeerTTL = 72 * time.Hour
	// ReviewWindow 互评期。
	//
	// 7 天：太短用户来不及评，太长则记忆已模糊，评价失去参考价值。
	ReviewWindow = 7 * 24 * time.Hour
)

// Order 订单领域对象。
type Order struct {
	ID        int64
	ProductID int64
	BuyerID   int64
	SellerID  int64

	PriceCents int64
	PayChannel uint8
	Status     uint8

	MeetPOIID    int64
	MeetTime     *time.Time
	BuyerMessage string

	BuyerConfirmed  bool
	SellerConfirmed bool

	CancelReason uint8
	CancelRemark string
	CancelledBy  int64

	AcceptedAt  *time.Time
	CompletedAt *time.Time
	ExpireAt    *time.Time
	CreatedAt   time.Time
}

// IsParticipant 判断某用户是否为本单的买卖双方之一。
//
// 订单详情、取消、确认等操作都要用它做权限判断，
// 收敛在领域对象上避免各处重复写 `o.BuyerID == uid || o.SellerID == uid`
// 而漏掉一处。
func (o *Order) IsParticipant(userID int64) bool {
	return o != nil && (o.BuyerID == userID || o.SellerID == userID)
}

// RoleOf 返回用户在本单中的角色。不是参与方时返回 0。
func (o *Order) RoleOf(userID int64) uint8 {
	switch {
	case o == nil:
		return 0
	case o.BuyerID == userID:
		return ReviewRoleBuyer
	case o.SellerID == userID:
		return ReviewRoleSeller
	default:
		return 0
	}
}

// IsActive 判断订单是否处于进行中（占用商品）。
func (o *Order) IsActive() bool {
	if o == nil {
		return false
	}
	switch o.Status {
	case StatusPendingSeller, StatusAccepted, StatusAwaitingPeer:
		return true
	default:
		return false
	}
}

// CanReview 判断是否还在互评期内。
func (o *Order) CanReview(now time.Time) bool {
	if o == nil || o.Status != StatusCompleted || o.CompletedAt == nil {
		return false
	}
	return now.Sub(*o.CompletedAt) <= ReviewWindow
}

// OrderRepo 订单仓储。
type OrderRepo interface {
	Create(ctx context.Context, o *Order) (*Order, error)
	GetByID(ctx context.Context, id int64) (*Order, error)

	// UpdateStatus 带 fromStatus 条件的状态变更，影响 0 行表示状态已被并发改变。
	//
	// clearActiveKey 为 true 时同时把 active_key 置 NULL，
	// 释放「同一商品只能有一个进行中订单」的唯一约束占用。
	UpdateStatus(ctx context.Context, id int64, fromStatus, toStatus uint8,
		patch *OrderPatch, clearActiveKey bool) (bool, error)

	// UpdateMeetup 更新面交约定。
	UpdateMeetup(ctx context.Context, id int64, poiID int64, at time.Time) error

	// MarkConfirmed 标记某方已确认完成。
	//
	// 返回双方确认状态，供调用方判断是否该转为已完成。
	MarkConfirmed(ctx context.Context, id int64, role uint8) (buyerDone, sellerDone bool, err error)

	ListByUser(ctx context.Context, userID int64, role uint8, statuses []uint8,
		cursor string, limit int) ([]*Order, string, bool, error)

	// FindActiveByProduct 查询某商品进行中的订单。
	FindActiveByProduct(ctx context.Context, productID int64) (*Order, error)

	// ListExpired 扫描已超时的订单，供定时任务关闭。
	ListExpired(ctx context.Context, now time.Time, limit int) ([]*Order, error)

	// CountCompletedByUser 统计完成的交易数。
	CountCompletedByUser(ctx context.Context, userID int64) (bought, sold int32, err error)
}

// OrderPatch 状态变更时附带的字段更新。
type OrderPatch struct {
	CancelReason *uint8
	CancelRemark *string
	CancelledBy  *int64
	AcceptedAt   *time.Time
	CompletedAt  *time.Time
	ExpireAt     *time.Time
	MeetPOIID    *int64
	MeetTime     *time.Time
}

// Review 评价领域对象。
type Review struct {
	ID         int64
	OrderID    int64
	ProductID  int64
	FromUserID int64
	ToUserID   int64
	Role       uint8
	Score      int32
	Tags       []int32
	Content    string
	Anonymous  bool
	CreatedAt  time.Time
}

// ScoreGroup 评分归类。
//
// 4~5 好评、3 中评、1~2 差评。这个划分决定信誉分的加减方向，
// 集中在此一处，避免 biz 与统计逻辑各判一遍而出现偏差。
func ScoreGroup(score int32) uint8 {
	switch {
	case score >= 4:
		return ScoreGroupPositive
	case score == 3:
		return ScoreGroupNeutral
	default:
		return ScoreGroupNegative
	}
}

// 评分分组。
const (
	ScoreGroupPositive uint8 = 1
	ScoreGroupNeutral  uint8 = 2
	ScoreGroupNegative uint8 = 3
)

// ReviewStats 评价统计。
type ReviewStats struct {
	UserID          int64
	TotalCount      int32
	PositiveCount   int32
	NeutralCount    int32
	NegativeCount   int32
	PositiveRate    int32
	AverageScoreX10 int32
}

// ReviewRepo 评价仓储。
type ReviewRepo interface {
	// Create 创建评价。
	//
	// 返回 false 表示该订单该角色已评价过（命中唯一索引），
	// 调用方据此返回幂等成功而非报错。
	Create(ctx context.Context, r *Review) (created bool, err error)
	ListByOrder(ctx context.Context, orderID int64) ([]*Review, error)
	ListByUser(ctx context.Context, userID int64, scoreGroup uint8,
		cursor string, limit int) ([]*Review, string, bool, error)
	// ExistsByOrderRoles 查询某订单双方是否已评价。
	ExistsByOrderRoles(ctx context.Context, orderID int64) (buyerDone, sellerDone bool, err error)
	StatsOf(ctx context.Context, userID int64) (*ReviewStats, error)
}

// LockResult 商品抢占结果。
type LockResult struct {
	Locked        bool
	CurrentStatus uint8
	PriceCents    int64
	SellerID      int64
}

// ProductGateway 对 product-service 的依赖。
type ProductGateway interface {
	// LockForOrder 抢占商品。
	LockForOrder(ctx context.Context, productID, orderID, buyerID int64) (*LockResult, error)
	// ReleaseOrderLock 释放抢占，商品恢复在售。
	ReleaseOrderLock(ctx context.Context, productID, orderID int64) (bool, error)
	// MarkSold 标记商品已售出。
	MarkSold(ctx context.Context, productID, orderID int64) (bool, error)
}

// 信誉分变更原因。与 api/user/v1 的 CreditReason 同值。
const (
	CreditReasonTradeCompleted uint8 = 4
	CreditReasonOrderAbandoned uint8 = 6
	CreditReasonReviewPositive uint8 = 1
	CreditReasonReviewNeutral  uint8 = 2
	CreditReasonReviewNegative uint8 = 3
)

// UserGateway 对 user-service 的依赖。
type UserGateway interface {
	// AdjustCredit 调整信誉分。幂等键为 (reason, refID)。
	AdjustCredit(ctx context.Context, userID int64, reason uint8, refID int64) error
}

// Locker 分布式锁。
type Locker interface {
	// Lock 获取锁。wait 为 0 时立即失败。
	Lock(ctx context.Context, key string, ttl, wait time.Duration) (func(context.Context) error, error)
}

// EventPublisher 领域事件发布。
type EventPublisher interface {
	Publish(ctx context.Context, topic string, payload []byte) error
}

// 本服务发出的事件主题。
const (
	// TopicOrderCreated 预订创建，notify-service 通知卖家。
	TopicOrderCreated = "trade.order_created"
	// TopicOrderAccepted 卖家接受。
	TopicOrderAccepted = "trade.order_accepted"
	// TopicOrderClosed 订单进入终态（拒绝/取消/超时）。
	TopicOrderClosed = "trade.order_closed"
	// TopicOrderCompleted 交易完成，双方进入互评期。
	TopicOrderCompleted = "trade.order_completed"
	// TopicReviewed 评价提交，user-service 据此调整信誉分。
	TopicReviewed = "trade.reviewed"
)
