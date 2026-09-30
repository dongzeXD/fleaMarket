package biz

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/fleamarket/pkg/errs"
	"github.com/fleamarket/pkg/idgen"
	"github.com/fleamarket/pkg/paging"
	"github.com/fleamarket/pkg/statemachine"
)

// 订单状态机事件。
type orderEvent string

const (
	evAccept   orderEvent = "accept"
	evReject   orderEvent = "reject"
	evCancel   orderEvent = "cancel"
	evSelfDone orderEvent = "self_done"
	evPeerDone orderEvent = "peer_done"
	evTimeout  orderEvent = "timeout"
)

// orderMachine 订单状态流转（doc.md §9.4）。
//
// 几条刻意的约束：
//   - PENDING_SELLER 不能直接 self_done：卖家还没接受就说交易完成了，
//     说明流程被跳过，此时应先接受再确认；
//   - AWAITING_PEER 仍可 cancel：面交现场发现实物与描述不符时，
//     即便一方已点了完成，另一方也应能取消；
//   - COMPLETED 是终态，不可回退——已完成的交易关联着评价与信誉分。
var orderMachine = statemachine.New[uint8, orderEvent]().
	Allow(StatusPendingSeller, evAccept, StatusAccepted).
	Allow(StatusPendingSeller, evReject, StatusRejected).
	Allow(StatusPendingSeller, evCancel, StatusCancelled).
	Allow(StatusPendingSeller, evTimeout, StatusExpired).
	Allow(StatusAccepted, evSelfDone, StatusAwaitingPeer).
	Allow(StatusAccepted, evCancel, StatusCancelled).
	Allow(StatusAccepted, evTimeout, StatusExpired).
	Allow(StatusAwaitingPeer, evPeerDone, StatusCompleted).
	Allow(StatusAwaitingPeer, evCancel, StatusCancelled).
	// 超时自动完成而非关闭：一方已确认说明交易大概率已发生，
	// 按未完成处理会让卖家白白失去成交记录。
	Allow(StatusAwaitingPeer, evTimeout, StatusCompleted)

// OrderUsecase 订单用例。
type OrderUsecase struct {
	orders   OrderRepo
	reviews  ReviewRepo
	products ProductGateway
	users    UserGateway
	locker   Locker
	ids      *idgen.Generator
	events   EventPublisher
	log      *slog.Logger
}

// NewOrderUsecase 构造用例。
func NewOrderUsecase(orders OrderRepo, reviews ReviewRepo, products ProductGateway,
	users UserGateway, locker Locker, ids *idgen.Generator,
	events EventPublisher, logger *slog.Logger) *OrderUsecase {

	return &OrderUsecase{
		orders: orders, reviews: reviews, products: products, users: users,
		locker: locker, ids: ids, events: events,
		log: logger.With(slog.String("usecase", "order")),
	}
}

// CreateOrderInput 发起预订的输入。
type CreateOrderInput struct {
	ProductID int64
	MeetPOIID int64
	MeetTime  *time.Time
	Message   string
}

const (
	// lockTTL 抢占商品时持锁的时长。
	//
	// 覆盖「调 product-service + 写订单」这两步的耗时即可。
	// 设得过长会在持锁进程崩溃时长时间阻塞其他买家。
	lockTTL = 5 * time.Second
	// lockWait 等待锁的时长。
	//
	// 短等待是刻意的：抢同一件商品的并发通常只有两三个人，
	// 让后来者快速失败并看到「已被预订」，比让他等半秒再失败体验更好。
	lockWait = 300 * time.Millisecond
)

