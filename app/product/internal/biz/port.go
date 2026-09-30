package biz

import (
	"context"
	"time"
)

// 本文件声明领域层对外部能力的依赖（端口）与领域对象。
//
// 与 user-service 同构：biz 只声明「需要什么」，实现由 data 层提供。

// 商品状态。与 api/product/v1 的 ProductStatus 同值。
const (
	StatusPendingReview uint8 = 1
	StatusOnSale        uint8 = 2
	StatusReserved      uint8 = 3
	StatusSold          uint8 = 4
	StatusOffline       uint8 = 5
	StatusRejected      uint8 = 6
)

// 成色。
const (
	ConditionNew         uint8 = 1
	ConditionLikeNew     uint8 = 2
	ConditionLightlyUsed uint8 = 3
	ConditionWellUsed    uint8 = 4
)

// 求购帖状态。
const (
	WantedOpen    uint8 = 1
	WantedClosed  uint8 = 2
	WantedExpired uint8 = 3
)

// Image 商品图片。
type Image struct {
	ObjectKey string
	Sort      int32
	Width     int32
	Height    int32
}

// Product 商品领域对象。
type Product struct {
	ID       int64
	SellerID int64

	Title       string
	Description string

	PriceCents         int64
	OriginalPriceCents int64

	Condition  uint8
	CategoryID int64
	SchoolID   int64
	CampusID   int64
	POIID      *int64

	Status       uint8
	RejectReason string
	HitWords     []string

	CoverKey string
	Images   []Image

	ViewCount     int64
	FavoriteCount int32
	InquiryCount  int32
	HeatScore     float64

	LockedOrderID *int64
	SoldOrderID   *int64

	CreatedAt time.Time
	UpdatedAt time.Time
}

// IsVisible 判断商品是否对外可见。
//
// 待审核、驳回、下架的商品只有卖家自己能看到——
// 这条规则在详情查询与列表查询中都要用，收敛在领域对象上避免各处判断不一致。
func (p *Product) IsVisible() bool {
	if p == nil {
		return false
	}
	switch p.Status {
	case StatusOnSale, StatusReserved, StatusSold:
		return true
	default:
		return false
	}
}

// IsOwnedBy 判断是否属于指定卖家。
func (p *Product) IsOwnedBy(userID int64) bool {
	return p != nil && p.SellerID == userID
}

// ProductUpdate 商品的部分更新。
//
// 每个字段用指针表示「是否提供」：价格 0 是合法值（免费赠送），
// 用零值判断会让卖家无法把商品改成免费。
type ProductUpdate struct {
	Title       *string
	Description *string
	PriceCents  *int64
	Condition   *uint8
	CategoryID  *int64
	POIID       *int64
	// ImageKeys 非 nil 时整体替换图片列表。
	ImageKeys []string
}

