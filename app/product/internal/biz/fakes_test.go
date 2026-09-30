package biz

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fleamarket/pkg/errs"
	"github.com/fleamarket/pkg/storage"
)

// 内存假实现，使 biz 层单测无需 MySQL 与对象存储。

type fakeProductRepo struct {
	mu    sync.Mutex
	items map[int64]*Product
	// lockFailOnce 用于模拟并发抢占失败。
	lockFailOnce bool
	priceChanges []string
}

func newFakeProductRepo() *fakeProductRepo {
	return &fakeProductRepo{items: map[int64]*Product{}}
}

func (f *fakeProductRepo) Create(_ context.Context, p *Product) (*Product, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := *p
	cp.CreatedAt = time.Now()
	f.items[p.ID] = &cp
	out := cp
	return &out, nil
}

func (f *fakeProductRepo) GetByID(_ context.Context, id int64) (*Product, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.items[id]
	if !ok {
		return nil, errs.ErrNotFound
	}
	out := *p
	return &out, nil
}

func (f *fakeProductRepo) Update(_ context.Context, id int64, u *ProductUpdate) (*Product, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.items[id]
	if !ok {
		return nil, errs.ErrNotFound
	}
	if u.Title != nil {
		p.Title = *u.Title
	}
	if u.Description != nil {
		p.Description = *u.Description
	}
	if u.PriceCents != nil {
		p.PriceCents = *u.PriceCents
	}
	if u.Condition != nil {
		p.Condition = *u.Condition
	}
	if u.CategoryID != nil {
		p.CategoryID = *u.CategoryID
	}
	if u.POIID != nil {
		p.POIID = u.POIID
	}
	out := *p
	return &out, nil
}

func (f *fakeProductRepo) UpdateStatus(_ context.Context, id int64, from, to uint8) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.items[id]
	if !ok {
		return false, errs.ErrNotFound
	}
	if p.Status != from {
		return false, nil
	}
	p.Status = to
	return true, nil
}

func (f *fakeProductRepo) SoftDelete(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.items, id)
	return nil
}

func (f *fakeProductRepo) BatchGet(_ context.Context, ids []int64) (map[int64]*Product, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[int64]*Product, len(ids))
	for _, id := range ids {
		if p, ok := f.items[id]; ok {
			cp := *p
			out[id] = &cp
		}
	}
	return out, nil
}

func (f *fakeProductRepo) ListBySeller(_ context.Context, sellerID int64, statuses []uint8,
	_ string, _ int) ([]*Product, string, bool, error) {

	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*Product
	for _, p := range f.items {
		if p.SellerID != sellerID {
			continue
		}
		if len(statuses) > 0 && !containsStatus(statuses, p.Status) {
			continue
		}
		cp := *p
		out = append(out, &cp)
	}
	return out, "", false, nil
}

func containsStatus(ss []uint8, s uint8) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// LockForOrder 以 CAS 语义实现，忠实反映生产行为。
func (f *fakeProductRepo) LockForOrder(_ context.Context, productID, orderID int64) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lockFailOnce {
		f.lockFailOnce = false
		return false, nil
	}
	p, ok := f.items[productID]
	if !ok {
		return false, errs.ErrNotFound
	}
	if p.Status != StatusOnSale {
		return false, nil
	}
	p.Status = StatusReserved
	oid := orderID
	p.LockedOrderID = &oid
	return true, nil
}

func (f *fakeProductRepo) ReleaseOrderLock(_ context.Context, productID, orderID int64) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.items[productID]
	if !ok {
		return false, errs.ErrNotFound
	}
	if p.Status != StatusReserved || p.LockedOrderID == nil || *p.LockedOrderID != orderID {
		return false, nil
	}
	p.Status = StatusOnSale
	p.LockedOrderID = nil
	return true, nil
}

func (f *fakeProductRepo) MarkSold(_ context.Context, productID, orderID int64) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.items[productID]
	if !ok {
		return false, errs.ErrNotFound
	}
	if p.Status != StatusReserved || p.LockedOrderID == nil || *p.LockedOrderID != orderID {
		return false, nil
	}
	p.Status = StatusSold
	oid := orderID
	p.SoldOrderID = &oid
	return true, nil
}

func (f *fakeProductRepo) ApplyModeration(_ context.Context, id int64, passed bool,
	reason string, hitWords []string) error {

	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.items[id]
	if !ok {
		return errs.ErrNotFound
	}
	if passed {
		p.Status = StatusOnSale
		p.RejectReason = ""
		p.HitWords = nil
	} else {
		p.Status = StatusRejected
		p.RejectReason = reason
		p.HitWords = hitWords
	}
	return nil
}

