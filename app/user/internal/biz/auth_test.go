package biz

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/fleamarket/pkg/auth"
	"github.com/fleamarket/pkg/idgen"
	kratoserrors "github.com/go-kratos/kratos/v3/errors"
)

const testJWTSecret = "user-service-test-secret-at-least-32-bytes"

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type authFixture struct {
	uc     *AuthUsecase
	users  *fakeUserRepo
	codes  *fakeCodeStore
	sms    *fakeSMS
	wechat *fakeWechat
	tokens *fakeTokenStore
	events *fakeEvents
	jwt    *auth.Manager
}

func newAuthFixture(t *testing.T) *authFixture {
	t.Helper()
	f := &authFixture{
		users:  newFakeUserRepo(),
		codes:  newFakeCodeStore(),
		sms:    &fakeSMS{},
		wechat: &fakeWechat{},
		tokens: newFakeTokenStore(),
		events: &fakeEvents{},
	}

	// TokenStore 必须作为 Revoker 传给 auth.Manager，否则 Verify 不查吊销名单，
	// 登出会静默失效。这一条由 TestLogoutRevokesRefreshToken 守住。
	jwtMgr, err := auth.NewManager(auth.Config{
		Secret:     testJWTSecret,
		Issuer:     "fleamarket",
		AccessTTL:  time.Hour,
		RefreshTTL: 24 * time.Hour,
	}, f.tokens)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := idgen.New(1)
	if err != nil {
		t.Fatal(err)
	}
	f.jwt = jwtMgr

	f.uc = NewAuthUsecase(f.users, f.codes, f.sms, f.wechat, f.tokens,
		jwtMgr, ids, f.events, quietLogger())
	return f
}

func codeOf(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		t.Fatal("期望错误，实际为 nil")
	}
	return int(kratoserrors.FromError(err).Code)
}

func reasonOf(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		t.Fatal("期望错误，实际为 nil")
	}
	return kratoserrors.FromError(err).Reason
}

func TestSendSmsCodeThenLoginRegistersUser(t *testing.T) {
	f := newAuthFixture(t)
	ctx := context.Background()
	const phone = "13800138000"

	retry, err := f.uc.SendSmsCode(ctx, SceneLogin, phone)
	if err != nil {
		t.Fatal(err)
	}
	if retry != 60 {
		t.Fatalf("重发间隔应为 60 秒，实际 %d", retry)
	}
	if len(f.sms.sent) != 1 {
		t.Fatalf("应发出 1 条短信，实际 %d", len(f.sms.sent))
	}

	code := f.codes.peek(SceneLogin, phone)
	if len(code) != 6 {
		t.Fatalf("验证码应为 6 位，实际 %q", code)
	}

	res, err := f.uc.LoginByPhone(ctx, phone, code)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsNewUser {
		t.Fatal("首次登录应自动注册")
	}
	if res.User.CreditScore != CreditScoreInitial {
		t.Fatalf("初始信誉分应为 %d，实际 %d", CreditScoreInitial, res.User.CreditScore)
	}
	if res.User.CampusAuthStatus != CampusAuthNone {
		t.Fatal("新用户应为未认证状态")
	}
	if res.User.Nickname == "" {
		t.Fatal("应生成默认昵称")
	}
	if res.Tokens.AccessToken == "" || res.Tokens.RefreshToken == "" {
		t.Fatal("应签发令牌")
	}
	if !f.events.has(TopicUserRegistered) {
		t.Fatal("应发布注册事件")
	}
}

// 验证码必须一次性消费，否则同一条码可被重放。
func TestVerifyCodeIsSingleUse(t *testing.T) {
	f := newAuthFixture(t)
	ctx := context.Background()
	const phone = "13800138001"

	if _, err := f.uc.SendSmsCode(ctx, SceneLogin, phone); err != nil {
		t.Fatal(err)
	}
	code := f.codes.peek(SceneLogin, phone)

	if _, err := f.uc.LoginByPhone(ctx, phone, code); err != nil {
		t.Fatal(err)
	}
	// 第二次使用同一验证码必须失败。
	if _, err := f.uc.LoginByPhone(ctx, phone, code); err == nil {
		t.Fatal("同一验证码不应能使用两次")
	}
}

