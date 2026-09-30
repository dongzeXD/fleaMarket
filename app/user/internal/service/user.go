package service

import (
	"context"

	"github.com/fleamarket/app/user/internal/biz"

	commonv1 "github.com/fleamarket/api/common/v1"
	userv1 "github.com/fleamarket/api/user/v1"
	"github.com/fleamarket/pkg/auth"
)

// UserService 实现 api/user/v1 的 UserService。
type UserService struct {
	userv1.UnimplementedUserServiceServer

	uc *biz.UserUsecase
}

// NewUserService 构造服务。
func NewUserService(uc *biz.UserUsecase) *UserService {
	return &UserService{uc: uc}
}

// GetMe 查询本人档案。
func (s *UserService) GetMe(ctx context.Context,
	_ *userv1.GetMeRequest) (*userv1.UserProfile, error) {

	uid, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}
	u, err := s.uc.GetByID(ctx, uid)
	if err != nil {
		return nil, err
	}
	return userProfileToProto(u), nil
}

// UpdateMe 更新本人昵称与头像。
func (s *UserService) UpdateMe(ctx context.Context,
	req *userv1.UpdateMeRequest) (*userv1.UserProfile, error) {

	uid, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}
	u, err := s.uc.UpdateProfile(ctx, uid, req.GetNickname(), req.GetAvatarKey())
	if err != nil {
		return nil, err
	}
	return userProfileToProto(u), nil
}

// GetUserHomepage 查看他人主页。
//
// 未登录也可访问：商品详情页会展示卖家信息，要求登录会让分享链接无法被
// 未注册用户打开，损失获客。因此 viewer 相关字段（是否已关注）在未登录时为 false。
func (s *UserService) GetUserHomepage(ctx context.Context,
	req *userv1.GetUserHomepageRequest) (*userv1.UserHomepage, error) {

	viewerID := auth.MustUserID(ctx)
	hp, err := s.uc.GetHomepage(ctx, req.GetUserId(), viewerID)
	if err != nil {
		return nil, err
	}
	return &userv1.UserHomepage{
		User:           userBriefToProto(hp.User),
		FollowingCount: hp.FollowingCount,
		FollowerCount:  hp.FollowerCount,
		IsFollowing:    hp.IsFollowing,
		IsBlocked:      hp.IsBlocked,
		// 商品数与交易数由网关聚合 product/trade 服务后填充；
		// 本服务无法提供，故置 partial 让客户端知道数据不完整。
		Partial: true,
	}, nil
}

// FollowUser 关注。
func (s *UserService) FollowUser(ctx context.Context,
	req *userv1.FollowUserRequest) (*userv1.FollowUserReply, error) {

	uid, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.uc.Follow(ctx, uid, req.GetUserId()); err != nil {
		return nil, err
	}
	return &userv1.FollowUserReply{}, nil
}

// UnfollowUser 取消关注。
func (s *UserService) UnfollowUser(ctx context.Context,
	req *userv1.UnfollowUserRequest) (*userv1.UnfollowUserReply, error) {

	uid, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.uc.Unfollow(ctx, uid, req.GetUserId()); err != nil {
		return nil, err
	}
	return &userv1.UnfollowUserReply{}, nil
}

// BlockUser 拉黑。
func (s *UserService) BlockUser(ctx context.Context,
	req *userv1.BlockUserRequest) (*userv1.BlockUserReply, error) {

	uid, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.uc.Block(ctx, uid, req.GetUserId(), uint8(req.GetReason())); err != nil {
		return nil, err
	}
	return &userv1.BlockUserReply{}, nil
}

// UnblockUser 取消拉黑。
func (s *UserService) UnblockUser(ctx context.Context,
	req *userv1.UnblockUserRequest) (*userv1.UnblockUserReply, error) {

	uid, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.uc.Unblock(ctx, uid, req.GetUserId()); err != nil {
		return nil, err
	}
	return &userv1.UnblockUserReply{}, nil
}

// ListFollowing 我的关注列表。
func (s *UserService) ListFollowing(ctx context.Context,
	req *userv1.ListFollowingRequest) (*userv1.ListFollowingReply, error) {

	uid, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}
	cursor, limit := cursorRequest(req.GetPage())
	page, err := s.uc.ListFollowing(ctx, uid, cursor, limit)
	if err != nil {
		return nil, err
	}
	return &userv1.ListFollowingReply{
		Users: userBriefsToProto(page.Items),
		Page:  cursorReply(page),
	}, nil
}

