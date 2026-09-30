package service

import (
	"context"

	"github.com/fleamarket/app/product/internal/biz"

	productv1 "github.com/fleamarket/api/product/v1"
	"github.com/fleamarket/pkg/auth"
)

// CategoryService 实现分类与 POI 字典查询。
type CategoryService struct {
	productv1.UnimplementedCategoryServiceServer

	uc *biz.CatalogUsecase
}

// NewCategoryService 构造服务。
func NewCategoryService(uc *biz.CatalogUsecase) *CategoryService {
	return &CategoryService{uc: uc}
}

// ListCategories 查询分类树。公开接口。
//
// enabled_only 的默认值处理：proto 的 bool 零值是 false，
// 而对外接口应默认只返回启用的分类。因此这里把「未传」视为 true，
// 只有管理后台显式传 false 才能看到停用分类。
func (s *CategoryService) ListCategories(ctx context.Context,
	req *productv1.ListCategoriesRequest) (*productv1.ListCategoriesReply, error) {

	enabledOnly := true
	// 管理员可以查看全部分类；普通用户一律只看启用的。
	if id, ok := auth.FromContext(ctx); ok && id.IsAdmin() && !req.GetEnabledOnly() {
		enabledOnly = false
	}

	list, err := s.uc.ListCategories(ctx, enabledOnly)
	if err != nil {
		return nil, err
	}
	out := make([]*productv1.Category, 0, len(list))
	for _, c := range list {
		out = append(out, categoryToProto(c))
	}
	return &productv1.ListCategoriesReply{Categories: out}, nil
}

// ListPOIs 查询校区面交点。
func (s *CategoryService) ListPOIs(ctx context.Context,
	req *productv1.ListPOIsRequest) (*productv1.ListPOIsReply, error) {

	list, err := s.uc.ListPOIs(ctx, req.GetCampusId(), poiTypesFromProto(req.GetTypes()))
	if err != nil {
		return nil, err
	}
	out := make([]*productv1.POI, 0, len(list))
	for _, p := range list {
		out = append(out, poiToProto(p))
	}
	return &productv1.ListPOIsReply{Pois: out}, nil
}

// FavoriteService 实现收藏。
type FavoriteService struct {
	productv1.UnimplementedFavoriteServiceServer

	uc       *biz.FavoriteUsecase
	products *biz.ProductUsecase
}

// NewFavoriteService 构造服务。
func NewFavoriteService(uc *biz.FavoriteUsecase, products *biz.ProductUsecase) *FavoriteService {
	return &FavoriteService{uc: uc, products: products}
}

// AddFavorite 收藏商品。
func (s *FavoriteService) AddFavorite(ctx context.Context,
	req *productv1.AddFavoriteRequest) (*productv1.AddFavoriteReply, error) {

	uid, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}
	count, err := s.uc.Add(ctx, uid, req.GetProductId())
	if err != nil {
		return nil, err
	}
	return &productv1.AddFavoriteReply{FavoriteCount: count}, nil
}

// RemoveFavorite 取消收藏。
func (s *FavoriteService) RemoveFavorite(ctx context.Context,
	req *productv1.RemoveFavoriteRequest) (*productv1.RemoveFavoriteReply, error) {

	uid, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}
	count, err := s.uc.Remove(ctx, uid, req.GetProductId())
	if err != nil {
		return nil, err
	}
	return &productv1.RemoveFavoriteReply{FavoriteCount: count}, nil
}

// ListFavorites 我的收藏。
func (s *FavoriteService) ListFavorites(ctx context.Context,
	req *productv1.ListFavoritesRequest) (*productv1.ListFavoritesReply, error) {

	uid, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}
	cursor, limit := cursorRequest(req.GetPage())
	page, err := s.uc.ListMine(ctx, uid, cursor, limit)
	if err != nil {
		return nil, err
	}
	// 收藏列表里的商品必然都是已收藏的，直接标记为 true，
	// 无需再查一次收藏状态。
	marks := make(map[int64]bool, len(page.Items))
	for _, p := range page.Items {
		marks[p.ID] = true
	}
	return &productv1.ListFavoritesReply{
		Products: productBriefsToProto(page.Items, marks),
		Page:     cursorReplyOf(page.NextCursor, page.HasMore),
	}, nil
}