func TestLoginByPhoneWrongCode(t *testing.T) {
	f := newAuthFixture(t)
	ctx := context.Background()
	const phone = "13800138002"

	if _, err := f.uc.SendSmsCode(ctx, SceneLogin, phone); err != nil {
		t.Fatal(err)
	}
	_, err := f.uc.LoginByPhone(ctx, phone, "000000")
	if got := codeOf(t, err); got != 400 {
		t.Fatalf("错误验证码应返回 400，实际 %d", got)
	}
}

// 暴力猜码必须在若干次失败后被拦截，否则 6 位码在 5 分钟内可被穷举。
func TestLoginByPhoneBlocksBruteForce(t *testing.T) {
	f := newAuthFixture(t)
	ctx := context.Background()
	const phone = "13800138003"

	if _, err := f.uc.SendSmsCode(ctx, SceneLogin, phone); err != nil {
		t.Fatal(err)
	}
	var lastErr error
	for i := 0; i < maxCodeFailures+2; i++ {
		_, lastErr = f.uc.LoginByPhone(ctx, phone, "111111")
	}
	if got := codeOf(t, lastErr); got != 429 {
		t.Fatalf("超过失败上限应返回 429，实际 %d: %v", got, lastErr)
	}
}

// 同号高频发送必须被拦下，并告知等待秒数。
func TestSendSmsCodeRateLimited(t *testing.T) {
	f := newAuthFixture(t)
	ctx := context.Background()
	const phone = "13800138004"

	if _, err := f.uc.SendSmsCode(ctx, SceneLogin, phone); err != nil {
		t.Fatal(err)
	}
	retry, err := f.uc.SendSmsCode(ctx, SceneLogin, phone)
	if got := codeOf(t, err); got != 429 {
		t.Fatalf("应返回 429，实际 %d", got)
	}
	if retry <= 0 {
		t.Fatal("应返回剩余等待秒数")
	}
	if md := kratoserrors.FromError(err).Metadata; md["retry_after"] == "" {
		t.Fatal("错误元数据应携带 retry_after，便于客户端倒计时")
	}
}

func TestSendSmsCodeRejectsUnknownScene(t *testing.T) {
	f := newAuthFixture(t)
	if _, err := f.uc.SendSmsCode(context.Background(), "hack", "13800138000"); err == nil {
		t.Fatal("未知场景应报错")
	}
}

// 短信发送失败时必须返回错误，而不是让用户以为已发出。
func TestSendSmsCodeSMSFailure(t *testing.T) {
	f := newAuthFixture(t)
	f.sms.err = io.ErrUnexpectedEOF
	_, err := f.uc.SendSmsCode(context.Background(), SceneLogin, "13800138005")
	if got := codeOf(t, err); got != 503 {
		t.Fatalf("短信故障应返回 503，实际 %d", got)
	}
}

func TestLoginByWechatRegistersAndUnifiesByUnionID(t *testing.T) {
	f := newAuthFixture(t)
	ctx := context.Background()
	f.wechat.web = &WechatProfile{UnionID: "u-1", OpenID: "o-web", Nickname: "小明"}
	f.wechat.mini = &WechatProfile{UnionID: "u-1", OpenID: "o-mini", Nickname: "小明"}

	first, err := f.uc.LoginByWechat(ctx, "code-web", WechatPlatformWeb)
	if err != nil {
		t.Fatal(err)
	}
	if !first.IsNewUser {
		t.Fatal("首次微信登录应注册")
	}

	// 同一 unionid 从小程序端登录应落到同一账号，而不是新建。
	second, err := f.uc.LoginByWechat(ctx, "code-mini", WechatPlatformMiniApp)
	if err != nil {
		t.Fatal(err)
	}
	if second.IsNewUser {
		t.Fatal("同一 unionid 不应重复注册")
	}
	if second.User.ID != first.User.ID {
		t.Fatalf("两端应归一到同一账号: %d vs %d", second.User.ID, first.User.ID)
	}

	// 另一端的 openid 应被补齐。
	u, err := f.users.GetByID(ctx, first.User.ID)
	if err != nil {
		t.Fatal(err)
	}
	if u.WxOpenIDWeb != "o-web" || u.WxOpenIDMini != "o-mini" {
		t.Fatalf("openid 未补齐: web=%q mini=%q", u.WxOpenIDWeb, u.WxOpenIDMini)
	}
}

