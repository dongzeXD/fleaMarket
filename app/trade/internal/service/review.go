package service

import (
	"context"

	"github.com/fleamarket/app/trade/internal/biz"

	tradev1 "github.com/fleamarket/api/trade/v1"
	"github.com/fleamarket/pkg/auth"
)

// ReviewService 实现 api/trade/v1 的 ReviewService。
type ReviewService struct {
	tradev1.UnimplementedReviewServiceServer

	uc *biz.ReviewUsecase
}

// NewReviewService 构造服务。
func NewReviewService(uc *biz.ReviewUsecase) *ReviewService {
	return &ReviewService{uc: uc}
}

// CreateReview 提交评价。
func (s *ReviewService) CreateReview(ctx context.Context,
	req *tradev1.CreateReviewRequest) (*tradev1.Review, error) {

	uid, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}
	tags := make([]int32, 0, len(req.GetTags()))
	for _, t := range req.GetTags() {
		if t != tradev1.ReviewTag_REVIEW_TAG_UNSPECIFIED {
			tags = append(tags, int32(t))
		}
	}
	r, err := s.uc.Create(ctx, uid, &biz.CreateReviewInput{
		OrderID:   req.GetOrderId(),
		Score:     req.GetScore(),
		Tags:      tags,
		Content:   req.GetContent(),
		Anonymous: req.GetAnonymous(),
	})
	if err != nil {
		return nil, err
	}
	return reviewToProto(r), nil
}

// ListOrderReviews 查看某订单的双方评价。
func (s *ReviewService) ListOrderReviews(ctx context.Context,
	req *tradev1.ListOrderReviewsRequest) (*tradev1.ListReviewsReply, error) {

	uid, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}
	list, err := s.uc.ListByOrder(ctx, req.GetOrderId(), uid)
	if err != nil {
		return nil, err
	}
	return &tradev1.ListReviewsReply{Reviews: reviewsToProto(list)}, nil
}

// ListUserReviews 查看某用户收到的评价。公开可见。
func (s *ReviewService) ListUserReviews(ctx context.Context,
	req *tradev1.ListUserReviewsRequest) (*tradev1.ListReviewsReply, error) {

	cursor, limit := cursorRequest(req.GetPage())
	page, err := s.uc.ListByUser(ctx, req.GetUserId(),
		uint8(req.GetScoreGroup()), cursor, limit)
	if err != nil {
		return nil, err
	}
	return &tradev1.ListReviewsReply{
		Reviews: reviewsToProto(page.Items),
		Page:    cursorReply(page.NextCursor, page.HasMore),
	}, nil
}

// GetUserReviewStats 用户评价统计。内部接口，供 user-service 聚合主页。
func (s *ReviewService) GetUserReviewStats(ctx context.Context,
	req *tradev1.GetUserReviewStatsRequest) (*tradev1.UserReviewStats, error) {

	st, err := s.uc.Stats(ctx, req.GetUserId())
	if err != nil {
		return nil, err
	}
	return &tradev1.UserReviewStats{
		UserId:          st.UserID,
		TotalCount:      st.TotalCount,
		PositiveCount:   st.PositiveCount,
		NeutralCount:    st.NeutralCount,
		NegativeCount:   st.NegativeCount,
		PositiveRate:    st.PositiveRate,
		AverageScoreX10: st.AverageScoreX10,
	}, nil
}

var (
	_ tradev1.ReviewServiceServer = (*ReviewService)(nil)
	_                             = auth.RoleUser
)
