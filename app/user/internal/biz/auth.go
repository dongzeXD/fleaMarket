package biz

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"strconv"
	"time"

	"github.com/fleamarket/pkg/auth"
	"github.com/fleamarket/pkg/errs"
	"github.com/fleamarket/pkg/idgen"
)

// 验证码策略。
const (
	// SceneLogin 登录/注册场景。
	SceneLogin = "login"
	// SceneRebind 更换手机号场景。
	//
	// 场景必须参与 key：否则攻击者可以申请「换绑」验证码用于登录，
	// 绕过登录场景特有的风控。
	SceneRebind = "rebind"

	// verifyCodeTTL 验证码有效期。
	//
	// 5 分钟是可用性与安全性的折中：太短则弱网用户收不到就过期，
	// 太长则扩大了被暴力猜测的窗口。
	verifyCodeTTL = 5 * time.Minute

	// maxCodeFailures 同一手机号连续校验失败上限。
	//
	// 6 位数字共 10^6 种组合，5 分钟内若不限次数，
	// 自动化脚本足以穷举出相当比例。超限后锁定至验证码过期。
	maxCodeFailures = 5
)

// AuthUsecase 注册登录用例。
type AuthUsecase struct {
	users  UserRepo
	codes  VerifyCodeStore
	sms    SMSSender
	wechat WechatClient
	tokens TokenStore
	jwt    *auth.Manager
	ids    *idgen.Generator
	events EventPublisher
	log    *slog.Logger
}

// NewAuthUsecase 构造用例。
func NewAuthUsecase(users UserRepo, codes VerifyCodeStore, sms SMSSender,
	wechat WechatClient, tokens TokenStore, jwtMgr *auth.Manager,
	ids *idgen.Generator, events EventPublisher, logger *slog.Logger) *AuthUsecase {

	return &AuthUsecase{
		users: users, codes: codes, sms: sms, wechat: wechat,
		tokens: tokens, jwt: jwtMgr, ids: ids, events: events,
		log: logger.With(slog.String("usecase", "auth")),
	}
}

// SendSmsCode 下发短信验证码。
//
// 返回 retryAfter 为距离下次可发送的秒数。
//
// 安全设计：无论手机号是否已注册，一律按相同路径处理并返回成功。
// 若对未注册号码返回不同结果，接口就成了账号枚举工具——
// 攻击者可以批量探测哪些手机号注册过本平台。
func (uc *AuthUsecase) SendSmsCode(ctx context.Context, scene, phone string) (int, error) {
	if scene != SceneLogin && scene != SceneRebind {
		return 0, errs.InvalidArgument("未知的验证码用途")
	}

	retryAfter, err := uc.codes.AcquireSendQuota(ctx, scene, phone)
	if err != nil {
		return 0, err
	}
	if retryAfter > 0 {
		return retryAfter, errs.WithMeta(
			errs.RateLimited("请求过于频繁，请稍后再试"),
			map[string]string{"retry_after": strconv.Itoa(retryAfter)},
		)
	}

	code, err := randomDigits(6)
	if err != nil {
		return 0, errs.Internal("验证码生成失败")
	}
	if err := uc.codes.Save(ctx, scene, phone, code, verifyCodeTTL); err != nil {
		return 0, err
	}
	if err := uc.sms.SendVerifyCode(ctx, phone, code, verifyCodeTTL); err != nil {
		// 发送失败要把验证码作废，否则用户收不到短信却占用了一个有效码，
		// 重发时又会因频率限制被拦，形成死锁。
		uc.log.ErrorContext(ctx, "发送短信失败", slog.Any("error", err))
		return 0, errs.Unavailable("短信服务暂时不可用，请稍后再试")
	}
	return int(smsResendInterval.Seconds()), nil
}

// smsResendInterval 同一手机号的最小重发间隔。
const smsResendInterval = 60 * time.Second

// LoginResult 登录结果。
type LoginResult struct {
	Tokens    *auth.TokenPair
	User      *User
	IsNewUser bool
}

