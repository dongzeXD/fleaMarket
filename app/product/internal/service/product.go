package service

import (
	"context"

	"github.com/fleamarket/app/product/internal/biz"

	productv1 "github.com/fleamarket/api/product/v1"
	"github.com/fleamarket/pkg/auth"
	"github.com/fleamarket/pkg/errs"
)

// ProductService 实现 api/product/v1 的 ProductService。
type ProductService struct {
	productv1.UnimplementedProductServiceServer

	uc  *biz.ProductUsecase
	fav *biz.FavoriteUsecase
}

// NewProductService 构造服务。
func NewProductService(uc *biz.ProductUsecase, fav *biz.FavoriteUsecase) *ProductService {
	return &ProductService{uc: uc, fav: fav}
}

// CreateImageUploadURL 申请图片上传凭证。
func (s *ProductService) CreateImageUploadURL(ctx context.Context,
	req *productv1.CreateImageUploadURLRequest) (*productv1.CreateImageUploadURLReply, error) {

	uid, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}
	ticket, err := s.uc.CreateImageUploadURL(ctx, uid, req.GetExtension(), req.GetSizeBytes())
	if err != nil {
		return nil, err
	}
	return &productv1.CreateImageUploadURLReply{
		UploadUrl:    ticket.UploadURL,
		ObjectKey:    ticket.ObjectKey,
		Headers:      ticket.Headers,
		ExpireAt:     ticket.ExpireAt.Unix(),
		MaxSizeBytes: ticket.MaxSize,
	}, nil
}

// CreateProduct 发布商品。
func (s *ProductService) CreateProduct(ctx context.Context,
	req *productv1.CreateProductRequest) (*productv1.Product, error) {

	uid, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}
	in := &biz.CreateProductInput{
		Title:              req.GetTitle(),
		Description:        req.GetDescription(),
		PriceCents:         req.GetPriceCents(),
		OriginalPriceCents: req.GetOriginalPriceCents(),
		Condition:          conditionFromProto(req.GetCondition()),
		CategoryID:         req.GetCategoryId(),
		CampusID:           req.GetCampusId(),
		ImageKeys:          req.GetImageKeys(),
	}
	if poi := req.GetPoiId(); poi != 0 {
		in.POIID = &poi
	}

	p, err := s.uc.Create(ctx, uid, in)
	if err != nil {
		return nil, err
	}
	return productToProto(p, false), nil
}

// GetProduct 查看商品详情。未登录可访问。
func (s *ProductService) GetProduct(ctx context.Context,
	req *productv1.GetProductRequest) (*productv1.Product, error) {

	viewerID := auth.MustUserID(ctx)
	p, err := s.uc.Get(ctx, req.GetProductId(), viewerID)
	if err != nil {
		return nil, err
	}
	marks := s.fav.MarkFavorited(ctx, viewerID, []*biz.Product{p})
	return productToProto(p, marks[p.ID]), nil
}

// UpdateProduct 修改商品。
func (s *ProductService) UpdateProduct(ctx context.Context,
	req *productv1.UpdateProductRequest) (*productv1.Product, error) {

	uid, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}

	u := &biz.ProductUpdate{
		Title:       req.Title,
		Description: req.Description,
		PriceCents:  req.PriceCents,
		CategoryID:  req.CategoryId,
		POIID:       req.PoiId,
		ImageKeys:   req.GetImageKeys(),
	}
	// 成色需要枚举转换，不能直接透传指针。
	if req.Condition != nil {
		c := conditionFromProto(req.GetCondition())
		if c == 0 {
			return nil, errs.InvalidArgument("成色取值非法")
		}
		u.Condition = &c
	}

	p, err := s.uc.Update(ctx, req.GetProductId(), uid, u)
	if err != nil {
		return nil, err
	}
	return productToProto(p, false), nil
}

