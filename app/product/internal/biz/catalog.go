package biz

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/fleamarket/pkg/errs"
	"github.com/fleamarket/pkg/idgen"
	"github.com/fleamarket/pkg/paging"
)

// FavoriteUsecase 收藏用例。
type FavoriteUsecase struct {
	favs     FavoriteRepo
	products ProductRepo
	log      *slog.Logger
}

// NewFavoriteUsecase 构造用例。
func NewFavoriteUsecase(favs FavoriteRepo, products ProductRepo, logger *slog.Logger) *FavoriteUsecase {
	return &FavoriteUsecase{favs: favs, products: products,
		log: logger.With(slog.String("usecase", "favorite"))}
}

// Add 收藏商品。
//
// 幂等：重复收藏返回当前收藏数而非报错。用户连点两次按钮是常态，
// 报错只会让前端不得不额外容错。
func (uc *FavoriteUsecase) Add(ctx context.Context, userID, productID int64) (int32, error) {
	p, err := uc.products.GetByID(ctx, productID)
	if err != nil {
		return 0, errs.FromDataError(err, "商品不存在")
	}
	if !p.IsVisible() {
		return 0, errs.NotFound("商品不存在")
	}
	if p.SellerID == userID {
		return 0, errs.InvalidArgument("不能收藏自己发布的商品")
	}

	added, err := uc.favs.Add(ctx, userID, productID)
	if err != nil {
		return 0, errs.FromDataError(err, "")
	}
	if !added {
		return p.FavoriteCount, nil
	}

	count, err := uc.products.AdjustFavoriteCount(ctx, productID, 1)
	if err != nil {
		// 收藏关系已写入，计数失败只影响展示数字。
		// 不回滚：定时任务会按关系表重算计数。
		uc.log.WarnContext(ctx, "累加收藏数失败",
			slog.Int64("product_id", productID), slog.Any("error", err))
		return p.FavoriteCount + 1, nil
	}
	return count, nil
}

// Remove 取消收藏。未收藏时也返回成功。
func (uc *FavoriteUsecase) Remove(ctx context.Context, userID, productID int64) (int32, error) {
	removed, err := uc.favs.Remove(ctx, userID, productID)
	if err != nil {
		return 0, errs.FromDataError(err, "")
	}
	if !removed {
		p, err := uc.products.GetByID(ctx, productID)
		if err != nil {
			return 0, nil
		}
		return p.FavoriteCount, nil
	}

	count, err := uc.products.AdjustFavoriteCount(ctx, productID, -1)
	if err != nil {
		uc.log.WarnContext(ctx, "扣减收藏数失败",
			slog.Int64("product_id", productID), slog.Any("error", err))
		return 0, nil
	}
	return count, nil
}

// ListMine 我的收藏。
//
// 已下架或已删除的商品仍保留在收藏关系中，但列表里会被过滤掉——
// 直接删关系会让「商品重新上架后收藏消失」，而用户并没有取消过收藏。
func (uc *FavoriteUsecase) ListMine(ctx context.Context, userID int64,
	cursor string, limit int) (*paging.Page[*Product], error) {

	limit = paging.NormalizeLimit(limit)
	ids, next, hasMore, err := uc.favs.ListByUser(ctx, userID, cursor, limit)
	if err != nil {
		return nil, errs.FromDataError(err, "")
	}
	if len(ids) == 0 {
		return &paging.Page[*Product]{}, nil
	}

	found, err := uc.products.BatchGet(ctx, ids)
	if err != nil {
		return nil, errs.FromDataError(err, "")
	}
	items := make([]*Product, 0, len(ids))
	for _, id := range ids {
		if p, ok := found[id]; ok && p.IsVisible() {
			items = append(items, p)
		}
	}
	return &paging.Page[*Product]{Items: items, NextCursor: next, HasMore: hasMore}, nil
}

// ListUserIDs 收藏了某商品的用户，供降价提醒扇出。
func (uc *FavoriteUsecase) ListUserIDs(ctx context.Context, productID int64,
	cursor string, limit int) ([]int64, string, bool, error) {

	if limit <= 0 || limit > 500 {
		limit = 200
	}
	ids, next, hasMore, err := uc.favs.ListUserIDsByProduct(ctx, productID, cursor, limit)
	if err != nil {
		return nil, "", false, errs.FromDataError(err, "")
	}
	return ids, next, hasMore, nil
}

