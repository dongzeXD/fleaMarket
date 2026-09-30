package data

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/fleamarket/app/trade/entity"
	"github.com/fleamarket/app/trade/internal/biz"

	"github.com/fleamarket/pkg/errs"
	"github.com/fleamarket/pkg/paging"
	"gorm.io/gorm"
)

// orderRepo 订单仓储的 GORM 实现。
type orderRepo struct {
	data *Data
}

// NewOrderRepo 构造仓储。
func NewOrderRepo(data *Data) biz.OrderRepo { return &orderRepo{data: data} }

// Create 创建订单。
//
// active_key 设为 product_id：uk_product_active 唯一索引会让同一商品的
// 第二个进行中订单插入失败，返回 ErrAlreadyExists。这是「同一商品只能有
// 一个进行中订单」的数据库层保障。
func (r *orderRepo) Create(ctx context.Context, o *biz.Order) (*biz.Order, error) {
	row := toOrderEntity(o)
	// 进行中订单占用 active_key。
	row.ActiveKey = &o.ProductID

	if err := r.data.DB.WithContext(ctx).Create(row).Error; err != nil {
		return nil, wrapDBError(err)
	}
	return toOrderBiz(row), nil
}

func (r *orderRepo) GetByID(ctx context.Context, id int64) (*biz.Order, error) {
	var row entity.Order
	if err := r.data.DB.WithContext(ctx).First(&row, "id = ?", id).Error; err != nil {
		return nil, wrapDBError(err)
	}
	return toOrderBiz(&row), nil
}

// UpdateStatus 带 fromStatus 条件的状态变更。
//
// 用 fromStatus 做乐观并发控制：两个请求同时操作同一订单
// （如卖家点接受、定时任务判超时）时，只有一个能成功，另一个影响 0 行。
func (r *orderRepo) UpdateStatus(ctx context.Context, id int64, from, to uint8,
	patch *biz.OrderPatch, clearActiveKey bool) (bool, error) {

	updates := map[string]any{"status": to}
	if clearActiveKey {
		// 终态释放 active_key，使同一商品可以被重新预订。
		updates["active_key"] = gorm.Expr("NULL")
	}
	applyPatch(updates, patch)

	res := r.data.DB.WithContext(ctx).Model(&entity.Order{}).
		Where("id = ? AND status = ?", id, from).
		Updates(updates)
	if res.Error != nil {
		return false, wrapDBError(res.Error)
	}
	return res.RowsAffected == 1, nil
}

func (r *orderRepo) UpdateMeetup(ctx context.Context, id int64, poiID int64, at time.Time) error {
	err := r.data.DB.WithContext(ctx).Model(&entity.Order{}).
		Where("id = ?", id).Updates(map[string]any{
		"meet_poi_id": poiID,
		"meet_time":   at,
	}).Error
	return wrapDBError(err)
}

// MarkConfirmed 标记某方已确认，返回双方最新确认状态。
//
// 用单条 UPDATE 加条件读取，而非「读-改-写」：
// 买卖双方可能同时点确认，读改写会丢掉其中一方的确认。
func (r *orderRepo) MarkConfirmed(ctx context.Context, id int64, role uint8) (bool, bool, error) {
	col := "buyer_confirmed"
	if role == biz.ReviewRoleSeller {
		col = "seller_confirmed"
	}
	if err := r.data.DB.WithContext(ctx).Model(&entity.Order{}).
		Where("id = ?", id).Update(col, true).Error; err != nil {
		return false, false, wrapDBError(err)
	}

	var row entity.Order
	if err := r.data.DB.WithContext(ctx).
		Select("buyer_confirmed", "seller_confirmed").
		First(&row, "id = ?", id).Error; err != nil {
		return false, false, wrapDBError(err)
	}
	return row.BuyerConfirmed, row.SellerConfirmed, nil
}

func (r *orderRepo) ListByUser(ctx context.Context, userID int64, role uint8,
	statuses []uint8, cursor string, limit int) ([]*biz.Order, string, bool, error) {

	limit = paging.NormalizeLimit(limit)
	cur, err := paging.DecodeCursor(cursor)
	if err != nil {
		return nil, "", false, errs.InvalidArgument("分页游标无效")
	}

	db := r.data.DB.WithContext(ctx).Model(&entity.Order{})
	switch role {
	case biz.ReviewRoleBuyer:
		db = db.Where("buyer_id = ?", userID)
	case biz.ReviewRoleSeller:
		db = db.Where("seller_id = ?", userID)
	default:
		// 未指定角色时查我参与的全部订单。
		db = db.Where("buyer_id = ? OR seller_id = ?", userID, userID)
	}
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

	var rows []entity.Order
	if err := db.Order("created_at DESC").Order("id DESC").
		Limit(limit + 1).Find(&rows).Error; err != nil {
		return nil, "", false, wrapDBError(err)
	}
	page := paging.BuildPage(rows, limit, func(x entity.Order) paging.Cursor {
		return paging.Cursor{SortValue: strconv.FormatInt(x.CreatedAt.UnixMilli(), 10), ID: x.ID}
	})
	items := make([]*biz.Order, 0, len(page.Items))
	for i := range page.Items {
		items = append(items, toOrderBiz(&page.Items[i]))
	}
	return items, page.NextCursor, page.HasMore, nil
}