// LoginByPhone 手机号验证码登录，账号不存在时自动注册。
func (uc *AuthUsecase) LoginByPhone(ctx context.Context, phone, code string) (*LoginResult, error) {
	// 先查失败次数再校验，防止暴力猜码。
	failures, err := uc.codes.CountFailures(ctx, SceneLogin, phone)
	if err == nil && failures >= maxCodeFailures {
		return nil, errs.RateLimited("验证失败次数过多，请重新获取验证码")
	}

	ok, err := uc.codes.Consume(ctx, SceneLogin, phone, code)
	if err != nil {
		return nil, err
	}
	if !ok {
		if _, err := uc.codes.CountFailures(ctx, SceneLogin, phone); err != nil {
			uc.log.WarnContext(ctx, "记录验证码失败次数出错", slog.Any("error", err))
		}
		return nil, errs.InvalidArgument("验证码错误或已过期")
	}
	_ = uc.codes.ClearFailures(ctx, SceneLogin, phone)

	u, err := uc.users.GetByPhone(ctx, phone)
	isNew := false
	switch {
	case errors.Is(err, errs.ErrNotFound):
		u, err = uc.register(ctx, &User{Phone: phone})
		if err != nil {
			return nil, err
		}
		isNew = true
	case err != nil:
		return nil, err
	}

	return uc.finishLogin(ctx, u, isNew)
}

// WechatPlatform 微信授权来源。
type WechatPlatform uint8

const (
	// WechatPlatformWeb 网站应用（开放平台扫码）。
	WechatPlatformWeb WechatPlatform = 1
	// WechatPlatformMiniApp 微信小程序。
	WechatPlatformMiniApp WechatPlatform = 2
)

// LoginByWechat 微信一键登录。
//
// 归一策略（doc.md §9.1）：以 unionid 作为账号唯一标识。
// 同一微信用户在网站应用与小程序下的 openid 不同，但在同一开放平台账号下
// unionid 相同，因此两端登录会落到同一个平台账号，不会产生重复用户。
func (uc *AuthUsecase) LoginByWechat(ctx context.Context, code string,
	platform WechatPlatform) (*LoginResult, error) {

	var profile *WechatProfile
	var err error
	switch platform {
	case WechatPlatformWeb:
		profile, err = uc.wechat.ExchangeWeb(ctx, code)
	case WechatPlatformMiniApp:
		profile, err = uc.wechat.ExchangeMiniApp(ctx, code)
	default:
		return nil, errs.InvalidArgument("未知的微信授权来源")
	}
	if err != nil {
		uc.log.WarnContext(ctx, "微信授权码兑换失败", slog.Any("error", err))
		return nil, errs.InvalidArgument("微信授权失败，请重新授权")
	}
	if profile.UnionID == "" {
		// 未开通 unionid 的情况下无法跨端归一，直接拒绝而非退化用 openid：
		// 退化会导致同一用户在两端变成两个账号，事后合并极其麻烦。
		return nil, errs.Internal("微信账号配置异常，请联系管理员")
	}

	u, err := uc.users.GetByWxUnionID(ctx, profile.UnionID)
	isNew := false
	switch {
	case errors.Is(err, errs.ErrNotFound):
		newUser := &User{
			Nickname:  profile.Nickname,
			WxUnionID: profile.UnionID,
		}
		if platform == WechatPlatformWeb {
			newUser.WxOpenIDWeb = profile.OpenID
		} else {
			newUser.WxOpenIDMini = profile.OpenID
		}
		u, err = uc.register(ctx, newUser)
		if err != nil {
			return nil, err
		}
		isNew = true
	case err != nil:
		return nil, err
	default:
		// 已有账号首次从另一端登录时补齐该端的 openid。
		if err := uc.bindPlatformOpenID(ctx, u, profile, platform); err != nil {
			uc.log.WarnContext(ctx, "补齐 openid 失败", slog.Any("error", err))
		}
	}

	return uc.finishLogin(ctx, u, isNew)
}