// ProductRepo 商品仓储。
type ProductRepo interface {
	Create(ctx context.Context, p *Product) (*Product, error)
	GetByID(ctx context.Context, id int64) (*Product, error)
	// Update 应用部分更新。返回更新后的完整对象。
	Update(ctx context.Context, id int64, u *ProductUpdate) (*Product, error)
	// UpdateStatus 变更状态。带 fromStatus 条件做乐观并发控制，
	// 影响 0 行表示状态已被他人改变。
	UpdateStatus(ctx context.Context, id int64, fromStatus, toStatus uint8) (bool, error)
	// SoftDelete 软删除。
	SoftDelete(ctx context.Context, id int64) error

	BatchGet(ctx context.Context, ids []int64) (map[int64]*Product, error)
	ListBySeller(ctx context.Context, sellerID int64, statuses []uint8,
		cursor string, limit int) ([]*Product, string, bool, error)

	// LockForOrder 以 CAS 方式抢占商品。
	//
	// 实现必须是单条 `UPDATE ... WHERE id=? AND status=ON_SALE`，
	// 靠影响行数判定成功。先查后改在并发下会让两个买家都认为抢到了。
	LockForOrder(ctx context.Context, productID, orderID int64) (bool, error)
	// ReleaseOrderLock 释放锁定，仅当持锁订单匹配时生效。
	ReleaseOrderLock(ctx context.Context, productID, orderID int64) (bool, error)
	// MarkSold 标记已售出，仅当持锁订单匹配时生效。
	MarkSold(ctx context.Context, productID, orderID int64) (bool, error)

	// ApplyModeration 回写审核结论。
	ApplyModeration(ctx context.Context, id int64, passed bool,
		reason string, hitWords []string) error

	// IncrViewCount 累加浏览量。
	IncrViewCount(ctx context.Context, id int64, delta int64) error
	// AdjustFavoriteCount 调整收藏数并返回新值。
	AdjustFavoriteCount(ctx context.Context, id int64, delta int32) (int32, error)
	// CountBySeller 统计卖家的在售与已售数量。
	CountBySeller(ctx context.Context, sellerID int64) (onSale, sold int32, err error)

	// RecordPriceChange 写入价格变更流水。
	RecordPriceChange(ctx context.Context, productID, oldCents, newCents int64) error
	// ListHeatCandidates 取出需要重算热度的商品（近 N 天内的在售商品）。
	ListHeatCandidates(ctx context.Context, since time.Time, limit, offset int) ([]*Product, error)
	// UpdateHeatScores 批量写回热度分。
	UpdateHeatScores(ctx context.Context, scores map[int64]float64) error

	// ScanForIndex 按主键游标扫描商品，供 search-service 全量重建索引。
	//
	// 用游标而非 OFFSET：全量扫描动辄数十万行，
	// 深分页的 OFFSET 会让数据库逐行跳过前面所有记录，越翻越慢。
	ScanForIndex(ctx context.Context, afterID int64, limit int) ([]*IndexDoc, int64, error)
	// SearchFallback 降级检索，供 search-service 在搜索引擎不可用时调用。
	SearchFallback(ctx context.Context, q *FallbackQuery) ([]*Product, string, bool, error)
}

// IndexDoc 供检索索引使用的商品文档。
//
// 与 Product 分离：索引需要一级分类 ID（冗余以避免筛选时展开子分类），
// 也需要知道文档是否已被软删除以便从索引移除。
type IndexDoc struct {
	Product *Product
	// ParentCategoryID 一级分类 ID。
	ParentCategoryID int64
	// Deleted 是否已软删除。软删除的商品需要从索引中移除，
	// 因此扫描必须包含它们，而不是被 GORM 的软删除过滤掉。
	Deleted bool
}

// FallbackQuery 降级检索的查询条件。
type FallbackQuery struct {
	Keyword       string
	MinPriceCents int64
	MaxPriceCents int64
	Conditions    []uint8
	CategoryIDs   []int64
	CampusIDs     []int64
	OnlyAvailable bool
	// Sort 仅支持 latest / price_asc / price_desc。
	Sort   string
	Cursor string
	Limit  int
}

// Category 分类。
type Category struct {
	ID       int64
	ParentID int64
	Name     string
	IconKey  string
	Sort     int32
	Enabled  bool
	Children []*Category
}

// POI 校园面交点。
type POI struct {
	ID          int64
	CampusID    int64
	Name        string
	Type        uint8
	Lat         float64
	Lng         float64
	Recommended bool
}

// CatalogRepo 分类与 POI 字典仓储。
type CatalogRepo interface {
	ListCategories(ctx context.Context, enabledOnly bool) ([]*Category, error)
	GetCategory(ctx context.Context, id int64) (*Category, error)
	ListPOIs(ctx context.Context, campusID int64, types []uint8) ([]*POI, error)
	GetPOI(ctx context.Context, id int64) (*POI, error)
}

