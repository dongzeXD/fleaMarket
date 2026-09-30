package service

import (
	"context"

	"github.com/fleamarket/app/user/internal/biz"

	commonv1 "github.com/fleamarket/api/common/v1"
	userv1 "github.com/fleamarket/api/user/v1"
	"github.com/fleamarket/pkg/crypto"
)

// CampusAuthService 实现 api/user/v1 的 CampusAuthService。
type CampusAuthService struct {
	userv1.UnimplementedCampusAuthServiceServer

	uc *biz.CampusAuthUsecase
}

// NewCampusAuthService 构造服务。
func NewCampusAuthService(uc *biz.CampusAuthUsecase) *CampusAuthService {
	return &CampusAuthService{uc: uc}
}

// ListSchools 查询学校字典。公开接口，注册流程需要。
func (s *CampusAuthService) ListSchools(ctx context.Context,
	req *userv1.ListSchoolsRequest) (*userv1.ListSchoolsReply, error) {

	page, pageSize := pageRequest(req.GetPage())
	schools, total, err := s.uc.ListSchools(ctx, req.GetKeyword(), page, pageSize)
	if err != nil {
		return nil, err
	}

	out := make([]*userv1.School, 0, len(schools))
	for _, sc := range schools {
		out = append(out, &userv1.School{
			SchoolId:     sc.ID,
			Name:         sc.Name,
			Province:     sc.Province,
			EmailDomains: sc.EmailDomains,
		})
	}
	return &userv1.ListSchoolsReply{
		Schools: out,
		Page: &commonv1.PageReply{
			Page:     int32(page),
			PageSize: int32(pageSize),
			Total:    total,
		},
	}, nil
}

// ListCampuses 查询某校的校区。
func (s *CampusAuthService) ListCampuses(ctx context.Context,
	req *userv1.ListCampusesRequest) (*userv1.ListCampusesReply, error) {

	list, err := s.uc.ListCampuses(ctx, req.GetSchoolId())
	if err != nil {
		return nil, err
	}
	out := make([]*userv1.Campus, 0, len(list))
	for _, c := range list {
		campus := &userv1.Campus{
			CampusId: c.ID,
			SchoolId: c.SchoolID,
			Name:     c.Name,
		}
		// 坐标为 0 表示未录入，此时不下发 location 字段，
		// 避免客户端把 (0,0) 当作真实坐标显示在几内亚湾。
		if c.Lat != 0 || c.Lng != 0 {
			campus.Location = &commonv1.GeoPoint{Lat: c.Lat, Lng: c.Lng}
		}
		out = append(out, campus)
	}
	return &userv1.ListCampusesReply{Campuses: out}, nil
}

// StartEmailAuth 发起校园邮箱认证。
func (s *CampusAuthService) StartEmailAuth(ctx context.Context,
	req *userv1.StartEmailAuthRequest) (*userv1.StartEmailAuthReply, error) {

	uid, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}
	res, err := s.uc.StartEmailAuth(ctx, uid,
		req.GetSchoolId(), req.GetCampusId(), req.GetEduEmail())
	if err != nil {
		return nil, err
	}
	return &userv1.StartEmailAuthReply{
		// 回显脱敏邮箱供用户确认收件地址，不回显完整地址——
		// 若认证页被他人窥屏，完整校园邮箱等同于泄漏学号。
		EmailMasked: crypto.MaskEmail(res.Email),
		ExpireAt:    res.ExpireAt.Unix(),
	}, nil
}

// VerifyEmailAuth 校验认证链接。
//
// 无需登录：用户可能在另一台设备的邮件客户端里点开链接，
// 那里没有登录态。安全性由签名令牌本身保证（一次性、30 分钟有效）。
func (s *CampusAuthService) VerifyEmailAuth(ctx context.Context,
	req *userv1.VerifyEmailAuthRequest) (*userv1.VerifyEmailAuthReply, error) {

	record, err := s.uc.VerifyEmailAuth(ctx, req.GetToken())
	if err != nil {
		return nil, err
	}
	return &userv1.VerifyEmailAuthReply{Auth: campusAuthToProto(record)}, nil
}

// SubmitStudentCardAuth 提交学号认证材料。
func (s *CampusAuthService) SubmitStudentCardAuth(ctx context.Context,
	req *userv1.SubmitStudentCardAuthRequest) (*userv1.SubmitStudentCardAuthReply, error) {

	uid, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}
	record, err := s.uc.SubmitStudentCardAuth(ctx, uid,
		req.GetSchoolId(), req.GetCampusId(),
		req.GetStudentNo(), req.GetRealName(), req.GetImageKeys())
	if err != nil {
		return nil, err
	}
	return &userv1.SubmitStudentCardAuthReply{Auth: campusAuthToProto(record)}, nil
}

// GetMyCampusAuth 查询本人认证状态。
func (s *CampusAuthService) GetMyCampusAuth(ctx context.Context,
	_ *userv1.GetMyCampusAuthRequest) (*userv1.CampusAuth, error) {

	uid, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}
	record, err := s.uc.GetMyAuth(ctx, uid)
	if err != nil {
		return nil, err
	}
	return campusAuthToProto(record), nil
}

// campusAuthToProto 转换认证记录。
//
// 学号只回传脱敏值：即便是本人接口，完整学号也没有展示必要，
// 而它是校园场景下定位到具体个人的关键信息。
func campusAuthToProto(a *biz.CampusAuth) *userv1.CampusAuth {
	if a == nil {
		return nil
	}
	return &userv1.CampusAuth{
		AuthId:          a.ID,
		UserId:          a.UserID,
		Method:          campusAuthMethodToProto(a.Method),
		Status:          campusAuthStatusToProto(a.Status),
		Campus:          campusRef(a.SchoolID, a.CampusID),
		StudentNoMasked: crypto.MaskTail(a.StudentNo, 4),
		RejectReason:    a.RejectReason,
		VerifiedAt:      unixPtr(a.VerifiedAt),
		ExpireAt:        unixPtr(a.ExpireAt),
	}
}

var _ userv1.CampusAuthServiceServer = (*CampusAuthService)(nil)