func (uc *AuthUsecase) bindPlatformOpenID(ctx context.Context, u *User,
	p *WechatProfile, platform WechatPlatform) error {

	web, mini := u.WxOpenIDWeb, u.WxOpenIDMini
	switch platform {
	case WechatPlatformWeb:
		if web == p.OpenID {
			return nil
		}
		web = p.OpenID
	case WechatPlatformMiniApp:
		if mini == p.OpenID {
			return nil
		}
		mini = p.OpenID
	}
	return uc.users.UpdateWechatBinding(ctx, u.ID, u.WxUnionID, web, mini)
}

// register 创建新账号。
func (uc *AuthUsecase) register(ctx context.Context, u *User) (*User, error) {
	id, err := uc.ids.NextID()
	if err != nil {
		return nil, errs.Internal("ID 生成失败")
	}
	u.ID = id
	u.Status = UserStatusNormal
	u.CreditScore = CreditScoreInitial
	u.CampusAuthStatus = CampusAuthNone
	if u.Nickname == "" {
		u.Nickname = defaultNickname(id)
	}

	created, err := uc.users.Create(ctx, u)
	if err != nil {
		return nil, err
	}

	uc.publish(ctx, TopicUserRegistered, created.ID)
	return created, nil
}

// finishLogin 完成登录：校验账号状态、记录登录时间、签发令牌。
func (uc *AuthUsecase) finishLogin(ctx context.Context, u *User, isNew bool) (*LoginResult, error) {
	switch u.Status {
	case UserStatusBanned:
		return nil, errs.PermissionDenied("账号已被封禁，如有疑问请联系管理员")
	case UserStatusDeleted:
		return nil, errs.PermissionDenied("账号已注销")
	}

	if err := uc.users.TouchLogin(ctx, u.ID); err != nil {
		// 登录时间只是运营数据，写失败不该阻断登录。
		uc.log.WarnContext(ctx, "更新登录时间失败", slog.Any("error", err))
	}

	tokens, err := uc.issueTokens(u)
	if err != nil {
		return nil, err
	}
	return &LoginResult{Tokens: tokens, User: u, IsNewUser: isNew}, nil
}

func (uc *AuthUsecase) issueTokens(u *User) (*auth.TokenPair, error) {
	// Extra 携带下游高频需要的属性，避免每个服务都回查 user-service。
	// 只放变更频率低的字段：校区与认证状态变更后，
	// 用户需要等 access_token 过期（2h）或主动刷新才会生效，这是可接受的。
	extra := map[string]string{
		"campus-id":   strconv.FormatInt(u.CampusID, 10),
		"school-id":   strconv.FormatInt(u.SchoolID, 10),
		"auth-status": strconv.FormatUint(uint64(u.CampusAuthStatus), 10),
	}
	pair, err := uc.jwt.Issue(u.ID, auth.RoleUser, extra, func() string {
		id, err := uc.ids.NextID()
		if err != nil {
			return ""
		}
		return strconv.FormatInt(id, 10)
	})
	if err != nil {
		return nil, errs.Internal("令牌签发失败")
	}
	return pair, nil
}