// ChangeProductStatus 卖家变更商品状态。
func (s *ProductService) ChangeProductStatus(ctx context.Context,
	req *productv1.ChangeProductStatusRequest) (*productv1.Product, error) {

	uid, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}
	var event string
	switch req.GetEvent() {
	case productv1.ProductEvent_PRODUCT_EVENT_OFFLINE:
		event = "offline"
	case productv1.ProductEvent_PRODUCT_EVENT_REPUBLISH:
		event = "republish"
	default:
		return nil, errs.InvalidArgument("不支持的操作")
	}

	p, err := s.uc.ChangeStatus(ctx, req.GetProductId(), uid, event)
	if err != nil {
		return nil, err
	}
	return productToProto(p, false), nil
}

// DeleteProduct 删除商品。
func (s *ProductService) DeleteProduct(ctx context.Context,
	req *productv1.DeleteProductRequest) (*productv1.DeleteProductReply, error) {

	uid, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.uc.Delete(ctx, req.GetProductId(), uid); err != nil {
		return nil, err
	}
	return &productv1.DeleteProductReply{}, nil
}

// ListMyProducts 我发布的商品。
func (s *ProductService) ListMyProducts(ctx context.Context,
	req *productv1.ListMyProductsRequest) (*productv1.ListProductsReply, error) {

	uid, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}
	statuses := make([]uint8, 0, len(req.GetStatuses()))
	for _, s := range req.GetStatuses() {
		if v, ok := statusFromProto(s); ok {
			statuses = append(statuses, v)
		}
	}
	cursor, limit := cursorRequest(req.GetPage())

	items, next, hasMore, err := s.uc.ListMine(ctx, uid, statuses, cursor, limit)
	if err != nil {
		return nil, err
	}
	return &productv1.ListProductsReply{
		Products: productBriefsToProto(items, nil),
		Page:     cursorReplyOf(next, hasMore),
	}, nil
}

// ListUserProducts 某用户公开在售的商品。
func (s *ProductService) ListUserProducts(ctx context.Context,
	req *productv1.ListUserProductsRequest) (*productv1.ListProductsReply, error) {

	viewerID := auth.MustUserID(ctx)
	cursor, limit := cursorRequest(req.GetPage())

	items, next, hasMore, err := s.uc.ListPublicBySeller(ctx, req.GetSellerId(), cursor, limit)
	if err != nil {
		return nil, err
	}
	marks := s.fav.MarkFavorited(ctx, viewerID, items)
	return &productv1.ListProductsReply{
		Products: productBriefsToProto(items, marks),
		Page:     cursorReplyOf(next, hasMore),
	}, nil
}

// --- 内部接口 ---

// BatchGetProductBrief 批量获取商品摘要。
func (s *ProductService) BatchGetProductBrief(ctx context.Context,
	req *productv1.BatchGetProductBriefRequest) (*productv1.BatchGetProductBriefReply, error) {

	found, err := s.uc.BatchGet(ctx, req.GetProductIds())
	if err != nil {
		return nil, err
	}
	out := make(map[int64]*productv1.ProductBrief, len(found))
	for id, p := range found {
		out[id] = productBriefToProto(p, false)
	}
	return &productv1.BatchGetProductBriefReply{Products: out}, nil
}

// LockForOrder 为下单抢占商品。仅 trade-service 调用。
func (s *ProductService) LockForOrder(ctx context.Context,
	req *productv1.LockForOrderRequest) (*productv1.LockForOrderReply, error) {

	res, err := s.uc.LockForOrder(ctx, req.GetProductId(), req.GetOrderId(), req.GetBuyerId())
	if err != nil {
		return nil, err
	}
	return &productv1.LockForOrderReply{
		Locked:        res.Locked,
		CurrentStatus: statusToProto(res.CurrentStatus),
		PriceCents:    res.PriceCents,
		SellerId:      res.SellerID,
	}, nil
}

// ReleaseOrderLock 释放锁定。
func (s *ProductService) ReleaseOrderLock(ctx context.Context,
	req *productv1.ReleaseOrderLockRequest) (*productv1.ReleaseOrderLockReply, error) {

	released, err := s.uc.ReleaseOrderLock(ctx, req.GetProductId(), req.GetOrderId())
	if err != nil {
		return nil, err
	}
	return &productv1.ReleaseOrderLockReply{Released: released}, nil
}