// Create 发起预订。
//
// 完整流程：
//  1. 取分布式锁（按商品维度），把并发收敛为串行；
//  2. 检查是否已有进行中的订单；
//  3. 生成订单 ID，调 product-service 原子抢占商品；
//  4. 抢占成功后写订单。写库失败要回滚抢占，否则商品会永久卡在「已预订」。
//
// 锁只是优化手段，真正的并发安全由两处保证：
// product-service 的 CAS 更新，以及订单表的 uk_product_active 唯一索引。
// 即便 Redis 整体不可用，也不会出现两个并存的有效订单。
func (uc *OrderUsecase) Create(ctx context.Context, buyerID int64,
	in *CreateOrderInput) (*Order, error) {

	unlock, err := uc.locker.Lock(ctx, orderLockKey(in.ProductID), lockTTL, lockWait)
	if err != nil {
		// 拿不到锁说明有人正在抢同一件商品。
		// 直接告知「已被预订」而非让用户等待——多半确实抢不到了。
		return nil, errs.Conflict("商品正在被其他同学预订，请稍后刷新查看")
	}
	defer func() {
		if unlock != nil {
			if err := unlock(ctx); err != nil {
				uc.log.WarnContext(ctx, "释放预订锁失败", slog.Any("error", err))
			}
		}
	}()

	// 已有进行中的订单时直接拒绝，避免白跑一次 product-service 调用。
	if existing, err := uc.orders.FindActiveByProduct(ctx, in.ProductID); err == nil && existing != nil {
		if existing.BuyerID == buyerID {
			return nil, errs.Conflict("你已预订该商品，请在订单列表中查看")
		}
		return nil, errs.Conflict("商品已被其他同学预订")
	}

	orderID, err := uc.ids.NextID()
	if err != nil {
		return nil, errs.Internal("ID 生成失败")
	}

	lock, err := uc.products.LockForOrder(ctx, in.ProductID, orderID, buyerID)
	if err != nil {
		return nil, errs.FromDataError(err, "商品不存在")
	}
	if !lock.Locked {
		return nil, uc.lockFailureError(lock.CurrentStatus)
	}

	expireAt := time.Now().Add(PendingSellerTTL)
	o := &Order{
		ID:           orderID,
		ProductID:    in.ProductID,
		BuyerID:      buyerID,
		SellerID:     lock.SellerID,
		PriceCents:   lock.PriceCents,
		PayChannel:   PayChannelOffline,
		Status:       StatusPendingSeller,
		MeetPOIID:    in.MeetPOIID,
		MeetTime:     in.MeetTime,
		BuyerMessage: in.Message,
		ExpireAt:     &expireAt,
	}

	created, err := uc.orders.Create(ctx, o)
	if err != nil {
		// 写订单失败必须回滚商品抢占，否则商品永久停留在「已预订」，
		// 卖家和买家都无法再操作它。
		if _, rerr := uc.products.ReleaseOrderLock(ctx, in.ProductID, orderID); rerr != nil {
			uc.log.ErrorContext(ctx, "订单创建失败后回滚商品锁定也失败，商品可能卡在已预订状态",
				slog.Int64("product_id", in.ProductID),
				slog.Int64("order_id", orderID),
				slog.Any("error", rerr))
		}
		if errors.Is(err, errs.ErrAlreadyExists) {
			return nil, errs.Conflict("商品已被其他同学预订")
		}
		return nil, errs.FromDataError(err, "")
	}

	uc.publish(ctx, TopicOrderCreated, created)
	return created, nil
}

// lockFailureError 把商品当前状态转成对用户有意义的提示。
func (uc *OrderUsecase) lockFailureError(productStatus uint8) error {
	// 商品状态常量与 product-service 一致。
	const (
		pOnSale   uint8 = 2
		pReserved uint8 = 3
		pSold     uint8 = 4
		pOffline  uint8 = 5
	)
	switch productStatus {
	case pReserved:
		return errs.Conflict("商品已被其他同学预订")
	case pSold:
		return errs.Conflict("商品已售出")
	case pOffline:
		return errs.Conflict("商品已下架")
	case pOnSale:
		// 状态是在售却抢占失败，说明刚好被并发抢走。
		return errs.Conflict("商品刚被其他同学预订，请刷新后重试")
	default:
		return errs.Conflict("商品当前不可预订")
	}
}

// Get 查看订单详情。仅买卖双方可见。
func (uc *OrderUsecase) Get(ctx context.Context, orderID, userID int64) (*Order, error) {
	o, err := uc.orders.GetByID(ctx, orderID)
	if err != nil {
		return nil, errs.FromDataError(err, "订单不存在")
	}
	if !o.IsParticipant(userID) {
		// 返回 404 而非 403：订单 ID 是雪花 ID，返回 403 等于确认
		// 「这个 ID 对应一个真实订单」，可被用来探测交易活跃度。
		return nil, errs.NotFound("订单不存在")
	}
	return o, nil
}

