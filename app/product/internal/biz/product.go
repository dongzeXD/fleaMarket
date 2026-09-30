package biz

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/fleamarket/pkg/errs"
	"github.com/fleamarket/pkg/idgen"
	"github.com/fleamarket/pkg/statemachine"
	"github.com/fleamarket/pkg/storage"
)

// 商品状态机的事件。
type productEvent string

const (
	evPass      productEvent = "pass"
	evReject    productEvent = "reject"
	evReserve   productEvent = "reserve"
	evCancel    productEvent = "cancel"
	evComplete  productEvent = "complete"
	evOffline   productEvent = "offline"
	evRepublish productEvent = "republish"
)

// productMachine 声明商品的合法状态流转（doc.md §9.2）。
//
// 用 pkg/statemachine 而非散落的 if 判断：状态规则集中声明后，
// 「哪些操作在当前状态可用」可以直接查询，前端按钮的可用性
// 不必再复制一遍规则。
var productMachine = statemachine.New[uint8, productEvent]().
	Allow(StatusPendingReview, evPass, StatusOnSale).
	Allow(StatusPendingReview, evReject, StatusRejected).
	Allow(StatusOnSale, evReserve, StatusReserved).
	Allow(StatusOnSale, evOffline, StatusOffline).
	Allow(StatusReserved, evCancel, StatusOnSale).
	Allow(StatusReserved, evComplete, StatusSold).
	Allow(StatusOffline, evRepublish, StatusPendingReview).
	// 驳回后允许修改并重新提交审核。
	Allow(StatusRejected, evRepublish, StatusPendingReview)

const (
	// maxImages 单个商品最多的图片数。
	maxImages = 9
	// uploadURLTTL 预签名上传凭证的有效期。
	//
	// 10 分钟是弱网可用性与凭证泄漏窗口之间的折中：
	// 太短会让上传大图的用户失败，太长则扩大了凭证被盗用的时间范围。
	uploadURLTTL = 10 * time.Minute
	// maxImageBytes 单张图片上限 5MB。
	maxImageBytes = 5 * 1024 * 1024
)

// ProductUsecase 商品用例。
type ProductUsecase struct {
	products ProductRepo
	catalog  CatalogRepo
	favs     FavoriteRepo
	users    UserGateway
	review   ModerationGateway
	files    storage.Storage
	ids      *idgen.Generator
	events   EventPublisher
	log      *slog.Logger
}

// NewProductUsecase 构造用例。
func NewProductUsecase(products ProductRepo, catalog CatalogRepo, favs FavoriteRepo,
	users UserGateway, review ModerationGateway, files storage.Storage,
	ids *idgen.Generator, events EventPublisher, logger *slog.Logger) *ProductUsecase {

	return &ProductUsecase{
		products: products, catalog: catalog, favs: favs, users: users,
		review: review, files: files, ids: ids, events: events,
		log: logger.With(slog.String("usecase", "product")),
	}
}

// UploadTicket 图片上传凭证。
type UploadTicket struct {
	UploadURL string
	ObjectKey string
	Headers   map[string]string
	ExpireAt  time.Time
	MaxSize   int64
}

// CreateImageUploadURL 签发图片上传凭证。
//
// key 形如 `product/{userID}/{snowflake}.{ext}`：前缀含用户 ID，
// 使提交阶段可以校验归属，挡住「拿到他人 key 后冒用其上传内容」。
func (uc *ProductUsecase) CreateImageUploadURL(ctx context.Context, userID int64,
	ext string, sizeBytes int64) (*UploadTicket, error) {

	if sizeBytes <= 0 || sizeBytes > maxImageBytes {
		return nil, errs.InvalidArgument("图片大小超出限制（最大 5MB）")
	}
	contentType, ok := imageContentType(ext)
	if !ok {
		return nil, errs.InvalidArgument("不支持的图片格式")
	}

	id, err := uc.ids.NextID()
	if err != nil {
		return nil, errs.Internal("ID 生成失败")
	}
	key := fmt.Sprintf("product/%d/%d.%s", userID, id, ext)

	up, err := uc.files.CreateUploadURL(ctx, storage.UploadRequest{
		Key:         key,
		ContentType: contentType,
		MaxSize:     maxImageBytes,
		TTL:         uploadURLTTL,
	})
	if err != nil {
		uc.log.ErrorContext(ctx, "签发上传凭证失败", slog.Any("error", err))
		return nil, errs.Unavailable("图片服务暂时不可用")
	}

	return &UploadTicket{
		UploadURL: up.URL,
		ObjectKey: up.Key,
		Headers:   up.Headers,
		ExpireAt:  up.ExpireAt,
		MaxSize:   maxImageBytes,
	}, nil
}

