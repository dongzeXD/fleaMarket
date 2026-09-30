package biz

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/fleamarket/pkg/errs"
)

// 内存假实现。锁与商品抢占带真实的并发语义，
// 使「10 个买家抢同一件商品只有 1 个成功」这类断言有意义。

type fakeOrderRepo struct {
	mu     sync.Mutex
	items  map[int64]*Order
	active map[int64]int64 // productID -> orderID，模拟 uk_product_active 唯一约束
}

func newFakeOrderRepo() *fakeOrderRepo {
	return &fakeOrderRepo{items: map[int64]*Order{}, active: map[int64]int64{}}
}

func (f *fakeOrderRepo) Create(_ context.Context, o *Order) (*Order, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// 模拟 uk_product_active：同一商品已有进行中订单时冲突。
	if _, exists := f.active[o.ProductID]; exists {
		return nil, errs.ErrAlreadyExists
	}
	f.active[o.ProductID] = o.ID
	cp := *o
	cp.CreatedAt = time.Now()
	f.items[o.ID] = &cp
	out := cp
	return &out, nil
}

func (f *fakeOrderRepo) GetByID(_ context.Context, id int64) (*Order, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.items[id]
	if !ok {
		return nil, errs.ErrNotFound
	}
	cp := *o
	return &cp, nil
}

func (f *fakeOrderRepo) UpdateStatus(_ context.Context, id int64, from, to uint8,
	patch *OrderPatch, clearActiveKey bool) (bool, error) {

	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.items[id]
	if !ok {
		return false, errs.ErrNotFound
	}
	if o.Status != from {
		return false, nil
	}
	o.Status = to
	if clearActiveKey {
		delete(f.active, o.ProductID)
	}
	if patch != nil {
		if patch.AcceptedAt != nil {
			o.AcceptedAt = patch.AcceptedAt
		}
		if patch.CompletedAt != nil {
			o.CompletedAt = patch.CompletedAt
		}
		if patch.ExpireAt != nil {
			o.ExpireAt = patch.ExpireAt
		}
		if patch.CancelReason != nil {
			o.CancelReason = *patch.CancelReason
		}
		if patch.CancelledBy != nil {
			o.CancelledBy = *patch.CancelledBy
		}
	}
	return true, nil
}

func (f *fakeOrderRepo) UpdateMeetup(_ context.Context, id int64, poiID int64, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.items[id]
	if !ok {
		return errs.ErrNotFound
	}
	o.MeetPOIID = poiID
	o.MeetTime = &at
	return nil
}

func (f *fakeOrderRepo) MarkConfirmed(_ context.Context, id int64, role uint8) (bool, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.items[id]
	if !ok {
		return false, false, errs.ErrNotFound
	}
	if role == ReviewRoleBuyer {
		o.BuyerConfirmed = true
	} else {
		o.SellerConfirmed = true
	}
	return o.BuyerConfirmed, o.SellerConfirmed, nil
}

func (f *fakeOrderRepo) ListByUser(_ context.Context, userID int64, role uint8,
	statuses []uint8, _ string, _ int) ([]*Order, string, bool, error) {

	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*Order
	for _, o := range f.items {
		match := false
		switch role {
		case ReviewRoleBuyer:
			match = o.BuyerID == userID
		case ReviewRoleSeller:
			match = o.SellerID == userID
		default:
			match = o.BuyerID == userID || o.SellerID == userID
		}
		if match {
			cp := *o
			out = append(out, &cp)
		}
	}
	return out, "", false, nil
}

func (f *fakeOrderRepo) FindActiveByProduct(_ context.Context, productID int64) (*Order, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	oid, ok := f.active[productID]
	if !ok {
		return nil, errs.ErrNotFound
	}
	item, ok := f.items[oid]
	if !ok {
		// active 表指向一个不存在的订单：模拟 active 表与订单表短暂不一致。
		return nil, errs.ErrNotFound
	}
	cp := *item
	return &cp, nil
}

func (f *fakeOrderRepo) ListExpired(_ context.Context, now time.Time, limit int) ([]*Order, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*Order
	for _, o := range f.items {
		if o.ExpireAt != nil && !o.ExpireAt.After(now) && o.IsActive() {
			cp := *o
			out = append(out, &cp)
		}
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (f *fakeOrderRepo) CountCompletedByUser(_ context.Context, userID int64) (int32, int32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var bought, sold int32
	for _, o := range f.items {
		if o.Status != StatusCompleted {
			continue
		}
		if o.BuyerID == userID {
			bought++
		}
		if o.SellerID == userID {
			sold++
		}
	}
	return bought, sold, nil
}

// fakeProductGateway 模拟商品抢占的 CAS 语义。
type fakeProductGateway struct {
	mu        sync.Mutex
	status    map[int64]uint8 // productID -> 状态
	lockedBy  map[int64]int64 // productID -> orderID
	price     int64
	sellerID  int64
	soldCalls int
}

func newFakeProductGateway(price, sellerID int64) *fakeProductGateway {
	return &fakeProductGateway{
		status:   map[int64]uint8{},
		lockedBy: map[int64]int64{},
		price:    price,
		sellerID: sellerID,
	}
}

func (f *fakeProductGateway) seed(productID int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status[productID] = 2 // ON_SALE
}

// LockForOrder 以 CAS 语义抢占：只有 ON_SALE 能被锁定。
func (f *fakeProductGateway) LockForOrder(_ context.Context, productID, orderID, buyerID int64) (*LockResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.status[productID]
	if st != 2 {
		return &LockResult{Locked: false, CurrentStatus: st, PriceCents: f.price, SellerID: f.sellerID}, nil
	}
	f.status[productID] = 3 // RESERVED
	f.lockedBy[productID] = orderID
	return &LockResult{Locked: true, CurrentStatus: 3, PriceCents: f.price, SellerID: f.sellerID}, nil
}

func (f *fakeProductGateway) ReleaseOrderLock(_ context.Context, productID, orderID int64) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lockedBy[productID] != orderID {
		return false, nil
	}
	f.status[productID] = 2
	delete(f.lockedBy, productID)
	return true, nil
}

func (f *fakeProductGateway) MarkSold(_ context.Context, productID, orderID int64) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lockedBy[productID] != orderID {
		return false, nil
	}
	f.status[productID] = 4 // SOLD
	f.soldCalls++
	return true, nil
}