// ListBlocked 我的黑名单。
func (s *UserService) ListBlocked(ctx context.Context,
	req *userv1.ListBlockedRequest) (*userv1.ListBlockedReply, error) {

	uid, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}
	cursor, limit := cursorRequest(req.GetPage())
	page, err := s.uc.ListBlocked(ctx, uid, cursor, limit)
	if err != nil {
		return nil, err
	}
	return &userv1.ListBlockedReply{
		Users: userBriefsToProto(page.Items),
		Page:  cursorReply(page),
	}, nil
}

// BatchGetUserBrief 批量查询摘要。内部接口，供 product/trade/im 回填卖家信息。
func (s *UserService) BatchGetUserBrief(ctx context.Context,
	req *userv1.BatchGetUserBriefRequest) (*userv1.BatchGetUserBriefReply, error) {

	users, err := s.uc.BatchGetBrief(ctx, req.GetUserIds())
	if err != nil {
		return nil, err
	}
	out := make(map[int64]*userv1.UserBrief, len(users))
	for id, u := range users {
		out[id] = userBriefToProto(u)
	}
	return &userv1.BatchGetUserBriefReply{Users: out}, nil
}

// GetSellerInfo 查询发布准入信息。内部接口，供 product-service 调用。
func (s *UserService) GetSellerInfo(ctx context.Context,
	req *userv1.GetSellerInfoRequest) (*userv1.SellerInfo, error) {

	info, err := s.uc.GetSellerInfo(ctx, req.GetUserId())
	if err != nil {
		return nil, err
	}
	return &userv1.SellerInfo{
		UserId:         info.UserID,
		Status:         accountStatusToProto(info.Status),
		CampusVerified: info.CampusVerified,
		SchoolId:       info.SchoolID,
		CampusId:       info.CampusID,
	}, nil
}

func accountStatusToProto(s uint8) userv1.AccountStatus {
	switch s {
	case biz.UserStatusNormal:
		return userv1.AccountStatus_ACCOUNT_STATUS_NORMAL
	case biz.UserStatusBanned:
		return userv1.AccountStatus_ACCOUNT_STATUS_BANNED
	case biz.UserStatusDeleted:
		return userv1.AccountStatus_ACCOUNT_STATUS_DELETED
	default:
		return userv1.AccountStatus_ACCOUNT_STATUS_UNSPECIFIED
	}
}

// IsBlocked 判断拉黑关系。内部接口，供 im-service 在每条消息发送前调用。
func (s *UserService) IsBlocked(ctx context.Context,
	req *userv1.IsBlockedRequest) (*userv1.IsBlockedReply, error) {

	blocked, err := s.uc.IsBlocked(ctx, req.GetUserId(), req.GetPeerUserId())
	if err != nil {
		return nil, err
	}
	return &userv1.IsBlockedReply{Blocked: blocked}, nil
}

// AdjustCredit 调整信誉分。内部接口，幂等键为 (reason, ref_id)。
func (s *UserService) AdjustCredit(ctx context.Context,
	req *userv1.AdjustCreditRequest) (*userv1.AdjustCreditReply, error) {

	score, delta, err := s.uc.AdjustCredit(ctx,
		req.GetUserId(), uint8(req.GetReason()), req.GetRefId())
	if err != nil {
		return nil, err
	}
	return &userv1.AdjustCreditReply{CreditScore: score, Delta: delta}, nil
}

// requireLogin 取出当前登录用户 ID。
//
// 为什么在 service 层而非只靠中间件：中间件 RequireLogin 是按接口分组挂载的，
// 一旦新增接口时漏挂，就会出现「未登录也能改他人资料」的漏洞。
// 在每个需要身份的方法里显式取一次，让漏洞不可能因配置疏忽而产生。
func requireLogin(ctx context.Context) (int64, error) {
	uid := auth.MustUserID(ctx)
	if uid <= 0 {
		return 0, auth.ErrUnauthenticated("请先登录")
	}
	return uid, nil
}

var (
	_ userv1.UserServiceServer = (*UserService)(nil)
	_                          = commonv1.SortOrder_SORT_ORDER_UNSPECIFIED
)
