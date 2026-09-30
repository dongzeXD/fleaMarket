package biz

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"github.com/fleamarket/app/search/internal/engine"

	"github.com/fleamarket/pkg/errs"
	"github.com/fleamarket/pkg/paging"
)

// SearchUsecase 检索用例。
type SearchUsecase struct {
	eng      engine.Engine
	products ProductGateway
	state    IndexSyncState
	log      *slog.Logger
}

// NewSearchUsecase 构造用例。
func NewSearchUsecase(eng engine.Engine, products ProductGateway,
	state IndexSyncState, logger *slog.Logger) *SearchUsecase {

	return &SearchUsecase{eng: eng, products: products, state: state,
		log: logger.With(slog.String("usecase", "search"))}
}

// FeedInput 信息流请求。
type FeedInput struct {
	Sort       Sort
	CategoryID int64
	// CampusID 观察者所在校区。为 0 时不做同校优先，退化为单路查询。
	CampusID int64
	// SchoolID 观察者所在学校。由网关从令牌中注入。
	SchoolID int64
	// ViewerID 观察者，用于在信息流中排除自己发布的商品。
	ViewerID int64
	Cursor   string
	Limit    int
}

// Feed 首页信息流。
func (uc *SearchUsecase) Feed(ctx context.Context, in *FeedInput) (*SearchResult, error) {
	filter := SearchFilter{}
	if in.CategoryID > 0 {
		filter.CategoryIDs = []int64{in.CategoryID}
	}
	return uc.search(ctx, "", filter, in.Sort, in.CampusID, in.SchoolID,
		in.ViewerID, in.Cursor, in.Limit)
}

// SearchInput 关键词搜索请求。
type SearchInput struct {
	Keyword  string
	Filter   SearchFilter
	Sort     Sort
	CampusID int64
	SchoolID int64
	ViewerID int64
	Cursor   string
	Limit    int
}

// Search 关键词搜索 + 筛选。
func (uc *SearchUsecase) Search(ctx context.Context, in *SearchInput) (*SearchResult, error) {
	return uc.search(ctx, in.Keyword, in.Filter, in.Sort, in.CampusID, in.SchoolID,
		in.ViewerID, in.Cursor, in.Limit)
}

// search 是 Feed 与 Search 的公共实现。
//
// 分路条件：
//   - 用户显式指定了校区筛选时不分路——他已经表达了「只看某校区」的意图，
//     此时再做同校优先是画蛇添足；
//   - 按价格排序时不分路——价格排序的语义是全局有序，
//     分路交错会产生「价格忽高忽低」的错乱列表。
func (uc *SearchUsecase) search(ctx context.Context, keyword string, filter SearchFilter,
	sort Sort, campusID, schoolID, viewerID int64, cursor string, limit int) (*SearchResult, error) {

	limit = paging.NormalizeLimit(limit)

	if !uc.shouldSplitLanes(filter, sort, campusID) {
		return uc.searchSingleLane(ctx, keyword, filter, sort, viewerID, cursor, limit)
	}
	return uc.searchWithLanes(ctx, keyword, filter, sort, campusID, schoolID,
		viewerID, cursor, limit)
}

func (uc *SearchUsecase) shouldSplitLanes(filter SearchFilter, sort Sort, campusID int64) bool {
	if campusID <= 0 {
		return false
	}
	if len(filter.CampusIDs) > 0 {
		return false
	}
	switch sort {
	case SortPriceAsc, SortPriceDesc:
		return false
	default:
		return true
	}
}

