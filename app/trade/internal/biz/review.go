package biz

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/fleamarket/pkg/errs"
	"github.com/fleamarket/pkg/idgen"
	"github.com/fleamarket/pkg/paging"
)

// ReviewUsecase 评价用例。
type ReviewUsecase struct {
	reviews ReviewRepo
	orders  OrderRepo
	users   UserGateway
	ids     *idgen.Generator
	events  EventPublisher
	log     *slog.Logger
}

// NewReviewUsecase 构造用例。
func NewReviewUsecase(reviews ReviewRepo, orders OrderRepo, users UserGateway,
	ids *idgen.Generator, events EventPublisher, logger *slog.Logger) *ReviewUsecase {

	return &ReviewUsecase{
		reviews: reviews, orders: orders, users: users, ids: ids, events: events,
		log: logger.With(slog.String("usecase", "review")),
	}
}

// CreateReviewInput 提交评价的输入。
type CreateReviewInput struct {
	OrderID   int64
	Score     int32
	Tags      []int32
	Content   string
	Anonymous bool
}

// Create 提交评价。
//
// 前置条件（doc.md §9.4）：
//   - 订单已完成；
//   - 评价者是买卖双方之一；
//   - 在 7 天互评期内；
//   - 该角色尚未评价过（由唯一索引保证，重复提交返回幂等成功）。
func (uc *ReviewUsecase) Create(ctx context.Context, userID int64,
	in *CreateReviewInput) (*Review, error) {

	if in.Score < 1 || in.Score > 5 {
		return nil, errs.InvalidArgument("评分需在 1~5 之间")
	}

	o, err := uc.orders.GetByID(ctx, in.OrderID)
	if err != nil {
		return nil, errs.FromDataError(err, "订单不存在")
	}
	role := o.RoleOf(userID)
	if role == 0 {
		return nil, errs.NotFound("订单不存在")
	}
	if o.Status != StatusCompleted {
		return nil, errs.Conflict("交易完成后才能评价")
	}
	if !o.CanReview(time.Now()) {
		return nil, errs.Conflict("互评期已结束")
	}

	toUserID := o.SellerID
	if role == ReviewRoleSeller {
		toUserID = o.BuyerID
	}

	id, err := uc.ids.NextID()
	if err != nil {
		return nil, errs.Internal("ID 生成失败")
	}
	r := &Review{
		ID:         id,
		OrderID:    o.ID,
		ProductID:  o.ProductID,
		FromUserID: userID,
		ToUserID:   toUserID,
		Role:       role,
		Score:      in.Score,
		Tags:       in.Tags,
		Content:    in.Content,
		Anonymous:  in.Anonymous,
		CreatedAt:  time.Now(),
	}

	created, err := uc.reviews.Create(ctx, r)
	if err != nil {
		return nil, errs.FromDataError(err, "")
	}
	if !created {
		// 已评价过。返回已有的那条而非报错——用户重复提交时
		// 看到自己的评价比看到「你已评价过」更自然。
		existing, err := uc.reviews.ListByOrder(ctx, o.ID)
		if err == nil {
			for _, e := range existing {
				if e.Role == role {
					return e, nil
				}
			}
		}
		return r, nil
	}

	// 评价转信誉分。
	//
	// 评分到原因的映射在此完成，但「加减多少分」由 user-service 决定
	// （doc.md §9.4）：调用方只声明「收到了好评」这件事实。
	reason := creditReasonOf(in.Score)
	if err := uc.users.AdjustCredit(ctx, toUserID, reason, r.ID); err != nil {
		// 信誉分失败不回滚评价：评价本身是用户的真实表达，
		// 分数可由对账任务按评价记录补偿。
		uc.log.WarnContext(ctx, "评价转信誉分失败",
			slog.Int64("review_id", r.ID), slog.Any("error", err))
	}

	uc.publishReviewed(ctx, r)
	return r, nil
}

// creditReasonOf 评分到信誉分变更原因的映射。
func creditReasonOf(score int32) uint8 {
	switch ScoreGroup(score) {
	case ScoreGroupPositive:
		return CreditReasonReviewPositive
	case ScoreGroupNeutral:
		return CreditReasonReviewNeutral
	default:
		return CreditReasonReviewNegative
	}
}

// ListByOrder 查看某订单的双方评价。
//
// 仅买卖双方可见：订单评价里可能包含面交细节，
// 对无关第三方公开没有必要。个人主页展示的评价走 ListByUser。
func (uc *ReviewUsecase) ListByOrder(ctx context.Context, orderID, userID int64) ([]*Review, error) {
	o, err := uc.orders.GetByID(ctx, orderID)
	if err != nil {
		return nil, errs.FromDataError(err, "订单不存在")
	}
	if !o.IsParticipant(userID) {
		return nil, errs.NotFound("订单不存在")
	}
	list, err := uc.reviews.ListByOrder(ctx, orderID)
	if err != nil {
		return nil, errs.FromDataError(err, "")
	}
	return list, nil
}

// ListByUser 查看某用户收到的评价，用于个人主页。公开可见。
func (uc *ReviewUsecase) ListByUser(ctx context.Context, userID int64, scoreGroup uint8,
	cursor string, limit int) (*paging.Page[*Review], error) {

	limit = paging.NormalizeLimit(limit)
	items, next, hasMore, err := uc.reviews.ListByUser(ctx, userID, scoreGroup, cursor, limit)
	if err != nil {
		return nil, errs.FromDataError(err, "")
	}
	return &paging.Page[*Review]{Items: items, NextCursor: next, HasMore: hasMore}, nil
}

// Stats 用户的评价统计。
func (uc *ReviewUsecase) Stats(ctx context.Context, userID int64) (*ReviewStats, error) {
	st, err := uc.reviews.StatsOf(ctx, userID)
	if err != nil {
		return nil, errs.FromDataError(err, "")
	}
	return st, nil
}

// ReviewedFlags 查询某订单双方是否已评价，供订单详情展示评价入口。
func (uc *ReviewUsecase) ReviewedFlags(ctx context.Context, orderID int64) (bool, bool) {
	buyerDone, sellerDone, err := uc.reviews.ExistsByOrderRoles(ctx, orderID)
	if err != nil {
		// 查询失败时按「未评价」处理：多展示一次评价入口的代价
		// 远小于让已完成交易的用户找不到评价按钮。
		return false, false
	}
	return buyerDone, sellerDone
}

func (uc *ReviewUsecase) publishReviewed(ctx context.Context, r *Review) {
	if uc.events == nil {
		return
	}
	payload, err := json.Marshal(map[string]any{
		"review_id":    r.ID,
		"order_id":     r.OrderID,
		"from_user_id": r.FromUserID,
		"to_user_id":   r.ToUserID,
		"score":        r.Score,
		"role":         r.Role,
	})
	if err != nil {
		return
	}
	if err := uc.events.Publish(ctx, TopicReviewed, payload); err != nil {
		uc.log.WarnContext(ctx, "发布评价事件失败", slog.Any("error", err))
	}
}