// ListFavoriteUserIDs 查询收藏了某商品的用户。内部接口。
func (s *FavoriteService) ListFavoriteUserIDs(ctx context.Context,
	req *productv1.ListFavoriteUserIDsRequest) (*productv1.ListFavoriteUserIDsReply, error) {

	ids, next, hasMore, err := s.uc.ListUserIDs(ctx, req.GetProductId(),
		req.GetCursor(), int(req.GetLimit()))
	if err != nil {
		return nil, err
	}
	return &productv1.ListFavoriteUserIDsReply{
		UserIds:    ids,
		NextCursor: next,
		HasMore:    hasMore,
	}, nil
}

// WantedService 实现求购帖。
type WantedService struct {
	productv1.UnimplementedWantedServiceServer

	uc *biz.WantedUsecase
}

// NewWantedService 构造服务。
func NewWantedService(uc *biz.WantedUsecase) *WantedService {
	return &WantedService{uc: uc}
}

// CreateWanted 发布求购帖。
func (s *WantedService) CreateWanted(ctx context.Context,
	req *productv1.CreateWantedRequest) (*productv1.WantedPost, error) {

	uid, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}
	w, err := s.uc.Create(ctx, uid, &biz.CreateWantedInput{
		Title:          req.GetTitle(),
		Description:    req.GetDescription(),
		BudgetMaxCents: req.GetBudgetMaxCents(),
		CategoryID:     req.GetCategoryId(),
		CampusID:       req.GetCampusId(),
		ValidDays:      req.GetValidDays(),
	})
	if err != nil {
		return nil, err
	}
	return wantedToProto(w), nil
}

// GetWanted 查看求购帖。
func (s *WantedService) GetWanted(ctx context.Context,
	req *productv1.GetWantedRequest) (*productv1.WantedPost, error) {

	w, err := s.uc.Get(ctx, req.GetWantedId(), auth.MustUserID(ctx))
	if err != nil {
		return nil, err
	}
	return wantedToProto(w), nil
}

// CloseWanted 关闭求购帖。
func (s *WantedService) CloseWanted(ctx context.Context,
	req *productv1.CloseWantedRequest) (*productv1.WantedPost, error) {

	uid, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}
	w, err := s.uc.Close(ctx, req.GetWantedId(), uid)
	if err != nil {
		return nil, err
	}
	return wantedToProto(w), nil
}

// ListWanted 浏览求购帖。
func (s *WantedService) ListWanted(ctx context.Context,
	req *productv1.ListWantedRequest) (*productv1.ListWantedReply, error) {

	cursor, limit := cursorRequest(req.GetPage())
	page, err := s.uc.List(ctx, req.GetCategoryId(), req.GetCampusId(),
		req.GetKeyword(), cursor, limit)
	if err != nil {
		return nil, err
	}
	return &productv1.ListWantedReply{
		Posts: wantedsToProto(page.Items),
		Page:  cursorReplyOf(page.NextCursor, page.HasMore),
	}, nil
}

// ListMyWanted 我的求购帖。
func (s *WantedService) ListMyWanted(ctx context.Context,
	req *productv1.ListMyWantedRequest) (*productv1.ListWantedReply, error) {

	uid, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}
	cursor, limit := cursorRequest(req.GetPage())
	page, err := s.uc.ListMine(ctx, uid, cursor, limit)
	if err != nil {
		return nil, err
	}
	return &productv1.ListWantedReply{
		Posts: wantedsToProto(page.Items),
		Page:  cursorReplyOf(page.NextCursor, page.HasMore),
	}, nil
}

var (
	_ productv1.CategoryServiceServer = (*CategoryService)(nil)
	_ productv1.FavoriteServiceServer = (*FavoriteService)(nil)
	_ productv1.WantedServiceServer   = (*WantedService)(nil)
)
