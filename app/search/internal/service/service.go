// Package service 是 search-service 的传输适配层。
//
// 只做 DTO ↔ 领域对象转换与身份提取，不含业务规则。
package service

import (
	"context"

	"github.com/fleamarket/app/search/internal/biz"

	commonv1 "github.com/fleamarket/api/common/v1"
	productv1 "github.com/fleamarket/api/product/v1"
	searchv1 "github.com/fleamarket/api/search/v1"
	"github.com/fleamarket/pkg/auth"
	"github.com/fleamarket/pkg/paging"
	"github.com/google/wire"
)

// ProviderSet 服务层的 wire provider 集合。
var ProviderSet = wire.NewSet(NewSearchService)

// SearchService 实现 api/search/v1 的 SearchService。
type SearchService struct {
	searchv1.UnimplementedSearchServiceServer

	uc *biz.SearchUsecase
}

// NewSearchService 构造服务。
func NewSearchService(uc *biz.SearchUsecase) *SearchService {
	return &SearchService{uc: uc}
}

// Feed 首页信息流。
func (s *SearchService) Feed(ctx context.Context,
	req *searchv1.FeedRequest) (*searchv1.SearchReply, error) {

	campusID, schoolID := s.viewerCampus(ctx, req.GetCampusId())
	cursor, limit := cursorRequest(req.GetPage())

	res, err := s.uc.Feed(ctx, &biz.FeedInput{
		Sort:       sortFromProto(req.GetSort()),
		CategoryID: req.GetCategoryId(),
		CampusID:   campusID,
		SchoolID:   schoolID,
		ViewerID:   auth.MustUserID(ctx),
		Cursor:     cursor,
		Limit:      limit,
	})
	if err != nil {
		return nil, err
	}
	return toSearchReply(res), nil
}

// SearchProducts 关键词搜索 + 筛选。
func (s *SearchService) SearchProducts(ctx context.Context,
	req *searchv1.SearchProductsRequest) (*searchv1.SearchReply, error) {

	campusID, schoolID := s.viewerCampus(ctx, req.GetCampusId())
	cursor, limit := cursorRequest(req.GetPage())

	res, err := s.uc.Search(ctx, &biz.SearchInput{
		Keyword:  req.GetKeyword(),
		Filter:   filterFromProto(req.GetFilter()),
		Sort:     sortFromProto(req.GetSort()),
		CampusID: campusID,
		SchoolID: schoolID,
		ViewerID: auth.MustUserID(ctx),
		Cursor:   cursor,
		Limit:    limit,
	})
	if err != nil {
		return nil, err
	}
	return toSearchReply(res), nil
}

// Facets 分面统计。
func (s *SearchService) Facets(ctx context.Context,
	req *searchv1.FacetsRequest) (*searchv1.FacetsReply, error) {

	res, err := s.uc.Facets(ctx, req.GetKeyword(), filterFromProto(req.GetFilter()))
	if err != nil {
		return nil, err
	}
	return &searchv1.FacetsReply{
		CategoryCounts:  res.Categories,
		ConditionCounts: res.Conditions,
		CampusCounts:    res.Campuses,
		Degraded:        res.Degraded,
	}, nil
}

// Reindex 全量重建索引。管理接口。
func (s *SearchService) Reindex(ctx context.Context,
	req *searchv1.ReindexRequest) (*searchv1.ReindexReply, error) {

	// 该接口无 http 注解，网关不会转发，但仍显式校验管理员身份：
	// 重建会产生大量下游查询，不能因为「网关不转发」就省掉校验
	// （doc.md §13 的双重校验原则）。
	if id, ok := auth.FromContext(ctx); ok && !id.IsAdmin() {
		return nil, auth.ErrPermissionDenied("需要管理员权限")
	}

	count, elapsed, err := s.uc.Reindex(ctx, req.GetRebuild())
	if err != nil {
		return nil, err
	}
	return &searchv1.ReindexReply{
		IndexedCount: count,
		ElapsedMs:    elapsed.Milliseconds(),
	}, nil
}

// IndexStatus 索引健康状态。
func (s *SearchService) IndexStatus(ctx context.Context,
	_ *searchv1.IndexStatusRequest) (*searchv1.IndexStatusReply, error) {

	st, err := s.uc.Status(ctx)
	if err != nil {
		return nil, err
	}
	out := &searchv1.IndexStatusReply{
		Healthy:         st.Healthy,
		DocumentCount:   st.DocumentCount,
		DeadLetterCount: st.DeadLetterCount,
	}
	if !st.LastSyncedAt.IsZero() {
		out.LastSyncedAt = st.LastSyncedAt.Unix()
	}
	return out, nil
}

