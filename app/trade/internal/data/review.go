package data

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/fleamarket/app/trade/entity"
	"github.com/fleamarket/app/trade/internal/biz"

	"github.com/fleamarket/pkg/errs"
	"github.com/fleamarket/pkg/paging"
	"gorm.io/gorm/clause"
)

// reviewRepo 评价仓储的 GORM 实现。
type reviewRepo struct {
	data *Data
}

// NewReviewRepo 构造仓储。
func NewReviewRepo(data *Data) biz.ReviewRepo { return &reviewRepo{data: data} }

// Create 创建评价。
//
// 用 ON CONFLICT DO NOTHING + 影响行数判定是否新增，而非先查后插：
// uk_order_role 唯一索引保证同一订单同一角色只能评一次，
// 靠影响行数判断幂等，无竞态窗口。
func (r *reviewRepo) Create(ctx context.Context, rv *biz.Review) (bool, error) {
	row := &entity.Review{
		ID:         rv.ID,
		OrderID:    rv.OrderID,
		ProductID:  rv.ProductID,
		FromUserID: rv.FromUserID,
		ToUserID:   rv.ToUserID,
		Role:       rv.Role,
		Score:      rv.Score,
		Tags:       joinTags(rv.Tags),
		Content:    rv.Content,
		Anonymous:  rv.Anonymous,
		CreatedAt:  time.Now(),
	}
	res := r.data.DB.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).Create(row)
	if res.Error != nil {
		return false, wrapDBError(res.Error)
	}
	return res.RowsAffected == 1, nil
}

func (r *reviewRepo) ListByOrder(ctx context.Context, orderID int64) ([]*biz.Review, error) {
	var rows []entity.Review
	if err := r.data.DB.WithContext(ctx).
		Where("order_id = ?", orderID).Order("role ASC").Find(&rows).Error; err != nil {
		return nil, wrapDBError(err)
	}
	out := make([]*biz.Review, 0, len(rows))
	for i := range rows {
		out = append(out, toReviewBiz(&rows[i]))
	}
	return out, nil
}

func (r *reviewRepo) ListByUser(ctx context.Context, userID int64, scoreGroup uint8,
	cursor string, limit int) ([]*biz.Review, string, bool, error) {

	limit = paging.NormalizeLimit(limit)
	cur, err := paging.DecodeCursor(cursor)
	if err != nil {
		return nil, "", false, errs.InvalidArgument("分页游标无效")
	}

	db := r.data.DB.WithContext(ctx).Model(&entity.Review{}).
		Where("to_user_id = ?", userID)

	// 按评分分组筛选。分组边界与 biz.ScoreGroup 一致。
	switch scoreGroup {
	case biz.ScoreGroupPositive:
		db = db.Where("score >= ?", 4)
	case biz.ScoreGroupNeutral:
		db = db.Where("score = ?", 3)
	case biz.ScoreGroupNegative:
		db = db.Where("score <= ?", 2)
	}

	if !cur.IsZero() {
		ts, err := strconv.ParseInt(cur.SortValue, 10, 64)
		if err != nil {
			return nil, "", false, errs.InvalidArgument("分页游标无效")
		}
		at := time.UnixMilli(ts)
		db = db.Where("(created_at < ?) OR (created_at = ? AND id < ?)", at, at, cur.ID)
	}

	var rows []entity.Review
	if err := db.Order("created_at DESC").Order("id DESC").
		Limit(limit + 1).Find(&rows).Error; err != nil {
		return nil, "", false, wrapDBError(err)
	}
	page := paging.BuildPage(rows, limit, func(x entity.Review) paging.Cursor {
		return paging.Cursor{SortValue: strconv.FormatInt(x.CreatedAt.UnixMilli(), 10), ID: x.ID}
	})
	out := make([]*biz.Review, 0, len(page.Items))
	for i := range page.Items {
		out = append(out, toReviewBiz(&page.Items[i]))
	}
	return out, page.NextCursor, page.HasMore, nil
}

// ExistsByOrderRoles 查询某订单双方是否已评价。
func (r *reviewRepo) ExistsByOrderRoles(ctx context.Context, orderID int64) (bool, bool, error) {
	var rows []entity.Review
	if err := r.data.DB.WithContext(ctx).Select("role").
		Where("order_id = ?", orderID).Find(&rows).Error; err != nil {
		return false, false, wrapDBError(err)
	}
	var buyerDone, sellerDone bool
	for _, x := range rows {
		switch x.Role {
		case biz.ReviewRoleBuyer:
			buyerDone = true
		case biz.ReviewRoleSeller:
			sellerDone = true
		}
	}
	return buyerDone, sellerDone, nil
}

// StatsOf 用户评价统计。
//
// 用一次 GROUP BY score 聚合，而非四次 COUNT：
// 一次往返拿到全部分组计数，再在内存里归并成好评/中评/差评。
func (r *reviewRepo) StatsOf(ctx context.Context, userID int64) (*biz.ReviewStats, error) {
	type agg struct {
		Score int32
		Cnt   int32
	}
	var rows []agg
	err := r.data.DB.WithContext(ctx).Model(&entity.Review{}).
		Select("score, COUNT(*) AS cnt").
		Where("to_user_id = ?", userID).
		Group("score").Scan(&rows).Error
	if err != nil {
		return nil, wrapDBError(err)
	}

	st := &biz.ReviewStats{UserID: userID}
	var scoreSum int32
	for _, x := range rows {
		st.TotalCount += x.Cnt
		scoreSum += x.Score * x.Cnt
		switch biz.ScoreGroup(x.Score) {
		case biz.ScoreGroupPositive:
			st.PositiveCount += x.Cnt
		case biz.ScoreGroupNeutral:
			st.NeutralCount += x.Cnt
		default:
			st.NegativeCount += x.Cnt
		}
	}
	if st.TotalCount > 0 {
		st.PositiveRate = st.PositiveCount * 100 / st.TotalCount
		// 平均分放大 10 倍用整数表示，四舍五入。
		st.AverageScoreX10 = (scoreSum*10 + st.TotalCount/2) / st.TotalCount
	}
	return st, nil
}

func toReviewBiz(row *entity.Review) *biz.Review {
	return &biz.Review{
		ID:         row.ID,
		OrderID:    row.OrderID,
		ProductID:  row.ProductID,
		FromUserID: row.FromUserID,
		ToUserID:   row.ToUserID,
		Role:       row.Role,
		Score:      row.Score,
		Tags:       splitTags(row.Tags),
		Content:    row.Content,
		Anonymous:  row.Anonymous,
		CreatedAt:  row.CreatedAt,
	}
}

// joinTags 把标签枚举值编码为逗号分隔字符串。
func joinTags(tags []int32) string {
	if len(tags) == 0 {
		return ""
	}
	parts := make([]string, 0, len(tags))
	for _, t := range tags {
		parts = append(parts, strconv.FormatInt(int64(t), 10))
	}
	return strings.Join(parts, ",")
}

func splitTags(s string) []int32 {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]int32, 0, len(parts))
	for _, p := range parts {
		if n, err := strconv.ParseInt(strings.TrimSpace(p), 10, 32); err == nil {
			out = append(out, int32(n))
		}
	}
	return out
}
