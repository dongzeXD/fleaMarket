// Package proxy 把网关收到的 HTTP 请求转发到下游 gRPC 服务。
//
// 实现方式：网关实现 proto 生成的 HTTPServer 接口，每个方法直接调用对应的
// gRPC 客户端。这样带来三个好处：
//   - 路由与参数绑定完全由 proto 的 google.api.http 注解生成，无需手写；
//   - 请求/响应类型在编译期对齐，契约变更会在编译时暴露；
//   - 网关不需要感知下游的 URL 结构，只依赖服务名与 etcd 发现。
//
// 代价是每新增一个下游方法要加一个转发函数。这是刻意的取舍——
// 相比通用的反射式转发，显式转发让「哪些接口对外可见」一目了然，
// 不会因为下游新增内部接口而意外暴露到公网。
package proxy

import (
	"context"

	"github.com/fleamarket/app/gateway/internal/conf"

	userv1 "github.com/fleamarket/api/user/v1"
)

// UserProxy 转发用户与身份相关的请求。
type UserProxy struct {
	userv1.UnimplementedAuthServiceServer
	userv1.UnimplementedUserServiceServer
	userv1.UnimplementedCampusAuthServiceServer

	auth   userv1.AuthServiceClient
	user   userv1.UserServiceClient
	campus userv1.CampusAuthServiceClient
	cookie *refreshCookie
}

// NewUserProxy 构造转发器。
func NewUserProxy(
	auth userv1.AuthServiceClient,
	user userv1.UserServiceClient,
	campus userv1.CampusAuthServiceClient,
	authCfg *conf.Auth,
) *UserProxy {
	return &UserProxy{
		auth: auth, user: user, campus: campus,
		cookie: newRefreshCookie(authCfg),
	}
}

// --- AuthService ---

func (p *UserProxy) SendSmsCode(ctx context.Context,
	req *userv1.SendSmsCodeRequest) (*userv1.SendSmsCodeReply, error) {
	return p.auth.SendSmsCode(ctx, req)
}

func (p *UserProxy) LoginByPhone(ctx context.Context,
	req *userv1.LoginByPhoneRequest) (*userv1.LoginReply, error) {
	reply, err := p.auth.LoginByPhone(ctx, req)
	if err != nil {
		return nil, err
	}
	return p.cookie.apply(ctx, reply), nil
}

func (p *UserProxy) LoginByWechat(ctx context.Context,
	req *userv1.LoginByWechatRequest) (*userv1.LoginReply, error) {
	reply, err := p.auth.LoginByWechat(ctx, req)
	if err != nil {
		return nil, err
	}
	return p.cookie.apply(ctx, reply), nil
}

// RefreshToken 刷新令牌。
//
// Web 端的 refresh_token 在 httpOnly Cookie 里，请求体为空，
// 故此处从 Cookie 补齐后再转发给下游。
func (p *UserProxy) RefreshToken(ctx context.Context,
	req *userv1.RefreshTokenRequest) (*userv1.LoginReply, error) {
	req.RefreshToken = p.cookie.read(ctx, req.GetRefreshToken())
	reply, err := p.auth.RefreshToken(ctx, req)
	if err != nil {
		return nil, err
	}
	return p.cookie.apply(ctx, reply), nil
}

func (p *UserProxy) Logout(ctx context.Context,
	req *userv1.LogoutRequest) (*userv1.LogoutReply, error) {
	req.RefreshToken = p.cookie.read(ctx, req.GetRefreshToken())
	reply, err := p.auth.Logout(ctx, req)
	if err != nil {
		return nil, err
	}
	// 即使下游成功，也必须清掉浏览器端的 Cookie，
	// 否则前端仍持有一个已被吊销的令牌，下次刷新才会发现失效。
	p.cookie.clear(ctx)
	return reply, nil
}

