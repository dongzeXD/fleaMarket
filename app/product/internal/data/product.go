package data

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/fleamarket/app/product/entity"
	"github.com/fleamarket/app/product/internal/biz"

	"github.com/fleamarket/pkg/errs"
	"github.com/fleamarket/pkg/idgen"
	"github.com/fleamarket/pkg/paging"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// productRepo 商品仓储的 GORM 实现。
type productRepo struct {
	data *Data
	ids  *idgen.Generator
}

// NewProductRepo 构造仓储。
func NewProductRepo(data *Data, ids *idgen.Generator) biz.ProductRepo {
	return &productRepo{data: data, ids: ids}
}

// Create 在单个事务内写入商品与图片。
//
// 必须同事务：若图片写入失败而商品已提交，会留下一个没有图片的商品，
// 而列表页的封面图取自主表冗余字段，这类数据在 UI 上表现为「破图」。
func (r *productRepo) Create(ctx context.Context, p *biz.Product) (*biz.Product, error) {
	row := toProductEntity(p)

	err := r.data.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(row).Error; err != nil {
			return err
		}
		return r.replaceImages(tx, p.ID, p.Images)
	})
	if err != nil {
		return nil, wrapDBError(err)
	}
	return r.GetByID(ctx, p.ID)
}

func (r *productRepo) GetByID(ctx context.Context, id int64) (*biz.Product, error) {
	var row entity.Product
	if err := r.data.DB.WithContext(ctx).First(&row, "id = ?", id).Error; err != nil {
		return nil, wrapDBError(err)
	}

	var imgs []entity.ProductImage
	if err := r.data.DB.WithContext(ctx).
		Where("product_id = ?", id).Order("sort ASC").Find(&imgs).Error; err != nil {
		return nil, wrapDBError(err)
	}
	return toProductBiz(&row, imgs), nil
}

func (r *productRepo) Update(ctx context.Context, id int64, u *biz.ProductUpdate) (*biz.Product, error) {
	updates := map[string]any{}
	if u.Title != nil {
		updates["title"] = *u.Title
	}
	if u.Description != nil {
		updates["description"] = *u.Description
	}
	if u.PriceCents != nil {
		updates["price_cents"] = *u.PriceCents
	}
	if u.Condition != nil {
		updates["condition"] = *u.Condition
	}
	if u.CategoryID != nil {
		updates["category_id"] = *u.CategoryID
	}
	if u.POIID != nil {
		updates["poi_id"] = *u.POIID
	}

	err := r.data.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if len(updates) > 0 {
			if err := tx.Model(&entity.Product{}).Where("id = ?", id).
				Updates(updates).Error; err != nil {
				return err
			}
		}
		if u.ImageKeys != nil {
			images := make([]biz.Image, 0, len(u.ImageKeys))
			for i, k := range u.ImageKeys {
				images = append(images, biz.Image{ObjectKey: k, Sort: int32(i)})
			}
			if err := r.replaceImages(tx, id, images); err != nil {
				return err
			}
			// 封面是冗余字段，换图后必须同步，否则列表页仍显示旧封面。
			cover := ""
			if len(u.ImageKeys) > 0 {
				cover = u.ImageKeys[0]
			}
			if err := tx.Model(&entity.Product{}).Where("id = ?", id).
				Update("cover_key", cover).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, wrapDBError(err)
	}
	return r.GetByID(ctx, id)
}

// replaceImages 整体替换图片。
//
// 删后重建而非逐条 diff：图片数量最多 9 张，diff 的复杂度换不来性能收益，
// 而「顺序调整」这类变更用 diff 处理极易出错。
func (r *productRepo) replaceImages(tx *gorm.DB, productID int64, images []biz.Image) error {
	if err := tx.Where("product_id = ?", productID).
		Delete(&entity.ProductImage{}).Error; err != nil {
		return err
	}
	if len(images) == 0 {
		return nil
	}
	rows := make([]entity.ProductImage, 0, len(images))
	for i, img := range images {
		id, err := r.ids.NextID()
		if err != nil {
			return err
		}
		rows = append(rows, entity.ProductImage{
			ID:        id,
			ProductID: productID,
			ObjectKey: img.ObjectKey,
			Sort:      int32(i),
			Width:     img.Width,
			Height:    img.Height,
			CreatedAt: time.Now(),
		})
	}
	return tx.Create(&rows).Error
}

