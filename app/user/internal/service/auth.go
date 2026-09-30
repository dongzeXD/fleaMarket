package service

import (
	"context"

	"github.com/fleamarket/app/user/internal/biz"

	userv1 "github.com/fleamarket/api/user/v1"
	"github.com/fleamarket/pkg/auth"
	"github.com/fleamarket/pkg/crypto"
	"github.com/fleamarket/pkg/errs"
)

// AuthService 实现 api/user/v1 的 AuthService。
//
// 本层只做 DTO 与领域对象的转换，不含任何业务规则——
// 规则全部在 biz 层，这样换传输协议（如新增 gRPC-Web）不影响业务逻辑。
type AuthService struct {
	userv1.UnimplementedAuthServiceServer

	uc *biz.AuthUsecase
}

// NewAuthService 构造服务。
func NewAuthService(uc *biz.AuthUsecase) *AuthService {
	return &AuthService{uc: uc}
}

// SendSmsCode 下发短信验证码。
func (s *AuthService) SendSmsCode(ctx context.Context,
	req *userv1.SendSmsCodeRequest) (*userv1.SendSmsCodeReply, error) {

	scene, err := smsSceneToBiz(req.GetScene())
	if err != nil {
		return nil, err
	}
	retryAfter, err := s.uc.SendSmsCode(ctx, scene, req.GetPhone())
	if err != nil {
		return nil, err
	}
	return &userv1.SendSmsCodeReply{RetryAfterSeconds: int32(retryAfter)}, nil
}

// LoginByPhone 手机号验证码登录。
func (s *AuthService) LoginByPhone(ctx context.Context,
	req *userv1.LoginByPhoneRequest) (*userv1.LoginReply, error) {

	res, err := s.uc.LoginByPhone(ctx, req.GetPhone(), req.GetCode())
	if err != nil {
		return nil, err
	}
	return loginReply(res), nil
}

// LoginByWechat 微信一键登录。
func (s *AuthService) LoginByWechat(ctx context.Context,
	req *userv1.LoginByWechatRequest) (*userv1.LoginReply, error) {

	platform, err := wechatPlatformToBiz(req.GetPlatform())
	if err != nil {
		return nil, err
	}
	res, err := s.uc.LoginByWechat(ctx, req.GetCode(), platform)
	if err != nil {
		return nil, err
	}
	return loginReply(res), nil
}

// RefreshToken 刷新访问令牌。
func (s *AuthService) RefreshToken(ctx context.Context,
	req *userv1.RefreshTokenRequest) (*userv1.LoginReply, error) {

	res, err := s.uc.Refresh(ctx, req.GetRefreshToken())
	if err != nil {
		return nil, err
	}
	return loginReply(res), nil
}

// Logout 退出登录。
func (s *AuthService) Logout(ctx context.Context,
	req *userv1.LogoutRequest) (*userv1.LogoutReply, error) {

	if err := s.uc.Logout(ctx, req.GetRefreshToken()); err != nil {
		return nil, err
	}
	return &userv1.LogoutReply{}, nil
}

// --- 枚举与结构转换 ---

func smsSceneToBiz(s userv1.SmsScene) (string, error) {
	switch s {
	case userv1.SmsScene_SMS_SCENE_LOGIN:
		return biz.SceneLogin, nil
	case userv1.SmsScene_SMS_SCENE_REBIND:
		return biz.SceneRebind, nil
	default:
		return "", errs.InvalidArgument("未知的验证码用途")
	}
}

func wechatPlatformToBiz(p userv1.WechatPlatform) (biz.WechatPlatform, error) {
	switch p {
	case userv1.WechatPlatform_WECHAT_PLATFORM_WEB:
		return biz.WechatPlatformWeb, nil
	case userv1.WechatPlatform_WECHAT_PLATFORM_MINIAPP:
		return biz.WechatPlatformMiniApp, nil
	default:
		return 0, errs.InvalidArgument("未知的微信授权来源")
	}
}

func loginReply(res *biz.LoginResult) *userv1.LoginReply {
	return &userv1.LoginReply{
		AccessToken: res.Tokens.AccessToken,
		// refresh_token 在 Web 端由网关写入 httpOnly Cookie 后清空此字段，
		// 避免落到前端可读的 JS 上下文里（doc.md §10.4）。
		// 小程序无 Cookie 机制，仍需明文下发。
		RefreshToken: res.Tokens.RefreshToken,
		ExpiresIn:    int32(res.Tokens.AccessExpiresIn),
		IsNewUser:    res.IsNewUser,
		Profile:      userProfileToProto(res.User),
	}
}

// userProfileToProto 转换本人档案。
//
// 手机号一律脱敏后下发：领域对象里是明文（业务逻辑需要它做检索与短信发送），
// 但绝不能出现在响应中。脱敏在这一层完成，是因为这里是数据离开服务的最后一道关口。
func userProfileToProto(u *biz.User) *userv1.UserProfile {
	if u == nil {
		return nil
	}
	return &userv1.UserProfile{
		UserId:           u.ID,
		Nickname:         u.Nickname,
		Avatar:           imageRef(u.AvatarKey),
		PhoneMasked:      crypto.MaskPhone(u.Phone),
		CreditScore:      u.CreditScore,
		CampusAuthStatus: campusAuthStatusToProto(u.CampusAuthStatus),
		Campus:           campusRef(u.SchoolID, u.CampusID),
		WechatBound:      u.WxUnionID != "",
		CreatedAt:        unixOrZero(u.CreatedAt),
	}
}

// 编译期确认已实现 proto 定义的全部方法。
var _ userv1.AuthServiceServer = (*AuthService)(nil)

// 让 auth 包的引用在本文件可见（RoleUser 等常量在 user.go 中使用）。
var _ = auth.RoleUser
