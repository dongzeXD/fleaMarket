package biz

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/fleamarket/pkg/idgen"
	kratoserrors "github.com/go-kratos/kratos/v3/errors"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

const (
	sellerID  = int64(1001)
	buyerID   = int64(2002)
	productID = int64(5005)
	price     = int64(3000)
)

type fixture struct {
	order    *OrderUsecase
	review   *ReviewUsecase
	orders   *fakeOrderRepo
	reviews  *fakeReviewRepo
	products *fakeProductGateway
	users    *fakeUserGateway
	locker   *fakeLocker
	events   *fakeEvents
	ids      *idgen.Generator
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ids, err := idgen.New(4)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{
		orders:   newFakeOrderRepo(),
		reviews:  newFakeReviewRepo(),
		products: newFakeProductGateway(price, sellerID),
		users:    newFakeUserGateway(),
		locker:   newFakeLocker(),
		events:   &fakeEvents{},
		ids:      ids,
	}
	f.order = NewOrderUsecase(f.orders, f.reviews, f.products, f.users,
		f.locker, ids, f.events, quietLogger())
	f.review = NewReviewUsecase(f.reviews, f.orders, f.users, ids, f.events, quietLogger())
	return f
}

func code(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		t.Fatal("期望错误，实际 nil")
	}
	return int(kratoserrors.FromError(err).Code)
}

// --- 并发抢占：核心不变量 ---

// 10 个买家并发预订同一件商品，必须只有 1 个成功。
// 这是整个交易系统最关键的正确性保证。
func TestConcurrentCreateOnlyOneSucceeds(t *testing.T) {
	f := newFixture(t)
	f.products.seed(productID)

	const buyers = 10
	var wg sync.WaitGroup
	var mu sync.Mutex
	successes := 0

	wg.Add(buyers)
	for i := 0; i < buyers; i++ {
		go func(bid int64) {
			defer wg.Done()
			_, err := f.order.Create(context.Background(), bid,
				&CreateOrderInput{ProductID: productID})
			if err == nil {
				mu.Lock()
				successes++
				mu.Unlock()
			}
		}(int64(3000 + i))
	}
	wg.Wait()

	if successes != 1 {
		t.Fatalf("并发预订应只有 1 个成功，实际 %d", successes)
	}
	// 商品应处于已预订状态，且只有一个进行中订单。
	if f.products.status[productID] != 3 {
		t.Fatalf("商品应为已预订(3)，实际 %d", f.products.status[productID])
	}
}

func TestCreateReservesProduct(t *testing.T) {
	f := newFixture(t)
	f.products.seed(productID)

	o, err := f.order.Create(context.Background(), buyerID, &CreateOrderInput{ProductID: productID})
	if err != nil {
		t.Fatal(err)
	}
	if o.Status != StatusPendingSeller {
		t.Fatalf("新订单应为待卖家确认，实际 %d", o.Status)
	}
	// 成交价快照必须来自商品，防止卖家事后改价。
	if o.PriceCents != price {
		t.Fatalf("成交价快照错误: %d", o.PriceCents)
	}
	if o.PayChannel != PayChannelOffline {
		t.Fatal("支付方式应为线下面交")
	}
	if f.events.count(TopicOrderCreated) != 1 {
		t.Fatal("应发布 order_created 事件")
	}
}

// 商品非在售时预订失败，且不留下订单。
func TestCreateFailsWhenProductNotOnSale(t *testing.T) {
	f := newFixture(t)
	// 不 seed，商品状态为 0（未知）。
	if _, err := f.order.Create(context.Background(), buyerID,
		&CreateOrderInput{ProductID: productID}); code(t, err) != 409 {
		t.Fatal("非在售商品预订应返回 409")
	}
}