// 拿不到 unionid 时必须拒绝，不能退化用 openid——
// 退化会让同一用户在两端变成两个账号，事后合并极其麻烦。
func TestLoginByWechatRejectsEmptyUnionID(t *testing.T) {
	f := newAuthFixture(t)
	f.wechat.web = &WechatProfile{UnionID: "", OpenID: "o-web"}
	_, err := f.uc.LoginByWechat(context.Background(), "code", WechatPlatformWeb)
	if got := codeOf(t, err); got != 500 {
		t.Fatalf("缺少 unionid 应返回 500，实际 %d", got)
	}
}

func TestLoginByWechatRejectsUnknownPlatform(t *testing.T) {
	f := newAuthFixture(t)
	if _, err := f.uc.LoginByWechat(context.Background(), "code", WechatPlatform(9)); err == nil {
		t.Fatal("未知平台应报错")
	}
}

func TestBannedUserCannotLogin(t *testing.T) {
	f := newAuthFixture(t)
	ctx := context.Background()
	const phone = "13800138006"

	// 先正常注册。
	if _, err := f.uc.SendSmsCode(ctx, SceneLogin, phone); err != nil {
		t.Fatal(err)
	}
	res, err := f.uc.LoginByPhone(ctx, phone, f.codes.peek(SceneLogin, phone))
	if err != nil {
		t.Fatal(err)
	}

	// 封禁后再登录。
	f.users.mu.Lock()
	f.users.byID[res.User.ID].Status = UserStatusBanned
	f.users.mu.Unlock()

	f.codes.lastSent = map[string]time.Time{} // 绕过重发限制
	if _, err := f.uc.SendSmsCode(ctx, SceneLogin, phone); err != nil {
		t.Fatal(err)
	}
	_, err = f.uc.LoginByPhone(ctx, phone, f.codes.peek(SceneLogin, phone))
	if got := codeOf(t, err); got != 403 {
		t.Fatalf("封禁账号应返回 403，实际 %d", got)
	}
}

func TestRefreshRotatesAndRevokesOldToken(t *testing.T) {
	f := newAuthFixture(t)
	ctx := context.Background()
	const phone = "13800138007"

	if _, err := f.uc.SendSmsCode(ctx, SceneLogin, phone); err != nil {
		t.Fatal(err)
	}
	res, err := f.uc.LoginByPhone(ctx, phone, f.codes.peek(SceneLogin, phone))
	if err != nil {
		t.Fatal(err)
	}
	oldRefresh := res.Tokens.RefreshToken

	next, err := f.uc.Refresh(ctx, oldRefresh)
	if err != nil {
		t.Fatal(err)
	}
	if next.Tokens.RefreshToken == oldRefresh {
		t.Fatal("refresh 令牌应轮换")
	}

	// 旧令牌应已被吊销。
	f.tokens.mu.Lock()
	revokedCount := len(f.tokens.revoked)
	f.tokens.mu.Unlock()
	if revokedCount != 1 {
		t.Fatalf("应吊销 1 个旧令牌，实际 %d", revokedCount)
	}
}

// access_token 不能用于刷新，否则类型区分失去意义。
func TestRefreshRejectsAccessToken(t *testing.T) {
	f := newAuthFixture(t)
	ctx := context.Background()
	const phone = "13800138008"

	if _, err := f.uc.SendSmsCode(ctx, SceneLogin, phone); err != nil {
		t.Fatal(err)
	}
	res, err := f.uc.LoginByPhone(ctx, phone, f.codes.peek(SceneLogin, phone))
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.uc.Refresh(ctx, res.Tokens.AccessToken)
	if got := codeOf(t, err); got != 401 {
		t.Fatalf("用 access 刷新应返回 401，实际 %d", got)
	}
}