func (f *fakeProductRepo) IncrViewCount(_ context.Context, id int64, delta int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p, ok := f.items[id]; ok {
		p.ViewCount += delta
	}
	return nil
}

func (f *fakeProductRepo) AdjustFavoriteCount(_ context.Context, id int64, delta int32) (int32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.items[id]
	if !ok {
		return 0, errs.ErrNotFound
	}
	p.FavoriteCount += delta
	if p.FavoriteCount < 0 {
		p.FavoriteCount = 0
	}
	return p.FavoriteCount, nil
}

func (f *fakeProductRepo) CountBySeller(_ context.Context, sellerID int64) (int32, int32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var onSale, sold int32
	for _, p := range f.items {
		if p.SellerID != sellerID {
			continue
		}
		switch p.Status {
		case StatusOnSale, StatusReserved:
			onSale++
		case StatusSold:
			sold++
		}
	}
	return onSale, sold, nil
}

func (f *fakeProductRepo) RecordPriceChange(_ context.Context, productID, oldC, newC int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.priceChanges = append(f.priceChanges, formatChange(productID, oldC, newC))
	return nil
}

func formatChange(id, o, n int64) string {
	return itoa(id) + ":" + itoa(o) + "->" + itoa(n)
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b []byte
	for v > 0 {
		b = append([]byte{byte('0' + v%10)}, b...)
		v /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}

func (f *fakeProductRepo) ListHeatCandidates(_ context.Context, _ time.Time,
	limit, offset int) ([]*Product, error) {

	f.mu.Lock()
	defer f.mu.Unlock()
	var all []*Product
	for _, p := range f.items {
		cp := *p
		all = append(all, &cp)
	}
	if offset >= len(all) {
		return nil, nil
	}
	end := offset + limit
	if end > len(all) {
		end = len(all)
	}
	return all[offset:end], nil
}

func (f *fakeProductRepo) UpdateHeatScores(_ context.Context, scores map[int64]float64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, s := range scores {
		if p, ok := f.items[id]; ok {
			p.HeatScore = s
		}
	}
	return nil
}

// ScanForIndex 按主键顺序返回，含已删除的商品。
func (f *fakeProductRepo) ScanForIndex(_ context.Context, afterID int64,
	limit int) ([]*IndexDoc, int64, error) {

	f.mu.Lock()
	defer f.mu.Unlock()

	ids := make([]int64, 0, len(f.items))
	for id := range f.items {
		if id > afterID {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	out := make([]*IndexDoc, 0, limit)
	var last int64
	for _, id := range ids {
		if len(out) >= limit {
			break
		}
		cp := *f.items[id]
		out = append(out, &IndexDoc{Product: &cp, ParentCategoryID: 100})
		last = id
	}
	next := int64(0)
	if len(out) == limit {
		next = last
	}
	return out, next, nil
}

// SearchFallback 简化实现：只按标题包含关键词过滤。
func (f *fakeProductRepo) SearchFallback(_ context.Context,
	q *FallbackQuery) ([]*Product, string, bool, error) {

	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*Product
	for _, p := range f.items {
		if q.Keyword != "" && !strings.Contains(p.Title, q.Keyword) {
			continue
		}
		cp := *p
		out = append(out, &cp)
	}
	return out, "", false, nil
}

type fakeCatalogRepo struct {
	categories map[int64]*Category
	pois       map[int64]*POI
}

func newFakeCatalogRepo() *fakeCatalogRepo {
	return &fakeCatalogRepo{
		categories: map[int64]*Category{
			100: {ID: 100, ParentID: 0, Name: "教材书籍", Enabled: true},
			101: {ID: 101, ParentID: 100, Name: "专业教材", Enabled: true},
			102: {ID: 102, ParentID: 100, Name: "已停用分类", Enabled: false},
		},
		pois: map[int64]*POI{
			1101: {ID: 1101, CampusID: 11, Name: "东门", Recommended: true},
			1201: {ID: 1201, CampusID: 12, Name: "东校区正门"},
		},
	}
}

func (f *fakeCatalogRepo) ListCategories(context.Context, bool) ([]*Category, error) {
	return []*Category{f.categories[100]}, nil
}

func (f *fakeCatalogRepo) GetCategory(_ context.Context, id int64) (*Category, error) {
	c, ok := f.categories[id]
	if !ok {
		return nil, errs.ErrNotFound
	}
	return c, nil
}

func (f *fakeCatalogRepo) ListPOIs(_ context.Context, campusID int64, _ []uint8) ([]*POI, error) {
	var out []*POI
	for _, p := range f.pois {
		if p.CampusID == campusID {
			out = append(out, p)
		}
	}
	return out, nil
}

func (f *fakeCatalogRepo) GetPOI(_ context.Context, id int64) (*POI, error) {
	p, ok := f.pois[id]
	if !ok {
		return nil, errs.ErrNotFound
	}
	return p, nil
}

type fakeFavoriteRepo struct {
	mu   sync.Mutex
	favs map[[2]int64]bool
}

func newFakeFavoriteRepo() *fakeFavoriteRepo {
	return &fakeFavoriteRepo{favs: map[[2]int64]bool{}}
}

func (f *fakeFavoriteRepo) Add(_ context.Context, userID, productID int64) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := [2]int64{userID, productID}
	if f.favs[k] {
		return false, nil
	}
	f.favs[k] = true
	return true, nil
}

func (f *fakeFavoriteRepo) Remove(_ context.Context, userID, productID int64) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := [2]int64{userID, productID}
	if !f.favs[k] {
		return false, nil
	}
	delete(f.favs, k)
	return true, nil
}

func (f *fakeFavoriteRepo) Exists(_ context.Context, userID, productID int64) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.favs[[2]int64{userID, productID}], nil
}