// Accept 卖家接受预订。
func (uc *OrderUsecase) Accept(ctx context.Context, orderID, sellerID int64,
	poiID int64, meetTime *time.Time) (*Order, error) {

	o, err := uc.orders.GetByID(ctx, orderID)
	if err != nil {
		return nil, errs.FromDataError(err, "订单不存在")
	}
	if o.SellerID != sellerID {
		return nil, errs.PermissionDenied("只有卖家可以接受预订")
	}

	next, err := orderMachine.Next(o.Status, evAccept)
	if err != nil {
		return nil, errs.Conflict("当前状态不支持该操作")
	}

	now := time.Now()
	expireAt := now.Add(AcceptedTTL)
	patch := &OrderPatch{AcceptedAt: &now, ExpireAt: &expireAt}
	if poiID > 0 {
		patch.MeetPOIID = &poiID
	}
	if meetTime != nil {
		patch.MeetTime = meetTime
	}

	ok, err := uc.orders.UpdateStatus(ctx, orderID, o.Status, next, patch, false)
	if err != nil {
		return nil, errs.FromDataError(err, "")
	}
	if !ok {
		return nil, errs.Conflict("订单状态已变更，请刷新后重试")
	}

	o.Status = next
	o.AcceptedAt = &now
	o.ExpireAt = &expireAt
	uc.publish(ctx, TopicOrderAccepted, o)
	return o, nil
}

// Reject 卖家拒绝预订。
func (uc *OrderUsecase) Reject(ctx context.Context, orderID, sellerID int64,
	reason string) (*Order, error) {

	o, err := uc.orders.GetByID(ctx, orderID)
	if err != nil {
		return nil, errs.FromDataError(err, "订单不存在")
	}
	if o.SellerID != sellerID {
		return nil, errs.PermissionDenied("只有卖家可以拒绝预订")
	}
	return uc.closeOrder(ctx, o, evReject, sellerID, 0, reason)
}

// Cancel 取消订单。买卖双方均可发起。
func (uc *OrderUsecase) Cancel(ctx context.Context, orderID, userID int64,
	reason uint8, remark string) (*Order, error) {

	o, err := uc.orders.GetByID(ctx, orderID)
	if err != nil {
		return nil, errs.FromDataError(err, "订单不存在")
	}
	if !o.IsParticipant(userID) {
		return nil, errs.NotFound("订单不存在")
	}

	// 买家在卖家已接受后取消要扣信誉分：他占用了商品又不履约，
	// 卖家为此拒绝了其他买家。不扣分会让「随手预订再随手取消」零成本。
	penalize := userID == o.BuyerID && o.Status != StatusPendingSeller

	closed, err := uc.closeOrder(ctx, o, evCancel, userID, reason, remark)
	if err != nil {
		return nil, err
	}

	if penalize {
		if err := uc.users.AdjustCredit(ctx, o.BuyerID, CreditReasonOrderAbandoned, o.ID); err != nil {
			// 信誉分调整失败不回滚取消：订单已关闭，
			// 分数可由对账任务按订单记录补偿。
			uc.log.WarnContext(ctx, "扣减信誉分失败",
				slog.Int64("order_id", o.ID), slog.Any("error", err))
		}
	}
	return closed, nil
}

// closeOrder 把订单转入终态并释放商品。
func (uc *OrderUsecase) closeOrder(ctx context.Context, o *Order, ev orderEvent,
	actorID int64, reason uint8, remark string) (*Order, error) {

	next, err := orderMachine.Next(o.Status, ev)
	if err != nil {
		return nil, errs.Conflict("当前状态不支持该操作")
	}

	patch := &OrderPatch{
		CancelReason: &reason,
		CancelRemark: &remark,
		CancelledBy:  &actorID,
	}
	// 终态清空 active_key，释放「同一商品只能有一个进行中订单」的占用。
	ok, err := uc.orders.UpdateStatus(ctx, o.ID, o.Status, next, patch, true)
	if err != nil {
		return nil, errs.FromDataError(err, "")
	}
	if !ok {
		return nil, errs.Conflict("订单状态已变更，请刷新后重试")
	}

	// 释放商品，使其恢复在售。
	//
	// 顺序是先改订单再放商品：反过来的话，放商品后改订单失败，
	// 会出现「商品在售但订单仍进行中」——另一个买家能预订成功，
	// 于是同一商品有了两个有效订单。
	if _, err := uc.products.ReleaseOrderLock(ctx, o.ProductID, o.ID); err != nil {
		uc.log.ErrorContext(ctx, "释放商品锁定失败，商品可能卡在已预订状态",
			slog.Int64("product_id", o.ProductID),
			slog.Int64("order_id", o.ID), slog.Any("error", err))
	}

	o.Status = next
	o.CancelReason = reason
	o.CancelRemark = remark
	o.CancelledBy = actorID
	uc.publish(ctx, TopicOrderClosed, o)
	return o, nil
}