// UpdateStatus 带 fromStatus 条件的状态变更，影响 0 行表示状态已被并发改变。
func (r *productRepo) UpdateStatus(ctx context.Context, id int64, from, to uint8) (bool, error) {
	res := r.data.DB.WithContext(ctx).Model(&entity.Product{}).
		Where("id = ? AND status = ?", id, from).
		Update("status", to)
	if res.Error != nil {
		return false, wrapDBError(res.Error)
	}
	return res.RowsAffected == 1, nil
}

func (r *productRepo) SoftDelete(ctx context.Context, id int64) error {
	err := r.data.DB.WithContext(ctx).Delete(&entity.Product{}, "id = ?", id).Error
	return wrapDBError(err)
}

func (r *productRepo) BatchGet(ctx context.Context, ids []int64) (map[int64]*biz.Product, error) {
	if len(ids) == 0 {
		return map[int64]*biz.Product{}, nil
	}
	var rows []entity.Product
	// 不查 description：批量场景都是列表展示，2000 字符的描述是纯浪费。
	err := r.data.DB.WithContext(ctx).
		Select("id", "seller_id", "title", "price_cents", "original_price_cents",
			"condition", "status", "cover_key", "school_id", "campus_id",
			"favorite_count", "view_count", "heat_score", "created_at").
		Where("id IN ?", ids).Find(&rows).Error
	if err != nil {
		return nil, wrapDBError(err)
	}
	out := make(map[int64]*biz.Product, len(rows))
	for i := range rows {
		out[rows[i].ID] = toProductBiz(&rows[i], nil)
	}
	return out, nil
}

func (r *productRepo) ListBySeller(ctx context.Context, sellerID int64, statuses []uint8,
	cursor string, limit int) ([]*biz.Product, string, bool, error) {

	limit = paging.NormalizeLimit(limit)
	cur, err := paging.DecodeCursor(cursor)
	if err != nil {
		return nil, "", false, errs.InvalidArgument("分页游标无效")
	}

	db := r.data.DB.WithContext(ctx).Model(&entity.Product{}).
		Where("seller_id = ?", sellerID)
	if len(statuses) > 0 {
		db = db.Where("status IN ?", statuses)
	}
	if !cur.IsZero() {
		ts, err := strconv.ParseInt(cur.SortValue, 10, 64)
		if err != nil {
			return nil, "", false, errs.InvalidArgument("分页游标无效")
		}
		at := time.UnixMilli(ts)
		db = db.Where("(created_at < ?) OR (created_at = ? AND id < ?)", at, at, cur.ID)
	}

	var rows []entity.Product
	if err := db.Order("created_at DESC").Order("id DESC").
		Limit(limit + 1).Find(&rows).Error; err != nil {
		return nil, "", false, wrapDBError(err)
	}

	page := paging.BuildPage(rows, limit, func(x entity.Product) paging.Cursor {
		return paging.Cursor{
			SortValue: strconv.FormatInt(x.CreatedAt.UnixMilli(), 10),
			ID:        x.ID,
		}
	})
	items := make([]*biz.Product, 0, len(page.Items))
	for i := range page.Items {
		items = append(items, toProductBiz(&page.Items[i], nil))
	}
	return items, page.NextCursor, page.HasMore, nil
}

// LockForOrder 以单条 CAS 更新抢占商品。
//
// 这是整个交易链路上并发安全的关键点（doc.md §9.4）：
// 条件里带 status = ON_SALE，由数据库保证只有一个并发请求能把它改成 RESERVED。
// 先 SELECT 再 UPDATE 的写法会让两个买家都读到 ON_SALE 并都认为抢到了。
func (r *productRepo) LockForOrder(ctx context.Context, productID, orderID int64) (bool, error) {
	res := r.data.DB.WithContext(ctx).Model(&entity.Product{}).
		Where("id = ? AND status = ?", productID, entity.ProductStatusOnSale).
		Updates(map[string]any{
			"status":          entity.ProductStatusReserved,
			"locked_order_id": orderID,
		})
	if res.Error != nil {
		return false, wrapDBError(res.Error)
	}
	return res.RowsAffected == 1, nil
}