// 刷新时必须从数据库重读用户，使认证状态等变更能及时反映到新令牌。
func TestRefreshReloadsUserAttributes(t *testing.T) {
	f := newAuthFixture(t)
	ctx := context.Background()
	const phone = "13800138009"

	if _, err := f.uc.SendSmsCode(ctx, SceneLogin, phone); err != nil {
		t.Fatal(err)
	}
	res, err := f.uc.LoginByPhone(ctx, phone, f.codes.peek(SceneLogin, phone))
	if err != nil {
		t.Fatal(err)
	}

	// 期间完成了校园认证。
	if err := f.users.UpdateCampusSnapshot(ctx, res.User.ID, CampusAuthPassed, 1, 11); err != nil {
		t.Fatal(err)
	}

	next, err := f.uc.Refresh(ctx, res.Tokens.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := f.jwt.Verify(ctx, next.Tokens.AccessToken, auth.TypeAccess)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Extra["auth-status"] != "3" {
		t.Fatalf("新令牌应反映最新认证状态，实际 %v", claims.Extra)
	}
	if claims.Extra["campus-id"] != "11" {
		t.Fatalf("新令牌应反映最新校区，实际 %v", claims.Extra)
	}
}

func TestRefreshRejectsBannedUser(t *testing.T) {
	f := newAuthFixture(t)
	ctx := context.Background()
	const phone = "13800138010"

	if _, err := f.uc.SendSmsCode(ctx, SceneLogin, phone); err != nil {
		t.Fatal(err)
	}
	res, err := f.uc.LoginByPhone(ctx, phone, f.codes.peek(SceneLogin, phone))
	if err != nil {
		t.Fatal(err)
	}
	f.users.mu.Lock()
	f.users.byID[res.User.ID].Status = UserStatusBanned
	f.users.mu.Unlock()

	if _, err := f.uc.Refresh(ctx, res.Tokens.RefreshToken); codeOf(t, err) != 403 {
		t.Fatal("封禁账号不应能刷新令牌")
	}
}

func TestLogoutRevokesRefreshToken(t *testing.T) {
	f := newAuthFixture(t)
	ctx := context.Background()
	const phone = "13800138011"

	if _, err := f.uc.SendSmsCode(ctx, SceneLogin, phone); err != nil {
		t.Fatal(err)
	}
	res, err := f.uc.LoginByPhone(ctx, phone, f.codes.peek(SceneLogin, phone))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.uc.Logout(ctx, res.Tokens.RefreshToken); err != nil {
		t.Fatal(err)
	}
	// 已登出的令牌不能再刷新。
	if _, err := f.uc.Refresh(ctx, res.Tokens.RefreshToken); err == nil {
		t.Fatal("登出后 refresh 令牌应失效")
	}
}

// 登出是幂等的：空令牌、无效令牌都应返回成功。
func TestLogoutIsTolerant(t *testing.T) {
	f := newAuthFixture(t)
	ctx := context.Background()
	if err := f.uc.Logout(ctx, ""); err != nil {
		t.Fatalf("空令牌登出应成功: %v", err)
	}
	if err := f.uc.Logout(ctx, "garbage.token.value"); err != nil {
		t.Fatalf("无效令牌登出应成功: %v", err)
	}
}

func TestDefaultNickname(t *testing.T) {
	got := defaultNickname(1234567890123)
	if len(got) == 0 {
		t.Fatal("昵称不应为空")
	}
	// 不应把完整的 19 位雪花 ID 暴露在昵称里。
	if got == "闲置小友1234567890123" {
		t.Fatalf("不应暴露完整 ID: %q", got)
	}
}

func TestRandomDigits(t *testing.T) {
	seen := map[string]int{}
	for i := 0; i < 200; i++ {
		s, err := randomDigits(6)
		if err != nil {
			t.Fatal(err)
		}
		if len(s) != 6 {
			t.Fatalf("长度应为 6，实际 %d", len(s))
		}
		for _, c := range s {
			if c < '0' || c > '9' {
				t.Fatalf("应只含数字，实际 %q", s)
			}
		}
		seen[s]++
	}
	// 200 次抽样若大量重复，说明随机性有问题。
	if len(seen) < 150 {
		t.Fatalf("随机性不足，200 次仅产生 %d 个不同值", len(seen))
	}
}
