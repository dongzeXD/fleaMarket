package data

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fleamarket/app/product/entity"
	"github.com/fleamarket/app/product/internal/biz"

	"github.com/fleamarket/pkg/errs"
	"github.com/fleamarket/pkg/idgen"
	"github.com/fleamarket/pkg/paging"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// catalogRepo 分类与 POI 字典仓储。
type catalogRepo struct {
	data *Data
}

// NewCatalogRepo 构造仓储。
func NewCatalogRepo(data *Data) biz.CatalogRepo { return &catalogRepo{data: data} }

// ListCategories 查询分类树。
//
// 一次查全表后在内存里组树，而非按层级查两次：
// 分类总量只有几十条，两次往返换不来任何收益。
func (r *catalogRepo) ListCategories(ctx context.Context, enabledOnly bool) ([]*biz.Category, error) {
	db := r.data.DB.WithContext(ctx).Model(&entity.Category{})
	if enabledOnly {
		db = db.Where("enabled = ?", true)
	}
	var rows []entity.Category
	if err := db.Order("parent_id ASC").Order("sort ASC").Find(&rows).Error; err != nil {
		return nil, wrapDBError(err)
	}

	byID := make(map[int64]*biz.Category, len(rows))
	var roots []*biz.Category
	for i := range rows {
		c := toCategoryBiz(&rows[i])
		byID[c.ID] = c
	}
	for _, c := range byID {
		if c.ParentID == 0 {
			roots = append(roots, c)
			continue
		}
		if parent, ok := byID[c.ParentID]; ok {
			parent.Children = append(parent.Children, c)
		}
		// 父分类被停用而子分类仍启用时，子分类会挂不上去。
		// 此时直接丢弃而非提升为一级：把「数码电子/手机」显示成顶级
		// 「手机」会让用户困惑，不如不显示，由运营修正数据。
	}

	sort.Slice(roots, func(i, j int) bool {
		if roots[i].Sort != roots[j].Sort {
			return roots[i].Sort < roots[j].Sort
		}
		return roots[i].ID < roots[j].ID
	})
	for _, root := range roots {
		children := root.Children
		sort.Slice(children, func(i, j int) bool {
			if children[i].Sort != children[j].Sort {
				return children[i].Sort < children[j].Sort
			}
			return children[i].ID < children[j].ID
		})
	}
	return roots, nil
}

func (r *catalogRepo) GetCategory(ctx context.Context, id int64) (*biz.Category, error) {
	var row entity.Category
	if err := r.data.DB.WithContext(ctx).First(&row, "id = ?", id).Error; err != nil {
		return nil, wrapDBError(err)
	}
	return toCategoryBiz(&row), nil
}

func (r *catalogRepo) ListPOIs(ctx context.Context, campusID int64, types []uint8) ([]*biz.POI, error) {
	db := r.data.DB.WithContext(ctx).Model(&entity.CampusPOI{}).
		Where("campus_id = ? AND enabled = ?", campusID, true)
	if len(types) > 0 {
		db = db.Where("type IN ?", types)
	}
	var rows []entity.CampusPOI
	// 推荐地点排在前面：面交安全的核心是选在公开场所，
	// 把推荐点置顶等于用默认值引导用户做更安全的选择。
	if err := db.Order("recommended DESC").Order("id ASC").Find(&rows).Error; err != nil {
		return nil, wrapDBError(err)
	}
	out := make([]*biz.POI, 0, len(rows))
	for i := range rows {
		out = append(out, toPOIBiz(&rows[i]))
	}
	return out, nil
}

func (r *catalogRepo) GetPOI(ctx context.Context, id int64) (*biz.POI, error) {
	var row entity.CampusPOI
	if err := r.data.DB.WithContext(ctx).
		First(&row, "id = ? AND enabled = ?", id, true).Error; err != nil {
		return nil, wrapDBError(err)
	}
	return toPOIBiz(&row), nil
}