// ReleaseOrderLock 释放锁定，要求持锁订单匹配。
//
// locked_order_id 参与 WHERE 条件，使一个延迟到达的取消请求
// 无法释放掉别人刚建立的锁。
func (r *productRepo) ReleaseOrderLock(ctx context.Context, productID, orderID int64) (bool, error) {
	res := r.data.DB.WithContext(ctx).Model(&entity.Product{}).
		Where("id = ? AND status = ? AND locked_order_id = ?",
			productID, entity.ProductStatusReserved, orderID).
		Updates(map[string]any{
			"status":          entity.ProductStatusOnSale,
			"locked_order_id": nil,
		})
	if res.Error != nil {
		return false, wrapDBError(res.Error)
	}
	return res.RowsAffected == 1, nil
}

func (r *productRepo) MarkSold(ctx context.Context, productID, orderID int64) (bool, error) {
	res := r.data.DB.WithContext(ctx).Model(&entity.Product{}).
		Where("id = ? AND status = ? AND locked_order_id = ?",
			productID, entity.ProductStatusReserved, orderID).
		Updates(map[string]any{
			"status":        entity.ProductStatusSold,
			"sold_order_id": orderID,
		})
	if res.Error != nil {
		return false, wrapDBError(res.Error)
	}
	return res.RowsAffected == 1, nil
}

func (r *productRepo) ApplyModeration(ctx context.Context, id int64, passed bool,
	reason string, hitWords []string) error {

	updates := map[string]any{}
	if passed {
		updates["status"] = entity.ProductStatusOnSale
		updates["reject_reason"] = ""
		updates["hit_words"] = ""
	} else {
		updates["status"] = entity.ProductStatusRejected
		updates["reject_reason"] = reason
		updates["hit_words"] = joinNonEmpty(hitWords)
	}
	// 只有待审核的商品能被审核结论改变状态：
	// 否则一条迟到的审核回写会把已售出的商品打回在售。
	res := r.data.DB.WithContext(ctx).Model(&entity.Product{}).
		Where("id = ? AND status = ?", id, entity.ProductStatusPendingReview).
		Updates(updates)
	if res.Error != nil {
		return wrapDBError(res.Error)
	}
	return nil
}

func (r *productRepo) IncrViewCount(ctx context.Context, id int64, delta int64) error {
	err := r.data.DB.WithContext(ctx).Model(&entity.Product{}).
		Where("id = ?", id).
		Update("view_count", gorm.Expr("view_count + ?", delta)).Error
	return wrapDBError(err)
}

// AdjustFavoriteCount 调整收藏数。
//
// 用 SQL 表达式在库内加减并夹到 0 以上，而非读改写：
// 并发收藏/取消时读改写会丢更新。
func (r *productRepo) AdjustFavoriteCount(ctx context.Context, id int64, delta int32) (int32, error) {
	err := r.data.DB.WithContext(ctx).Model(&entity.Product{}).
		Where("id = ?", id).
		Update("favorite_count", gorm.Expr("GREATEST(0, favorite_count + ?)", delta)).Error
	if err != nil {
		return 0, wrapDBError(err)
	}
	var row entity.Product
	if err := r.data.DB.WithContext(ctx).Select("favorite_count").
		First(&row, "id = ?", id).Error; err != nil {
		return 0, wrapDBError(err)
	}
	return row.FavoriteCount, nil
}

func (r *productRepo) CountBySeller(ctx context.Context, sellerID int64) (int32, int32, error) {
	type agg struct {
		Status uint8
		Cnt    int64
	}
	var rows []agg
	err := r.data.DB.WithContext(ctx).Model(&entity.Product{}).
		Select("status, COUNT(*) AS cnt").
		Where("seller_id = ? AND status IN ?", sellerID, []uint8{
			entity.ProductStatusOnSale, entity.ProductStatusReserved, entity.ProductStatusSold,
		}).
		Group("status").Scan(&rows).Error
	if err != nil {
		return 0, 0, wrapDBError(err)
	}
	var onSale, sold int32
	for _, x := range rows {
		switch x.Status {
		case entity.ProductStatusOnSale, entity.ProductStatusReserved:
			onSale += int32(x.Cnt)
		case entity.ProductStatusSold:
			sold += int32(x.Cnt)
		}
	}
	return onSale, sold, nil
}