// searchSingleLane 单路查询，用偏移量作游标。
func (uc *SearchUsecase) searchSingleLane(ctx context.Context, keyword string,
	filter SearchFilter, sort Sort, viewerID int64, cursor string, limit int) (*SearchResult, error) {

	c, err := decodeLaneCursor(cursor)
	if err != nil {
		return nil, errs.InvalidArgument("分页游标无效")
	}
	offset := c.offsets[0]

	q := engine.Query{
		Keyword: keyword,
		Filter:  toEngineFilter(filter, nil, nil, viewerID),
		Sort:    toEngineSort(sort),
		Offset:  offset,
		// 多查一条判断是否还有下一页。
		Limit: limit + 1,
	}

	res, err := uc.eng.Search(ctx, q)
	if err != nil {
		if errors.Is(err, engine.ErrUnavailable) {
			return uc.fallback(ctx, keyword, filter, sort, cursor, limit)
		}
		return nil, errs.Internal("搜索失败")
	}

	items := toBriefs(res.Documents)
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	items = uc.reconcile(ctx, items)

	next := ""
	if hasMore {
		nc := laneCursor{}
		nc.offsets[0] = offset + limit
		next = nc.encode()
	}
	return &SearchResult{
		Items:          items,
		NextCursor:     next,
		HasMore:        hasMore,
		EstimatedTotal: res.EstimatedTotal,
	}, nil
}

// searchWithLanes 三路查询后按 6:3:1 交错。
func (uc *SearchUsecase) searchWithLanes(ctx context.Context, keyword string,
	filter SearchFilter, sort Sort, campusID, schoolID, viewerID int64,
	cursor string, limit int) (*SearchResult, error) {

	c, err := decodeLaneCursor(cursor)
	if err != nil {
		return nil, errs.InvalidArgument("分页游标无效")
	}
	quota := allocateQuota(limit)

	// 距离排序在分路模式下退化为时间排序：
	// 「距离」已经由分路本身表达（同校区在前），
	// 引擎层面无需也无法再对距离排序。
	engSort := toEngineSort(sort)
	if sort == SortDistance {
		engSort = engine.SortLatest
	}

	var lanes [laneCount][]*ProductBrief
	var estimated int64

	for lane := 0; lane < laneCount; lane++ {
		f := uc.laneFilter(filter, lane, campusID, schoolID, viewerID)
		// 每路多取一些：回填机制可能从某一路多拿，
		// 只取配额数会让回填无料可用。
		laneLimit := quota[lane] + limit
		res, err := uc.eng.Search(ctx, engine.Query{
			Keyword: keyword,
			Filter:  f,
			Sort:    engSort,
			Offset:  c.offsets[lane],
			Limit:   laneLimit,
		})
		if err != nil {
			if errors.Is(err, engine.ErrUnavailable) {
				return uc.fallback(ctx, keyword, filter, sort, cursor, limit)
			}
			// 单路失败（如该校区无索引数据导致的异常）不影响其他路，
			// 记录后继续，避免一路故障让整个信息流不可用。
			uc.log.WarnContext(ctx, "分路检索失败",
				slog.Int("lane", lane), slog.Any("error", err))
			continue
		}
		lanes[lane] = toBriefs(res.Documents)
		if lane == laneSameCampus {
			estimated = res.EstimatedTotal
		}
	}

	merged, consumed := mergeLanes(lanes, limit)
	merged = dedupe(merged)
	merged = uc.reconcile(ctx, merged)

	// 任一路还有剩余即认为有下一页。
	hasMore := false
	for lane := 0; lane < laneCount; lane++ {
		if consumed[lane] < len(lanes[lane]) {
			hasMore = true
			break
		}
	}

	next := ""
	if hasMore {
		nc := laneCursor{}
		for lane := 0; lane < laneCount; lane++ {
			nc.offsets[lane] = c.offsets[lane] + consumed[lane]
		}
		next = nc.encode()
	}

	return &SearchResult{
		Items:          merged,
		NextCursor:     next,
		HasMore:        hasMore,
		EstimatedTotal: estimated,
	}, nil
}

