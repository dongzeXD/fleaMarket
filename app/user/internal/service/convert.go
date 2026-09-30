package service

import (
	"time"

	"github.com/fleamarket/app/user/internal/biz"

	commonv1 "github.com/fleamarket/api/common/v1"
	userv1 "github.com/fleamarket/api/user/v1"
	"github.com/fleamarket/pkg/paging"
)

// 本文件集中放置 proto ↔ 领域对象的公共转换函数，
// 避免同样的映射逻辑在三个 service 文件里各写一遍。

func imageRef(key string) *commonv1.ImageRef {
	if key == "" {
		return nil
	}
	// URL 留空：拼 CDN 域名与图片处理参数是网关的职责，
	// 各业务服务不应各自持有一份 CDN 配置。
	return &commonv1.ImageRef{ObjectKey: key}
}

func campusRef(schoolID, campusID int64) *commonv1.CampusRef {
	if campusID == 0 {
		return nil
	}
	// 只回填 ID：学校与校区名称由网关按字典批量补全，
	// 避免每个接口都联表查字典。
	return &commonv1.CampusRef{
		CampusId: campusID,
		School:   &commonv1.SchoolRef{SchoolId: schoolID},
	}
}

func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func unixPtr(t *time.Time) int64 {
	if t == nil || t.IsZero() {
		return 0
	}
	return t.Unix()
}

func campusAuthStatusToProto(s uint8) userv1.CampusAuthStatus {
	switch s {
	case biz.CampusAuthNone:
		return userv1.CampusAuthStatus_CAMPUS_AUTH_STATUS_NONE
	case biz.CampusAuthPending:
		return userv1.CampusAuthStatus_CAMPUS_AUTH_STATUS_PENDING
	case biz.CampusAuthPassed:
		return userv1.CampusAuthStatus_CAMPUS_AUTH_STATUS_PASSED
	case biz.CampusAuthRejected:
		return userv1.CampusAuthStatus_CAMPUS_AUTH_STATUS_REJECTED
	case biz.CampusAuthExpired:
		return userv1.CampusAuthStatus_CAMPUS_AUTH_STATUS_EXPIRED
	default:
		return userv1.CampusAuthStatus_CAMPUS_AUTH_STATUS_UNSPECIFIED
	}
}

func campusAuthMethodToProto(m uint8) userv1.CampusAuthMethod {
	switch m {
	case biz.AuthMethodEduEmail:
		return userv1.CampusAuthMethod_CAMPUS_AUTH_METHOD_EDU_EMAIL
	case biz.AuthMethodStudentCard:
		return userv1.CampusAuthMethod_CAMPUS_AUTH_METHOD_STUDENT_CARD
	default:
		return userv1.CampusAuthMethod_CAMPUS_AUTH_METHOD_UNSPECIFIED
	}
}

// userBriefToProto 转换公开摘要。
//
// 与 userProfileToProto 的区别：摘要面向他人，不含任何手机号信息
// （连脱敏后的也不含）。脱敏手机号只对本人可见。
func userBriefToProto(u *biz.User) *userv1.UserBrief {
	if u == nil {
		return nil
	}
	return &userv1.UserBrief{
		UserId:         u.ID,
		Nickname:       u.Nickname,
		Avatar:         imageRef(u.AvatarKey),
		CreditScore:    u.CreditScore,
		CampusVerified: u.IsCampusVerified(),
		Campus:         campusRef(u.SchoolID, u.CampusID),
	}
}

func userBriefsToProto(us []*biz.User) []*userv1.UserBrief {
	out := make([]*userv1.UserBrief, 0, len(us))
	for _, u := range us {
		out = append(out, userBriefToProto(u))
	}
	return out
}

func cursorReply(p *paging.Page[*biz.User]) *commonv1.CursorReply {
	if p == nil {
		return &commonv1.CursorReply{}
	}
	return &commonv1.CursorReply{NextCursor: p.NextCursor, HasMore: p.HasMore}
}

func cursorRequest(r *commonv1.CursorRequest) (string, int) {
	if r == nil {
		return "", paging.DefaultLimit
	}
	return r.GetCursor(), int(r.GetLimit())
}

func pageRequest(r *commonv1.PageRequest) (int, int) {
	if r == nil {
		return 1, paging.DefaultPageSize
	}
	return int(r.GetPage()), int(r.GetPageSize())
}
