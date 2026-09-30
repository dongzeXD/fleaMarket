package auth

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"
)

const testSecret = "a-very-long-test-secret-at-least-32-bytes!!"

func newTestManager(t *testing.T, revoker Revoker) *Manager {
	t.Helper()
	m, err := NewManager(Config{
		Secret:     testSecret,
		Issuer:     "fleamarket-test",
		AccessTTL:  time.Hour,
		RefreshTTL: 24 * time.Hour,
	}, revoker)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

var seq int

func idGen() string {
	seq++
	return "tok-" + strconv.Itoa(seq)
}

// 密钥过短必须在启动时拒绝，而不是等到线上被暴力破解。
func TestNewManagerRejectsWeakSecret(t *testing.T) {
	if _, err := NewManager(Config{Secret: ""}, nil); err == nil {
		t.Fatal("空密钥应报错")
	}
	if _, err := NewManager(Config{Secret: "short"}, nil); err == nil {
		t.Fatal("过短密钥应报错")
	}
}

func TestIssueAndVerify(t *testing.T) {
	m := newTestManager(t, nil)
	pair, err := m.Issue(10086, RoleUser, map[string]string{"campus-id": "7"}, idGen)
	if err != nil {
		t.Fatal(err)
	}
	if pair.AccessToken == "" || pair.RefreshToken == "" {
		t.Fatal("令牌为空")
	}
	if pair.AccessExpiresIn != 3600 {
		t.Fatalf("expires_in 应为 3600，实际 %d", pair.AccessExpiresIn)
	}

	claims, err := m.Verify(context.Background(), pair.AccessToken, TypeAccess)
	if err != nil {
		t.Fatal(err)
	}
	if claims.UserID != 10086 {
		t.Fatalf("UserID = %d", claims.UserID)
	}
	if claims.Role != RoleUser {
		t.Fatalf("Role = %q", claims.Role)
	}
	if claims.Extra["campus-id"] != "7" {
		t.Fatalf("Extra 丢失: %v", claims.Extra)
	}
}

// 这是最关键的一条：refresh_token 绝不能当 access_token 使用，
// 否则 access 的短有效期形同虚设。
func TestRefreshTokenCannotBeUsedAsAccessToken(t *testing.T) {
	m := newTestManager(t, nil)
	pair, err := m.Issue(1, RoleUser, nil, idGen)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Verify(context.Background(), pair.RefreshToken, TypeAccess); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("refresh 当 access 用应被拒绝，实际 %v", err)
	}
	// 反向同理。
	if _, err := m.Verify(context.Background(), pair.AccessToken, TypeRefresh); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("access 当 refresh 用应被拒绝，实际 %v", err)
	}
	// 正确用法应通过。
	if _, err := m.Verify(context.Background(), pair.RefreshToken, TypeRefresh); err != nil {
		t.Fatalf("refresh 作为 refresh 校验应通过: %v", err)
	}
}

// refresh 令牌不应携带业务属性，否则过期的校区信息会被长期延续。
func TestRefreshTokenCarriesNoExtra(t *testing.T) {
	m := newTestManager(t, nil)
	pair, err := m.Issue(1, RoleUser, map[string]string{"campus-id": "7"}, idGen)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := m.Verify(context.Background(), pair.RefreshToken, TypeRefresh)
	if err != nil {
		t.Fatal(err)
	}
	if len(claims.Extra) != 0 {
		t.Fatalf("refresh 不应携带 Extra，实际 %v", claims.Extra)
	}
}

func TestVerifyExpiredToken(t *testing.T) {
	m := newTestManager(t, nil)
	// 直接以负 TTL 签发一个已过期的令牌。
	// 不通过 Config 构造，因为负 TTL 的配置会被 setDefaults 主动拒绝。
	expired, err := m.sign(1, RoleUser, TypeAccess, "tok-expired", nil,
		time.Now().Add(-2*time.Hour), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Verify(context.Background(), expired, TypeAccess); !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("应返回 ErrTokenExpired，实际 %v", err)
	}
}

// 负数 TTL 是配置错误，静默改成默认值会掩盖问题。
func TestNewManagerRejectsNegativeTTL(t *testing.T) {
	if _, err := NewManager(Config{Secret: testSecret, AccessTTL: -time.Minute}, nil); err == nil {
		t.Fatal("负的 AccessTTL 应报错")
	}
	if _, err := NewManager(Config{Secret: testSecret, RefreshTTL: -time.Minute}, nil); err == nil {
		t.Fatal("负的 RefreshTTL 应报错")
	}
	// 0 表示未设置，应取默认值。
	m, err := NewManager(Config{Secret: testSecret}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if m.cfg.AccessTTL != 2*time.Hour {
		t.Fatalf("默认 AccessTTL 应为 2h，实际 %v", m.cfg.AccessTTL)
	}
}

func TestVerifyRejectsTamperedToken(t *testing.T) {
	m := newTestManager(t, nil)
	pair, err := m.Issue(1, RoleUser, nil, idGen)
	if err != nil {
		t.Fatal(err)
	}
	// 篡改最后一个字符破坏签名。
	tampered := pair.AccessToken[:len(pair.AccessToken)-1] + "X"
	if _, err := m.Verify(context.Background(), tampered, TypeAccess); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("篡改令牌应被拒绝，实际 %v", err)
	}
}

