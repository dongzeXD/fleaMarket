package biz

import (
	"context"
	"log/slog"
	"strconv"

	"github.com/fleamarket/pkg/errs"
	"github.com/fleamarket/pkg/idgen"
	"github.com/fleamarket/pkg/paging"
)

// UserUsecase 用户档案与社交关系用例。
type UserUsecase struct {
	users  UserRepo
	social SocialRepo
	credit CreditRepo
	ids    *idgen.Generator
	events EventPublisher
	log    *slog.Logger
}

// NewUserUsecase 构造用例。
func NewUserUsecase(users UserRepo, social SocialRepo, credit CreditRepo,
	ids *idgen.Generator, events EventPublisher, logger *slog.Logger) *UserUsecase {

	return &UserUsecase{
		users: users, social: social, credit: credit, ids: ids, events: events,
		log: logger.With(slog.String("usecase", "user")),
	}
}

// GetByID 查询用户。
func (uc *UserUsecase) GetByID(ctx context.Context, id int64) (*User, error) {
	u, err := uc.users.GetByID(ctx, id)
	if err != nil {
		return nil, errs.FromDataError(err, "用户不存在")
	}
	return u, nil
}

// UpdateProfile 更新本人昵称与头像。
func (uc *UserUsecase) UpdateProfile(ctx context.Context, userID int64,
	nickname, avatarKey string) (*User, error) {

	if err := uc.users.UpdateProfile(ctx, userID, nickname, avatarKey); err != nil {
		return nil, errs.FromDataError(err, "用户不存在")
	}
	return uc.GetByID(ctx, userID)
}

// Homepage 他人主页的本服务部分。
//
// 商品数与交易数由网关并发调用 product/trade 服务聚合（doc.md §9.1），
// 本服务只负责用户档案与社交关系，不跨服务同步调用——
// 否则 user-service 会反向依赖 product-service，形成调用环。
type Homepage struct {
	User           *User
	FollowingCount int32
	FollowerCount  int32
	IsFollowing    bool
	IsBlocked      bool
}

// GetHomepage 查询主页。viewerID 为 0 表示未登录访客。
func (uc *UserUsecase) GetHomepage(ctx context.Context, userID, viewerID int64) (*Homepage, error) {
	u, err := uc.GetByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	// 已注销账号的主页不再展示。封禁账号仍可见，
	// 因为其历史交易对手方需要能查看评价来源。
	if u.Status == UserStatusDeleted {
		return nil, errs.NotFound("用户不存在")
	}

	hp := &Homepage{User: u}

	following, follower, err := uc.users.CountsOf(ctx, userID)
	if err != nil {
		// 计数失败不阻断主页展示，降级为 0。
		uc.log.WarnContext(ctx, "查询关注计数失败", slog.Any("error", err))
	} else {
		hp.FollowingCount, hp.FollowerCount = following, follower
	}

	if viewerID > 0 && viewerID != userID {
		if ok, err := uc.social.IsFollowing(ctx, viewerID, userID); err == nil {
			hp.IsFollowing = ok
		}
		if ok, err := uc.social.IsBlockedEitherWay(ctx, viewerID, userID); err == nil {
			hp.IsBlocked = ok
		}
	}
	return hp, nil
}

// Follow 关注。
func (uc *UserUsecase) Follow(ctx context.Context, followerID, followeeID int64) error {
	if followerID == followeeID {
		return errs.InvalidArgument("不能关注自己")
	}
	if _, err := uc.users.GetByID(ctx, followeeID); err != nil {
		return errs.FromDataError(err, "用户不存在")
	}
	// 存在拉黑关系时不允许关注：否则被拉黑者可以通过关注持续获取
	// 对方的动态推送，拉黑就没挡住骚扰。
	if blocked, err := uc.social.IsBlockedEitherWay(ctx, followerID, followeeID); err == nil && blocked {
		return errs.PermissionDenied("无法关注该用户")
	}
	if err := uc.social.Follow(ctx, followerID, followeeID); err != nil {
		return errs.FromDataError(err, "")
	}
	return nil
}