// MarkFavorited 为商品列表回填「当前用户是否已收藏」。
//
// 批量查询而非逐条判断：一页 20 条商品逐个查会产生 20 次往返，
// 在列表接口上是不可接受的。
func (uc *FavoriteUsecase) MarkFavorited(ctx context.Context, userID int64,
	products []*Product) map[int64]bool {

	if userID <= 0 || len(products) == 0 {
		return map[int64]bool{}
	}
	ids := make([]int64, 0, len(products))
	for _, p := range products {
		ids = append(ids, p.ID)
	}
	marks, err := uc.favs.ExistsBatch(ctx, userID, ids)
	if err != nil {
		// 失败时退化为全部未收藏：这只影响心形图标的初始状态，
		// 不值得让整个列表请求失败。
		uc.log.WarnContext(ctx, "批量查询收藏状态失败", slog.Any("error", err))
		return map[int64]bool{}
	}
	return marks
}

// WantedUsecase 求购帖用例。
type WantedUsecase struct {
	wanted  WantedRepo
	catalog CatalogRepo
	users   UserGateway
	ids     *idgen.Generator
	events  EventPublisher
	log     *slog.Logger
}

// NewWantedUsecase 构造用例。
func NewWantedUsecase(wanted WantedRepo, catalog CatalogRepo, users UserGateway,
	ids *idgen.Generator, events EventPublisher, logger *slog.Logger) *WantedUsecase {

	return &WantedUsecase{wanted: wanted, catalog: catalog, users: users,
		ids: ids, events: events, log: logger.With(slog.String("usecase", "wanted"))}
}

// CreateWantedInput 发布求购帖的输入。
type CreateWantedInput struct {
	Title          string
	Description    string
	BudgetMaxCents int64
	CategoryID     int64
	CampusID       int64
	ValidDays      int32
}

// Create 发布求购帖。
//
// 与发布商品同样要求校园认证：求购帖也会引来私聊，
// 若允许未认证账号发布，就成了绕过认证闸门的骚扰入口。
func (uc *WantedUsecase) Create(ctx context.Context, userID int64,
	in *CreateWantedInput) (*WantedPost, error) {

	seller, err := uc.users.GetSellerInfo(ctx, userID)
	if err != nil {
		return nil, errs.Unavailable("暂时无法校验发布资格，请稍后重试")
	}
	if !seller.CampusVerified {
		return nil, errs.WithMeta(
			errs.PermissionDenied("请先完成校园认证"),
			map[string]string{"required": "campus_auth"},
		)
	}

	c, err := uc.catalog.GetCategory(ctx, in.CategoryID)
	if err != nil {
		return nil, errs.InvalidArgument("分类不存在")
	}
	if c.ParentID == 0 {
		return nil, errs.InvalidArgument("请选择具体的二级分类")
	}

	campusID := in.CampusID
	if campusID == 0 {
		campusID = seller.CampusID
	}

	days := in.ValidDays
	if days <= 0 || days > 30 {
		days = 14
	}

	id, err := uc.ids.NextID()
	if err != nil {
		return nil, errs.Internal("ID 生成失败")
	}

	w := &WantedPost{
		ID:             id,
		UserID:         userID,
		Title:          strings.TrimSpace(in.Title),
		Description:    strings.TrimSpace(in.Description),
		BudgetMaxCents: in.BudgetMaxCents,
		CategoryID:     in.CategoryID,
		SchoolID:       seller.SchoolID,
		CampusID:       campusID,
		Status:         WantedOpen,
		ExpireAt:       time.Now().AddDate(0, 0, int(days)),
	}

	created, err := uc.wanted.Create(ctx, w)
	if err != nil {
		return nil, errs.FromDataError(err, "")
	}

	if uc.events != nil {
		payload, _ := json.Marshal(map[string]any{
			"wanted_id":   created.ID,
			"user_id":     created.UserID,
			"category_id": created.CategoryID,
			"campus_id":   created.CampusID,
		})
		if err := uc.events.Publish(ctx, TopicWantedCreated, payload); err != nil {
			uc.log.WarnContext(ctx, "发布求购事件失败", slog.Any("error", err))
		}
	}
	return created, nil
}