func toCategoryBiz(row *entity.Category) *biz.Category {
	return &biz.Category{
		ID:       row.ID,
		ParentID: row.ParentID,
		Name:     row.Name,
		IconKey:  row.IconKey,
		Sort:     row.Sort,
		Enabled:  row.Enabled,
	}
}

func toPOIBiz(row *entity.CampusPOI) *biz.POI {
	return &biz.POI{
		ID:          row.ID,
		CampusID:    row.CampusID,
		Name:        row.Name,
		Type:        row.Type,
		Lat:         row.Lat,
		Lng:         row.Lng,
		Recommended: row.Recommended,
	}
}

// favoriteRepo 收藏仓储。
type favoriteRepo struct {
	data *Data
}

// NewFavoriteRepo 构造仓储。
func NewFavoriteRepo(data *Data) biz.FavoriteRepo { return &favoriteRepo{data: data} }

// Add 收藏。返回 false 表示已收藏过。
//
// 用 ON CONFLICT DO NOTHING 的影响行数判定是否新增，
// 而非先查后插——后者在并发下会让计数被加两次。
func (r *favoriteRepo) Add(ctx context.Context, userID, productID int64) (bool, error) {
	row := &entity.Favorite{UserID: userID, ProductID: productID, CreatedAt: time.Now()}
	res := r.data.DB.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).Create(row)
	if res.Error != nil {
		return false, wrapDBError(res.Error)
	}
	return res.RowsAffected == 1, nil
}

func (r *favoriteRepo) Remove(ctx context.Context, userID, productID int64) (bool, error) {
	res := r.data.DB.WithContext(ctx).
		Where("user_id = ? AND product_id = ?", userID, productID).
		Delete(&entity.Favorite{})
	if res.Error != nil {
		return false, wrapDBError(res.Error)
	}
	return res.RowsAffected == 1, nil
}

func (r *favoriteRepo) Exists(ctx context.Context, userID, productID int64) (bool, error) {
	var cnt int64
	err := r.data.DB.WithContext(ctx).Model(&entity.Favorite{}).
		Where("user_id = ? AND product_id = ?", userID, productID).Count(&cnt).Error
	if err != nil {
		return false, wrapDBError(err)
	}
	return cnt > 0, nil
}

// ExistsBatch 批量判断收藏状态。
//
// 一次 IN 查询替代 N 次单查：列表页一页 20 条商品，
// 逐条判断会产生 20 次往返，是最容易被忽视的 N+1 问题。
func (r *favoriteRepo) ExistsBatch(ctx context.Context, userID int64,
	productIDs []int64) (map[int64]bool, error) {

	if len(productIDs) == 0 {
		return map[int64]bool{}, nil
	}
	var rows []entity.Favorite
	err := r.data.DB.WithContext(ctx).Select("product_id").
		Where("user_id = ? AND product_id IN ?", userID, productIDs).
		Find(&rows).Error
	if err != nil {
		return nil, wrapDBError(err)
	}
	out := make(map[int64]bool, len(rows))
	for _, x := range rows {
		out[x.ProductID] = true
	}
	return out, nil
}

func (r *favoriteRepo) ListByUser(ctx context.Context, userID int64,
	cursor string, limit int) ([]int64, string, bool, error) {

	return r.listCursor(ctx, "user_id = ?", userID, "product_id", cursor, limit)
}

func (r *favoriteRepo) ListUserIDsByProduct(ctx context.Context, productID int64,
	cursor string, limit int) ([]int64, string, bool, error) {

	return r.listCursor(ctx, "product_id = ?", productID, "user_id", cursor, limit)
}