// UpdateMeetup 修改面交约定。
func (uc *OrderUsecase) UpdateMeetup(ctx context.Context, orderID, userID int64,
	poiID int64, at time.Time) (*Order, error) {

	o, err := uc.orders.GetByID(ctx, orderID)
	if err != nil {
		return nil, errs.FromDataError(err, "订单不存在")
	}
	if !o.IsParticipant(userID) {
		return nil, errs.NotFound("订单不存在")
	}
	// 只有进行中的订单能改约定。已完成或已取消的订单改时间毫无意义。
	if o.Status != StatusPendingSeller && o.Status != StatusAccepted {
		return nil, errs.Conflict("当前状态无法修改面交约定")
	}
	// 约定时间必须在未来：允许过去时间会让「已过期的约定」看起来仍有效。
	if at.Before(time.Now()) {
		return nil, errs.InvalidArgument("面交时间不能早于当前时间")
	}

	if err := uc.orders.UpdateMeetup(ctx, orderID, poiID, at); err != nil {
		return nil, errs.FromDataError(err, "")
	}
	o.MeetPOIID = poiID
	o.MeetTime = &at
	return o, nil
}

// ConfirmDone 确认交易完成。
//
// 需双方都确认。单方确认后进入 AWAITING_PEER，
// 72 小时内对方未确认则由定时任务自动完成。
func (uc *OrderUsecase) ConfirmDone(ctx context.Context, orderID, userID int64) (*Order, error) {
	o, err := uc.orders.GetByID(ctx, orderID)
	if err != nil {
		return nil, errs.FromDataError(err, "订单不存在")
	}
	role := o.RoleOf(userID)
	if role == 0 {
		return nil, errs.NotFound("订单不存在")
	}
	if o.Status != StatusAccepted && o.Status != StatusAwaitingPeer {
		return nil, errs.Conflict("当前状态无法确认完成")
	}

	// 重复确认视为成功：用户可能因网络重试而点两次。
	if (role == ReviewRoleBuyer && o.BuyerConfirmed) ||
		(role == ReviewRoleSeller && o.SellerConfirmed) {
		return o, nil
	}

	buyerDone, sellerDone, err := uc.orders.MarkConfirmed(ctx, orderID, role)
	if err != nil {
		return nil, errs.FromDataError(err, "")
	}
	o.BuyerConfirmed, o.SellerConfirmed = buyerDone, sellerDone

	if buyerDone && sellerDone {
		return uc.completeOrder(ctx, o)
	}

	// 单方确认：转入等待对方，并设置自动完成的超时。
	if o.Status == StatusAccepted {
		next, err := orderMachine.Next(o.Status, evSelfDone)
		if err != nil {
			return nil, errs.Conflict("当前状态不支持该操作")
		}
		expireAt := time.Now().Add(AwaitingPeerTTL)
		ok, err := uc.orders.UpdateStatus(ctx, orderID, o.Status, next,
			&OrderPatch{ExpireAt: &expireAt}, false)
		if err != nil {
			return nil, errs.FromDataError(err, "")
		}
		if ok {
			o.Status = next
			o.ExpireAt = &expireAt
		}
	}
	return o, nil
}

// completeOrder 把订单转为已完成并结算副作用。
func (uc *OrderUsecase) completeOrder(ctx context.Context, o *Order) (*Order, error) {
	next := StatusCompleted
	now := time.Now()
	// 完成时清空 active_key：商品已售出，不再是「进行中订单」。
	ok, err := uc.orders.UpdateStatus(ctx, o.ID, o.Status, next,
		&OrderPatch{CompletedAt: &now}, true)
	if err != nil {
		return nil, errs.FromDataError(err, "")
	}
	if !ok {
		return nil, errs.Conflict("订单状态已变更，请刷新后重试")
	}
	o.Status = next
	o.CompletedAt = &now

	// 标记商品已售出。失败不回滚订单：订单完成是双方意志的结果，
	// 商品状态可由对账任务按订单补齐。
	if _, err := uc.products.MarkSold(ctx, o.ProductID, o.ID); err != nil {
		uc.log.ErrorContext(ctx, "标记商品已售出失败",
			slog.Int64("product_id", o.ProductID), slog.Any("error", err))
	}

	// 双方各加一次「完成交易」的信誉分。
	for _, uid := range []int64{o.BuyerID, o.SellerID} {
		if err := uc.users.AdjustCredit(ctx, uid, CreditReasonTradeCompleted, o.ID); err != nil {
			uc.log.WarnContext(ctx, "增加完成交易信誉分失败",
				slog.Int64("user_id", uid), slog.Any("error", err))
		}
	}

	uc.publish(ctx, TopicOrderCompleted, o)
	return o, nil
}