// fakeLocker 真实互斥的内存锁，用于验证串行化。
type fakeLocker struct {
	mu   sync.Mutex
	held map[string]bool
}

func newFakeLocker() *fakeLocker {
	return &fakeLocker{held: map[string]bool{}}
}

func (l *fakeLocker) Lock(ctx context.Context, key string, ttl, wait time.Duration) (func(context.Context) error, error) {
	deadline := time.Now().Add(wait)
	for {
		l.mu.Lock()
		if !l.held[key] {
			l.held[key] = true
			l.mu.Unlock()
			return func(context.Context) error {
				l.mu.Lock()
				delete(l.held, key)
				l.mu.Unlock()
				return nil
			}, nil
		}
		l.mu.Unlock()
		if wait <= 0 || time.Now().After(deadline) {
			return nil, errors.New("lock not acquired")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

type fakeUserGateway struct {
	mu      sync.Mutex
	credits map[int64][]uint8 // userID -> 变更原因序列
	// idempotent 记录已处理的 (userID, reason, refID)，与修正后的
	// user-service 幂等键一致——同一订单会给买卖双方各加分，
	// 键必须含 userID 才能区分。
	idempotent map[[3]int64]bool
}

func newFakeUserGateway() *fakeUserGateway {
	return &fakeUserGateway{credits: map[int64][]uint8{}, idempotent: map[[3]int64]bool{}}
}

func (f *fakeUserGateway) AdjustCredit(_ context.Context, userID int64, reason uint8, refID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := [3]int64{userID, int64(reason), refID}
	if f.idempotent[key] {
		return nil
	}
	f.idempotent[key] = true
	f.credits[userID] = append(f.credits[userID], reason)
	return nil
}

func (f *fakeUserGateway) creditCount(userID int64, reason uint8) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.credits[userID] {
		if r == reason {
			n++
		}
	}
	return n
}

type fakeReviewRepo struct {
	mu    sync.Mutex
	items map[int64]*Review
	// byOrderRole 模拟 uk_order_role 唯一约束。
	byOrderRole map[[2]int64]int64
}

func newFakeReviewRepo() *fakeReviewRepo {
	return &fakeReviewRepo{items: map[int64]*Review{}, byOrderRole: map[[2]int64]int64{}}
}

func (f *fakeReviewRepo) Create(_ context.Context, r *Review) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := [2]int64{r.OrderID, int64(r.Role)}
	if _, exists := f.byOrderRole[key]; exists {
		return false, nil
	}
	f.byOrderRole[key] = r.ID
	cp := *r
	f.items[r.ID] = &cp
	return true, nil
}

func (f *fakeReviewRepo) ListByOrder(_ context.Context, orderID int64) ([]*Review, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*Review
	for _, r := range f.items {
		if r.OrderID == orderID {
			cp := *r
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (f *fakeReviewRepo) ListByUser(_ context.Context, userID int64, group uint8,
	_ string, _ int) ([]*Review, string, bool, error) {

	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*Review
	for _, r := range f.items {
		if r.ToUserID == userID {
			cp := *r
			out = append(out, &cp)
		}
	}
	return out, "", false, nil
}

func (f *fakeReviewRepo) ExistsByOrderRoles(_ context.Context, orderID int64) (bool, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, buyer := f.byOrderRole[[2]int64{orderID, int64(ReviewRoleBuyer)}]
	_, seller := f.byOrderRole[[2]int64{orderID, int64(ReviewRoleSeller)}]
	return buyer, seller, nil
}

func (f *fakeReviewRepo) StatsOf(_ context.Context, userID int64) (*ReviewStats, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := &ReviewStats{UserID: userID}
	var sum int32
	for _, r := range f.items {
		if r.ToUserID != userID {
			continue
		}
		st.TotalCount++
		sum += r.Score
		switch ScoreGroup(r.Score) {
		case ScoreGroupPositive:
			st.PositiveCount++
		case ScoreGroupNeutral:
			st.NeutralCount++
		default:
			st.NegativeCount++
		}
	}
	if st.TotalCount > 0 {
		st.PositiveRate = st.PositiveCount * 100 / st.TotalCount
		st.AverageScoreX10 = (sum*10 + st.TotalCount/2) / st.TotalCount
	}
	return st, nil
}

type fakeEvents struct {
	mu     sync.Mutex
	topics []string
}

func (f *fakeEvents) Publish(_ context.Context, topic string, _ []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.topics = append(f.topics, topic)
	return nil
}

func (f *fakeEvents) count(topic string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, t := range f.topics {
		if t == topic {
			n++
		}
	}
	return n
}
