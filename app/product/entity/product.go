package entity

import (
	"time"

	"gorm.io/gorm"
)

// 商品状态。与 api/product/v1 的 ProductStatus 枚举同值。
const (
	ProductStatusPendingReview uint8 = 1
	ProductStatusOnSale        uint8 = 2
	ProductStatusReserved      uint8 = 3
	ProductStatusSold          uint8 = 4
	ProductStatusOffline       uint8 = 5
	ProductStatusRejected      uint8 = 6
)

// 成色。
const (
	ConditionNew         uint8 = 1
	ConditionLikeNew     uint8 = 2
	ConditionLightlyUsed uint8 = 3
	ConditionWellUsed    uint8 = 4
)

// Product 商品主表。
//
// 索引设计围绕三类高频查询：
//   - 我的商品：idx_seller_status
//   - 校区内按价格筛选：idx_campus_price
//   - 信息流按热度/时间排序：idx_campus_heat、idx_campus_created
//
// 全部复合索引都以 campus_id 为首列，因为「同校区优先」是本平台
// 所有列表查询的固定前置条件（doc.md §9.3），没有跨校区的全局浏览场景。
type Product struct {
	ID       int64 `gorm:"primaryKey;autoIncrement:false;comment:雪花ID"`
	SellerID int64 `gorm:"not null;index:idx_seller_status,priority:1;comment:卖家ID"`

	Title       string `gorm:"type:varchar(60);not null;comment:标题"`
	Description string `gorm:"type:varchar(2000);comment:描述"`

	PriceCents int64 `gorm:"not null;index:idx_campus_price,priority:2;comment:价格(分)"`
	// OriginalPriceCents 原价，用于展示「原价 ¥x」。0 表示未填。
	OriginalPriceCents int64 `gorm:"not null;default:0;comment:原价(分)"`

	Condition  uint8 `gorm:"not null;comment:成色 1-全新 2-几乎全新 3-轻微使用 4-明显使用"`
	CategoryID int64 `gorm:"not null;index:idx_category_status,priority:1;comment:二级分类ID"`

	SchoolID int64 `gorm:"not null;comment:学校ID,冗余自卖家认证信息"`
	CampusID int64 `gorm:"not null;index:idx_campus_price,priority:1;index:idx_campus_heat,priority:1;index:idx_campus_created,priority:1;comment:交易校区"`
	// POIID 建议面交地点。用指针以区分「未设置」与「设置为 0」。
	POIID *int64 `gorm:"comment:面交地点POI"`

	Status uint8 `gorm:"not null;default:1;index:idx_seller_status,priority:2;index:idx_category_status,priority:2;comment:见 ProductStatus*"`
	// RejectReason 审核驳回原因。
	RejectReason string `gorm:"type:varchar(255);comment:驳回原因"`
	// HitWords 审核命中的违规词，逗号分隔。仅卖家可见，用于说明具体问题。
	HitWords string `gorm:"type:varchar(512);comment:命中违规词"`

	// CoverKey 封面图对象 key。冗余在主表，使列表查询无需联表查图片。
	CoverKey string `gorm:"type:varchar(255);comment:封面对象key"`

	ViewCount     int64 `gorm:"not null;default:0;comment:浏览量"`
	FavoriteCount int32 `gorm:"not null;default:0;comment:收藏数"`
	InquiryCount  int32 `gorm:"not null;default:0;comment:咨询数,用于热度计算"`
	// HeatScore 热度分，由定时任务批量重算（doc.md §9.3）。
	HeatScore float64 `gorm:"not null;default:0;index:idx_campus_heat,priority:2;comment:热度分"`

	// LockedOrderID 当前锁定该商品的订单 ID。
	//
	// 记录它而非只改状态，是为了让 ReleaseOrderLock 能校验「是不是这一单」——
	// 否则一个过期的取消请求可能释放掉别人刚建立的锁。
	LockedOrderID *int64 `gorm:"comment:当前锁定的订单ID"`
	// SoldOrderID 成交订单 ID。
	SoldOrderID *int64 `gorm:"comment:成交订单ID"`

	CreatedAt time.Time `gorm:"index:idx_campus_created,priority:2"`
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`
}

func (Product) TableName() string { return "t_product" }

// ProductImage 商品图片。
//
// 独立成表而非在主表存 JSON 数组：图片需要单独记录宽高（供前端预留占位
// 避免布局跳变），且未来可能要按图片维度做审核。
type ProductImage struct {
	ID        int64  `gorm:"primaryKey;autoIncrement:false"`
	ProductID int64  `gorm:"not null;index:idx_product_sort,priority:1;comment:商品ID"`
	ObjectKey string `gorm:"type:varchar(255);not null;comment:对象key"`
	Sort      int32  `gorm:"not null;default:0;index:idx_product_sort,priority:2;comment:排序,0为封面"`
	Width     int32  `gorm:"not null;default:0"`
	Height    int32  `gorm:"not null;default:0"`
	CreatedAt time.Time
}

func (ProductImage) TableName() string { return "t_product_image" }

// PriceHistory 价格变更流水。
//
// 用途有两个：触发收藏用户的降价提醒，以及在详情页展示「近期降价」标记。
// 也是防「先涨后降假促销」的取证依据。
type PriceHistory struct {
	ID            int64     `gorm:"primaryKey;autoIncrement:false"`
	ProductID     int64     `gorm:"not null;index:idx_product_created,priority:1;comment:商品ID"`
	OldPriceCents int64     `gorm:"not null;comment:原价格(分)"`
	NewPriceCents int64     `gorm:"not null;comment:新价格(分)"`
	CreatedAt     time.Time `gorm:"index:idx_product_created,priority:2"`
}

func (PriceHistory) TableName() string { return "t_price_history" }
