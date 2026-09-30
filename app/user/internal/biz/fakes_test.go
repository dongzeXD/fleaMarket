package biz

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/fleamarket/pkg/errs"
	"github.com/fleamarket/pkg/storage"
)

// 本文件提供 biz 层单测所需的内存假实现。
//
// 刻意不用 mock 生成器：这些假实现带有真实的状态与约束
// （唯一索引冲突、幂等键、原子消费），能覆盖「并发下同一验证码被用两次」
// 这类只有状态机才能暴露的问题，而基于期望调用的 mock 做不到。

type fakeUserRepo struct {
	mu        sync.Mutex
	byID      map[int64]*User
	byPhone   map[string]int64
	byUnionID map[string]int64
	counts    map[int64][2]int32
	failNext  error
}

func newFakeUserRepo() *fakeUserRepo {
	return &fakeUserRepo{
		byID:      map[int64]*User{},
		byPhone:   map[string]int64{},
		byUnionID: map[string]int64{},
		counts:    map[int64][2]int32{},
	}
}

func (f *fakeUserRepo) Create(_ context.Context, u *User) (*User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failNext != nil {
		err := f.failNext
		f.failNext = nil
		return nil, err
	}
	if u.Phone != "" {
		if _, dup := f.byPhone[u.Phone]; dup {
			return nil, errs.ErrAlreadyExists
		}
		f.byPhone[u.Phone] = u.ID
	}
	if u.WxUnionID != "" {
		if _, dup := f.byUnionID[u.WxUnionID]; dup {
			return nil, errs.ErrAlreadyExists
		}
		f.byUnionID[u.WxUnionID] = u.ID
	}
	cp := *u
	cp.CreatedAt = time.Now()
	f.byID[u.ID] = &cp
	out := cp
	return &out, nil
}

func (f *fakeUserRepo) GetByID(_ context.Context, id int64) (*User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.byID[id]
	if !ok {
		return nil, errs.ErrNotFound
	}
	out := *u
	return &out, nil
}

func (f *fakeUserRepo) GetByPhone(_ context.Context, phone string) (*User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id, ok := f.byPhone[phone]
	if !ok {
		return nil, errs.ErrNotFound
	}
	out := *f.byID[id]
	return &out, nil
}

func (f *fakeUserRepo) GetByWxUnionID(_ context.Context, unionID string) (*User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id, ok := f.byUnionID[unionID]
	if !ok {
		return nil, errs.ErrNotFound
	}
	out := *f.byID[id]
	return &out, nil
}

func (f *fakeUserRepo) BatchGetBrief(_ context.Context, ids []int64) (map[int64]*User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[int64]*User, len(ids))
	for _, id := range ids {
		if u, ok := f.byID[id]; ok {
			cp := *u
			out[id] = &cp
		}
	}
	return out, nil
}

func (f *fakeUserRepo) UpdateProfile(_ context.Context, id int64, nickname, avatarKey string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.byID[id]
	if !ok {
		return errs.ErrNotFound
	}
	u.Nickname, u.AvatarKey = nickname, avatarKey
	return nil
}

func (f *fakeUserRepo) UpdateWechatBinding(_ context.Context, id int64, unionID, web, mini string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.byID[id]
	if !ok {
		return errs.ErrNotFound
	}
	u.WxUnionID, u.WxOpenIDWeb, u.WxOpenIDMini = unionID, web, mini
	f.byUnionID[unionID] = id
	return nil
}

func (f *fakeUserRepo) TouchLogin(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if u, ok := f.byID[id]; ok {
		u.LastLoginAt = time.Now()
	}
	return nil
}

func (f *fakeUserRepo) UpdateCampusSnapshot(_ context.Context, id int64,
	status uint8, schoolID, campusID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.byID[id]
	if !ok {
		return errs.ErrNotFound
	}
	u.CampusAuthStatus, u.SchoolID, u.CampusID = status, schoolID, campusID
	return nil
}

func (f *fakeUserRepo) CountsOf(_ context.Context, id int64) (int32, int32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.counts[id]
	return c[0], c[1], nil
}