// Unfollow 取消关注。
//
// 幂等：未关注时调用也返回成功。取消一个不存在的关系与取消成功
// 对客户端而言是同一个终态，报错只会迫使客户端做无意义的容错。
func (uc *UserUsecase) Unfollow(ctx context.Context, followerID, followeeID int64) error {
	if err := uc.social.Unfollow(ctx, followerID, followeeID); err != nil {
		return errs.FromDataError(err, "")
	}
	return nil
}

// Block 拉黑。
func (uc *UserUsecase) Block(ctx context.Context, userID, blockedID int64, reason uint8) error {
	if userID == blockedID {
		return errs.InvalidArgument("不能拉黑自己")
	}
	if _, err := uc.users.GetByID(ctx, blockedID); err != nil {
		return errs.FromDataError(err, "用户不存在")
	}
	if err := uc.social.Block(ctx, userID, blockedID, reason); err != nil {
		return errs.FromDataError(err, "")
	}
	// 拉黑后双方的关注关系一并解除，避免「已拉黑但仍在互相关注」的矛盾状态。
	if err := uc.social.Unfollow(ctx, userID, blockedID); err != nil {
		uc.log.WarnContext(ctx, "拉黑后解除关注失败", slog.Any("error", err))
	}
	if err := uc.social.Unfollow(ctx, blockedID, userID); err != nil {
		uc.log.WarnContext(ctx, "拉黑后解除反向关注失败", slog.Any("error", err))
	}

	// 通知 im-service 清理会话缓存。
	if uc.events != nil {
		payload := []byte(`{"user_id":` + strconv.FormatInt(userID, 10) +
			`,"blocked_user_id":` + strconv.FormatInt(blockedID, 10) + `}`)
		if err := uc.events.Publish(ctx, TopicUserBlocked, payload); err != nil {
			uc.log.WarnContext(ctx, "发布拉黑事件失败", slog.Any("error", err))
		}
	}
	return nil
}

// Unblock 取消拉黑。同样幂等。
func (uc *UserUsecase) Unblock(ctx context.Context, userID, blockedID int64) error {
	if err := uc.social.Unblock(ctx, userID, blockedID); err != nil {
		return errs.FromDataError(err, "")
	}
	return nil
}

// ListFollowing 我的关注列表。
func (uc *UserUsecase) ListFollowing(ctx context.Context, userID int64,
	cursor string, limit int) (*paging.Page[*User], error) {

	return uc.listRelation(ctx, cursor, limit, func(c string, l int) ([]int64, string, bool, error) {
		return uc.social.ListFollowing(ctx, userID, c, l)
	})
}

// ListBlocked 我的黑名单。
func (uc *UserUsecase) ListBlocked(ctx context.Context, userID int64,
	cursor string, limit int) (*paging.Page[*User], error) {

	return uc.listRelation(ctx, cursor, limit, func(c string, l int) ([]int64, string, bool, error) {
		return uc.social.ListBlocked(ctx, userID, c, l)
	})
}

// listRelation 把「取 ID 列表 → 批量补全档案」这段重复逻辑收拢。
//
// 之所以分两步而不在 SQL 里 JOIN：关系表与用户表可能因数据量增长被分开部署，
// 且批量查询天然可以走缓存。代价是多一次往返，在 limit ≤ 50 的前提下可接受。
func (uc *UserUsecase) listRelation(ctx context.Context, cursor string, limit int,
	fetch func(string, int) ([]int64, string, bool, error)) (*paging.Page[*User], error) {

	limit = paging.NormalizeLimit(limit)
	ids, next, hasMore, err := fetch(cursor, limit)
	if err != nil {
		return nil, errs.FromDataError(err, "")
	}
	if len(ids) == 0 {
		return &paging.Page[*User]{Items: nil}, nil
	}

	brief, err := uc.users.BatchGetBrief(ctx, ids)
	if err != nil {
		return nil, errs.FromDataError(err, "")
	}
	// 按关系表的顺序输出，而非 map 的随机顺序。
	items := make([]*User, 0, len(ids))
	for _, id := range ids {
		if u, ok := brief[id]; ok {
			items = append(items, u)
		}
	}
	return &paging.Page[*User]{Items: items, NextCursor: next, HasMore: hasMore}, nil
}

