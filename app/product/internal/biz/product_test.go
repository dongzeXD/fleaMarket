package biz

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/fleamarket/pkg/idgen"
	kratoserrors "github.com/go-kratos/kratos/v3/errors"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type fixture struct {
	uc     *ProductUsecase
	favUC  *FavoriteUsecase
	repo   *fakeProductRepo
	favs   *fakeFavoriteRepo
	users  *fakeUserGateway
	review *fakeModeration
	files  *fakeStorage
	events *fakeEvents
}

const (
	sellerID = int64(1001)
	buyerID  = int64(2002)
)

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ids, err := idgen.New(3)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{
		repo:   newFakeProductRepo(),
		favs:   newFakeFavoriteRepo(),
		users:  &fakeUserGateway{seller: &SellerInfo{UserID: sellerID, CampusVerified: true, SchoolID: 1, CampusID: 11, Status: 1}},
		review: &fakeModeration{},
		files:  newFakeStorage(),
		events: &fakeEvents{},
	}
	catalog := newFakeCatalogRepo()
	f.uc = NewProductUsecase(f.repo, catalog, f.favs, f.users, f.review,
		f.files, ids, f.events, quietLogger())
	f.favUC = NewFavoriteUsecase(f.favs, f.repo, quietLogger())
	return f
}

func code(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		t.Fatal("期望错误，实际为 nil")
	}
	return int(kratoserrors.FromError(err).Code)
}

func validInput(keys ...string) *CreateProductInput {
	return &CreateProductInput{
		Title:       "高等数学教材 第七版",
		Description: "九成新，无笔记",
		PriceCents:  1500,
		Condition:   ConditionLikeNew,
		CategoryID:  101,
		CampusID:    11,
		ImageKeys:   keys,
	}
}

func TestCreateHappyPath(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	key := "product/1001/img1.jpg"
	f.files.put(key)

	p, err := f.uc.Create(ctx, sellerID, validInput(key))
	if err != nil {
		t.Fatal(err)
	}
	// 发布后必须是待审核，不能直接上架——否则审核机制形同虚设。
	if p.Status != StatusPendingReview {
		t.Fatalf("新发布商品应为待审核，实际 %d", p.Status)
	}
	if p.CoverKey != key {
		t.Fatalf("封面应取第一张图，实际 %q", p.CoverKey)
	}
	if p.SchoolID != 1 {
		t.Fatalf("学校应取自卖家认证信息，实际 %d", p.SchoolID)
	}
	if !f.events.has(TopicProductCreated) {
		t.Fatal("应发布 product.created 事件")
	}
	if f.review.calls != 1 {
		t.Fatalf("应送审一次，实际 %d", f.review.calls)
	}
}

// 未通过校园认证不可发布，这是平台防诈骗的第一道闸门。
func TestCreateRequiresCampusVerification(t *testing.T) {
	f := newFixture(t)
	f.users.seller = &SellerInfo{UserID: sellerID, CampusVerified: false, Status: 1}
	key := "product/1001/img1.jpg"
	f.files.put(key)

	_, err := f.uc.Create(context.Background(), sellerID, validInput(key))
	if got := code(t, err); got != 403 {
		t.Fatalf("未认证应返回 403，实际 %d", got)
	}
	// 错误里要带上「缺什么」，前端据此跳转认证页而非只弹一句文案。
	if md := kratoserrors.FromError(err).Metadata; md["required"] != "campus_auth" {
		t.Fatalf("应在 metadata 指明所需认证，实际 %v", md)
	}
}

func TestCreateRejectsBannedSeller(t *testing.T) {
	f := newFixture(t)
	f.users.seller = &SellerInfo{UserID: sellerID, CampusVerified: true, Status: 2}
	key := "product/1001/img1.jpg"
	f.files.put(key)

	if got := code(t, mustErr(f.uc.Create(context.Background(), sellerID, validInput(key)))); got != 403 {
		t.Fatalf("封禁账号应返回 403，实际 %d", got)
	}
}

// user-service 不可用时必须明确报错，不能默认放行发布。
func TestCreateFailsWhenUserServiceDown(t *testing.T) {
	f := newFixture(t)
	f.users.err = errGatewayDown
	key := "product/1001/img1.jpg"
	f.files.put(key)

	if got := code(t, mustErr(f.uc.Create(context.Background(), sellerID, validInput(key)))); got != 503 {
		t.Fatalf("依赖不可用应返回 503，实际 %d", got)
	}
}