// fakeCodeStore 模拟验证码存储，Consume 为原子的「校验+删除」。
type fakeCodeStore struct {
	mu       sync.Mutex
	codes    map[string]string
	failures map[string]int64
	// lastSent 记录上次发送时间，用于模拟频率限制。
	lastSent map[string]time.Time
	interval time.Duration
}

func newFakeCodeStore() *fakeCodeStore {
	return &fakeCodeStore{
		codes:    map[string]string{},
		failures: map[string]int64{},
		lastSent: map[string]time.Time{},
		interval: smsResendInterval,
	}
}

func ckey(scene, target string) string { return scene + ":" + target }

func (f *fakeCodeStore) Save(_ context.Context, scene, target, code string, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.codes[ckey(scene, target)] = code
	return nil
}

func (f *fakeCodeStore) Consume(_ context.Context, scene, target, code string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := ckey(scene, target)
	want, ok := f.codes[k]
	if !ok || want != code {
		return false, nil
	}
	// 校验通过即删除，保证同一验证码只能用一次。
	delete(f.codes, k)
	return true, nil
}

func (f *fakeCodeStore) AcquireSendQuota(_ context.Context, scene, target string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := ckey(scene, target)
	if last, ok := f.lastSent[k]; ok {
		if elapsed := time.Since(last); elapsed < f.interval {
			return int((f.interval - elapsed).Seconds()) + 1, nil
		}
	}
	f.lastSent[k] = time.Now()
	return 0, nil
}

func (f *fakeCodeStore) CountFailures(_ context.Context, scene, target string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := ckey(scene, target)
	f.failures[k]++
	return f.failures[k], nil
}

func (f *fakeCodeStore) ClearFailures(_ context.Context, scene, target string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.failures, ckey(scene, target))
	return nil
}

// peek 供测试读取当前有效验证码。
func (f *fakeCodeStore) peek(scene, target string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.codes[ckey(scene, target)]
}

type fakeSMS struct {
	mu   sync.Mutex
	sent []string
	err  error
}

func (f *fakeSMS) SendVerifyCode(_ context.Context, phone, code string, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, phone+":"+code)
	return nil
}

type fakeWechat struct {
	web  *WechatProfile
	mini *WechatProfile
	err  error
}

func (f *fakeWechat) ExchangeWeb(context.Context, string) (*WechatProfile, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.web, nil
}

func (f *fakeWechat) ExchangeMiniApp(context.Context, string) (*WechatProfile, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.mini, nil
}

type fakeTokenStore struct {
	mu      sync.Mutex
	revoked map[string]bool
}

func newFakeTokenStore() *fakeTokenStore {
	return &fakeTokenStore{revoked: map[string]bool{}}
}

func (f *fakeTokenStore) Revoke(_ context.Context, id string, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revoked[id] = true
	return nil
}

func (f *fakeTokenStore) IsRevoked(_ context.Context, id string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.revoked[id], nil
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

type fakeSocialRepo struct {
	mu        sync.Mutex
	following map[[2]int64]bool
	blocked   map[[2]int64]bool
}

func newFakeSocialRepo() *fakeSocialRepo {
	return &fakeSocialRepo{
		following: map[[2]int64]bool{},
		blocked:   map[[2]int64]bool{},
	}
}

func (f *fakeSocialRepo) Follow(_ context.Context, a, b int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.following[[2]int64{a, b}] = true
	return nil
}

func (f *fakeSocialRepo) Unfollow(_ context.Context, a, b int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.following, [2]int64{a, b})
	return nil
}

func (f *fakeSocialRepo) IsFollowing(_ context.Context, a, b int64) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.following[[2]int64{a, b}], nil
}

func (f *fakeSocialRepo) ListFollowing(_ context.Context, a int64, _ string, _ int) ([]int64, string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []int64
	for k := range f.following {
		if k[0] == a {
			out = append(out, k[1])
		}
	}
	return out, "", false, nil
}

func (f *fakeSocialRepo) Block(_ context.Context, a, b int64, _ uint8) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.blocked[[2]int64{a, b}] = true
	return nil
}

func (f *fakeSocialRepo) Unblock(_ context.Context, a, b int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.blocked, [2]int64{a, b})
	return nil
}

