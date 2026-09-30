package biz

import (
	"context"
	"errors"
	"time"
)

// SocialRepo 关注与拉黑关系仓储。
type SocialRepo interface {
	Follow(ctx context.Context, followerID, followeeID int64) error
	Unfollow(ctx context.Context, followerID, followeeID int64) error
	IsFollowing(ctx context.Context, followerID, followeeID int64) (bool, error)
	ListFollowing(ctx context.Context, followerID int64, cursor string, limit int) ([]int64, string, bool, error)

	Block(ctx context.Context, userID, blockedID int64, reason uint8) error
	Unblock(ctx context.Context, userID, blockedID int64) error
	// IsBlockedEitherWay 判断两人之间是否存在任一方向的拉黑。
	//
	// 为何是双向：拉黑的产品语义是「我不想和这个人产生任何交互」，
	// 因此被拉黑方同样不应能发起会话。若只判单向，
	// 被拉黑者仍可主动骚扰拉黑者，拉黑功能就失去意义了。
	IsBlockedEitherWay(ctx context.Context, userID, peerID int64) (bool, error)
	ListBlocked(ctx context.Context, userID int64, cursor string, limit int) ([]int64, string, bool, error)
}

// CreditRepo 信誉分仓储。
type CreditRepo interface {
	// Adjust 在单个事务内写流水并更新用户表的冗余分值。
	//
	// applied 为 false 表示命中幂等（同一 reason+refID 已处理过），
	// 此时 score 返回当前分值，delta 为 0。
	Adjust(ctx context.Context, userID int64, delta int32, reason uint8, refID int64,
		remark string, minScore, maxScore int32) (score int32, applied bool, err error)
}

// 信誉分变更原因。与 api/user/v1 的 CreditReason 枚举同序。
const (
	CreditReasonReviewPositive uint8 = 1
	CreditReasonReviewNeutral  uint8 = 2
	CreditReasonReviewNegative uint8 = 3
	CreditReasonTradeCompleted uint8 = 4
	CreditReasonViolation      uint8 = 5
	CreditReasonOrderAbandoned uint8 = 6
)

// 信誉分的取值范围与初始值。
const (
	CreditScoreInitial int32 = 100
	CreditScoreMin     int32 = 0
	CreditScoreMax     int32 = 200
)

// creditDelta 各原因对应的分值变化。
//
// 把「加减多少分」收在服务端一处（doc.md §9.4）：调用方只声明「发生了什么」，
// 不决定分值。否则规则会散落到 trade、review 等多个服务，日后调整需要多处同步发布。
var creditDelta = map[uint8]int32{
	CreditReasonReviewPositive: +2,
	CreditReasonReviewNeutral:  0,
	CreditReasonReviewNegative: -5,
	CreditReasonTradeCompleted: +1,
	CreditReasonViolation:      -20,
	CreditReasonOrderAbandoned: -3,
}

// ErrUnknownCreditReason 未知的信誉分变更原因。
var ErrUnknownCreditReason = errors.New("unknown credit reason")

// CreditDeltaOf 查询某原因对应的分值变化。
func CreditDeltaOf(reason uint8) (int32, error) {
	d, ok := creditDelta[reason]
	if !ok {
		return 0, ErrUnknownCreditReason
	}
	return d, nil
}

// School 学校。
type School struct {
	ID           int64
	Name         string
	Province     string
	EmailDomains []string
}

// AllowsEmail 判断邮箱域名是否属于本校。
//
// 只做后缀匹配且要求边界对齐：
// 若简单用 strings.HasSuffix，域名 "pku.edu.cn" 会让 "evilpku.edu.cn" 也通过。
// 因此要求邮箱域名与白名单完全相等，或以 ".<白名单>" 结尾。
func (s *School) AllowsEmail(email string) bool {
	at := -1
	for i := len(email) - 1; i >= 0; i-- {
		if email[i] == '@' {
			at = i
			break
		}
	}
	if at < 0 || at == len(email)-1 {
		return false
	}
	domain := lowerASCII(email[at+1:])
	for _, d := range s.EmailDomains {
		d = lowerASCII(d)
		if d == "" {
			continue
		}
		if domain == d {
			return true
		}
		if len(domain) > len(d) && domain[len(domain)-len(d)-1] == '.' &&
			domain[len(domain)-len(d):] == d {
			return true
		}
	}
	return false
}

func lowerASCII(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}

// Campus 校区。
type Campus struct {
	ID       int64
	SchoolID int64
	Name     string
	Lat      float64
	Lng      float64
}

// CampusAuth 一条校园认证记录。
type CampusAuth struct {
	ID        int64
	UserID    int64
	SchoolID  int64
	CampusID  int64
	Method    uint8
	Status    uint8
	StudentNo string
	RealName  string
	EduEmail  string
	ImageKeys []string

	RejectReason string
	ReviewerID   int64
	VerifiedAt   *time.Time
	ExpireAt     *time.Time
	CreatedAt    time.Time
}

// SchoolRepo 学校与校区字典仓储。
type SchoolRepo interface {
	ListSchools(ctx context.Context, keyword string, page, pageSize int) ([]*School, int64, error)
	GetSchool(ctx context.Context, id int64) (*School, error)
	ListCampuses(ctx context.Context, schoolID int64) ([]*Campus, error)
	GetCampus(ctx context.Context, id int64) (*Campus, error)
}

// CampusAuthRepo 认证记录仓储。
type CampusAuthRepo interface {
	Create(ctx context.Context, a *CampusAuth) (*CampusAuth, error)
	GetLatestByUser(ctx context.Context, userID int64) (*CampusAuth, error)
	GetByID(ctx context.Context, id int64) (*CampusAuth, error)
	// MarkPassed 置为通过。
	MarkPassed(ctx context.Context, id int64, reviewerID int64, expireAt *time.Time) error
	// MarkRejected 置为驳回。
	MarkRejected(ctx context.Context, id int64, reviewerID int64, reason string) error
	// ExistsPassedStudentNo 判断该校的某学号是否已被其他用户认证通过。
	//
	// 这是防小号的关键约束：一个学号只能绑定一个账号。
	ExistsPassedStudentNo(ctx context.Context, schoolID int64, studentNo string, exceptUserID int64) (bool, error)
	// ListImagesToPurge 找出认证结论已产出超过 retention 且照片未清理的记录。
	ListImagesToPurge(ctx context.Context, retention time.Duration, limit int) ([]*CampusAuth, error)
	// MarkImagePurged 标记照片已清理。
	MarkImagePurged(ctx context.Context, id int64) error
}

// EmailAuthTokenStore 邮箱认证链接的一次性令牌存储。
type EmailAuthTokenStore interface {
	// Save 保存令牌到认证记录的映射。
	Save(ctx context.Context, token string, authID int64, ttl time.Duration) error
	// Consume 校验并失效。必须原子，防止同一链接被重复使用。
	Consume(ctx context.Context, token string) (authID int64, ok bool, err error)
}