// Get 查看求购帖。
func (uc *WantedUsecase) Get(ctx context.Context, wantedID, viewerID int64) (*WantedPost, error) {
	w, err := uc.wanted.GetByID(ctx, wantedID)
	if err != nil {
		return nil, errs.FromDataError(err, "求购帖不存在")
	}
	// 过期状态在读取时纠正，不依赖定时任务的及时性。
	if w.Status == WantedOpen && w.ExpireAt.Before(time.Now()) {
		w.Status = WantedExpired
	}
	if viewerID != w.UserID {
		if err := uc.wanted.IncrViewCount(ctx, wantedID, 1); err != nil {
			uc.log.WarnContext(ctx, "累加求购浏览量失败", slog.Any("error", err))
		}
	}
	return w, nil
}

// Close 关闭求购帖。
func (uc *WantedUsecase) Close(ctx context.Context, wantedID, userID int64) (*WantedPost, error) {
	w, err := uc.wanted.GetByID(ctx, wantedID)
	if err != nil {
		return nil, errs.FromDataError(err, "求购帖不存在")
	}
	if w.UserID != userID {
		return nil, errs.PermissionDenied("只能关闭自己发布的求购")
	}
	if w.Status != WantedOpen {
		return nil, errs.Conflict("该求购帖已关闭")
	}
	ok, err := uc.wanted.UpdateStatus(ctx, wantedID, WantedOpen, WantedClosed)
	if err != nil {
		return nil, errs.FromDataError(err, "")
	}
	if !ok {
		return nil, errs.Conflict("状态已变更，请刷新后重试")
	}
	w.Status = WantedClosed
	return w, nil
}

// List 浏览求购帖。
func (uc *WantedUsecase) List(ctx context.Context, categoryID, campusID int64,
	keyword, cursor string, limit int) (*paging.Page[*WantedPost], error) {

	limit = paging.NormalizeLimit(limit)
	items, next, hasMore, err := uc.wanted.List(ctx, categoryID, campusID, keyword, cursor, limit)
	if err != nil {
		return nil, errs.FromDataError(err, "")
	}
	return &paging.Page[*WantedPost]{Items: items, NextCursor: next, HasMore: hasMore}, nil
}

// ListMine 我的求购帖。
func (uc *WantedUsecase) ListMine(ctx context.Context, userID int64,
	cursor string, limit int) (*paging.Page[*WantedPost], error) {

	limit = paging.NormalizeLimit(limit)
	items, next, hasMore, err := uc.wanted.ListByUser(ctx, userID, cursor, limit)
	if err != nil {
		return nil, errs.FromDataError(err, "")
	}
	return &paging.Page[*WantedPost]{Items: items, NextCursor: next, HasMore: hasMore}, nil
}

// ExpireOverdue 把过期求购帖置为 EXPIRED，由定时任务调用。
func (uc *WantedUsecase) ExpireOverdue(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		limit = 200
	}
	n, err := uc.wanted.ExpireOverdue(ctx, limit)
	if err != nil {
		return 0, errs.FromDataError(err, "")
	}
	return n, nil
}

// CatalogUsecase 分类与 POI 字典用例。
type CatalogUsecase struct {
	catalog CatalogRepo
}

// NewCatalogUsecase 构造用例。
func NewCatalogUsecase(catalog CatalogRepo) *CatalogUsecase {
	return &CatalogUsecase{catalog: catalog}
}

// ListCategories 查询分类树。
func (uc *CatalogUsecase) ListCategories(ctx context.Context, enabledOnly bool) ([]*Category, error) {
	list, err := uc.catalog.ListCategories(ctx, enabledOnly)
	if err != nil {
		return nil, errs.FromDataError(err, "")
	}
	return list, nil
}

// ListPOIs 查询校区面交点。
func (uc *CatalogUsecase) ListPOIs(ctx context.Context, campusID int64, types []uint8) ([]*POI, error) {
	if campusID <= 0 {
		return nil, errs.InvalidArgument("请指定校区")
	}
	list, err := uc.catalog.ListPOIs(ctx, campusID, types)
	if err != nil {
		return nil, errs.FromDataError(err, "")
	}
	return list, nil
}