// CreateProductInput 发布商品的输入。
type CreateProductInput struct {
	Title              string
	Description        string
	PriceCents         int64
	OriginalPriceCents int64
	Condition          uint8
	CategoryID         int64
	CampusID           int64
	POIID              *int64
	ImageKeys          []string
}

// Create 发布商品。
//
// 校验顺序是刻意的：先校验发布者资格（最便宜且最可能失败），
// 再校验字典引用，最后才校验图片（需要访问对象存储，最慢）。
// 让失败尽早发生可以省掉无谓的外部调用。
func (uc *ProductUsecase) Create(ctx context.Context, sellerID int64,
	in *CreateProductInput) (*Product, error) {

	seller, err := uc.requirePublishPermission(ctx, sellerID)
	if err != nil {
		return nil, err
	}

	if err := uc.validateCategory(ctx, in.CategoryID); err != nil {
		return nil, err
	}
	campusID := in.CampusID
	if campusID == 0 {
		campusID = seller.CampusID
	}
	if err := uc.validatePOI(ctx, in.POIID, campusID); err != nil {
		return nil, err
	}
	images, err := uc.validateImages(ctx, sellerID, in.ImageKeys)
	if err != nil {
		return nil, err
	}

	id, err := uc.ids.NextID()
	if err != nil {
		return nil, errs.Internal("ID 生成失败")
	}

	p := &Product{
		ID:                 id,
		SellerID:           sellerID,
		Title:              strings.TrimSpace(in.Title),
		Description:        strings.TrimSpace(in.Description),
		PriceCents:         in.PriceCents,
		OriginalPriceCents: in.OriginalPriceCents,
		Condition:          in.Condition,
		CategoryID:         in.CategoryID,
		SchoolID:           seller.SchoolID,
		CampusID:           campusID,
		POIID:              in.POIID,
		Status:             StatusPendingReview,
		CoverKey:           images[0].ObjectKey,
		Images:             images,
	}

	created, err := uc.products.Create(ctx, p)
	if err != nil {
		return nil, errs.FromDataError(err, "")
	}

	uc.publishProduct(ctx, TopicProductCreated, created)
	uc.submitForModeration(ctx, created)
	return created, nil
}

// Get 查看商品详情。
//
// viewerID 为 0 表示未登录访客。不可见的商品只有卖家自己能看到——
// 对他人一律返回 404 而非 403，避免通过错误码区分「不存在」与「存在但不可见」，
// 那会让人能枚举出待审核商品的 ID。
func (uc *ProductUsecase) Get(ctx context.Context, productID, viewerID int64) (*Product, error) {
	p, err := uc.products.GetByID(ctx, productID)
	if err != nil {
		return nil, errs.FromDataError(err, "商品不存在")
	}
	if !p.IsVisible() && !p.IsOwnedBy(viewerID) {
		return nil, errs.NotFound("商品不存在")
	}

	// 浏览量累加失败不影响详情返回。
	if viewerID != p.SellerID {
		if err := uc.products.IncrViewCount(ctx, productID, 1); err != nil {
			uc.log.WarnContext(ctx, "累加浏览量失败", slog.Any("error", err))
		}
	}
	return p, nil
}