// laneFilter 构造某一路的过滤条件。
//
// 三路互斥：同校区 / 同校但排除本校区 / 排除本校。
// Meilisearch 无法表达「NOT IN」与「!=」的组合时，用 school_id 区分即可，
// 因为「同校异校区」与「其他学校」在 school_id 上天然互斥。
func (uc *SearchUsecase) laneFilter(base SearchFilter, lane int,
	campusID, schoolID, viewerID int64) engine.Filter {

	switch lane {
	case laneSameCampus:
		return toEngineFilter(base, []int64{campusID}, nil, viewerID)
	case laneSameSchool:
		// 同校异校区：限定 school 后由调用方在结果中排除本校区。
		// 这里通过 excludeCampus 参数实现。
		f := toEngineFilter(base, nil, []int64{schoolID}, viewerID)
		f.ExcludeCampusID = campusID
		return f
	default:
		f := toEngineFilter(base, nil, nil, viewerID)
		f.ExcludeSchoolID = schoolID
		return f
	}
}

// reconcile 用权威数据校正索引结果。
//
// 索引是异步更新的，可能返回已下架或价格已变的商品。
// 列表页展示的状态与价格必须以 product-service 为准，
// 否则用户会点进一个「已下架」的商品，或看到与详情页不一致的价格。
//
// 失败时返回原结果而非报错：索引数据虽可能略旧，但比整页失败可用。
func (uc *SearchUsecase) reconcile(ctx context.Context, items []*ProductBrief) []*ProductBrief {
	if len(items) == 0 {
		return items
	}
	ids := make([]int64, 0, len(items))
	for _, it := range items {
		ids = append(ids, it.ProductID)
	}
	authoritative, err := uc.products.BatchGetBrief(ctx, ids)
	if err != nil {
		uc.log.WarnContext(ctx, "校正索引结果失败，返回索引原始数据",
			slog.Any("error", err))
		return items
	}

	out := make([]*ProductBrief, 0, len(items))
	for _, it := range items {
		fresh, ok := authoritative[it.ProductID]
		if !ok {
			// 权威数据里查不到，说明已被删除，索引尚未同步。丢弃。
			continue
		}
		if !shouldIndex(fresh.Status) {
			// 已下架或被驳回，不该出现在结果里。
			continue
		}
		out = append(out, fresh)
	}
	return out
}

// fallback 搜索引擎不可用时回落数据库模糊匹配。
func (uc *SearchUsecase) fallback(ctx context.Context, keyword string, filter SearchFilter,
	sort Sort, cursor string, limit int) (*SearchResult, error) {

	uc.log.WarnContext(ctx, "搜索引擎不可用，降级为数据库模糊匹配")

	// 热度与距离排序在降级路径不支持（见 product.proto 的说明），
	// 统一退化为时间排序。前端会因 degraded 标记而提示结果质量下降。
	fbSort := "latest"
	switch sort {
	case SortPriceAsc:
		fbSort = "price_asc"
	case SortPriceDesc:
		fbSort = "price_desc"
	}

	res, err := uc.products.SearchFallback(ctx, FallbackQuery{
		Keyword:       keyword,
		MinPriceCents: filter.MinPriceCents,
		MaxPriceCents: filter.MaxPriceCents,
		Conditions:    filter.Conditions,
		CategoryIDs:   filter.CategoryIDs,
		CampusIDs:     filter.CampusIDs,
		OnlyAvailable: filter.OnlyAvailable,
		Sort:          fbSort,
		Cursor:        cursor,
		Limit:         limit,
	})
	if err != nil {
		// 降级路径也失败，说明 product-service 或数据库同样有问题。
		return nil, errs.Unavailable("搜索服务暂时不可用，请稍后重试")
	}

	return &SearchResult{
		Items:      res.Items,
		NextCursor: res.NextCursor,
		HasMore:    res.HasMore,
		Degraded:   true,
	}, nil
}