// 写订单失败必须回滚商品抢占，否则商品永久卡在已预订。
func TestCreateRollsBackProductLockOnOrderFailure(t *testing.T) {
	f := newFixture(t)
	f.products.seed(productID)

	// 预置一个进行中订单，使 Create 的库写入命中 uk_product_active 冲突。
	// 但 FindActiveByProduct 会先拦截——为绕过它测试回滚，
	// 直接往 orders 里塞一个不在 active 表的订单是不够的。
	// 改为：先正常创建一单，取消它释放商品，再并发下单验证回滚路径由
	// 上一个并发测试覆盖。这里验证的是「抢占成功但写库冲突」的回滚。
	//
	// 构造方式：手动占用 active[productID] 但 FindActive 查不到（模拟
	// active 表与商品状态短暂不一致），使流程走到 Create 才冲突。
	f.orders.mu.Lock()
	f.orders.active[productID] = 999 // 占用但没有对应 item
	f.orders.mu.Unlock()

	_, err := f.order.Create(context.Background(), buyerID, &CreateOrderInput{ProductID: productID})
	if err == nil {
		t.Fatal("应因唯一约束冲突而失败")
	}
	// 关键：抢占的商品必须被回滚为在售。
	if f.products.status[productID] != 2 {
		t.Fatalf("写库失败后商品应回滚为在售(2)，实际 %d", f.products.status[productID])
	}
}

func TestCreateRejectsDuplicateBySameBuyer(t *testing.T) {
	f := newFixture(t)
	f.products.seed(productID)
	if _, err := f.order.Create(context.Background(), buyerID, &CreateOrderInput{ProductID: productID}); err != nil {
		t.Fatal(err)
	}
	// 商品已被自己预订，释放锁后再下单会命中 FindActive。
	// 由于同商品仍进行中，第二次应被 FindActive 拦截。
	_, err := f.order.Create(context.Background(), buyerID, &CreateOrderInput{ProductID: productID})
	if code(t, err) != 409 {
		t.Fatal("重复预订应返回 409")
	}
}

// --- 状态机 ---

