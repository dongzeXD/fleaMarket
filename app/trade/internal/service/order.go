package service

import (
	"context"
	"time"

	"github.com/fleamarket/app/trade/internal/biz"

	tradev1 "github.com/fleamarket/api/trade/v1"
	"github.com/fleamarket/pkg/auth"
)

// OrderService 实现 api/trade/v1 的 OrderService。
type OrderService struct {
	tradev1.UnimplementedOrderServiceServer

	uc     *biz.OrderUsecase
	review *biz.ReviewUsecase
}

// NewOrderService 构造服务。
func NewOrderService(uc *biz.OrderUsecase, review *biz.ReviewUsecase) *OrderService {
	return &OrderService{uc: uc, review: review}
}

// CreateOrder 发起预订。
func (s *OrderService) CreateOrder(ctx context.Context,
	req *tradev1.CreateOrderRequest) (*tradev1.Order, error) {

	uid, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}
	in := &biz.CreateOrderInput{
		ProductID: req.GetProductId(),
		MeetPOIID: req.GetMeetPoiId(),
		Message:   req.GetMessage(),
	}
	if t := req.GetMeetTime(); t > 0 {
		mt := time.Unix(t, 0)
		in.MeetTime = &mt
	}
	o, err := s.uc.Create(ctx, uid, in)
	if err != nil {
		return nil, err
	}
	return orderToProto(o), nil
}

// GetOrder 查看订单详情。
func (s *OrderService) GetOrder(ctx context.Context,
	req *tradev1.GetOrderRequest) (*tradev1.Order, error) {

	uid, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}
	o, err := s.uc.Get(ctx, req.GetOrderId(), uid)
	if err != nil {
		return nil, err
	}
	out := orderToProto(o)
	// 已完成的订单附带评价状态，供前端决定是否展示评价入口。
	if o.Status == biz.StatusCompleted {
		out.BuyerReviewed, out.SellerReviewed = s.review.ReviewedFlags(ctx, o.ID)
	}
	return out, nil
}

// AcceptOrder 卖家接受预订。
func (s *OrderService) AcceptOrder(ctx context.Context,
	req *tradev1.AcceptOrderRequest) (*tradev1.Order, error) {

	uid, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}
	var meetTime *time.Time
	if t := req.GetMeetTime(); t > 0 {
		mt := time.Unix(t, 0)
		meetTime = &mt
	}
	o, err := s.uc.Accept(ctx, req.GetOrderId(), uid, req.GetMeetPoiId(), meetTime)
	if err != nil {
		return nil, err
	}
	return orderToProto(o), nil
}

// RejectOrder 卖家拒绝预订。
func (s *OrderService) RejectOrder(ctx context.Context,
	req *tradev1.RejectOrderRequest) (*tradev1.Order, error) {

	uid, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}
	o, err := s.uc.Reject(ctx, req.GetOrderId(), uid, req.GetReason())
	if err != nil {
		return nil, err
	}
	return orderToProto(o), nil
}

// CancelOrder 取消订单。
func (s *OrderService) CancelOrder(ctx context.Context,
	req *tradev1.CancelOrderRequest) (*tradev1.Order, error) {

	uid, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}
	o, err := s.uc.Cancel(ctx, req.GetOrderId(), uid,
		uint8(req.GetReason()), req.GetRemark())
	if err != nil {
		return nil, err
	}
	return orderToProto(o), nil
}

// UpdateMeetup 修改面交约定。
func (s *OrderService) UpdateMeetup(ctx context.Context,
	req *tradev1.UpdateMeetupRequest) (*tradev1.Order, error) {

	uid, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}
	o, err := s.uc.UpdateMeetup(ctx, req.GetOrderId(), uid,
		req.GetMeetPoiId(), time.Unix(req.GetMeetTime(), 0))
	if err != nil {
		return nil, err
	}
	return orderToProto(o), nil
}

// ConfirmDone 确认交易完成。
func (s *OrderService) ConfirmDone(ctx context.Context,
	req *tradev1.ConfirmDoneRequest) (*tradev1.Order, error) {

	uid, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}
	o, err := s.uc.ConfirmDone(ctx, req.GetOrderId(), uid)
	if err != nil {
		return nil, err
	}
	return orderToProto(o), nil
}

// ListMyOrders 我的交易记录。
func (s *OrderService) ListMyOrders(ctx context.Context,
	req *tradev1.ListMyOrdersRequest) (*tradev1.ListOrdersReply, error) {

	uid, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}
	statuses := make([]uint8, 0, len(req.GetStatuses()))
	for _, st := range req.GetStatuses() {
		if v, ok := orderStatusFromProto(st); ok {
			statuses = append(statuses, v)
		}
	}
	cursor, limit := cursorRequest(req.GetPage())

	page, err := s.uc.ListMine(ctx, uid, uint8(req.GetRole()), statuses, cursor, limit)
	if err != nil {
		return nil, err
	}
	return &tradev1.ListOrdersReply{
		Orders: ordersToProto(page.Items),
		Page:   cursorReply(page.NextCursor, page.HasMore),
	}, nil
}

// CountUserTrades 统计成交数。内部接口，供 user-service 聚合主页。
func (s *OrderService) CountUserTrades(ctx context.Context,
	req *tradev1.CountUserTradesRequest) (*tradev1.CountUserTradesReply, error) {

	bought, sold, err := s.uc.CountTrades(ctx, req.GetUserId())
	if err != nil {
		return nil, err
	}
	return &tradev1.CountUserTradesReply{BoughtCount: bought, SoldCount: sold}, nil
}

// requireLogin 取出当前登录用户 ID。
func requireLogin(ctx context.Context) (int64, error) {
	uid := auth.MustUserID(ctx)
	if uid <= 0 {
		return 0, auth.ErrUnauthenticated("请先登录")
	}
	return uid, nil
}

var _ tradev1.OrderServiceServer = (*OrderService)(nil)