func (r *productRepo) RecordPriceChange(ctx context.Context, productID, oldC, newC int64) error {
	id, err := r.ids.NextID()
	if err != nil {
		return err
	}
	row := &entity.PriceHistory{
		ID:            id,
		ProductID:     productID,
		OldPriceCents: oldC,
		NewPriceCents: newC,
		CreatedAt:     time.Now(),
	}
	return wrapDBError(r.data.DB.WithContext(ctx).Create(row).Error)
}

func (r *productRepo) ListHeatCandidates(ctx context.Context, since time.Time,
	limit, offset int) ([]*biz.Product, error) {

	var rows []entity.Product
	err := r.data.DB.WithContext(ctx).
		Select("id", "view_count", "favorite_count", "inquiry_count", "created_at").
		Where("created_at >= ? AND status IN ?", since, []uint8{
			entity.ProductStatusOnSale, entity.ProductStatusReserved,
		}).
		Order("id ASC").Limit(limit).Offset(offset).Find(&rows).Error
	if err != nil {
		return nil, wrapDBError(err)
	}
	out := make([]*biz.Product, 0, len(rows))
	for i := range rows {
		out = append(out, toProductBiz(&rows[i], nil))
	}
	return out, nil
}

// UpdateHeatScores 批量写回热度分。
//
// 用 CASE WHEN 拼一条 UPDATE 而非逐条执行：一批 500 条商品
// 逐条更新是 500 次往返，定时任务会被网络延迟主导。
func (r *productRepo) UpdateHeatScores(ctx context.Context, scores map[int64]float64) error {
	if len(scores) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(scores))
	caseExpr := "CASE id"
	args := make([]any, 0, len(scores)*2)
	for id, s := range scores {
		ids = append(ids, id)
		caseExpr += " WHEN ? THEN ?"
		args = append(args, id, s)
	}
	caseExpr += " END"

	err := r.data.DB.WithContext(ctx).Model(&entity.Product{}).
		Where("id IN ?", ids).
		Update("heat_score", gorm.Expr(caseExpr, args...)).Error
	return wrapDBError(err)
}

// --- 转换 ---

func toProductEntity(p *biz.Product) *entity.Product {
	return &entity.Product{
		ID:                 p.ID,
		SellerID:           p.SellerID,
		Title:              p.Title,
		Description:        p.Description,
		PriceCents:         p.PriceCents,
		OriginalPriceCents: p.OriginalPriceCents,
		Condition:          p.Condition,
		CategoryID:         p.CategoryID,
		SchoolID:           p.SchoolID,
		CampusID:           p.CampusID,
		POIID:              p.POIID,
		Status:             p.Status,
		CoverKey:           p.CoverKey,
		CreatedAt:          time.Now(),
	}
}

func toProductBiz(row *entity.Product, imgs []entity.ProductImage) *biz.Product {
	p := &biz.Product{
		ID:                 row.ID,
		SellerID:           row.SellerID,
		Title:              row.Title,
		Description:        row.Description,
		PriceCents:         row.PriceCents,
		OriginalPriceCents: row.OriginalPriceCents,
		Condition:          row.Condition,
		CategoryID:         row.CategoryID,
		SchoolID:           row.SchoolID,
		CampusID:           row.CampusID,
		POIID:              row.POIID,
		Status:             row.Status,
		RejectReason:       row.RejectReason,
		HitWords:           splitNonEmpty(row.HitWords),
		CoverKey:           row.CoverKey,
		ViewCount:          row.ViewCount,
		FavoriteCount:      row.FavoriteCount,
		InquiryCount:       row.InquiryCount,
		HeatScore:          row.HeatScore,
		LockedOrderID:      row.LockedOrderID,
		SoldOrderID:        row.SoldOrderID,
		CreatedAt:          row.CreatedAt,
		UpdatedAt:          row.UpdatedAt,
	}
	for _, img := range imgs {
		p.Images = append(p.Images, biz.Image{
			ObjectKey: img.ObjectKey,
			Sort:      img.Sort,
			Width:     img.Width,
			Height:    img.Height,
		})
	}
	return p
}

// wrapDBError 把 GORM 错误转成 biz 层可识别的哨兵错误。
func wrapDBError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return errs.ErrNotFound
	case errors.Is(err, gorm.ErrDuplicatedKey):
		return errs.ErrAlreadyExists
	default:
		return err
	}
}

var _ = clause.OnConflict{}