// 商品必须挂在二级分类上：挂一级会产生「在一级但不在任何二级」的孤儿商品。
func TestCreateRejectsTopLevelCategory(t *testing.T) {
	f := newFixture(t)
	key := "product/1001/img1.jpg"
	f.files.put(key)
	in := validInput(key)
	in.CategoryID = 100

	if got := code(t, mustErr(f.uc.Create(context.Background(), sellerID, in))); got != 400 {
		t.Fatalf("一级分类应被拒绝，实际 %d", got)
	}
}

func TestCreateRejectsDisabledCategory(t *testing.T) {
	f := newFixture(t)
	key := "product/1001/img1.jpg"
	f.files.put(key)
	in := validInput(key)
	in.CategoryID = 102

	if got := code(t, mustErr(f.uc.Create(context.Background(), sellerID, in))); got != 400 {
		t.Fatalf("停用分类应被拒绝，实际 %d", got)
	}
}

// 冒用他人上传的图片必须被挡住。预签名直传模式下这是唯一把关点。
func TestCreateRejectsForeignImageKey(t *testing.T) {
	f := newFixture(t)
	foreign := "product/9999/img1.jpg"
	f.files.put(foreign)

	if got := code(t, mustErr(f.uc.Create(context.Background(), sellerID, validInput(foreign)))); got != 403 {
		t.Fatalf("他人 key 应返回 403，实际 %d", got)
	}
}

// 伪造 key 但未真正上传，必须被 Stat 挡住。
func TestCreateRejectsNonExistentImage(t *testing.T) {
	f := newFixture(t)
	if got := code(t, mustErr(f.uc.Create(context.Background(), sellerID,
		validInput("product/1001/never-uploaded.jpg")))); got != 400 {
		t.Fatalf("未上传的图片应返回 400，实际 %d", got)
	}
}

func TestCreateRejectsDuplicateImages(t *testing.T) {
	f := newFixture(t)
	key := "product/1001/img1.jpg"
	f.files.put(key)
	if got := code(t, mustErr(f.uc.Create(context.Background(), sellerID,
		validInput(key, key)))); got != 400 {
		t.Fatalf("重复图片应返回 400，实际 %d", got)
	}
}

func TestCreateRejectsPOIFromOtherCampus(t *testing.T) {
	f := newFixture(t)
	key := "product/1001/img1.jpg"
	f.files.put(key)
	in := validInput(key)
	other := int64(1201) // 属于校区 12
	in.POIID = &other

	if got := code(t, mustErr(f.uc.Create(context.Background(), sellerID, in))); got != 400 {
		t.Fatalf("跨校区 POI 应被拒绝，实际 %d", got)
	}
}

// --- 状态机 ---

func TestStateMachineLegalTransitions(t *testing.T) {
	cases := []struct {
		from uint8
		ev   productEvent
		want uint8
	}{
		{StatusPendingReview, evPass, StatusOnSale},
		{StatusPendingReview, evReject, StatusRejected},
		{StatusOnSale, evReserve, StatusReserved},
		{StatusOnSale, evOffline, StatusOffline},
		{StatusReserved, evCancel, StatusOnSale},
		{StatusReserved, evComplete, StatusSold},
		{StatusOffline, evRepublish, StatusPendingReview},
		{StatusRejected, evRepublish, StatusPendingReview},
	}
	for _, c := range cases {
		got, err := productMachine.Next(c.from, c.ev)
		if err != nil {
			t.Fatalf("Next(%d,%s) 报错: %v", c.from, c.ev, err)
		}
		if got != c.want {
			t.Fatalf("Next(%d,%s) = %d, want %d", c.from, c.ev, got, c.want)
		}
	}
}

func TestStateMachineIllegalTransitions(t *testing.T) {
	illegal := []struct {
		from uint8
		ev   productEvent
	}{
		{StatusSold, evReserve}, // 已售出是终态
		{StatusSold, evOffline},
		{StatusOnSale, evPass},      // 不能重复过审
		{StatusReserved, evOffline}, // 已预订不能直接下架
		{StatusPendingReview, evReserve},
	}
	for _, c := range illegal {
		if _, err := productMachine.Next(c.from, c.ev); err == nil {
			t.Fatalf("Next(%d,%s) 应为非法流转", c.from, c.ev)
		}
	}
}