// ListMine 我的交易记录。
func (uc *OrderUsecase) ListMine(ctx context.Context, userID int64, role uint8,
	statuses []uint8, cursor string, limit int) (*paging.Page[*Order], error) {

	limit = paging.NormalizeLimit(limit)
	items, next, hasMore, err := uc.orders.ListByUser(ctx, userID, role, statuses, cursor, limit)
	if err != nil {
		return nil, errs.FromDataError(err, "")
	}
	return &paging.Page[*Order]{Items: items, NextCursor: next, HasMore: hasMore}, nil
}

// CountTrades 统计完成的交易数。
func (uc *OrderUsecase) CountTrades(ctx context.Context, userID int64) (int32, int32, error) {
	bought, sold, err := uc.orders.CountCompletedByUser(ctx, userID)
	if err != nil {
		return 0, 0, errs.FromDataError(err, "")
	}
	return bought, sold, nil
}

// CloseExpired 处理超时订单，由定时任务调用。
//
// 不同状态的超时有不同归宿：
//   - 待卖家确认 / 已接受 → EXPIRED，释放商品；
//   - 等待对方确认 → COMPLETED，因为一方已确认说明交易大概率已发生。
func (uc *OrderUsecase) CloseExpired(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		limit = 100
	}
	orders, err := uc.orders.ListExpired(ctx, time.Now(), limit)
	if err != nil {
		return 0, errs.FromDataError(err, "")
	}

	handled := 0
	for _, o := range orders {
		next, err := orderMachine.Next(o.Status, evTimeout)
		if err != nil {
			continue
		}

		if next == StatusCompleted {
			if _, err := uc.completeOrder(ctx, o); err != nil {
				uc.log.WarnContext(ctx, "超时自动完成失败",
					slog.Int64("order_id", o.ID), slog.Any("error", err))
				continue
			}
			handled++
			continue
		}

		// 超时关闭：这里不走 closeOrder，因为超时不属于任何一方的取消行为，
		// cancelled_by 应为 0 以区分「主动取消」与「系统超时」。
		var zero uint8
		empty := ""
		var system int64
		ok, err := uc.orders.UpdateStatus(ctx, o.ID, o.Status, next,
			&OrderPatch{CancelReason: &zero, CancelRemark: &empty, CancelledBy: &system}, true)
		if err != nil || !ok {
			continue
		}
		if _, err := uc.products.ReleaseOrderLock(ctx, o.ProductID, o.ID); err != nil {
			uc.log.ErrorContext(ctx, "超时释放商品锁定失败",
				slog.Int64("product_id", o.ProductID), slog.Any("error", err))
		}
		o.Status = next
		uc.publish(ctx, TopicOrderClosed, o)
		handled++
	}
	return handled, nil
}

// AllowedActions 返回某用户在当前状态下可执行的操作。
//
// 由服务端计算：状态规则若在前端复制一份，调整时两边必然不同步。
func (uc *OrderUsecase) AllowedActions(o *Order, userID int64) []string {
	role := o.RoleOf(userID)
	if role == 0 {
		return nil
	}
	var out []string
	for _, ev := range orderMachine.AllowedEvents(o.Status) {
		switch ev {
		case evAccept, evReject:
			// 只有卖家能接受或拒绝。
			if role == ReviewRoleSeller {
				out = append(out, string(ev))
			}
		case evCancel:
			out = append(out, "cancel")
		case evSelfDone, evPeerDone:
			// 已确认过的一方不再展示「确认完成」。
			if (role == ReviewRoleBuyer && !o.BuyerConfirmed) ||
				(role == ReviewRoleSeller && !o.SellerConfirmed) {
				out = append(out, "confirm_done")
			}
		case evTimeout:
			// 超时由系统触发，不对用户暴露。
		}
	}
	return out
}

func orderLockKey(productID int64) string {
	return "order:product:" + itoa(productID)
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func (uc *OrderUsecase) publish(ctx context.Context, topic string, o *Order) {
	if uc.events == nil || o == nil {
		return
	}
	payload, err := json.Marshal(map[string]any{
		"order_id":    o.ID,
		"product_id":  o.ProductID,
		"buyer_id":    o.BuyerID,
		"seller_id":   o.SellerID,
		"status":      o.Status,
		"price_cents": o.PriceCents,
	})
	if err != nil {
		return
	}
	if err := uc.events.Publish(ctx, topic, payload); err != nil {
		uc.log.WarnContext(ctx, "发布订单事件失败",
			slog.String("topic", topic), slog.Any("error", err))
	}
}
