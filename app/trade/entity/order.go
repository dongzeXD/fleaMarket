package entity

import (
	"time"

	"gorm.io/gorm"
)

// 订单状态。与 api/trade/v1 的 OrderStatus 同值。
const (
	OrderStatusPendingSeller uint8 = 1
	OrderStatusAccepted      uint8 = 2
	OrderStatusAwaitingPeer  uint8 = 3
	OrderStatusCompleted     uint8 = 4
	OrderStatusRejected      uint8 = 5
	OrderStatusCancelled     uint8 = 6
	OrderStatusExpired       uint8 = 7
)

// 支付方式。当前只有线下面交（doc.md §1.3）。
const (
	PayChannelOffline uint8 = 1
)

// 评价角色。
const (
	ReviewRoleBuyer  uint8 = 1
	ReviewRoleSeller uint8 = 2
)

// Order 订单（预订凭证）。
//
// 本平台不做资金托管，订单的实质是「预订锁定 + 面交约定」，
// 而非支付单据。
//
// 索引设计：
//   - uk_product_active 保证同一商品同时只能有一个进行中的订单，
//     这是数据库层面对「避免重复预订」的最后一道保障；
//   - idx_buyer_status / idx_seller_status 支撑双视角的交易记录查询；
//   - idx_expire_at 供定时任务扫描超时订单。
type Order struct {
	ID        int64 `gorm:"primaryKey;autoIncrement:false;comment:雪花ID"`
	ProductID int64 `gorm:"not null;index:idx_product;comment:商品ID"`
	BuyerID   int64 `gorm:"not null;index:idx_buyer_status,priority:1;comment:买家ID"`
	SellerID  int64 `gorm:"not null;index:idx_seller_status,priority:1;comment:卖家ID"`

	// PriceCents 成交价快照。锁定商品时固定，之后卖家改价不影响本单。
	PriceCents int64 `gorm:"not null;comment:成交价(分)"`
	// PayChannel 固定为 1（线下面交）。保留字段以便日后扩展，
	// 但服务端拒绝其他取值。
	PayChannel uint8 `gorm:"not null;default:1;comment:支付方式,仅线下面交"`

	Status uint8 `gorm:"not null;default:1;index:idx_buyer_status,priority:2;index:idx_seller_status,priority:2;comment:见 OrderStatus*"`

	// ActiveKey 用于「同一商品只能有一个进行中订单」的唯一约束。
	//
	// 进行中时取 product_id，终态时置为 NULL。
	// MySQL 的唯一索引允许多个 NULL，因此同一商品的历史订单不会互相冲突，
	// 而进行中的订单最多只能有一条。
	//
	// 为什么需要它：商品侧的 CAS 锁定已经挡住了并发预订，
	// 但那依赖商品状态正确。加上这条约束后，即便商品状态因故障出现偏差，
	// 数据库也不会产生两个并存的有效订单。
	ActiveKey *int64 `gorm:"uniqueIndex:uk_product_active;comment:进行中订单的商品ID,终态置NULL"`

	MeetPOIID    int64      `gorm:"comment:面交地点POI"`
	MeetTime     *time.Time `gorm:"comment:约定面交时间"`
	BuyerMessage string     `gorm:"type:varchar(200);comment:买家留言"`

	BuyerConfirmed  bool `gorm:"not null;default:false;comment:买家已确认完成"`
	SellerConfirmed bool `gorm:"not null;default:false;comment:卖家已确认完成"`

	CancelReason uint8  `gorm:"not null;default:0;comment:取消原因"`
	CancelRemark string `gorm:"type:varchar(200);comment:取消备注"`
	CancelledBy  int64  `gorm:"comment:发起取消的用户"`

	AcceptedAt  *time.Time `gorm:"comment:卖家接受时间"`
	CompletedAt *time.Time `gorm:"comment:完成时间"`
	// ExpireAt 当前状态的超时时间。定时任务据此关闭超时订单。
	ExpireAt *time.Time `gorm:"index:idx_expire_at;comment:当前状态超时时间"`

	CreatedAt time.Time `gorm:"index"`
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`
}

func (Order) TableName() string { return "t_order" }

// Review 一条评价。
//
// uk_order_role 保证同一订单同一角色只能评一次——
// 这是幂等的根本保障，比在应用层查一次再插更可靠。
type Review struct {
	ID        int64 `gorm:"primaryKey;autoIncrement:false"`
	OrderID   int64 `gorm:"not null;uniqueIndex:uk_order_role,priority:1;comment:订单ID"`
	ProductID int64 `gorm:"not null;comment:商品ID"`

	FromUserID int64 `gorm:"not null;index:idx_from;comment:评价者"`
	ToUserID   int64 `gorm:"not null;index:idx_to_score,priority:1;comment:被评价者"`
	Role       uint8 `gorm:"not null;uniqueIndex:uk_order_role,priority:2;comment:1-买家评卖家 2-卖家评买家"`

	Score int32 `gorm:"not null;index:idx_to_score,priority:2;comment:评分 1-5"`
	// Tags 评价标签，逗号分隔的枚举值。
	Tags    string `gorm:"type:varchar(64);comment:标签枚举值,逗号分隔"`
	Content string `gorm:"type:varchar(500);comment:评价内容"`
	// Anonymous 匿名评价不展示评价者，但仍计入信誉分。
	Anonymous bool `gorm:"not null;default:false;comment:是否匿名"`

	CreatedAt time.Time `gorm:"index:idx_to_created,priority:2"`
	UpdatedAt time.Time
}

func (Review) TableName() string { return "t_review" }