// Update 修改商品。
func (uc *ProductUsecase) Update(ctx context.Context, productID, sellerID int64,
	u *ProductUpdate) (*Product, error) {

	old, err := uc.products.GetByID(ctx, productID)
	if err != nil {
		return nil, errs.FromDataError(err, "商品不存在")
	}
	if !old.IsOwnedBy(sellerID) {
		return nil, errs.PermissionDenied("只能修改自己发布的商品")
	}
	// 已售出与已预订的商品不允许改动：前者是交易记录的引用对象，
	// 后者若被改价会让买家看到的与下单时不一致。
	switch old.Status {
	case StatusSold:
		return nil, errs.Conflict("已售出的商品不可修改")
	case StatusReserved:
		return nil, errs.Conflict("商品已被预订，请先取消预订再修改")
	}

	if u.CategoryID != nil {
		if err := uc.validateCategory(ctx, *u.CategoryID); err != nil {
			return nil, err
		}
	}
	if u.POIID != nil {
		if err := uc.validatePOI(ctx, u.POIID, old.CampusID); err != nil {
			return nil, err
		}
	}
	if u.ImageKeys != nil {
		if _, err := uc.validateImages(ctx, sellerID, u.ImageKeys); err != nil {
			return nil, err
		}
	}

	updated, err := uc.products.Update(ctx, productID, u)
	if err != nil {
		return nil, errs.FromDataError(err, "商品不存在")
	}

	// 价格变化要留痕并触发降价提醒。
	if u.PriceCents != nil && *u.PriceCents != old.PriceCents {
		if err := uc.products.RecordPriceChange(ctx, productID, old.PriceCents, *u.PriceCents); err != nil {
			uc.log.WarnContext(ctx, "记录价格变更失败", slog.Any("error", err))
		}
		if *u.PriceCents < old.PriceCents {
			uc.publishPriceDrop(ctx, updated, old.PriceCents)
		}
	}

	// 标题或描述变化需重新送审：否则卖家可以先发一条正常商品过审，
	// 再把内容改成违规的，绕过整个审核机制。
	if contentChanged(u) {
		if ok, err := uc.products.UpdateStatus(ctx, productID, updated.Status, StatusPendingReview); err != nil {
			uc.log.WarnContext(ctx, "重置为待审核失败", slog.Any("error", err))
		} else if ok {
			updated.Status = StatusPendingReview
			uc.submitForModeration(ctx, updated)
		}
	}

	uc.publishProduct(ctx, TopicProductUpdated, updated)
	return updated, nil
}

// ChangeStatus 卖家主动变更状态（下架 / 重新上架）。
func (uc *ProductUsecase) ChangeStatus(ctx context.Context, productID, sellerID int64,
	event string) (*Product, error) {

	var ev productEvent
	switch event {
	case "offline":
		ev = evOffline
	case "republish":
		ev = evRepublish
	default:
		return nil, errs.InvalidArgument("不支持的操作")
	}

	p, err := uc.products.GetByID(ctx, productID)
	if err != nil {
		return nil, errs.FromDataError(err, "商品不存在")
	}
	if !p.IsOwnedBy(sellerID) {
		return nil, errs.PermissionDenied("只能操作自己发布的商品")
	}

	next, err := productMachine.Next(p.Status, ev)
	if err != nil {
		return nil, errs.Conflict("当前状态不支持该操作")
	}

	ok, err := uc.products.UpdateStatus(ctx, productID, p.Status, next)
	if err != nil {
		return nil, errs.FromDataError(err, "")
	}
	if !ok {
		// 影响 0 行说明状态已被并发修改，让客户端重新拉取后重试。
		return nil, errs.Conflict("商品状态已变更，请刷新后重试")
	}
	p.Status = next

	uc.publishProduct(ctx, TopicProductStatusChanged, p)
	// 重新上架需要再过一次审核。
	if next == StatusPendingReview {
		uc.submitForModeration(ctx, p)
	}
	return p, nil
}