// 用另一个密钥签发的令牌不得通过校验。
func TestVerifyRejectsForeignSecret(t *testing.T) {
	other, err := NewManager(Config{
		Secret: "another-very-long-secret-at-least-32-bytes",
		Issuer: "fleamarket-test",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := other.Issue(1, RoleUser, nil, idGen)
	if err != nil {
		t.Fatal(err)
	}
	m := newTestManager(t, nil)
	if _, err := m.Verify(context.Background(), pair.AccessToken, TypeAccess); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("他方密钥签发的令牌应被拒绝，实际 %v", err)
	}
}

// alg=none 是 JWT 最经典的攻击面，必须确认被拒。
func TestVerifyRejectsAlgNone(t *testing.T) {
	m := newTestManager(t, nil)
	// {"alg":"none","typ":"JWT"} . {"uid":1,"typ":"access",...} . （空签名）
	none := "eyJhbGciOiJub25lIiwidHlwIjoiSldUIn0." +
		"eyJpc3MiOiJmbGVhbWFya2V0LXRlc3QiLCJzdWIiOiIxIiwiZXhwIjo5OTk5OTk5OTk5LCJ1aWQiOjEsInR5cCI6ImFjY2VzcyJ9."
	if _, err := m.Verify(context.Background(), none, TypeAccess); err == nil {
		t.Fatal("alg=none 必须被拒绝")
	}
}

func TestVerifyMissingToken(t *testing.T) {
	m := newTestManager(t, nil)
	if _, err := m.Verify(context.Background(), "", TypeAccess); !errors.Is(err, ErrTokenMissing) {
		t.Fatalf("空令牌应返回 ErrTokenMissing，实际 %v", err)
	}
}

type fakeRevoker struct {
	revoked map[string]bool
	err     error
}

func (f *fakeRevoker) IsRevoked(_ context.Context, id string) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	return f.revoked[id], nil
}

func TestVerifyRespectsRevocation(t *testing.T) {
	rv := &fakeRevoker{revoked: map[string]bool{}}
	m := newTestManager(t, rv)
	pair, err := m.Issue(1, RoleUser, nil, idGen)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := m.Verify(context.Background(), pair.AccessToken, TypeAccess)
	if err != nil {
		t.Fatal(err)
	}
	rv.revoked[claims.TokenID] = true
	if _, err := m.Verify(context.Background(), pair.AccessToken, TypeAccess); !errors.Is(err, ErrTokenRevoked) {
		t.Fatalf("已吊销令牌应被拒绝，实际 %v", err)
	}
}

// 吊销名单不可用时放行而非拒绝：否则 Redis 故障会升级为全站无法登录。
func TestVerifyDegradesWhenRevokerFails(t *testing.T) {
	rv := &fakeRevoker{err: errors.New("redis down")}
	m := newTestManager(t, rv)
	pair, err := m.Issue(1, RoleUser, nil, idGen)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Verify(context.Background(), pair.AccessToken, TypeAccess); err != nil {
		t.Fatalf("吊销名单故障时应放行，实际 %v", err)
	}
}

func TestIssueRejectsInvalidInput(t *testing.T) {
	m := newTestManager(t, nil)
	if _, err := m.Issue(0, RoleUser, nil, idGen); err == nil {
		t.Fatal("userID<=0 应报错")
	}
	if _, err := m.Issue(1, RoleUser, nil, nil); err == nil {
		t.Fatal("nil id 生成器应报错")
	}
}

func TestIdentityContext(t *testing.T) {
	ctx := context.Background()
	if _, ok := FromContext(ctx); ok {
		t.Fatal("空 context 不应有身份")
	}
	if got := MustUserID(ctx); got != 0 {
		t.Fatalf("未登录时 MustUserID 应为 0，实际 %d", got)
	}

	ctx = NewContext(ctx, &Identity{UserID: 42, Role: RoleAdmin})
	id, ok := FromContext(ctx)
	if !ok || id.UserID != 42 {
		t.Fatalf("身份读取失败: %+v %v", id, ok)
	}
	if !id.IsAdmin() {
		t.Fatal("应识别为管理员")
	}
	if got := MustUserID(ctx); got != 42 {
		t.Fatalf("MustUserID = %d", got)
	}
}

func TestBearerToken(t *testing.T) {
	cases := map[string]string{
		"Bearer abc.def.ghi": "abc.def.ghi",
		"bearer abc":         "abc",
		"abc.def.ghi":        "abc.def.ghi", // 容错裸令牌
		"":                   "",
	}
	for in, want := range cases {
		if got := bearerToken(in); got != want {
			t.Errorf("bearerToken(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseUnverifiedDoesNotValidate(t *testing.T) {
	m := newTestManager(t, nil)
	pair, err := m.Issue(777, RoleUser, nil, idGen)
	if err != nil {
		t.Fatal(err)
	}
	tampered := pair.AccessToken[:len(pair.AccessToken)-1] + "X"
	// 签名已坏，但仍应能读出载荷用于日志排查。
	claims, err := ParseUnverified(tampered)
	if err != nil {
		t.Fatal(err)
	}
	if claims.UserID != 777 {
		t.Fatalf("UserID = %d", claims.UserID)
	}
}
