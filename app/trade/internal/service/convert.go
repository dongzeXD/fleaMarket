// Package service 是 trade-service 的传输适配层。
package service

import (
	"time"

	"github.com/fleamarket/app/trade/internal/biz"

	commonv1 "github.com/fleamarket/api/common/v1"
	tradev1 "github.com/fleamarket/api/trade/v1"
	"github.com/fleamarket/pkg/paging"
	"github.com/google/wire"
)

// ProviderSet 服务层的 wire provider 集合。
var ProviderSet = wire.NewSet(
	NewOrderService,
	NewReviewService,
)

// unixPtr 把时间指针转 Unix 秒。空指针或零值返回 0。
func unixPtr(t *time.Time) int64 {
	if t == nil || t.IsZero() {
		return 0
	}
	return t.Unix()
}

func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func orderStatusToProto(s uint8) tradev1.OrderStatus {
	switch s {
	case biz.StatusPendingSeller:
		return tradev1.OrderStatus_ORDER_STATUS_PENDING_SELLER
	case biz.StatusAccepted:
		return tradev1.OrderStatus_ORDER_STATUS_ACCEPTED
	case biz.StatusAwaitingPeer:
		return tradev1.OrderStatus_ORDER_STATUS_AWAITING_PEER
	case biz.StatusCompleted:
		return tradev1.OrderStatus_ORDER_STATUS_COMPLETED
	case biz.StatusRejected:
		return tradev1.OrderStatus_ORDER_STATUS_REJECTED
	case biz.StatusCancelled:
		return tradev1.OrderStatus_ORDER_STATUS_CANCELLED
	case biz.StatusExpired:
		return tradev1.OrderStatus_ORDER_STATUS_EXPIRED
	default:
		return tradev1.OrderStatus_ORDER_STATUS_UNSPECIFIED
	}
}

func orderStatusFromProto(s tradev1.OrderStatus) (uint8, bool) {
	switch s {
	case tradev1.OrderStatus_ORDER_STATUS_PENDING_SELLER:
		return biz.StatusPendingSeller, true
	case tradev1.OrderStatus_ORDER_STATUS_ACCEPTED:
		return biz.StatusAccepted, true
	case tradev1.OrderStatus_ORDER_STATUS_AWAITING_PEER:
		return biz.StatusAwaitingPeer, true
	case tradev1.OrderStatus_ORDER_STATUS_COMPLETED:
		return biz.StatusCompleted, true
	case tradev1.OrderStatus_ORDER_STATUS_REJECTED:
		return biz.StatusRejected, true
	case tradev1.OrderStatus_ORDER_STATUS_CANCELLED:
		return biz.StatusCancelled, true
	case tradev1.OrderStatus_ORDER_STATUS_EXPIRED:
		return biz.StatusExpired, true
	default:
		return 0, false
	}
}

func orderToProto(o *biz.Order) *tradev1.Order {
	if o == nil {
		return nil
	}
	return &tradev1.Order{
		OrderId:    o.ID,
		ProductId:  o.ProductID,
		BuyerId:    o.BuyerID,
		SellerId:   o.SellerID,
		PriceCents: o.PriceCents,
		// 支付方式恒为线下面交。
		PayChannel:      tradev1.PayChannel_PAY_CHANNEL_OFFLINE,
		Status:          orderStatusToProto(o.Status),
		MeetPoiId:       o.MeetPOIID,
		MeetTime:        unixPtr(o.MeetTime),
		BuyerMessage:    o.BuyerMessage,
		BuyerConfirmed:  o.BuyerConfirmed,
		SellerConfirmed: o.SellerConfirmed,
		CancelReason:    tradev1.CancelReason(o.CancelReason),
		CancelRemark:    o.CancelRemark,
		CancelledBy:     o.CancelledBy,
		CreatedAt:       unixOrZero(o.CreatedAt),
		AcceptedAt:      unixPtr(o.AcceptedAt),
		CompletedAt:     unixPtr(o.CompletedAt),
		ExpireAt:        unixPtr(o.ExpireAt),
	}
}

func ordersToProto(os []*biz.Order) []*tradev1.Order {
	out := make([]*tradev1.Order, 0, len(os))
	for _, o := range os {
		out = append(out, orderToProto(o))
	}
	return out
}

func reviewToProto(r *biz.Review) *tradev1.Review {
	if r == nil {
		return nil
	}
	out := &tradev1.Review{
		ReviewId:  r.ID,
		OrderId:   r.OrderID,
		ProductId: r.ProductID,
		ToUserId:  r.ToUserID,
		Role:      tradev1.ReviewRole(r.Role),
		Score:     r.Score,
		Content:   r.Content,
		Anonymous: r.Anonymous,
		CreatedAt: unixOrZero(r.CreatedAt),
	}
	// 匿名评价不回传评价者 ID，从源头切断关联。
	if !r.Anonymous {
		out.FromUserId = r.FromUserID
	}
	for _, t := range r.Tags {
		out.Tags = append(out.Tags, tradev1.ReviewTag(t))
	}
	return out
}

func reviewsToProto(rs []*biz.Review) []*tradev1.Review {
	out := make([]*tradev1.Review, 0, len(rs))
	for _, r := range rs {
		out = append(out, reviewToProto(r))
	}
	return out
}

func cursorRequest(r *commonv1.CursorRequest) (string, int) {
	if r == nil {
		return "", paging.DefaultLimit
	}
	return r.GetCursor(), int(r.GetLimit())
}

func cursorReply(next string, hasMore bool) *commonv1.CursorReply {
	return &commonv1.CursorReply{NextCursor: next, HasMore: hasMore}
}