// Delete 软删除商品。
func (uc *ProductUsecase) Delete(ctx context.Context, productID, sellerID int64) error {
	p, err := uc.products.GetByID(ctx, productID)
	if err != nil {
		return errs.FromDataError(err, "商品不存在")
	}
	if !p.IsOwnedBy(sellerID) {
		return errs.PermissionDenied("只能删除自己发布的商品")
	}
	switch p.Status {
	case StatusSold:
		// 已售出商品是订单与评价的引用对象，删除会让交易记录出现悬空引用。
		return errs.Conflict("已售出的商品不可删除，可在个人主页隐藏")
	case StatusReserved:
		return errs.Conflict("商品已被预订，请先取消预订")
	}

	if err := uc.products.SoftDelete(ctx, productID); err != nil {
		return errs.FromDataError(err, "")
	}
	uc.publishProduct(ctx, TopicProductDeleted, p)
	return nil
}

// ListMine 我发布的商品，可按状态筛选。
func (uc *ProductUsecase) ListMine(ctx context.Context, sellerID int64, statuses []uint8,
	cursor string, limit int) ([]*Product, string, bool, error) {

	items, next, hasMore, err := uc.products.ListBySeller(ctx, sellerID, statuses, cursor, limit)
	if err != nil {
		return nil, "", false, errs.FromDataError(err, "")
	}
	return items, next, hasMore, nil
}

// ListPublicBySeller 某用户公开在售的商品，用于他人主页。
//
// 状态被硬编码为「在售 + 已预订」，不接受调用方指定：
// 否则他人主页就能通过传 PENDING_REVIEW 窥见该用户待审核的商品。
func (uc *ProductUsecase) ListPublicBySeller(ctx context.Context, sellerID int64,
	cursor string, limit int) ([]*Product, string, bool, error) {

	items, next, hasMore, err := uc.products.ListBySeller(ctx, sellerID,
		[]uint8{StatusOnSale, StatusReserved}, cursor, limit)
	if err != nil {
		return nil, "", false, errs.FromDataError(err, "")
	}
	return items, next, hasMore, nil
}

// BatchGet 批量获取商品，供其他服务回填商品卡片。
func (uc *ProductUsecase) BatchGet(ctx context.Context, ids []int64) (map[int64]*Product, error) {
	if len(ids) == 0 {
		return map[int64]*Product{}, nil
	}
	out, err := uc.products.BatchGet(ctx, ids)
	if err != nil {
		return nil, errs.FromDataError(err, "")
	}
	return out, nil
}

// CountBySeller 统计卖家的在售与已售数量。
func (uc *ProductUsecase) CountBySeller(ctx context.Context, sellerID int64) (int32, int32, error) {
	onSale, sold, err := uc.products.CountBySeller(ctx, sellerID)
	if err != nil {
		return 0, 0, errs.FromDataError(err, "")
	}
	return onSale, sold, nil
}

// ScanForIndex 扫描商品供检索索引使用。
func (uc *ProductUsecase) ScanForIndex(ctx context.Context, afterID int64,
	limit int) ([]*IndexDoc, int64, error) {

	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	docs, next, err := uc.products.ScanForIndex(ctx, afterID, limit)
	if err != nil {
		return nil, 0, errs.FromDataError(err, "")
	}
	return docs, next, nil
}

// SearchFallback 降级检索。
//
// 仅供 search-service 在搜索引擎不可用时调用。这里做的是数据库模糊匹配，
// 结果质量明显低于搜索引擎（无分词、无相关度排序），
// 因此调用方必须向用户标记降级状态。
func (uc *ProductUsecase) SearchFallback(ctx context.Context,
	q *FallbackQuery) ([]*Product, string, bool, error) {

	if q.Limit <= 0 || q.Limit > 50 {
		q.Limit = 20
	}
	items, next, hasMore, err := uc.products.SearchFallback(ctx, q)
	if err != nil {
		return nil, "", false, errs.FromDataError(err, "")
	}
	return items, next, hasMore, nil
}

// AllowedEvents 返回当前状态下卖家可执行的操作。
//
// 由服务端计算而非前端判断：状态规则若在前端复制一份，
// 规则调整时两边必然不同步。
func (uc *ProductUsecase) AllowedEvents(status uint8) []string {
	evs := productMachine.AllowedEvents(status)
	out := make([]string, 0, len(evs))
	for _, e := range evs {
		// 只暴露卖家可主动触发的事件，审核与交易类事件不对外。
		switch e {
		case evOffline, evRepublish:
			out = append(out, string(e))
		}
	}
	return out
}