// Facets 分面统计。
func (uc *SearchUsecase) Facets(ctx context.Context, keyword string,
	filter SearchFilter) (*FacetResult, error) {

	res, err := uc.eng.Facets(ctx, keyword, toEngineFilter(filter, nil, nil, 0))
	if err != nil {
		if errors.Is(err, engine.ErrUnavailable) {
			// 分面统计没有降级方案：用 SQL 做多维 GROUP BY 的代价太高，
			// 且它只是筛选面板上的数字，缺失不影响筛选功能本身。
			return &FacetResult{
				Categories: map[int64]int64{},
				Conditions: map[int32]int64{},
				Campuses:   map[int64]int64{},
				Degraded:   true,
			}, nil
		}
		return nil, errs.Internal("统计失败")
	}

	out := &FacetResult{
		Categories: parseInt64Counts(res.Categories),
		Conditions: parseInt32Counts(res.Conditions),
		Campuses:   parseInt64Counts(res.Campuses),
	}
	return out, nil
}

// HandleProductEvent 消费商品事件并更新索引。
//
// 幂等：Upsert 与 Delete 都是幂等操作，重复消费同一事件无副作用。
// 这是必需的——MQ 只保证至少一次投递。
func (uc *SearchUsecase) HandleProductEvent(ctx context.Context, topic string, payload []byte) error {
	var ev struct {
		ProductID int64 `json:"product_id"`
		Status    uint8 `json:"status"`
	}
	if err := json.Unmarshal(payload, &ev); err != nil {
		// 解析失败的消息重试也不会成功，直接确认掉以免堵住队列。
		// 返回 nil 使消费者 ACK；错误已记录在日志中。
		uc.log.ErrorContext(ctx, "事件解析失败，已跳过",
			slog.String("topic", topic), slog.String("payload", string(payload)))
		return nil
	}
	if ev.ProductID == 0 {
		return nil
	}

	if topic == TopicProductDeleted || !shouldIndex(ev.Status) {
		// 删除、下架、驳回都应从索引移除，而非留一个不可见的文档——
		// 留着会让分面统计的数字包含用户搜不到的商品。
		if err := uc.eng.Delete(ctx, ev.ProductID); err != nil {
			return err
		}
		uc.markSynced(ctx)
		return nil
	}

	// 事件载荷不含完整商品信息，回查权威数据后再索引。
	//
	// 为什么不把全部字段塞进事件：事件会变大且与商品表强耦合，
	// 商品加字段就要同步改事件结构与所有消费者。回查多一次 RPC，
	// 但让事件保持为「发生了什么」的轻量通知。
	docs, _, err := uc.products.ScanForIndex(ctx, ev.ProductID-1, 1)
	if err != nil {
		return err
	}
	if len(docs) == 0 || docs[0].ProductID != ev.ProductID {
		// 查不到说明已被删除，按删除处理。
		if err := uc.eng.Delete(ctx, ev.ProductID); err != nil {
			return err
		}
		uc.markSynced(ctx)
		return nil
	}

	if err := uc.eng.Upsert(ctx, docs[0]); err != nil {
		return err
	}
	uc.markSynced(ctx)
	return nil
}

func (uc *SearchUsecase) markSynced(ctx context.Context) {
	if uc.state == nil {
		return
	}
	if err := uc.state.MarkSynced(ctx, time.Now()); err != nil {
		uc.log.WarnContext(ctx, "记录同步时间失败", slog.Any("error", err))
	}
}

// Reindex 全量重建索引。
func (uc *SearchUsecase) Reindex(ctx context.Context, rebuild bool) (int64, time.Duration, error) {
	start := time.Now()

	if !rebuild {
		// 增量补齐：逐批扫描并 Upsert，不切换索引。
		var total int64
		afterID := int64(0)
		for {
			docs, next, err := uc.products.ScanForIndex(ctx, afterID, 500)
			if err != nil {
				return total, time.Since(start), errs.FromDataError(err, "")
			}
			if len(docs) == 0 {
				break
			}
			if err := uc.eng.Upsert(ctx, docs...); err != nil {
				return total, time.Since(start), errs.Internal("写入索引失败")
			}
			total += int64(len(docs))
			if next == 0 {
				break
			}
			afterID = next
		}
		uc.markSynced(ctx)
		return total, time.Since(start), nil
	}

	// 全量重建：在临时索引上构建后原子切换，重建期间搜索不中断。
	total, err := uc.eng.SwapForRebuild(ctx, func(yield func(engine.Document) error) error {
		afterID := int64(0)
		for {
			docs, next, err := uc.products.ScanForIndex(ctx, afterID, 500)
			if err != nil {
				return err
			}
			if len(docs) == 0 {
				return nil
			}
			for _, d := range docs {
				if err := yield(d); err != nil {
					return err
				}
			}
			if next == 0 {
				return nil
			}
			afterID = next
		}
	})
	if err != nil {
		return total, time.Since(start), errs.Internal("重建索引失败")
	}
	uc.markSynced(ctx)
	return total, time.Since(start), nil
}