// FavoriteRepo 收藏仓储。
type FavoriteRepo interface {
	// Add 收藏。返回 false 表示已收藏过（幂等）。
	Add(ctx context.Context, userID, productID int64) (bool, error)
	// Remove 取消收藏。返回 false 表示原本未收藏。
	Remove(ctx context.Context, userID, productID int64) (bool, error)
	// Exists 判断是否已收藏。
	Exists(ctx context.Context, userID, productID int64) (bool, error)
	// ExistsBatch 批量判断，用于列表页一次性回填 favorited 标记。
	ExistsBatch(ctx context.Context, userID int64, productIDs []int64) (map[int64]bool, error)
	// ListByUser 我的收藏，返回商品 ID。
	ListByUser(ctx context.Context, userID int64, cursor string, limit int) ([]int64, string, bool, error)
	// ListUserIDsByProduct 收藏了某商品的用户，供降价提醒扇出。
	ListUserIDsByProduct(ctx context.Context, productID int64, cursor string, limit int) ([]int64, string, bool, error)
}

// WantedPost 求购帖。
type WantedPost struct {
	ID             int64
	UserID         int64
	Title          string
	Description    string
	BudgetMaxCents int64
	CategoryID     int64
	SchoolID       int64
	CampusID       int64
	Status         uint8
	ViewCount      int64
	ExpireAt       time.Time
	CreatedAt      time.Time
}

// WantedRepo 求购帖仓储。
type WantedRepo interface {
	Create(ctx context.Context, w *WantedPost) (*WantedPost, error)
	GetByID(ctx context.Context, id int64) (*WantedPost, error)
	UpdateStatus(ctx context.Context, id int64, fromStatus, toStatus uint8) (bool, error)
	IncrViewCount(ctx context.Context, id int64, delta int64) error
	List(ctx context.Context, categoryID, campusID int64, keyword string,
		cursor string, limit int) ([]*WantedPost, string, bool, error)
	ListByUser(ctx context.Context, userID int64, cursor string, limit int) ([]*WantedPost, string, bool, error)
	// ExpireOverdue 把过期的求购帖置为 EXPIRED，返回处理条数。
	ExpireOverdue(ctx context.Context, limit int) (int, error)
}

// SellerInfo 卖家的准入信息，来自 user-service。
type SellerInfo struct {
	UserID int64
	// CampusVerified 是否已通过校园认证。未认证不可发布。
	CampusVerified bool
	SchoolID       int64
	CampusID       int64
	Status         uint8
}

// UserGateway 对 user-service 的依赖。
//
// 定义成接口而非直接用 gRPC client：biz 不该认识 protobuf 类型，
// 且单测需要能在没有 user-service 的情况下运行。
type UserGateway interface {
	// GetSellerInfo 查询发布者的准入信息。
	GetSellerInfo(ctx context.Context, userID int64) (*SellerInfo, error)
}

// ModerationGateway 对 review-service 的依赖。
type ModerationGateway interface {
	// SubmitProduct 提交商品送审。
	//
	// 返回 immediate 为 true 表示同步审核已得出结论（无命中或命中 BLOCK），
	// 此时 passed 有效；为 false 表示转入人工审核，结论稍后经 MQ 回写。
	SubmitProduct(ctx context.Context, productID int64, title, description string) (immediate, passed bool, reason string, hitWords []string, err error)
}

// EventPublisher 领域事件发布。
type EventPublisher interface {
	Publish(ctx context.Context, topic string, payload []byte) error
}

// 本服务发出的事件主题。
const (
	// TopicProductCreated 商品发布（尚未过审）。
	TopicProductCreated = "product.created"
	// TopicProductUpdated 商品信息变更，search-service 据此更新索引。
	TopicProductUpdated = "product.updated"
	// TopicProductStatusChanged 状态变更。
	TopicProductStatusChanged = "product.status_changed"
	// TopicProductDeleted 商品删除，search-service 据此移除索引。
	TopicProductDeleted = "product.deleted"
	// TopicPriceDropped 降价，notify-service 据此向收藏用户推送。
	TopicPriceDropped = "product.price_dropped"
	// TopicWantedCreated 求购帖发布。
	TopicWantedCreated = "product.wanted_created"
)