// BatchGetBrief 批量查询用户摘要，供其他服务回填卖家信息。
func (uc *UserUsecase) BatchGetBrief(ctx context.Context, ids []int64) (map[int64]*User, error) {
	if len(ids) == 0 {
		return map[int64]*User{}, nil
	}
	out, err := uc.users.BatchGetBrief(ctx, ids)
	if err != nil {
		return nil, errs.FromDataError(err, "")
	}
	return out, nil
}

// SellerInfo 发布准入信息。
type SellerInfo struct {
	UserID         int64
	Status         uint8
	CampusVerified bool
	SchoolID       int64
	CampusID       int64
}

// GetSellerInfo 查询发布准入信息，供 product-service 在发布前校验资格。
//
// 单独提供而非复用 BatchGetBrief：后者不含账号状态，
// 而「能查到用户」不等于「账号正常」——被封禁的账号依然存在。
func (uc *UserUsecase) GetSellerInfo(ctx context.Context, userID int64) (*SellerInfo, error) {
	u, err := uc.users.GetByID(ctx, userID)
	if err != nil {
		return nil, errs.FromDataError(err, "用户不存在")
	}
	return &SellerInfo{
		UserID:         u.ID,
		Status:         u.Status,
		CampusVerified: u.IsCampusVerified(),
		SchoolID:       u.SchoolID,
		CampusID:       u.CampusID,
	}, nil
}

// IsBlocked 判断两人间是否存在任一方向的拉黑，供 im-service 前置校验。
func (uc *UserUsecase) IsBlocked(ctx context.Context, userID, peerID int64) (bool, error) {
	if userID == peerID {
		return false, nil
	}
	blocked, err := uc.social.IsBlockedEitherWay(ctx, userID, peerID)
	if err != nil {
		return false, errs.FromDataError(err, "")
	}
	return blocked, nil
}

// AdjustCredit 调整信誉分。
//
// 幂等键为 (reason, refID)：消息队列只保证「至少一次」投递，
// 同一条 trade.reviewed 事件可能被消费多次，若不幂等会重复扣分。
func (uc *UserUsecase) AdjustCredit(ctx context.Context, userID int64,
	reason uint8, refID int64) (score int32, delta int32, err error) {

	d, err := CreditDeltaOf(reason)
	if err != nil {
		return 0, 0, errs.InvalidArgument("未知的信誉分变更原因")
	}
	if _, err := uc.users.GetByID(ctx, userID); err != nil {
		return 0, 0, errs.FromDataError(err, "用户不存在")
	}

	score, applied, err := uc.credit.Adjust(ctx, userID, d, reason, refID,
		creditRemark(reason), CreditScoreMin, CreditScoreMax)
	if err != nil {
		return 0, 0, errs.FromDataError(err, "")
	}
	if !applied {
		// 命中幂等：返回当前分值与 0 变化，调用方无需区分是首次还是重复。
		return score, 0, nil
	}
	return score, d, nil
}

func creditRemark(reason uint8) string {
	switch reason {
	case CreditReasonReviewPositive:
		return "收到好评"
	case CreditReasonReviewNeutral:
		return "收到中评"
	case CreditReasonReviewNegative:
		return "收到差评"
	case CreditReasonTradeCompleted:
		return "完成交易"
	case CreditReasonViolation:
		return "违规处罚"
	case CreditReasonOrderAbandoned:
		return "预订后未履约"
	default:
		return ""
	}
}