// --- 内部接口：供 trade-service 与 review-service 调用 ---

// LockResult 抢占结果。
type LockResult struct {
	Locked        bool
	CurrentStatus uint8
	PriceCents    int64
	SellerID      int64
}

// LockForOrder 为下单抢占商品。
func (uc *ProductUsecase) LockForOrder(ctx context.Context, productID, orderID, buyerID int64) (*LockResult, error) {
	p, err := uc.products.GetByID(ctx, productID)
	if err != nil {
		return nil, errs.FromDataError(err, "商品不存在")
	}
	if p.SellerID == buyerID {
		return nil, errs.InvalidArgument("不能购买自己发布的商品")
	}
	if p.Status != StatusOnSale {
		return &LockResult{Locked: false, CurrentStatus: p.Status,
			PriceCents: p.PriceCents, SellerID: p.SellerID}, nil
	}

	locked, err := uc.products.LockForOrder(ctx, productID, orderID)
	if err != nil {
		return nil, errs.FromDataError(err, "")
	}
	if !locked {
		// 抢占失败说明被并发请求先拿到了，重读状态给出准确提示。
		cur, err := uc.products.GetByID(ctx, productID)
		if err != nil {
			return &LockResult{Locked: false, CurrentStatus: StatusReserved}, nil
		}
		return &LockResult{Locked: false, CurrentStatus: cur.Status,
			PriceCents: cur.PriceCents, SellerID: cur.SellerID}, nil
	}

	p.Status = StatusReserved
	uc.publishProduct(ctx, TopicProductStatusChanged, p)
	// 价格快照以抢占时刻为准，防止卖家在买家下单后改价。
	return &LockResult{Locked: true, CurrentStatus: StatusReserved,
		PriceCents: p.PriceCents, SellerID: p.SellerID}, nil
}

// ReleaseOrderLock 释放锁定。
func (uc *ProductUsecase) ReleaseOrderLock(ctx context.Context, productID, orderID int64) (bool, error) {
	released, err := uc.products.ReleaseOrderLock(ctx, productID, orderID)
	if err != nil {
		return false, errs.FromDataError(err, "")
	}
	if released {
		if p, err := uc.products.GetByID(ctx, productID); err == nil {
			uc.publishProduct(ctx, TopicProductStatusChanged, p)
		}
	}
	return released, nil
}

// MarkSold 标记已售出。
func (uc *ProductUsecase) MarkSold(ctx context.Context, productID, orderID int64) (bool, error) {
	marked, err := uc.products.MarkSold(ctx, productID, orderID)
	if err != nil {
		return false, errs.FromDataError(err, "")
	}
	if marked {
		if p, err := uc.products.GetByID(ctx, productID); err == nil {
			uc.publishProduct(ctx, TopicProductStatusChanged, p)
		}
	}
	return marked, nil
}

// ApplyModerationResult 回写审核结论。
func (uc *ProductUsecase) ApplyModerationResult(ctx context.Context, productID int64,
	passed bool, reason string, hitWords []string) error {

	if err := uc.products.ApplyModeration(ctx, productID, passed, reason, hitWords); err != nil {
		return errs.FromDataError(err, "商品不存在")
	}
	if p, err := uc.products.GetByID(ctx, productID); err == nil {
		uc.publishProduct(ctx, TopicProductStatusChanged, p)
	}
	return nil
}

// --- 校验辅助 ---