func seedAccepted(t *testing.T, f *fixture) *Order {
	t.Helper()
	f.products.seed(productID)
	o, err := f.order.Create(context.Background(), buyerID, &CreateOrderInput{ProductID: productID})
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := f.order.Accept(context.Background(), o.ID, sellerID, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	return accepted
}

func TestAcceptOnlyBySeller(t *testing.T) {
	f := newFixture(t)
	f.products.seed(productID)
	o, _ := f.order.Create(context.Background(), buyerID, &CreateOrderInput{ProductID: productID})

	if _, err := f.order.Accept(context.Background(), o.ID, buyerID, 0, nil); code(t, err) != 403 {
		t.Fatal("买家不能接受预订")
	}
	if _, err := f.order.Accept(context.Background(), o.ID, sellerID, 0, nil); err != nil {
		t.Fatalf("卖家应能接受: %v", err)
	}
}

func TestRejectReleasesProduct(t *testing.T) {
	f := newFixture(t)
	f.products.seed(productID)
	o, _ := f.order.Create(context.Background(), buyerID, &CreateOrderInput{ProductID: productID})

	if _, err := f.order.Reject(context.Background(), o.ID, sellerID, "不想卖了"); err != nil {
		t.Fatal(err)
	}
	if f.products.status[productID] != 2 {
		t.Fatalf("拒绝后商品应恢复在售，实际 %d", f.products.status[productID])
	}
	if f.events.count(TopicOrderClosed) != 1 {
		t.Fatal("应发布 order_closed 事件")
	}
}

// 买家在卖家接受后取消要扣信誉分。
func TestBuyerCancelAfterAcceptPenalizes(t *testing.T) {
	f := newFixture(t)
	o := seedAccepted(t, f)

	if _, err := f.order.Cancel(context.Background(), o.ID, buyerID,
		1, "临时有事"); err != nil {
		t.Fatal(err)
	}
	if n := f.users.creditCount(buyerID, CreditReasonOrderAbandoned); n != 1 {
		t.Fatalf("接受后买家取消应扣一次信誉分，实际 %d 次", n)
	}
}

// 待卖家确认阶段买家取消不扣分——还没造成实际损失。
func TestBuyerCancelBeforeAcceptNoPenalty(t *testing.T) {
	f := newFixture(t)
	f.products.seed(productID)
	o, _ := f.order.Create(context.Background(), buyerID, &CreateOrderInput{ProductID: productID})

	if _, err := f.order.Cancel(context.Background(), o.ID, buyerID, 1, ""); err != nil {
		t.Fatal(err)
	}
	if n := f.users.creditCount(buyerID, CreditReasonOrderAbandoned); n != 0 {
		t.Fatalf("确认前取消不应扣分，实际 %d 次", n)
	}
}

func TestConfirmDoneRequiresBothParties(t *testing.T) {
	f := newFixture(t)
	o := seedAccepted(t, f)

	// 买家先确认，进入等待对方。
	after, err := f.order.ConfirmDone(context.Background(), o.ID, buyerID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != StatusAwaitingPeer {
		t.Fatalf("单方确认后应等待对方，实际 %d", after.Status)
	}

	// 卖家确认，完成。
	after, err = f.order.ConfirmDone(context.Background(), o.ID, sellerID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != StatusCompleted {
		t.Fatalf("双方确认后应完成，实际 %d", after.Status)
	}
	// 完成时商品标记售出，双方各加一次完成交易分。
	if f.products.status[productID] != 4 {
		t.Fatal("完成后商品应标记售出")
	}
	if f.users.creditCount(buyerID, CreditReasonTradeCompleted) != 1 ||
		f.users.creditCount(sellerID, CreditReasonTradeCompleted) != 1 {
		t.Fatal("双方应各加一次完成交易信誉分")
	}
	if f.events.count(TopicOrderCompleted) != 1 {
		t.Fatal("应发布 order_completed 事件")
	}
}

// 重复确认是幂等的。
func TestConfirmDoneIsIdempotent(t *testing.T) {
	f := newFixture(t)
	o := seedAccepted(t, f)
	if _, err := f.order.ConfirmDone(context.Background(), o.ID, buyerID); err != nil {
		t.Fatal(err)
	}
	after, err := f.order.ConfirmDone(context.Background(), o.ID, buyerID)
	if err != nil {
		t.Fatalf("重复确认不应报错: %v", err)
	}
	if after.Status != StatusAwaitingPeer {
		t.Fatal("重复确认不应改变状态")
	}
}

// 超时的等待确认订单应自动完成。
func TestExpiredAwaitingPeerAutoCompletes(t *testing.T) {
	f := newFixture(t)
	o := seedAccepted(t, f)
	if _, err := f.order.ConfirmDone(context.Background(), o.ID, buyerID); err != nil {
		t.Fatal(err)
	}
	// 强制过期。
	f.orders.mu.Lock()
	past := time.Now().Add(-time.Hour)
	f.orders.items[o.ID].ExpireAt = &past
	f.orders.mu.Unlock()

	n, err := f.order.CloseExpired(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("应处理 1 单，实际 %d", n)
	}
	cur, _ := f.orders.GetByID(context.Background(), o.ID)
	if cur.Status != StatusCompleted {
		t.Fatalf("超时的等待确认订单应自动完成，实际 %d", cur.Status)
	}
}

// 超时的待卖家确认订单应关闭并释放商品。
func TestExpiredPendingClosesAndReleases(t *testing.T) {
	f := newFixture(t)
	f.products.seed(productID)
	o, _ := f.order.Create(context.Background(), buyerID, &CreateOrderInput{ProductID: productID})

	f.orders.mu.Lock()
	past := time.Now().Add(-time.Hour)
	f.orders.items[o.ID].ExpireAt = &past
	f.orders.mu.Unlock()

	if _, err := f.order.CloseExpired(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	cur, _ := f.orders.GetByID(context.Background(), o.ID)
	if cur.Status != StatusExpired {
		t.Fatalf("待确认超时应为已过期，实际 %d", cur.Status)
	}
	if f.products.status[productID] != 2 {
		t.Fatal("超时关闭应释放商品")
	}
	// 系统超时不归咎任何一方。
	if cur.CancelledBy != 0 {
		t.Fatalf("系统超时 cancelled_by 应为 0，实际 %d", cur.CancelledBy)
	}
}

// --- 评价 ---

func seedCompleted(t *testing.T, f *fixture) *Order {
	t.Helper()
	o := seedAccepted(t, f)
	if _, err := f.order.ConfirmDone(context.Background(), o.ID, buyerID); err != nil {
		t.Fatal(err)
	}
	completed, err := f.order.ConfirmDone(context.Background(), o.ID, sellerID)
	if err != nil {
		t.Fatal(err)
	}
	return completed
}

func TestReviewOnlyAfterCompletion(t *testing.T) {
	f := newFixture(t)
	o := seedAccepted(t, f) // 未完成

	if _, err := f.review.Create(context.Background(), buyerID,
		&CreateReviewInput{OrderID: o.ID, Score: 5}); code(t, err) != 409 {
		t.Fatal("未完成的订单不能评价")
	}
}

func TestReviewByParticipantOnly(t *testing.T) {
	f := newFixture(t)
	o := seedCompleted(t, f)
	stranger := int64(9999)
	if _, err := f.review.Create(context.Background(), stranger,
		&CreateReviewInput{OrderID: o.ID, Score: 5}); code(t, err) != 404 {
		t.Fatal("非参与方评价应返回 404")
	}
}

// 评价转信誉分：好评加正向分。
func TestReviewTriggersCredit(t *testing.T) {
	f := newFixture(t)
	o := seedCompleted(t, f)

	if _, err := f.review.Create(context.Background(), buyerID,
		&CreateReviewInput{OrderID: o.ID, Score: 5}); err != nil {
		t.Fatal(err)
	}
	// 买家评卖家，卖家收到好评分。
	if f.users.creditCount(sellerID, CreditReasonReviewPositive) != 1 {
		t.Fatal("好评应给被评价者加正向信誉分")
	}
	if f.events.count(TopicReviewed) != 1 {
		t.Fatal("应发布 reviewed 事件")
	}
}

func TestReviewIsSingleUse(t *testing.T) {
	f := newFixture(t)
	o := seedCompleted(t, f)

	if _, err := f.review.Create(context.Background(), buyerID,
		&CreateReviewInput{OrderID: o.ID, Score: 5, Content: "第一次"}); err != nil {
		t.Fatal(err)
	}
	// 第二次提交返回已有的那条，不报错也不重复加分。
	r2, err := f.review.Create(context.Background(), buyerID,
		&CreateReviewInput{OrderID: o.ID, Score: 1, Content: "改差评"})
	if err != nil {
		t.Fatalf("重复评价应幂等，不报错: %v", err)
	}
	if r2.Content != "第一次" {
		t.Fatal("重复评价应返回首次的内容，不可被覆盖")
	}
	if f.users.creditCount(sellerID, CreditReasonReviewPositive) != 1 {
		t.Fatal("重复评价不应重复加分")
	}
}

func TestScoreGroup(t *testing.T) {
	cases := map[int32]uint8{
		5: ScoreGroupPositive,
		4: ScoreGroupPositive,
		3: ScoreGroupNeutral,
		2: ScoreGroupNegative,
		1: ScoreGroupNegative,
	}
	for score, want := range cases {
		if got := ScoreGroup(score); got != want {
			t.Errorf("ScoreGroup(%d) = %d, want %d", score, got, want)
		}
	}
}

func TestReviewStats(t *testing.T) {
	f := newFixture(t)
	target := int64(7777)
	for i, sc := range []int32{5, 4, 3, 1} {
		f.reviews.Create(context.Background(), &Review{
			ID: int64(i + 1), OrderID: int64(i + 1), ToUserID: target,
			Role: ReviewRoleBuyer, Score: sc,
		})
	}
	st, err := f.review.Stats(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if st.TotalCount != 4 {
		t.Fatalf("总数应为 4，实际 %d", st.TotalCount)
	}
	if st.PositiveCount != 2 || st.NeutralCount != 1 || st.NegativeCount != 1 {
		t.Fatalf("分组统计错误: +%d ~%d -%d", st.PositiveCount, st.NeutralCount, st.NegativeCount)
	}
	if st.PositiveRate != 50 {
		t.Fatalf("好评率应为 50%%，实际 %d", st.PositiveRate)
	}
	// 平均 (5+4+3+1)/4 = 3.25 → 33（四舍五入）
	if st.AverageScoreX10 != 33 {
		t.Fatalf("平均分应为 3.3(33)，实际 %d", st.AverageScoreX10)
	}
}