// FindActiveByProduct 查询某商品进行中的订单。
//
// 直接查 active_key：它只在进行中订单上有值，比按 status IN (...) 过滤更精确，
// 也正好命中 uk_product_active 索引。
func (r *orderRepo) FindActiveByProduct(ctx context.Context, productID int64) (*biz.Order, error) {
	var row entity.Order
	err := r.data.DB.WithContext(ctx).
		Where("active_key = ?", productID).First(&row).Error
	if err != nil {
		return nil, wrapDBError(err)
	}
	return toOrderBiz(&row), nil
}

// ListExpired 扫描已超时的进行中订单。
func (r *orderRepo) ListExpired(ctx context.Context, now time.Time, limit int) ([]*biz.Order, error) {
	var rows []entity.Order
	err := r.data.DB.WithContext(ctx).
		Where("expire_at IS NOT NULL AND expire_at <= ?", now).
		Where("status IN ?", []uint8{
			entity.OrderStatusPendingSeller,
			entity.OrderStatusAccepted,
			entity.OrderStatusAwaitingPeer,
		}).
		Order("expire_at ASC").Limit(limit).Find(&rows).Error
	if err != nil {
		return nil, wrapDBError(err)
	}
	out := make([]*biz.Order, 0, len(rows))
	for i := range rows {
		out = append(out, toOrderBiz(&rows[i]))
	}
	return out, nil
}

// CountCompletedByUser 统计完成的交易数。
func (r *orderRepo) CountCompletedByUser(ctx context.Context, userID int64) (int32, int32, error) {
	var bought, sold int64
	db := r.data.DB.WithContext(ctx).Model(&entity.Order{}).
		Where("status = ?", entity.OrderStatusCompleted)
	if err := db.Session(&gorm.Session{}).
		Where("buyer_id = ?", userID).Count(&bought).Error; err != nil {
		return 0, 0, wrapDBError(err)
	}
	if err := db.Session(&gorm.Session{}).
		Where("seller_id = ?", userID).Count(&sold).Error; err != nil {
		return 0, 0, wrapDBError(err)
	}
	return int32(bought), int32(sold), nil
}

// applyPatch 把 OrderPatch 中的非空字段写入 updates。
func applyPatch(updates map[string]any, p *biz.OrderPatch) {
	if p == nil {
		return
	}
	if p.CancelReason != nil {
		updates["cancel_reason"] = *p.CancelReason
	}
	if p.CancelRemark != nil {
		updates["cancel_remark"] = *p.CancelRemark
	}
	if p.CancelledBy != nil {
		updates["cancelled_by"] = *p.CancelledBy
	}
	if p.AcceptedAt != nil {
		updates["accepted_at"] = *p.AcceptedAt
	}
	if p.CompletedAt != nil {
		updates["completed_at"] = *p.CompletedAt
	}
	if p.ExpireAt != nil {
		updates["expire_at"] = *p.ExpireAt
	}
	if p.MeetPOIID != nil {
		updates["meet_poi_id"] = *p.MeetPOIID
	}
	if p.MeetTime != nil {
		updates["meet_time"] = *p.MeetTime
	}
}

func toOrderEntity(o *biz.Order) *entity.Order {
	return &entity.Order{
		ID:              o.ID,
		ProductID:       o.ProductID,
		BuyerID:         o.BuyerID,
		SellerID:        o.SellerID,
		PriceCents:      o.PriceCents,
		PayChannel:      o.PayChannel,
		Status:          o.Status,
		MeetPOIID:       o.MeetPOIID,
		MeetTime:        o.MeetTime,
		BuyerMessage:    o.BuyerMessage,
		BuyerConfirmed:  o.BuyerConfirmed,
		SellerConfirmed: o.SellerConfirmed,
		CreatedAt:       time.Now(),
	}
}

func toOrderBiz(row *entity.Order) *biz.Order {
	return &biz.Order{
		ID:              row.ID,
		ProductID:       row.ProductID,
		BuyerID:         row.BuyerID,
		SellerID:        row.SellerID,
		PriceCents:      row.PriceCents,
		PayChannel:      row.PayChannel,
		Status:          row.Status,
		MeetPOIID:       row.MeetPOIID,
		MeetTime:        row.MeetTime,
		BuyerMessage:    row.BuyerMessage,
		BuyerConfirmed:  row.BuyerConfirmed,
		SellerConfirmed: row.SellerConfirmed,
		CancelReason:    row.CancelReason,
		CancelRemark:    row.CancelRemark,
		CancelledBy:     row.CancelledBy,
		AcceptedAt:      row.AcceptedAt,
		CompletedAt:     row.CompletedAt,
		ExpireAt:        row.ExpireAt,
		CreatedAt:       row.CreatedAt,
	}
}

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
