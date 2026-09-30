// Package engine 定义检索引擎抽象与 Meilisearch 实现。
//
// 抽象成接口的理由（doc.md §2.2）：校园二手的商品量级在十万级，
// Meilisearch 的单二进制部署与内置中文分词恰好匹配；
// 但若日后需要复杂聚合，替换为 Elasticsearch 只需新增一个实现，
// biz 层与 service 层不受影响。
package engine

import (
	"context"
	"errors"
	"time"
)

// ErrUnavailable 引擎不可用。
//
// 调用方据此决定是否降级，而不是靠判断具体的网络错误类型——
// 后者会让降级逻辑与 HTTP 客户端的实现细节耦合。
var ErrUnavailable = errors.New("engine: search engine unavailable")

// Document 索引中的一篇商品文档。
type Document struct {
	ProductID int64 `json:"id"`
	SellerID  int64 `json:"seller_id"`

	Title       string `json:"title"`
	Description string `json:"description"`

	PriceCents int64 `json:"price_cents"`
	Condition  uint8 `json:"condition"`
	Status     uint8 `json:"status"`

	CategoryID int64 `json:"category_id"`
	// ParentCategoryID 一级分类。冗余进索引，
	// 使按一级分类筛选无需先展开其下全部二级分类再拼 IN 条件。
	ParentCategoryID int64 `json:"parent_category_id"`

	SchoolID int64 `json:"school_id"`
	CampusID int64 `json:"campus_id"`

	CoverKey      string  `json:"cover_key"`
	HeatScore     float64 `json:"heat_score"`
	FavoriteCount int32   `json:"favorite_count"`
	CreatedAt     int64   `json:"created_at"`
}

// Filter 检索过滤条件。
type Filter struct {
	MinPriceCents int64
	// MaxPriceCents 为 0 表示不限。
	MaxPriceCents int64
	Conditions    []uint8
	CategoryIDs   []int64
	CampusIDs     []int64
	SchoolIDs     []int64
	// Statuses 限定商品状态。为空时由调用方决定默认值。
	Statuses []uint8
	// ExcludeSellerID 排除某卖家的商品，用于信息流中隐藏自己的发布。
	ExcludeSellerID int64
	// ExcludeCampusID 排除某校区。用于「同校但不同校区」这一路：
	// 限定 school_id 后再排掉本校区，两路才互斥。
	ExcludeCampusID int64
	// ExcludeSchoolID 排除某学校。用于「其他学校」这一路。
	ExcludeSchoolID int64
}

// Sort 排序方式。
type Sort string

const (
	SortLatest    Sort = "latest"
	SortHot       Sort = "hot"
	SortPriceAsc  Sort = "price_asc"
	SortPriceDesc Sort = "price_desc"
)

// Query 一次检索请求。
type Query struct {
	Keyword string
	Filter  Filter
	Sort    Sort
	// Offset/Limit 采用偏移分页。
	//
	// 这里不用游标：搜索引擎的排序键（相关度）无法作为稳定游标，
	// 且搜索场景用户很少翻到深页。对外的游标由 biz 层用偏移量编码而成。
	Offset int
	Limit  int
}

// Result 检索结果。
type Result struct {
	Documents []Document
	// EstimatedTotal 命中总数的估算值。
	//
	// Meilisearch 返回的是估算而非精确计数——精确计数需要遍历全部命中，
	// 代价与结果集大小成正比。用于展示「约 N 件」，不可用于分页计算。
	EstimatedTotal int64
}

// FacetResult 分面统计结果。
type FacetResult struct {
	// 各维度的取值到命中数量的映射。key 为字段值的字符串形式。
	Categories map[string]int64
	Conditions map[string]int64
	Campuses   map[string]int64
}

// Stats 索引状态。
type Stats struct {
	DocumentCount int64
	// Indexing 是否正在处理索引任务。
	Indexing bool
}

// Engine 检索引擎。
type Engine interface {
	// EnsureIndex 幂等地创建索引并配置可筛选/可排序字段。
	//
	// 在服务启动时调用：索引配置是代码的一部分，
	// 靠人工在控制台配置会让新环境的部署遗漏配置而行为不一致。
	EnsureIndex(ctx context.Context) error

	// Upsert 写入或更新文档。
	Upsert(ctx context.Context, docs ...Document) error
	// Delete 按 ID 删除文档。
	Delete(ctx context.Context, productIDs ...int64) error

	// Search 执行检索。
	Search(ctx context.Context, q Query) (*Result, error)
	// Facets 分面统计。
	Facets(ctx context.Context, keyword string, f Filter) (*FacetResult, error)

	// Stats 查询索引状态。
	Stats(ctx context.Context) (*Stats, error)
	// Health 探活。不可用时返回 ErrUnavailable。
	Health(ctx context.Context) error

	// SwapForRebuild 以临时索引全量重建后原子切换。
	//
	// 为什么需要它：清空现有索引再逐批写入，会让重建期间（可能数分钟）
	// 的搜索返回空结果。先在临时索引上构建、完成后交换别名，
	// 使用户始终能搜到内容（doc.md §9.3）。
	SwapForRebuild(ctx context.Context, feed func(yield func(Document) error) error) (int64, error)
}

// Timeouts 引擎调用的超时配置。
type Timeouts struct {
	// Search 检索超时。必须短：搜索在用户等待的关键路径上，
	// 宁可降级返回有损结果，也不能让页面转圈。
	Search time.Duration
	// Index 写索引超时。可稍长，它在后台消费链路上。
	Index time.Duration
	// Health 探活超时。
	Health time.Duration
}

func (t *Timeouts) setDefaults() {
	if t.Search <= 0 {
		t.Search = 500 * time.Millisecond
	}
	if t.Index <= 0 {
		t.Index = 5 * time.Second
	}
	if t.Health <= 0 {
		t.Health = time.Second
	}
}