func (f *fakeFavoriteRepo) ExistsBatch(_ context.Context, userID int64,
	ids []int64) (map[int64]bool, error) {

	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[int64]bool, len(ids))
	for _, id := range ids {
		if f.favs[[2]int64{userID, id}] {
			out[id] = true
		}
	}
	return out, nil
}

func (f *fakeFavoriteRepo) ListByUser(_ context.Context, userID int64,
	_ string, _ int) ([]int64, string, bool, error) {

	f.mu.Lock()
	defer f.mu.Unlock()
	var out []int64
	for k := range f.favs {
		if k[0] == userID {
			out = append(out, k[1])
		}
	}
	return out, "", false, nil
}

func (f *fakeFavoriteRepo) ListUserIDsByProduct(_ context.Context, productID int64,
	_ string, _ int) ([]int64, string, bool, error) {

	f.mu.Lock()
	defer f.mu.Unlock()
	var out []int64
	for k := range f.favs {
		if k[1] == productID {
			out = append(out, k[0])
		}
	}
	return out, "", false, nil
}

type fakeUserGateway struct {
	seller *SellerInfo
	err    error
}

func (f *fakeUserGateway) GetSellerInfo(context.Context, int64) (*SellerInfo, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.seller, nil
}

type fakeModeration struct {
	immediate bool
	passed    bool
	reason    string
	hitWords  []string
	err       error
	calls     int
}

func (f *fakeModeration) SubmitProduct(context.Context, int64, string, string) (bool, bool, string, []string, error) {
	f.calls++
	if f.err != nil {
		return false, false, "", nil, f.err
	}
	return f.immediate, f.passed, f.reason, f.hitWords, nil
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

func (f *fakeEvents) has(topic string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, t := range f.topics {
		if t == topic {
			return true
		}
	}
	return false
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

type fakeStorage struct {
	mu      sync.Mutex
	objects map[string]bool
}

func newFakeStorage() *fakeStorage {
	return &fakeStorage{objects: map[string]bool{}}
}

func (f *fakeStorage) put(keys ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, k := range keys {
		f.objects[k] = true
	}
}

func (f *fakeStorage) CreateUploadURL(_ context.Context,
	req storage.UploadRequest) (*storage.PresignedUpload, error) {

	return &storage.PresignedUpload{
		URL:      "https://s3.example.com/" + req.Key + "?signed=1",
		Key:      req.Key,
		Method:   "PUT",
		Headers:  map[string]string{"Content-Type": req.ContentType},
		ExpireAt: time.Now().Add(req.TTL),
		MaxSize:  req.MaxSize,
	}, nil
}

func (f *fakeStorage) PublicURL(_ context.Context, key, _ string, _ time.Duration) (string, error) {
	return "https://cdn.example.com/" + key, nil
}

func (f *fakeStorage) Stat(_ context.Context, key string) (*storage.ObjectInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.objects[key] {
		return nil, storage.ErrNotFound
	}
	return &storage.ObjectInfo{Key: key, Size: 2048, ContentType: "image/jpeg"}, nil
}

func (f *fakeStorage) Delete(_ context.Context, keys ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, k := range keys {
		delete(f.objects, k)
	}
	return nil
}

func (f *fakeStorage) Move(context.Context, string, string) error { return nil }

var errGatewayDown = errors.New("user-service unavailable")