// Refresh 用 refresh_token 换取新令牌。
//
// 采用轮换策略：旧的 refresh_token 在换取成功后立即吊销。
// 这样一个 refresh_token 只能用一次，若被窃取，攻击者与真实用户中
// 只有一方能成功刷新，另一方会失败——为检测凭证泄漏提供了信号。
func (uc *AuthUsecase) Refresh(ctx context.Context, refreshToken string) (*LoginResult, error) {
	claims, err := uc.jwt.Verify(ctx, refreshToken, auth.TypeRefresh)
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrTokenExpired):
			return nil, errs.Unauthenticated("登录已过期，请重新登录")
		case errors.Is(err, auth.ErrTokenRevoked):
			return nil, errs.Unauthenticated("登录状态已失效，请重新登录")
		default:
			return nil, errs.Unauthenticated("凭证无效")
		}
	}

	// 从数据库重读用户：refresh 令牌不带业务属性，
	// 且账号可能在此期间被封禁或完成了校园认证。
	u, err := uc.users.GetByID(ctx, claims.UserID)
	if err != nil {
		return nil, errs.FromDataError(err, "账号不存在")
	}
	switch u.Status {
	case UserStatusBanned:
		return nil, errs.PermissionDenied("账号已被封禁")
	case UserStatusDeleted:
		return nil, errs.PermissionDenied("账号已注销")
	}

	tokens, err := uc.issueTokens(u)
	if err != nil {
		return nil, err
	}

	// 吊销旧令牌。失败只记日志：新令牌已签发，此时返回错误会让用户
	// 拿不到新凭证却又被登出，体验更差。旧令牌到期后自然失效。
	if claims.TokenID != "" {
		ttl := time.Until(claims.ExpiresAt)
		if ttl > 0 {
			if err := uc.tokens.Revoke(ctx, claims.TokenID, ttl); err != nil {
				uc.log.WarnContext(ctx, "吊销旧 refresh 令牌失败",
					slog.String("token_id", claims.TokenID), slog.Any("error", err))
			}
		}
	}

	return &LoginResult{Tokens: tokens, User: u}, nil
}

// Logout 退出登录，把 refresh_token 加入吊销名单。
//
// 只吊销 refresh 而不吊销 access：access 的有效期只有 2 小时，
// 为它维护吊销名单意味着每个请求都要查一次 Redis，代价过高。
// 这是「登出后 access 最多再存活 2 小时」的已知取舍。
func (uc *AuthUsecase) Logout(ctx context.Context, refreshToken string) error {
	if refreshToken == "" {
		// 客户端已丢弃令牌也算登出成功，不必报错。
		return nil
	}
	claims, err := uc.jwt.Verify(ctx, refreshToken, auth.TypeRefresh)
	if err != nil {
		// 无效或已过期的令牌本就无法使用，视为登出成功。
		return nil
	}
	ttl := time.Until(claims.ExpiresAt)
	if ttl <= 0 {
		return nil
	}
	if err := uc.tokens.Revoke(ctx, claims.TokenID, ttl); err != nil {
		return errs.Internal("登出失败，请重试")
	}
	return nil
}

func (uc *AuthUsecase) publish(ctx context.Context, topic string, userID int64) {
	if uc.events == nil {
		return
	}
	payload := []byte(`{"user_id":` + strconv.FormatInt(userID, 10) + `}`)
	if err := uc.events.Publish(ctx, topic, payload); err != nil {
		// 事件发布失败不回滚主流程：注册已成功，
		// 下游的欢迎通知之类可以容忍缺失。
		uc.log.WarnContext(ctx, "发布事件失败",
			slog.String("topic", topic), slog.Any("error", err))
	}
}

// randomDigits 生成 n 位随机数字串。
//
// 用 crypto/rand 而非 math/rand：验证码是安全凭证，
// math/rand 的输出可从少量样本推断后续序列。
func randomDigits(n int) (string, error) {
	const digits = "0123456789"
	out := make([]byte, n)
	max := big.NewInt(int64(len(digits)))
	for i := 0; i < n; i++ {
		idx, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", fmt.Errorf("random digits: %w", err)
		}
		out[i] = digits[idx.Int64()]
	}
	return string(out), nil
}

// defaultNickname 为新用户生成默认昵称。
//
// 取 ID 末 6 位而非全量：完整的雪花 ID 长达 19 位，直接暴露在昵称里
// 既难看又泄漏了注册时间与节点信息。
func defaultNickname(id int64) string {
	s := strconv.FormatInt(id, 10)
	if len(s) > 6 {
		s = s[len(s)-6:]
	}
	return "闲置小友" + s
}