func (f *fakeSocialRepo) IsBlockedEitherWay(_ context.Context, a, b int64) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.blocked[[2]int64{a, b}] || f.blocked[[2]int64{b, a}], nil
}

func (f *fakeSocialRepo) ListBlocked(_ context.Context, a int64, _ string, _ int) ([]int64, string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []int64
	for k := range f.blocked {
		if k[0] == a {
			out = append(out, k[1])
		}
	}
	return out, "", false, nil
}

// fakeCreditRepo 带幂等键的信誉分仓储。
type fakeCreditRepo struct {
	mu      sync.Mutex
	scores  map[int64]int32
	applied map[[2]int64]bool // key: {reason, refID}
}

func newFakeCreditRepo() *fakeCreditRepo {
	return &fakeCreditRepo{
		scores:  map[int64]int32{},
		applied: map[[2]int64]bool{},
	}
}

func (f *fakeCreditRepo) Adjust(_ context.Context, userID int64, delta int32,
	reason uint8, refID int64, _ string, minScore, maxScore int32) (int32, bool, error) {

	f.mu.Lock()
	defer f.mu.Unlock()

	if _, ok := f.scores[userID]; !ok {
		f.scores[userID] = CreditScoreInitial
	}
	key := [2]int64{int64(reason), refID}
	if f.applied[key] {
		return f.scores[userID], false, nil
	}
	f.applied[key] = true

	next := f.scores[userID] + delta
	if next < minScore {
		next = minScore
	}
	if next > maxScore {
		next = maxScore
	}
	f.scores[userID] = next
	return next, true, nil
}

type fakeSchoolRepo struct {
	schools  map[int64]*School
	campuses map[int64]*Campus
}

func newFakeSchoolRepo() *fakeSchoolRepo {
	return &fakeSchoolRepo{
		schools: map[int64]*School{
			1: {ID: 1, Name: "示例大学", Province: "北京",
				EmailDomains: []string{"stu.example.edu.cn", "example.edu.cn"}},
		},
		campuses: map[int64]*Campus{
			11: {ID: 11, SchoolID: 1, Name: "主校区"},
			12: {ID: 12, SchoolID: 1, Name: "东校区"},
			21: {ID: 21, SchoolID: 2, Name: "他校校区"},
		},
	}
}

func (f *fakeSchoolRepo) ListSchools(_ context.Context, keyword string, _, _ int) ([]*School, int64, error) {
	var out []*School
	for _, s := range f.schools {
		if keyword == "" || strings.Contains(s.Name, keyword) {
			out = append(out, s)
		}
	}
	return out, int64(len(out)), nil
}

func (f *fakeSchoolRepo) GetSchool(_ context.Context, id int64) (*School, error) {
	s, ok := f.schools[id]
	if !ok {
		return nil, errs.ErrNotFound
	}
	return s, nil
}