// listCursor 收藏列表的公共游标分页实现。
func (r *favoriteRepo) listCursor(ctx context.Context, where string, arg int64,
	peerCol, cursor string, limit int) ([]int64, string, bool, error) {

	if limit <= 0 {
		limit = paging.DefaultLimit
	}
	if limit > 500 {
		limit = 500
	}
	cur, err := paging.DecodeCursor(cursor)
	if err != nil {
		return nil, "", false, errs.InvalidArgument("分页游标无效")
	}

	type row struct {
		Peer      int64     `gorm:"column:peer"`
		CreatedAt time.Time `gorm:"column:created_at"`
	}

	db := r.data.DB.WithContext(ctx).Model(&entity.Favorite{}).
		Select(peerCol+" AS peer", "created_at").
		Where(where, arg)
	if !cur.IsZero() {
		ts, err := strconv.ParseInt(cur.SortValue, 10, 64)
		if err != nil {
			return nil, "", false, errs.InvalidArgument("分页游标无效")
		}
		at := time.UnixMilli(ts)
		db = db.Where("(created_at < ?) OR (created_at = ? AND "+peerCol+" < ?)", at, at, cur.ID)
	}

	var rows []row
	if err := db.Order("created_at DESC").Order(peerCol + " DESC").
		Limit(limit + 1).Scan(&rows).Error; err != nil {
		return nil, "", false, wrapDBError(err)
	}

	page := paging.BuildPage(rows, limit, func(x row) paging.Cursor {
		return paging.Cursor{
			SortValue: strconv.FormatInt(x.CreatedAt.UnixMilli(), 10),
			ID:        x.Peer,
		}
	})
	ids := make([]int64, 0, len(page.Items))
	for _, x := range page.Items {
		ids = append(ids, x.Peer)
	}
	return ids, page.NextCursor, page.HasMore, nil
}

// wantedRepo 求购帖仓储。
type wantedRepo struct {
	data *Data
	ids  *idgen.Generator
}

// NewWantedRepo 构造仓储。
func NewWantedRepo(data *Data, ids *idgen.Generator) biz.WantedRepo {
	return &wantedRepo{data: data, ids: ids}
}

func (r *wantedRepo) Create(ctx context.Context, w *biz.WantedPost) (*biz.WantedPost, error) {
	row := &entity.WantedPost{
		ID:             w.ID,
		UserID:         w.UserID,
		Title:          w.Title,
		Description:    w.Description,
		BudgetMaxCents: w.BudgetMaxCents,
		CategoryID:     w.CategoryID,
		SchoolID:       w.SchoolID,
		CampusID:       w.CampusID,
		Status:         w.Status,
		ExpireAt:       w.ExpireAt,
		CreatedAt:      time.Now(),
	}
	if err := r.data.DB.WithContext(ctx).Create(row).Error; err != nil {
		return nil, wrapDBError(err)
	}
	return toWantedBiz(row), nil
}

func (r *wantedRepo) GetByID(ctx context.Context, id int64) (*biz.WantedPost, error) {
	var row entity.WantedPost
	if err := r.data.DB.WithContext(ctx).First(&row, "id = ?", id).Error; err != nil {
		return nil, wrapDBError(err)
	}
	return toWantedBiz(&row), nil
}

func (r *wantedRepo) UpdateStatus(ctx context.Context, id int64, from, to uint8) (bool, error) {
	res := r.data.DB.WithContext(ctx).Model(&entity.WantedPost{}).
		Where("id = ? AND status = ?", id, from).Update("status", to)
	if res.Error != nil {
		return false, wrapDBError(res.Error)
	}
	return res.RowsAffected == 1, nil
}

func (r *wantedRepo) IncrViewCount(ctx context.Context, id int64, delta int64) error {
	err := r.data.DB.WithContext(ctx).Model(&entity.WantedPost{}).
		Where("id = ?", id).
		Update("view_count", gorm.Expr("view_count + ?", delta)).Error
	return wrapDBError(err)
}

func (r *wantedRepo) List(ctx context.Context, categoryID, campusID int64,
	keyword, cursor string, limit int) ([]*biz.WantedPost, string, bool, error) {

	db := r.data.DB.WithContext(ctx).Model(&entity.WantedPost{}).
		Where("status = ?", entity.WantedStatusOpen).
		Where("expire_at > ?", time.Now())
	if categoryID > 0 {
		db = db.Where("category_id = ?", categoryID)
	}
	if campusID > 0 {
		db = db.Where("campus_id = ?", campusID)
	}
	if kw := strings.TrimSpace(keyword); kw != "" {
		// 求购帖量级小，这里用模糊匹配是可接受的。
		// 商品搜索走 Meilisearch，不走这条路径。
		db = db.Where("title LIKE ?", "%"+kw+"%")
	}
	return r.listCursor(ctx, db, cursor, limit)
}

