package data

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/fleamarket/app/product/entity"
	"github.com/fleamarket/app/product/internal/biz"

	"github.com/fleamarket/pkg/errs"
	"github.com/fleamarket/pkg/paging"
	"gorm.io/gorm"
)

// 本文件实现供 search-service 使用的两个内部查询：
// 全量索引扫描与降级检索。

// ScanForIndex 按主键游标扫描商品。
//
// 两个关键点：
//   - Unscoped()：必须包含已软删除的商品。search-service 需要知道
//     哪些文档该从索引移除，而 GORM 默认会把软删除的行过滤掉，
//     导致已删除商品永远留在索引里被搜到。
//   - 联表取一级分类：索引中冗余 parent_category_id，
//     使按一级分类筛选无需先展开其下全部二级分类。
func (r *productRepo) ScanForIndex(ctx context.Context, afterID int64,
	limit int) ([]*biz.IndexDoc, int64, error) {

	type scanRow struct {
		entity.Product
		ParentCategoryID int64 `gorm:"column:parent_category_id"`
	}

	var rows []scanRow
	err := r.data.DB.WithContext(ctx).
		Unscoped().
		Table("t_product AS p").
		Select("p.*, COALESCE(c.parent_id, 0) AS parent_category_id").
		Joins("LEFT JOIN t_category AS c ON c.id = p.category_id").
		Where("p.id > ?", afterID).
		Order("p.id ASC").
		Limit(limit).
		Scan(&rows).Error
	if err != nil {
		return nil, 0, wrapDBError(err)
	}

	out := make([]*biz.IndexDoc, 0, len(rows))
	var lastID int64
	for i := range rows {
		p := toProductBiz(&rows[i].Product, nil)
		out = append(out, &biz.IndexDoc{
			Product:          p,
			ParentCategoryID: rows[i].ParentCategoryID,
			Deleted:          rows[i].DeletedAt.Valid,
		})
		lastID = rows[i].ID
	}

	// 返回 0 表示已扫描完毕：不足一批说明到底了。
	next := int64(0)
	if len(rows) == limit {
		next = lastID
	}
	return out, next, nil
}

// SearchFallback 数据库模糊匹配的降级检索。
//
// 明确的能力边界：
//   - 用 LIKE '%kw%' 匹配标题，无分词、无相关度排序；
//   - 只支持时间与价格排序，不支持热度与距离（见 product.proto 的说明）；
//   - 前缀通配符使索引失效，因此必须靠其他条件（状态、校区）收窄范围，
//     并强制 limit 上限，避免一次全表扫描把数据库拖垮。
func (r *productRepo) SearchFallback(ctx context.Context,
	q *biz.FallbackQuery) ([]*biz.Product, string, bool, error) {

	limit := q.Limit
	if limit <= 0 || limit > 50 {
		limit = 20
	}

	db := r.data.DB.WithContext(ctx).Model(&entity.Product{})

	// 状态过滤放在最前：它是选择性最好的条件，
	// 能让后续的 LIKE 只作用在一个小得多的集合上。
	if q.OnlyAvailable {
		db = db.Where("status = ?", entity.ProductStatusOnSale)
	} else {
		db = db.Where("status IN ?", []uint8{
			entity.ProductStatusOnSale,
			entity.ProductStatusReserved,
			entity.ProductStatusSold,
		})
	}
	if len(q.CampusIDs) > 0 {
		db = db.Where("campus_id IN ?", q.CampusIDs)
	}
	if len(q.CategoryIDs) > 0 {
		// 同时匹配二级与一级分类，与索引侧行为保持一致。
		db = db.Where(
			"category_id IN ? OR category_id IN (SELECT id FROM t_category WHERE parent_id IN ?)",
			q.CategoryIDs, q.CategoryIDs)
	}
	if len(q.Conditions) > 0 {
		db = db.Where("condition IN ?", q.Conditions)
	}
	if q.MinPriceCents > 0 {
		db = db.Where("price_cents >= ?", q.MinPriceCents)
	}
	if q.MaxPriceCents > 0 {
		db = db.Where("price_cents <= ?", q.MaxPriceCents)
	}
	if kw := strings.TrimSpace(q.Keyword); kw != "" {
		// 转义 LIKE 的通配符，否则用户输入 "%" 会匹配全部商品。
		db = db.Where("title LIKE ?", "%"+escapeLike(kw)+"%")
	}

	cur, err := paging.DecodeCursor(q.Cursor)
	if err != nil {
		return nil, "", false, errs.InvalidArgument("分页游标无效")
	}

	sortCol, sortDesc := fallbackSortColumn(q.Sort)
	if !cur.IsZero() {
		db, err = applyFallbackCursor(db, sortCol, sortDesc, cur)
		if err != nil {
			return nil, "", false, err
		}
	}

	dir := "ASC"
	if sortDesc {
		dir = "DESC"
	}
	var rows []entity.Product
	if err := db.Order(sortCol + " " + dir).Order("id " + dir).
		Limit(limit + 1).Find(&rows).Error; err != nil {
		return nil, "", false, wrapDBError(err)
	}

	page := paging.BuildPage(rows, limit, func(x entity.Product) paging.Cursor {
		return paging.Cursor{SortValue: fallbackSortValue(sortCol, &x), ID: x.ID}
	})
	items := make([]*biz.Product, 0, len(page.Items))
	for i := range page.Items {
		items = append(items, toProductBiz(&page.Items[i], nil))
	}
	return items, page.NextCursor, page.HasMore, nil
}

// fallbackSortColumn 返回排序列与方向。
func fallbackSortColumn(sort string) (string, bool) {
	switch sort {
	case "price_asc":
		return "price_cents", false
	case "price_desc":
		return "price_cents", true
	default:
		// 默认按发布时间倒序。
		return "created_at", true
	}
}

func fallbackSortValue(col string, p *entity.Product) string {
	if col == "price_cents" {
		return strconv.FormatInt(p.PriceCents, 10)
	}
	return strconv.FormatInt(p.CreatedAt.UnixMilli(), 10)
}

// applyFallbackCursor 拼接复合游标条件。
//
// 必须带 id 作为决胜键：按价格排序时大量商品价格相同，
// 只用价格做游标会导致同价商品被跳过或重复。
func applyFallbackCursor(db *gorm.DB, col string, desc bool, cur paging.Cursor) (*gorm.DB, error) {
	cmp := "<"
	if !desc {
		cmp = ">"
	}

	if col == "price_cents" {
		v, err := strconv.ParseInt(cur.SortValue, 10, 64)
		if err != nil {
			return nil, errs.InvalidArgument("分页游标无效")
		}
		return db.Where(
			"("+col+" "+cmp+" ?) OR ("+col+" = ? AND id "+cmp+" ?)",
			v, v, cur.ID), nil
	}

	ms, err := strconv.ParseInt(cur.SortValue, 10, 64)
	if err != nil {
		return nil, errs.InvalidArgument("分页游标无效")
	}
	at := time.UnixMilli(ms)
	return db.Where(
		"("+col+" "+cmp+" ?) OR ("+col+" = ? AND id "+cmp+" ?)",
		at, at, cur.ID), nil
}

// escapeLike 转义 LIKE 模式中的特殊字符。
//
// 不转义的后果：用户搜 "%" 会匹配所有商品（等于全表扫描），
// 搜 "_" 会匹配任意单字符。这既是性能问题也是行为错误。
func escapeLike(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 8)
	for _, r := range s {
		switch r {
		case '%', '_', '\\':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}