// requirePublishPermission 校验发布资格。
//
// 未通过校园认证不可发布，这是平台防诈骗的第一道闸门（doc.md §9.1）。
// 即便网关已在令牌里带了认证状态，这里仍要向 user-service 核实——
// 令牌有 2 小时有效期，其间用户可能已被封禁。
func (uc *ProductUsecase) requirePublishPermission(ctx context.Context, userID int64) (*SellerInfo, error) {
	seller, err := uc.users.GetSellerInfo(ctx, userID)
	if err != nil {
		uc.log.ErrorContext(ctx, "查询发布者信息失败", slog.Any("error", err))
		return nil, errs.Unavailable("暂时无法校验发布资格，请稍后重试")
	}
	if seller.Status != 1 {
		return nil, errs.PermissionDenied("账号状态异常，无法发布")
	}
	if !seller.CampusVerified {
		return nil, errs.WithMeta(
			errs.PermissionDenied("请先完成校园认证"),
			map[string]string{"required": "campus_auth"},
		)
	}
	return seller, nil
}

func (uc *ProductUsecase) validateCategory(ctx context.Context, categoryID int64) error {
	c, err := uc.catalog.GetCategory(ctx, categoryID)
	if err != nil {
		return errs.InvalidArgument("分类不存在")
	}
	if !c.Enabled {
		return errs.InvalidArgument("该分类已停用")
	}
	// 必须挂在二级分类上：一级分类是导航分区，
	// 允许挂载会让筛选结果出现「在一级但不在任何二级」的孤儿商品。
	if c.ParentID == 0 {
		return errs.InvalidArgument("请选择具体的二级分类")
	}
	return nil
}

func (uc *ProductUsecase) validatePOI(ctx context.Context, poiID *int64, campusID int64) error {
	if poiID == nil || *poiID == 0 {
		return nil
	}
	poi, err := uc.catalog.GetPOI(ctx, *poiID)
	if err != nil {
		return errs.InvalidArgument("面交地点不存在")
	}
	if poi.CampusID != campusID {
		return errs.InvalidArgument("面交地点不在所选校区内")
	}
	return nil
}

// validateImages 校验图片归属与存在性。
//
// 两项校验各挡一类问题：
//   - 前缀归属：挡住「拿到他人 key 后冒用其上传的图片」；
//   - Stat 存在：挡住「伪造 key 但从未真正上传」。
//
// 预签名直传模式下服务端不参与传输，这是唯一的把关点。
func (uc *ProductUsecase) validateImages(ctx context.Context, userID int64,
	keys []string) ([]Image, error) {

	if len(keys) == 0 {
		return nil, errs.InvalidArgument("请至少上传一张图片")
	}
	if len(keys) > maxImages {
		return nil, errs.InvalidArgument(fmt.Sprintf("最多上传 %d 张图片", maxImages))
	}

	seen := make(map[string]struct{}, len(keys))
	images := make([]Image, 0, len(keys))
	for i, key := range keys {
		if _, dup := seen[key]; dup {
			return nil, errs.InvalidArgument("图片重复")
		}
		seen[key] = struct{}{}

		if !ownsObjectKey(key, userID) {
			return nil, errs.PermissionDenied("图片归属校验失败")
		}
		if _, err := uc.files.Stat(ctx, key); err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				return nil, errs.InvalidArgument("图片尚未上传完成，请重试")
			}
			return nil, errs.Unavailable("图片服务暂时不可用")
		}
		images = append(images, Image{ObjectKey: key, Sort: int32(i)})
	}
	return images, nil
}

// ownsObjectKey 校验 key 是否位于该用户的上传目录下。
// 约定的形态为 `product/{userID}/...`。
func ownsObjectKey(key string, userID int64) bool {
	want := fmt.Sprintf("product/%d/", userID)
	return strings.HasPrefix(key, want)
}

func imageContentType(ext string) (string, bool) {
	switch strings.ToLower(ext) {
	case "jpg", "jpeg":
		return "image/jpeg", true
	case "png":
		return "image/png", true
	case "webp":
		return "image/webp", true
	default:
		return "", false
	}
}

func contentChanged(u *ProductUpdate) bool {
	return u.Title != nil || u.Description != nil
}

// --- 热度计算 ---