func (r *wantedRepo) ListByUser(ctx context.Context, userID int64,
	cursor string, limit int) ([]*biz.WantedPost, string, bool, error) {

	db := r.data.DB.WithContext(ctx).Model(&entity.WantedPost{}).
		Where("user_id = ?", userID)
	return r.listCursor(ctx, db, cursor, limit)
}

func (r *wantedRepo) listCursor(ctx context.Context, db *gorm.DB,
	cursor string, limit int) ([]*biz.WantedPost, string, bool, error) {

	limit = paging.NormalizeLimit(limit)
	cur, err := paging.DecodeCursor(cursor)
	if err != nil {
		return nil, "", false, errs.InvalidArgument("分页游标无效")
	}
	if !cur.IsZero() {
		ts, err := strconv.ParseInt(cur.SortValue, 10, 64)
		if err != nil {
			return nil, "", false, errs.InvalidArgument("分页游标无效")
		}
		at := time.UnixMilli(ts)
		db = db.Where("(created_at < ?) OR (created_at = ? AND id < ?)", at, at, cur.ID)
	}

	var rows []entity.WantedPost
	if err := db.Order("created_at DESC").Order("id DESC").
		Limit(limit + 1).Find(&rows).Error; err != nil {
		return nil, "", false, wrapDBError(err)
	}

	page := paging.BuildPage(rows, limit, func(x entity.WantedPost) paging.Cursor {
		return paging.Cursor{
			SortValue: strconv.FormatInt(x.CreatedAt.UnixMilli(), 10),
			ID:        x.ID,
		}
	})
	items := make([]*biz.WantedPost, 0, len(page.Items))
	for i := range page.Items {
		items = append(items, toWantedBiz(&page.Items[i]))
	}
	return items, page.NextCursor, page.HasMore, nil
}

// ExpireOverdue 把过期的求购帖置为 EXPIRED。
//
// 带 LIMIT 分批处理：一次性 UPDATE 全表在积压时会长时间持锁，
// 阻塞正常的读写。
func (r *wantedRepo) ExpireOverdue(ctx context.Context, limit int) (int, error) {
	var ids []int64
	err := r.data.DB.WithContext(ctx).Model(&entity.WantedPost{}).
		Where("status = ? AND expire_at <= ?", entity.WantedStatusOpen, time.Now()).
		Order("id ASC").Limit(limit).Pluck("id", &ids).Error
	if err != nil {
		return 0, wrapDBError(err)
	}
	if len(ids) == 0 {
		return 0, nil
	}
	res := r.data.DB.WithContext(ctx).Model(&entity.WantedPost{}).
		Where("id IN ? AND status = ?", ids, entity.WantedStatusOpen).
		Update("status", entity.WantedStatusExpired)
	if res.Error != nil {
		return 0, wrapDBError(res.Error)
	}
	return int(res.RowsAffected), nil
}

func toWantedBiz(row *entity.WantedPost) *biz.WantedPost {
	return &biz.WantedPost{
		ID:             row.ID,
		UserID:         row.UserID,
		Title:          row.Title,
		Description:    row.Description,
		BudgetMaxCents: row.BudgetMaxCents,
		CategoryID:     row.CategoryID,
		SchoolID:       row.SchoolID,
		CampusID:       row.CampusID,
		Status:         row.Status,
		ViewCount:      row.ViewCount,
		ExpireAt:       row.ExpireAt,
		CreatedAt:      row.CreatedAt,
	}
}

// splitNonEmpty 按逗号切分并剔除空项。
func splitNonEmpty(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func joinNonEmpty(ss []string) string {
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return strings.Join(out, ",")
}