// --- UserService ---
//
// 注意此处刻意不转发 BatchGetUserBrief / IsBlocked / AdjustCredit：
// 它们是服务间内部接口（proto 中无 http 注解），若从网关暴露出去，
// 任何人都能任意调整他人信誉分。不写转发函数，编译产物里就不存在这条路径。

func (p *UserProxy) GetMe(ctx context.Context,
	req *userv1.GetMeRequest) (*userv1.UserProfile, error) {
	return p.user.GetMe(ctx, req)
}

func (p *UserProxy) UpdateMe(ctx context.Context,
	req *userv1.UpdateMeRequest) (*userv1.UserProfile, error) {
	return p.user.UpdateMe(ctx, req)
}

func (p *UserProxy) GetUserHomepage(ctx context.Context,
	req *userv1.GetUserHomepageRequest) (*userv1.UserHomepage, error) {
	return p.user.GetUserHomepage(ctx, req)
}

func (p *UserProxy) FollowUser(ctx context.Context,
	req *userv1.FollowUserRequest) (*userv1.FollowUserReply, error) {
	return p.user.FollowUser(ctx, req)
}

func (p *UserProxy) UnfollowUser(ctx context.Context,
	req *userv1.UnfollowUserRequest) (*userv1.UnfollowUserReply, error) {
	return p.user.UnfollowUser(ctx, req)
}

func (p *UserProxy) BlockUser(ctx context.Context,
	req *userv1.BlockUserRequest) (*userv1.BlockUserReply, error) {
	return p.user.BlockUser(ctx, req)
}

func (p *UserProxy) UnblockUser(ctx context.Context,
	req *userv1.UnblockUserRequest) (*userv1.UnblockUserReply, error) {
	return p.user.UnblockUser(ctx, req)
}

func (p *UserProxy) ListFollowing(ctx context.Context,
	req *userv1.ListFollowingRequest) (*userv1.ListFollowingReply, error) {
	return p.user.ListFollowing(ctx, req)
}

func (p *UserProxy) ListBlocked(ctx context.Context,
	req *userv1.ListBlockedRequest) (*userv1.ListBlockedReply, error) {
	return p.user.ListBlocked(ctx, req)
}

// --- CampusAuthService ---

func (p *UserProxy) ListSchools(ctx context.Context,
	req *userv1.ListSchoolsRequest) (*userv1.ListSchoolsReply, error) {
	return p.campus.ListSchools(ctx, req)
}

func (p *UserProxy) ListCampuses(ctx context.Context,
	req *userv1.ListCampusesRequest) (*userv1.ListCampusesReply, error) {
	return p.campus.ListCampuses(ctx, req)
}

func (p *UserProxy) StartEmailAuth(ctx context.Context,
	req *userv1.StartEmailAuthRequest) (*userv1.StartEmailAuthReply, error) {
	return p.campus.StartEmailAuth(ctx, req)
}

func (p *UserProxy) VerifyEmailAuth(ctx context.Context,
	req *userv1.VerifyEmailAuthRequest) (*userv1.VerifyEmailAuthReply, error) {
	return p.campus.VerifyEmailAuth(ctx, req)
}

func (p *UserProxy) SubmitStudentCardAuth(ctx context.Context,
	req *userv1.SubmitStudentCardAuthRequest) (*userv1.SubmitStudentCardAuthReply, error) {
	return p.campus.SubmitStudentCardAuth(ctx, req)
}

func (p *UserProxy) GetMyCampusAuth(ctx context.Context,
	req *userv1.GetMyCampusAuthRequest) (*userv1.CampusAuth, error) {
	return p.campus.GetMyCampusAuth(ctx, req)
}

// 编译期确认三个 HTTP 服务接口都已实现。
var (
	_ userv1.AuthServiceHTTPServer       = (*UserProxy)(nil)
	_ userv1.UserServiceHTTPServer       = (*UserProxy)(nil)
	_ userv1.CampusAuthServiceHTTPServer = (*UserProxy)(nil)
)