// IndexStatus 索引健康状态。
type IndexStatus struct {
	Healthy         bool
	DocumentCount   int64
	LastSyncedAt    time.Time
	DeadLetterCount int64
}

// Status 查询索引状态。
func (uc *SearchUsecase) Status(ctx context.Context) (*IndexStatus, error) {
	out := &IndexStatus{}

	if err := uc.eng.Health(ctx); err == nil {
		out.Healthy = true
		if st, err := uc.eng.Stats(ctx); err == nil {
			out.DocumentCount = st.DocumentCount
		}
	}
	if uc.state != nil {
		if at, err := uc.state.LastSynced(ctx); err == nil {
			out.LastSyncedAt = at
		}
		if n, err := uc.state.DeadLetterCount(ctx); err == nil {
			out.DeadLetterCount = n
		}
	}
	return out, nil
}

// --- 转换辅助 ---

func toEngineFilter(f SearchFilter, campusIDs, schoolIDs []int64, excludeSeller int64) engine.Filter {
	out := engine.Filter{
		MinPriceCents:   f.MinPriceCents,
		MaxPriceCents:   f.MaxPriceCents,
		Conditions:      f.Conditions,
		CategoryIDs:     f.CategoryIDs,
		ExcludeSellerID: excludeSeller,
	}
	if len(campusIDs) > 0 {
		out.CampusIDs = campusIDs
	} else {
		out.CampusIDs = f.CampusIDs
	}
	out.SchoolIDs = schoolIDs

	if f.OnlyAvailable {
		out.Statuses = []uint8{statusOnSale}
	} else {
		out.Statuses = indexedStatuses
	}
	return out
}

func toEngineSort(s Sort) engine.Sort {
	switch s {
	case SortLatest:
		return engine.SortLatest
	case SortHot:
		return engine.SortHot
	case SortPriceAsc:
		return engine.SortPriceAsc
	case SortPriceDesc:
		return engine.SortPriceDesc
	case SortDistance:
		// 距离由分路表达，引擎层面按时间排。
		return engine.SortLatest
	default:
		return engine.SortLatest
	}
}

func toBriefs(docs []engine.Document) []*ProductBrief {
	out := make([]*ProductBrief, 0, len(docs))
	for _, d := range docs {
		out = append(out, &ProductBrief{
			ProductID:     d.ProductID,
			SellerID:      d.SellerID,
			Title:         d.Title,
			PriceCents:    d.PriceCents,
			Condition:     d.Condition,
			Status:        d.Status,
			CoverKey:      d.CoverKey,
			SchoolID:      d.SchoolID,
			CampusID:      d.CampusID,
			FavoriteCount: d.FavoriteCount,
			CreatedAt:     d.CreatedAt,
		})
	}
	return out
}

func parseInt64(s string) (int64, error) {
	return strconv.ParseInt(s, 10, 64)
}

func parseInt64Counts(m map[string]int64) map[int64]int64 {
	out := make(map[int64]int64, len(m))
	for k, v := range m {
		if n, err := parseInt64(k); err == nil {
			out[n] = v
		}
	}
	return out
}

func parseInt32Counts(m map[string]int64) map[int32]int64 {
	out := make(map[int32]int64, len(m))
	for k, v := range m {
		if n, err := parseInt64(k); err == nil {
			out[int32(n)] = v
		}
	}
	return out
}