func (f *fakeSchoolRepo) ListCampuses(_ context.Context, schoolID int64) ([]*Campus, error) {
	var out []*Campus
	for _, c := range f.campuses {
		if c.SchoolID == schoolID {
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *fakeSchoolRepo) GetCampus(_ context.Context, id int64) (*Campus, error) {
	c, ok := f.campuses[id]
	if !ok {
		return nil, errs.ErrNotFound
	}
	return c, nil
}

type fakeAuthRepo struct {
	mu     sync.Mutex
	byID   map[int64]*CampusAuth
	byUser map[int64][]int64
	purged map[int64]bool
}

func newFakeAuthRepo() *fakeAuthRepo {
	return &fakeAuthRepo{
		byID:   map[int64]*CampusAuth{},
		byUser: map[int64][]int64{},
		purged: map[int64]bool{},
	}
}

func (f *fakeAuthRepo) Create(_ context.Context, a *CampusAuth) (*CampusAuth, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := *a
	cp.CreatedAt = time.Now()
	f.byID[a.ID] = &cp
	f.byUser[a.UserID] = append(f.byUser[a.UserID], a.ID)
	out := cp
	return &out, nil
}

func (f *fakeAuthRepo) GetLatestByUser(_ context.Context, userID int64) (*CampusAuth, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ids := f.byUser[userID]
	if len(ids) == 0 {
		return nil, errs.ErrNotFound
	}
	out := *f.byID[ids[len(ids)-1]]
	return &out, nil
}

func (f *fakeAuthRepo) GetByID(_ context.Context, id int64) (*CampusAuth, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.byID[id]
	if !ok {
		return nil, errs.ErrNotFound
	}
	out := *a
	return &out, nil
}

func (f *fakeAuthRepo) MarkPassed(_ context.Context, id, reviewerID int64, expireAt *time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.byID[id]
	if !ok {
		return errs.ErrNotFound
	}
	now := time.Now()
	a.Status, a.ReviewerID, a.VerifiedAt, a.ExpireAt = CampusAuthPassed, reviewerID, &now, expireAt
	return nil
}

func (f *fakeAuthRepo) MarkRejected(_ context.Context, id, reviewerID int64, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.byID[id]
	if !ok {
		return errs.ErrNotFound
	}
	a.Status, a.ReviewerID, a.RejectReason = CampusAuthRejected, reviewerID, reason
	return nil
}

func (f *fakeAuthRepo) ExistsPassedStudentNo(_ context.Context, schoolID int64,
	studentNo string, exceptUserID int64) (bool, error) {

	f.mu.Lock()
	defer f.mu.Unlock()
	for _, a := range f.byID {
		if a.SchoolID == schoolID && a.StudentNo == studentNo &&
			a.Status == CampusAuthPassed && a.UserID != exceptUserID {
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeAuthRepo) ListImagesToPurge(_ context.Context, retention time.Duration,
	limit int) ([]*CampusAuth, error) {

	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*CampusAuth
	for _, a := range f.byID {
		if f.purged[a.ID] || len(a.ImageKeys) == 0 || a.VerifiedAt == nil {
			continue
		}
		if time.Since(*a.VerifiedAt) >= retention {
			cp := *a
			out = append(out, &cp)
		}
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (f *fakeAuthRepo) MarkImagePurged(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.purged[id] = true
	if a, ok := f.byID[id]; ok {
		a.ImageKeys = nil
	}
	return nil
}

type fakeEmailTokenStore struct {
	mu   sync.Mutex
	data map[string]int64
}

func newFakeEmailTokenStore() *fakeEmailTokenStore {
	return &fakeEmailTokenStore{data: map[string]int64{}}
}

func (f *fakeEmailTokenStore) Save(_ context.Context, token string, authID int64, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.data[token] = authID
	return nil
}

func (f *fakeEmailTokenStore) Consume(_ context.Context, token string) (int64, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id, ok := f.data[token]
	if !ok {
		return 0, false, nil
	}
	delete(f.data, token)
	return id, true, nil
}

type fakeMailer struct {
	mu    sync.Mutex
	links []string
	err   error
}

func (f *fakeMailer) SendCampusAuthLink(_ context.Context, email, link string, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.links = append(f.links, link)
	return nil
}

// lastToken 从最近一封邮件的链接中取出 token，模拟用户点击。
func (f *fakeMailer) lastToken() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.links) == 0 {
		return ""
	}
	link := f.links[len(f.links)-1]
	i := strings.Index(link, "token=")
	if i < 0 {
		return ""
	}
	return link[i+len("token="):]
}

// fakeStorage 只实现 Stat 与 Delete，其余方法本测试用不到。
type fakeStorage struct {
	mu      sync.Mutex
	objects map[string]bool
	deleted []string
}

func newFakeStorage() *fakeStorage {
	return &fakeStorage{objects: map[string]bool{}}
}

func (f *fakeStorage) put(key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[key] = true
}

func (f *fakeStorage) CreateUploadURL(context.Context, storage.UploadRequest) (*storage.PresignedUpload, error) {
	return nil, errors.New("not implemented")
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
	return &storage.ObjectInfo{Key: key, Size: 1024, ContentType: "image/jpeg"}, nil
}

func (f *fakeStorage) Delete(_ context.Context, keys ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, k := range keys {
		delete(f.objects, k)
		f.deleted = append(f.deleted, k)
	}
	return nil
}

func (f *fakeStorage) Move(context.Context, string, string) error { return nil }
