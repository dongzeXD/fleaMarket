package biz

import (
	"context"
	"time"

	"github.com/fleamarket/app/search/internal/engine"
)

// 领域对象与端口声明。
//
// search-service 不拥有任何数据库表：索引在 Meilisearch，
// 权威数据在 product-service。因此本服务的「仓储」全是对外部的网关。

// ProductBrief 检索结果中的商品摘要。
//
// 字段与 product.v1.ProductBrief 对齐，但在 biz 层重新声明，
// 避免领域逻辑直接依赖 protobuf 生成的类型。
type ProductBrief struct {
	ProductID     int64
	SellerID      int64
	Title         string
	PriceCents    int64
	Condition     uint8
	Status        uint8
	CoverKey      string
	SchoolID      int64
	CampusID      int64
	FavoriteCount int32
	CreatedAt     int64
}

// SearchFilter 检索筛选条件。
type SearchFilter struct {
	MinPriceCents int64
	MaxPriceCents int64
	Conditions    []uint8
	CategoryIDs   []int64
	CampusIDs     []int64
	// OnlyAvailable 仅看在售（排除已预订）。
	OnlyAvailable bool
}

// Sort 排序方式。
type Sort string

const (
	SortLatest    Sort = "latest"
	SortHot       Sort = "hot"
	SortDistance  Sort = "distance"
	SortPriceAsc  Sort = "price_asc"
	SortPriceDesc Sort = "price_desc"
)

// SearchResult 检索结果。
type SearchResult struct {
	Items          []*ProductBrief
	NextCursor     string
	HasMore        bool
	EstimatedTotal int64
	// Degraded 是否处于降级状态（搜索引擎不可用，回落数据库模糊匹配）。
	Degraded bool
}

// FacetResult 分面统计。
type FacetResult struct {
	Categories map[int64]int64
	Conditions map[int32]int64
	Campuses   map[int64]int64
	Degraded   bool
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
	// Sort 只支持 latest / price_asc / price_desc（见 product.proto 的说明）。
	Sort   string
	Cursor string
	Limit  int
}

// FallbackResult 降级检索结果。
type FallbackResult struct {
	Items      []*ProductBrief
	NextCursor string
	HasMore    bool
}

// ProductGateway 对 product-service 的依赖。
type ProductGateway interface {
	// ScanForIndex 按主键游标扫描商品，用于全量重建索引。
	ScanForIndex(ctx context.Context, afterID int64, limit int) ([]engine.Document, int64, error)
	// SearchFallback 降级检索。搜索引擎不可用时使用。
	SearchFallback(ctx context.Context, q FallbackQuery) (*FallbackResult, error)
	// BatchGetBrief 按 ID 批量取商品摘要。
	//
	// 用于校正索引与权威数据的偏差：索引是异步更新的，
	// 可能返回已下架商品。列表展示的状态与价格以此为准。
	BatchGetBrief(ctx context.Context, ids []int64) (map[int64]*ProductBrief, error)
}

// IndexSyncState 索引同步状态。
type IndexSyncState interface {
	// MarkSynced 记录最近一次成功同步的时间。
	MarkSynced(ctx context.Context, at time.Time) error
	// LastSynced 读取最近同步时间。零值表示从未同步。
	LastSynced(ctx context.Context) (time.Time, error)
	// DeadLetterCount 待处理的死信数量。
	DeadLetterCount(ctx context.Context) (int64, error)
}

// 消费的事件主题。与 product-service 发布的保持一致。
const (
	TopicProductCreated       = "product.created"
	TopicProductUpdated       = "product.updated"
	TopicProductStatusChanged = "product.status_changed"
	TopicProductDeleted       = "product.deleted"
)

// 索引中保留的商品状态。
//
// 只索引对外可见的状态：待审核与驳回的商品不该出现在任何搜索结果里。
// 已售出仍索引，因为「看看别人卖过什么价」对买家有参考价值。
var indexedStatuses = []uint8{
	statusOnSale, statusReserved, statusSold,
}

// 商品状态。与 product-service 的定义同值。
const (
	statusPendingReview uint8 = 1
	statusOnSale        uint8 = 2
	statusReserved      uint8 = 3
	statusSold          uint8 = 4
	statusOffline       uint8 = 5
	statusRejected      uint8 = 6
)

// shouldIndex 判断该状态的商品是否应留在索引中。
func shouldIndex(status uint8) bool {
	for _, s := range indexedStatuses {
		if s == status {
			return true
		}
	}
	return false
}