func TestSoldIsTerminal(t *testing.T) {
	if !productMachine.IsTerminal(StatusSold) {
		t.Fatal("已售出应为终态")
	}
}

// AllowedEvents 只能暴露卖家可主动触发的操作，
// 审核与交易类事件不应出现在给前端的按钮列表里。
func TestAllowedEventsOnlyExposesSellerActions(t *testing.T) {
	f := newFixture(t)
	onSale := f.uc.AllowedEvents(StatusOnSale)
	if len(onSale) != 1 || onSale[0] != "offline" {
		t.Fatalf("在售商品只应可下架，实际 %v", onSale)
	}
	reserved := f.uc.AllowedEvents(StatusReserved)
	if len(reserved) != 0 {
		t.Fatalf("已预订不应有卖家可触发操作，实际 %v", reserved)
	}
	offline := f.uc.AllowedEvents(StatusOffline)
	if len(offline) != 1 || offline[0] != "republish" {
		t.Fatalf("已下架只应可重新上架，实际 %v", offline)
	}
}

// --- 抢占 ---

func TestLockForOrderSucceeds(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	p := seedOnSale(t, f, 3000)

	res, err := f.uc.LockForOrder(ctx, p.ID, 555, buyerID)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Locked {
		t.Fatal("应抢占成功")
	}
	// 价格快照必须是抢占时刻的价格，防止卖家事后改价。
	if res.PriceCents != 3000 {
		t.Fatalf("价格快照错误: %d", res.PriceCents)
	}
	if res.SellerID != sellerID {
		t.Fatalf("卖家 ID 错误: %d", res.SellerID)
	}
}

func TestLockForOrderRejectsSelfPurchase(t *testing.T) {
	f := newFixture(t)
	p := seedOnSale(t, f, 1000)
	if got := code(t, mustErrLock(f.uc.LockForOrder(context.Background(), p.ID, 1, sellerID))); got != 400 {
		t.Fatalf("不能购买自己的商品，应返回 400，实际 %d", got)
	}
}

// 第二个买家必须抢不到，且能拿到准确的当前状态用于提示。
func TestLockForOrderSecondBuyerFails(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	p := seedOnSale(t, f, 1000)

	if _, err := f.uc.LockForOrder(ctx, p.ID, 100, buyerID); err != nil {
		t.Fatal(err)
	}
	res, err := f.uc.LockForOrder(ctx, p.ID, 200, 3003)
	if err != nil {
		t.Fatal(err)
	}
	if res.Locked {
		t.Fatal("第二个买家不应抢到")
	}
	if res.CurrentStatus != StatusReserved {
		t.Fatalf("应回报已预订状态，实际 %d", res.CurrentStatus)
	}
}