// RecomputeHeat 重算近 7 天商品的热度分，由定时任务调用。
//
// 公式（doc.md §9.3）：
//
//	heat = log10(view+1)*1.0 + fav*3.0 + inquiry*5.0 - 时间衰减(小时^1.5 * 0.05)
//
// 三项权重的依据是「行为的信号强度」：浏览最廉价故取对数压缩，
// 收藏代表明确兴趣，主动咨询代表接近成交。时间衰减保证新商品有曝光机会，
// 否则热度榜会被早期爆款长期占据。
func (uc *ProductUsecase) RecomputeHeat(ctx context.Context, window time.Duration, batch int) (int, error) {
	if window <= 0 {
		window = 7 * 24 * time.Hour
	}
	if batch <= 0 {
		batch = 500
	}
	since := time.Now().Add(-window)

	total := 0
	for offset := 0; ; offset += batch {
		items, err := uc.products.ListHeatCandidates(ctx, since, batch, offset)
		if err != nil {
			return total, err
		}
		if len(items) == 0 {
			break
		}
		scores := make(map[int64]float64, len(items))
		for _, p := range items {
			scores[p.ID] = heatScore(p, time.Now())
		}
		if err := uc.products.UpdateHeatScores(ctx, scores); err != nil {
			return total, err
		}
		total += len(items)
		if len(items) < batch {
			break
		}
	}
	return total, nil
}

func heatScore(p *Product, now time.Time) float64 {
	hours := now.Sub(p.CreatedAt).Hours()
	if hours < 0 {
		hours = 0
	}
	decay := math.Pow(hours, 1.5) * 0.05
	return math.Log10(float64(p.ViewCount)+1)*1.0 +
		float64(p.FavoriteCount)*3.0 +
		float64(p.InquiryCount)*5.0 -
		decay
}

// --- 事件发布 ---

func (uc *ProductUsecase) publishProduct(ctx context.Context, topic string, p *Product) {
	if uc.events == nil || p == nil {
		return
	}
	payload, err := json.Marshal(map[string]any{
		"product_id":  p.ID,
		"seller_id":   p.SellerID,
		"category_id": p.CategoryID,
		"school_id":   p.SchoolID,
		"campus_id":   p.CampusID,
		"status":      p.Status,
		"price_cents": p.PriceCents,
	})
	if err != nil {
		return
	}
	if err := uc.events.Publish(ctx, topic, payload); err != nil {
		// 事件失败不回滚主流程：商品已落库，索引可由全量重建兜底
		// （doc.md §9.3 的 ReindexAll）。
		uc.log.WarnContext(ctx, "发布商品事件失败",
			slog.String("topic", topic), slog.Any("error", err))
	}
}

func (uc *ProductUsecase) publishPriceDrop(ctx context.Context, p *Product, oldCents int64) {
	if uc.events == nil {
		return
	}
	payload, err := json.Marshal(map[string]any{
		"product_id":      p.ID,
		"title":           p.Title,
		"old_price_cents": oldCents,
		"new_price_cents": p.PriceCents,
	})
	if err != nil {
		return
	}
	if err := uc.events.Publish(ctx, TopicPriceDropped, payload); err != nil {
		uc.log.WarnContext(ctx, "发布降价事件失败", slog.Any("error", err))
	}
}

// submitForModeration 送审。
//
// 同步得出结论时立即回写状态，转人工时保持 PENDING_REVIEW
// 等 MQ 回写（doc.md §9.6）。审核服务不可用时降级为强制人工审核，
// 而不是直接放行——商品内容的违规风险高于短暂的发布延迟。
func (uc *ProductUsecase) submitForModeration(ctx context.Context, p *Product) {
	if uc.review == nil {
		return
	}
	immediate, passed, reason, hitWords, err := uc.review.SubmitProduct(ctx, p.ID, p.Title, p.Description)
	if err != nil {
		uc.log.WarnContext(ctx, "送审失败，保持待审核状态等待人工处理",
			slog.Int64("product_id", p.ID), slog.Any("error", err))
		return
	}
	if !immediate {
		return
	}
	if err := uc.ApplyModerationResult(ctx, p.ID, passed, reason, hitWords); err != nil {
		uc.log.ErrorContext(ctx, "回写审核结论失败",
			slog.Int64("product_id", p.ID), slog.Any("error", err))
	}
}