// MarkSold 标记已售出。
func (s *ProductService) MarkSold(ctx context.Context,
	req *productv1.MarkSoldRequest) (*productv1.MarkSoldReply, error) {

	marked, err := s.uc.MarkSold(ctx, req.GetProductId(), req.GetOrderId())
	if err != nil {
		return nil, err
	}
	return &productv1.MarkSoldReply{Marked: marked}, nil
}

// ApplyModerationResult 回写审核结论。仅 review-service 调用。
func (s *ProductService) ApplyModerationResult(ctx context.Context,
	req *productv1.ApplyModerationResultRequest) (*productv1.ApplyModerationResultReply, error) {

	if err := s.uc.ApplyModerationResult(ctx, req.GetProductId(), req.GetPassed(),
		req.GetRejectReason(), req.GetHitWords()); err != nil {
		return nil, err
	}
	return &productv1.ApplyModerationResultReply{}, nil
}

// CountUserProducts 统计用户商品数。供 user-service 聚合主页。
func (s *ProductService) CountUserProducts(ctx context.Context,
	req *productv1.CountUserProductsRequest) (*productv1.CountUserProductsReply, error) {

	onSale, sold, err := s.uc.CountBySeller(ctx, req.GetSellerId())
	if err != nil {
		return nil, err
	}
	return &productv1.CountUserProductsReply{OnSaleCount: onSale, SoldCount: sold}, nil
}

// ScanForIndex 扫描商品供检索索引。内部接口，仅 search-service 调用。
func (s *ProductService) ScanForIndex(ctx context.Context,
	req *productv1.ScanForIndexRequest) (*productv1.ScanForIndexReply, error) {

	docs, next, err := s.uc.ScanForIndex(ctx, req.GetAfterId(), int(req.GetLimit()))
	if err != nil {
		return nil, err
	}
	out := make([]*productv1.IndexDocument, 0, len(docs))
	for _, d := range docs {
		out = append(out, indexDocToProto(d))
	}
	return &productv1.ScanForIndexReply{Documents: out, NextAfterId: next}, nil
}

// SearchFallback 降级检索。内部接口，仅 search-service 在搜索引擎不可用时调用。
func (s *ProductService) SearchFallback(ctx context.Context,
	req *productv1.SearchFallbackRequest) (*productv1.SearchFallbackReply, error) {

	conditions := make([]uint8, 0, len(req.GetConditions()))
	for _, c := range req.GetConditions() {
		if v := conditionFromProto(c); v != 0 {
			conditions = append(conditions, v)
		}
	}

	items, next, hasMore, err := s.uc.SearchFallback(ctx, &biz.FallbackQuery{
		Keyword:       req.GetKeyword(),
		MinPriceCents: req.GetMinPriceCents(),
		MaxPriceCents: req.GetMaxPriceCents(),
		Conditions:    conditions,
		CategoryIDs:   req.GetCategoryIds(),
		CampusIDs:     req.GetCampusIds(),
		OnlyAvailable: req.GetOnlyAvailable(),
		Sort:          req.GetSort(),
		Cursor:        req.GetCursor(),
		Limit:         int(req.GetLimit()),
	})
	if err != nil {
		return nil, err
	}
	return &productv1.SearchFallbackReply{
		Products:   productBriefsToProto(items, nil),
		NextCursor: next,
		HasMore:    hasMore,
	}, nil
}

// requireLogin 取出当前登录用户 ID。
//
// 在每个需要身份的方法里显式调用，不只依赖中间件：
// 中间件按接口分组挂载，新增接口时漏挂就会出现越权。
func requireLogin(ctx context.Context) (int64, error) {
	uid := auth.MustUserID(ctx)
	if uid <= 0 {
		return 0, auth.ErrUnauthenticated("请先登录")
	}
	return uid, nil
}

var _ productv1.ProductServiceServer = (*ProductService)(nil)