// 释放锁必须校验订单归属，否则过期的取消请求会误释放他人刚建立的锁。
func TestReleaseOrderLockRequiresMatchingOrder(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	p := seedOnSale(t, f, 1000)

	if _, err := f.uc.LockForOrder(ctx, p.ID, 100, buyerID); err != nil {
		t.Fatal(err)
	}
	// 用错误的订单 ID 释放，应失败。
	released, err := f.uc.ReleaseOrderLock(ctx, p.ID, 999)
	if err != nil {
		t.Fatal(err)
	}
	if released {
		t.Fatal("不匹配的订单不应能释放锁")
	}
	// 正确的订单 ID 可以释放。
	released, err = f.uc.ReleaseOrderLock(ctx, p.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !released {
		t.Fatal("匹配的订单应能释放锁")
	}
	cur, _ := f.repo.GetByID(ctx, p.ID)
	if cur.Status != StatusOnSale {
		t.Fatalf("释放后应恢复在售，实际 %d", cur.Status)
	}
}

func TestMarkSoldRequiresMatchingOrder(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	p := seedOnSale(t, f, 1000)
	if _, err := f.uc.LockForOrder(ctx, p.ID, 100, buyerID); err != nil {
		t.Fatal(err)
	}
	if marked, _ := f.uc.MarkSold(ctx, p.ID, 999); marked {
		t.Fatal("不匹配的订单不应能标记售出")
	}
	if marked, _ := f.uc.MarkSold(ctx, p.ID, 100); !marked {
		t.Fatal("匹配的订单应能标记售出")
	}
}

// --- 可见性 ---

// 待审核商品对他人应返回 404 而非 403：
// 用错误码区分「不存在」与「存在但不可见」会让人能枚举待审核商品。
func TestGetHidesInvisibleProductFromOthers(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	key := "product/1001/img1.jpg"
	f.files.put(key)
	p, err := f.uc.Create(ctx, sellerID, validInput(key))
	if err != nil {
		t.Fatal(err)
	}

	if got := code(t, mustErr(f.uc.Get(ctx, p.ID, buyerID))); got != 404 {
		t.Fatalf("他人查看待审核商品应返回 404，实际 %d", got)
	}
	// 卖家自己能看到。
	if _, err := f.uc.Get(ctx, p.ID, sellerID); err != nil {
		t.Fatalf("卖家应能查看自己的待审核商品: %v", err)
	}
}

func TestGetDoesNotCountSellerOwnView(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	p := seedOnSale(t, f, 1000)

	if _, err := f.uc.Get(ctx, p.ID, sellerID); err != nil {
		t.Fatal(err)
	}
	cur, _ := f.repo.GetByID(ctx, p.ID)
	if cur.ViewCount != 0 {
		t.Fatalf("卖家自己浏览不应计数，实际 %d", cur.ViewCount)
	}

	if _, err := f.uc.Get(ctx, p.ID, buyerID); err != nil {
		t.Fatal(err)
	}
	cur, _ = f.repo.GetByID(ctx, p.ID)
	if cur.ViewCount != 1 {
		t.Fatalf("他人浏览应计数，实际 %d", cur.ViewCount)
	}
}

// --- 修改 ---

func TestUpdateRejectsNonOwner(t *testing.T) {
	f := newFixture(t)
	p := seedOnSale(t, f, 1000)
	newTitle := "被篡改的标题"
	if got := code(t, mustErr(f.uc.Update(context.Background(), p.ID, buyerID,
		&ProductUpdate{Title: &newTitle}))); got != 403 {
		t.Fatalf("非卖家修改应返回 403，实际 %d", got)
	}
}

func TestUpdateRejectsSoldProduct(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	p := seedOnSale(t, f, 1000)
	if _, err := f.uc.LockForOrder(ctx, p.ID, 1, buyerID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.uc.MarkSold(ctx, p.ID, 1); err != nil {
		t.Fatal(err)
	}
	price := int64(500)
	if got := code(t, mustErr(f.uc.Update(ctx, p.ID, sellerID,
		&ProductUpdate{PriceCents: &price}))); got != 409 {
		t.Fatalf("已售出商品不可修改，应返回 409，实际 %d", got)
	}
}

// 降价要留痕并触发提醒；涨价只留痕不提醒。
func TestUpdatePriceDropPublishesEvent(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	p := seedOnSale(t, f, 2000)

	lower := int64(1500)
	if _, err := f.uc.Update(ctx, p.ID, sellerID, &ProductUpdate{PriceCents: &lower}); err != nil {
		t.Fatal(err)
	}
	if !f.events.has(TopicPriceDropped) {
		t.Fatal("降价应发布 price_dropped 事件")
	}
	if len(f.repo.priceChanges) != 1 {
		t.Fatalf("应记录 1 条价格变更，实际 %d", len(f.repo.priceChanges))
	}

	higher := int64(2500)
	if _, err := f.uc.Update(ctx, p.ID, sellerID, &ProductUpdate{PriceCents: &higher}); err != nil {
		t.Fatal(err)
	}
	if f.events.count(TopicPriceDropped) != 1 {
		t.Fatal("涨价不应发布降价事件")
	}
	if len(f.repo.priceChanges) != 2 {
		t.Fatalf("涨价也应留痕，实际 %d 条", len(f.repo.priceChanges))
	}
}

// 改标题必须重新送审：否则卖家能先发正常商品过审，再改成违规内容。
func TestUpdateContentTriggersRemoderation(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	p := seedOnSale(t, f, 1000)
	before := f.review.calls

	newTitle := "改过的标题"
	updated, err := f.uc.Update(ctx, p.ID, sellerID, &ProductUpdate{Title: &newTitle})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != StatusPendingReview {
		t.Fatalf("改内容后应回到待审核，实际 %d", updated.Status)
	}
	if f.review.calls != before+1 {
		t.Fatalf("应重新送审，实际调用 %d 次", f.review.calls-before)
	}
}

// 只改价格不触发重新送审——价格本身没有违规风险。
func TestUpdatePriceOnlyDoesNotRemoderate(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	p := seedOnSale(t, f, 2000)
	before := f.review.calls

	lower := int64(1800)
	updated, err := f.uc.Update(ctx, p.ID, sellerID, &ProductUpdate{PriceCents: &lower})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != StatusOnSale {
		t.Fatalf("只改价应保持在售，实际 %d", updated.Status)
	}
	if f.review.calls != before {
		t.Fatal("只改价不应重新送审")
	}
}

// --- 删除 ---

func TestDeleteRejectsSoldProduct(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	p := seedOnSale(t, f, 1000)
	if _, err := f.uc.LockForOrder(ctx, p.ID, 1, buyerID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.uc.MarkSold(ctx, p.ID, 1); err != nil {
		t.Fatal(err)
	}
	// 已售出商品是订单与评价的引用对象，删除会产生悬空引用。
	if got := code(t, f.uc.Delete(ctx, p.ID, sellerID)); got != 409 {
		t.Fatalf("已售出商品不可删除，应返回 409，实际 %d", got)
	}
}

func TestDeleteRejectsNonOwner(t *testing.T) {
	f := newFixture(t)
	p := seedOnSale(t, f, 1000)
	if got := code(t, f.uc.Delete(context.Background(), p.ID, buyerID)); got != 403 {
		t.Fatalf("非卖家删除应返回 403，实际 %d", got)
	}
}

// --- 审核回写 ---

func TestApplyModerationPassPutsOnSale(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	key := "product/1001/img1.jpg"
	f.files.put(key)
	p, err := f.uc.Create(ctx, sellerID, validInput(key))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.uc.ApplyModerationResult(ctx, p.ID, true, "", nil); err != nil {
		t.Fatal(err)
	}
	cur, _ := f.repo.GetByID(ctx, p.ID)
	if cur.Status != StatusOnSale {
		t.Fatalf("过审后应在售，实际 %d", cur.Status)
	}
}

func TestApplyModerationRejectRecordsReason(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	key := "product/1001/img1.jpg"
	f.files.put(key)
	p, err := f.uc.Create(ctx, sellerID, validInput(key))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.uc.ApplyModerationResult(ctx, p.ID, false, "含违禁内容", []string{"代写"}); err != nil {
		t.Fatal(err)
	}
	cur, _ := f.repo.GetByID(ctx, p.ID)
	if cur.Status != StatusRejected {
		t.Fatalf("驳回后状态错误: %d", cur.Status)
	}
	if cur.RejectReason != "含违禁内容" {
		t.Fatalf("应记录驳回原因，实际 %q", cur.RejectReason)
	}
	if len(cur.HitWords) != 1 {
		t.Fatalf("应记录命中词，实际 %v", cur.HitWords)
	}
}

// 审核服务不可用时保持待审核，不能默认放行。
func TestModerationFailureKeepsPendingReview(t *testing.T) {
	f := newFixture(t)
	f.review.err = errGatewayDown
	ctx := context.Background()
	key := "product/1001/img1.jpg"
	f.files.put(key)

	p, err := f.uc.Create(ctx, sellerID, validInput(key))
	if err != nil {
		t.Fatal(err)
	}
	cur, _ := f.repo.GetByID(ctx, p.ID)
	if cur.Status != StatusPendingReview {
		t.Fatalf("审核不可用时应保持待审核，实际 %d", cur.Status)
	}
}

// --- 热度 ---

// 三项权重体现「行为的信号强度」：咨询 > 收藏 > 浏览。
func TestHeatScoreWeighting(t *testing.T) {
	now := time.Now()
	base := &Product{CreatedAt: now}

	views := &Product{ViewCount: 100, CreatedAt: now}
	favs := &Product{FavoriteCount: 1, CreatedAt: now}
	inquiries := &Product{InquiryCount: 1, CreatedAt: now}

	if heatScore(favs, now) <= heatScore(views, now) {
		t.Fatal("1 次收藏的权重应高于 100 次浏览")
	}
	if heatScore(inquiries, now) <= heatScore(favs, now) {
		t.Fatal("咨询的权重应高于收藏")
	}
	if heatScore(base, now) != 0 {
		t.Fatalf("无行为无时长的基准分应为 0，实际 %f", heatScore(base, now))
	}
}

// 时间衰减保证新商品有曝光机会，否则热度榜会被早期爆款长期占据。
func TestHeatScoreTimeDecay(t *testing.T) {
	now := time.Now()
	fresh := &Product{FavoriteCount: 5, CreatedAt: now}
	old := &Product{FavoriteCount: 5, CreatedAt: now.Add(-72 * time.Hour)}

	if heatScore(old, now) >= heatScore(fresh, now) {
		t.Fatal("同等互动下，较早发布的商品热度应更低")
	}
}

func TestRecomputeHeatProcessesAll(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		seedOnSale(t, f, int64(1000+i))
	}
	n, err := f.uc.RecomputeHeat(ctx, 7*24*time.Hour, 2)
	if err != nil {
		t.Fatal(err)
	}
	if n != 5 {
		t.Fatalf("应处理 5 条，实际 %d", n)
	}
}

// --- 收藏 ---

func TestFavoriteAddIsIdempotent(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	p := seedOnSale(t, f, 1000)

	c1, err := f.favUC.Add(ctx, buyerID, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if c1 != 1 {
		t.Fatalf("首次收藏后应为 1，实际 %d", c1)
	}
	c2, err := f.favUC.Add(ctx, buyerID, p.ID)
	if err != nil {
		t.Fatalf("重复收藏不应报错: %v", err)
	}
	if c2 != 1 {
		t.Fatalf("重复收藏不应重复计数，实际 %d", c2)
	}
}

func TestFavoriteRejectsOwnProduct(t *testing.T) {
	f := newFixture(t)
	p := seedOnSale(t, f, 1000)
	if got := code(t, mustErrInt32(f.favUC.Add(context.Background(), sellerID, p.ID))); got != 400 {
		t.Fatalf("不能收藏自己的商品，应返回 400，实际 %d", got)
	}
}

func TestFavoriteRemoveIsIdempotent(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	p := seedOnSale(t, f, 1000)
	if _, err := f.favUC.Remove(ctx, buyerID, p.ID); err != nil {
		t.Fatalf("取消未收藏的商品不应报错: %v", err)
	}
}

// 已下架的商品仍保留收藏关系，但不出现在列表里——
// 直接删关系会让商品重新上架后收藏莫名消失。
func TestFavoriteListFiltersInvisibleProducts(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	p := seedOnSale(t, f, 1000)
	if _, err := f.favUC.Add(ctx, buyerID, p.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := f.uc.ChangeStatus(ctx, p.ID, sellerID, "offline"); err != nil {
		t.Fatal(err)
	}

	page, err := f.favUC.ListMine(ctx, buyerID, "", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 0 {
		t.Fatalf("已下架商品不应出现在收藏列表，实际 %d 条", len(page.Items))
	}
	// 但关系仍在。
	exists, _ := f.favs.Exists(ctx, buyerID, p.ID)
	if !exists {
		t.Fatal("收藏关系应保留，以便商品重新上架后恢复可见")
	}
}

// --- 辅助 ---

func seedOnSale(t *testing.T, f *fixture, priceCents int64) *Product {
	t.Helper()
	ctx := context.Background()
	key := "product/1001/img" + itoa(priceCents) + ".jpg"
	f.files.put(key)
	in := validInput(key)
	in.PriceCents = priceCents
	p, err := f.uc.Create(ctx, sellerID, in)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.uc.ApplyModerationResult(ctx, p.ID, true, "", nil); err != nil {
		t.Fatal(err)
	}
	cur, err := f.repo.GetByID(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	return cur
}

func mustErr[T any](_ T, err error) error { return err }

func mustErrLock(_ *LockResult, err error) error { return err }

func mustErrInt32(_ int32, err error) error { return err }