// viewerCampus 决定用于同校优先的校区与学校。
//
// 优先取令牌中的认证信息，其次才是请求参数。
//
// 为什么以令牌为准：同校优先的依据应是「用户实际归属的校区」。
// 若完全信任请求参数，任何人都能伪造 campus_id 去窥探其他校区的信息流。
// 未登录用户没有令牌，此时接受参数——他们本就只能看公开内容。
func (s *SearchService) viewerCampus(ctx context.Context, fromRequest int64) (int64, int64) {
	id, ok := auth.FromContext(ctx)
	if !ok {
		return fromRequest, 0
	}
	campusID := parseUint(id.Extra["campus-id"])
	schoolID := parseUint(id.Extra["school-id"])
	if campusID > 0 {
		return campusID, schoolID
	}
	// 已登录但未完成校园认证，令牌里没有校区，按请求参数处理。
	return fromRequest, schoolID
}

// --- 转换 ---

func sortFromProto(s searchv1.FeedSort) biz.Sort {
	switch s {
	case searchv1.FeedSort_FEED_SORT_LATEST:
		return biz.SortLatest
	case searchv1.FeedSort_FEED_SORT_HOT:
		return biz.SortHot
	case searchv1.FeedSort_FEED_SORT_DISTANCE:
		return biz.SortDistance
	case searchv1.FeedSort_FEED_SORT_PRICE_ASC:
		return biz.SortPriceAsc
	case searchv1.FeedSort_FEED_SORT_PRICE_DESC:
		return biz.SortPriceDesc
	default:
		// 未指定时按热度：新用户打开首页最想看的是「大家都在看什么」，
		// 而非某个恰好刚发布的冷门商品。
		return biz.SortHot
	}
}

func filterFromProto(f *searchv1.SearchFilter) biz.SearchFilter {
	if f == nil {
		return biz.SearchFilter{}
	}
	conditions := make([]uint8, 0, len(f.GetConditions()))
	for _, c := range f.GetConditions() {
		if c != productv1.Condition_CONDITION_UNSPECIFIED {
			conditions = append(conditions, uint8(c))
		}
	}
	return biz.SearchFilter{
		MinPriceCents: f.GetMinPriceCents(),
		MaxPriceCents: f.GetMaxPriceCents(),
		Conditions:    conditions,
		CategoryIDs:   f.GetCategoryIds(),
		CampusIDs:     f.GetCampusIds(),
		OnlyAvailable: f.GetOnlyAvailable(),
	}
}

func toSearchReply(res *biz.SearchResult) *searchv1.SearchReply {
	return &searchv1.SearchReply{
		Products:       briefsToProto(res.Items),
		Page:           &commonv1.CursorReply{NextCursor: res.NextCursor, HasMore: res.HasMore},
		EstimatedTotal: res.EstimatedTotal,
		Degraded:       res.Degraded,
	}
}

func briefsToProto(items []*biz.ProductBrief) []*productv1.ProductBrief {
	out := make([]*productv1.ProductBrief, 0, len(items))
	for _, it := range items {
		if it == nil {
			continue
		}
		b := &productv1.ProductBrief{
			ProductId:     it.ProductID,
			SellerId:      it.SellerID,
			Title:         it.Title,
			PriceCents:    it.PriceCents,
			Condition:     productv1.Condition(it.Condition),
			Status:        productv1.ProductStatus(it.Status),
			FavoriteCount: it.FavoriteCount,
			CreatedAt:     it.CreatedAt,
		}
		if it.CoverKey != "" {
			b.Cover = &commonv1.ImageRef{ObjectKey: it.CoverKey}
		}
		if it.CampusID != 0 {
			b.Campus = &commonv1.CampusRef{
				CampusId: it.CampusID,
				School:   &commonv1.SchoolRef{SchoolId: it.SchoolID},
			}
		}
		out = append(out, b)
	}
	return out
}

func cursorRequest(r *commonv1.CursorRequest) (string, int) {
	if r == nil {
		return "", paging.DefaultLimit
	}
	return r.GetCursor(), int(r.GetLimit())
}

// parseUint 解析 metadata 中的数字串。
//
// 自行实现而非用 strconv：metadata 的值来自网关注入，
// 非法输入应静默视作 0（未知校区）而不是报错——
// 一个畸形的 metadata 不该让搜索接口失败。
func parseUint(s string) int64 {
	if s == "" {
		return 0
	}
	var n int64
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int64(c-'0')
		if n > 1<<62 {
			return 0
		}
	}
	return n
}

var _ searchv1.SearchServiceServer = (*SearchService)(nil)
