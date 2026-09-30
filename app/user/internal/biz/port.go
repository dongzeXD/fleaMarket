package biz

import (
	"context"
	"time"
)

// 本文件集中声明领域层对外部能力的依赖（端口）。
//
// 依赖倒置的落点：biz 只声明「我需要什么能力」，具体怎么实现
// （MySQL 还是 Redis、真实短信网关还是控制台打印）由 data 层提供并在
// wire 中装配。这样领域逻辑的单测可以完全脱离中间件。

// User 领域对象。
//
// 与 entity.User 的区别：entity 是表结构的映射（含密文、摘要列），
// User 是领域概念（手机号已解密）。二者刻意分离，避免加密细节渗透到业务逻辑。
type User struct {
	ID       int64
	Phone    string // 明文，仅在进程内流转，绝不下发
	Nickname string

	AvatarKey string
	Status    uint8

	WxUnionID    string
	WxOpenIDWeb  string
	WxOpenIDMini string

	CreditScore      int32
	CampusAuthStatus uint8
	SchoolID         int64
	CampusID         int64

	LastLoginAt time.Time
	CreatedAt   time.Time
}

// IsCampusVerified 是否已通过校园认证。
//
// 平台的核心准入规则：未认证用户只能浏览，不能发布商品、不能发起会话
// （doc.md §9.1）。这是防诈骗的第一道闸门。
func (u *User) IsCampusVerified() bool {
	return u != nil && u.CampusAuthStatus == CampusAuthPassed
}

// IsActive 账号是否可正常使用。
func (u *User) IsActive() bool {
	return u != nil && u.Status == UserStatusNormal
}

// 账号状态。与 entity 中的常量同值，此处重新声明是为了让 biz 不依赖 entity——
// biz 是领域层，不应因为表结构调整而被动改动。
const (
	UserStatusNormal  uint8 = 1
	UserStatusBanned  uint8 = 2
	UserStatusDeleted uint8 = 3
)

// 校园认证状态。
const (
	CampusAuthNone     uint8 = 1
	CampusAuthPending  uint8 = 2
	CampusAuthPassed   uint8 = 3
	CampusAuthRejected uint8 = 4
	CampusAuthExpired  uint8 = 5
)

// 认证方式。
const (
	AuthMethodEduEmail    uint8 = 1
	AuthMethodStudentCard uint8 = 2
)

// UserRepo 用户仓储。
type UserRepo interface {
	// Create 创建用户。手机号的加密与摘要由实现负责。
	Create(ctx context.Context, u *User) (*User, error)
	// GetByID 按 ID 查询。不存在返回 errs.ErrNotFound。
	GetByID(ctx context.Context, id int64) (*User, error)
	// GetByPhone 按手机号查询（内部转为摘要检索）。
	GetByPhone(ctx context.Context, phone string) (*User, error)
	// GetByWxUnionID 按微信 unionid 查询。
	GetByWxUnionID(ctx context.Context, unionID string) (*User, error)
	// BatchGetBrief 批量查询，返回 map[userID]*User。查不到的 ID 不出现在结果中。
	BatchGetBrief(ctx context.Context, ids []int64) (map[int64]*User, error)
	// UpdateProfile 更新昵称与头像。
	UpdateProfile(ctx context.Context, id int64, nickname, avatarKey string) error
	// UpdateWechatBinding 绑定微信标识。
	UpdateWechatBinding(ctx context.Context, id int64, unionID, openIDWeb, openIDMini string) error
	// TouchLogin 记录登录时间。
	TouchLogin(ctx context.Context, id int64) error
	// UpdateCampusSnapshot 同步认证结果到用户表的冗余字段。
	UpdateCampusSnapshot(ctx context.Context, id int64, status uint8, schoolID, campusID int64) error
	// CountsOf 统计主页所需的计数（关注数、粉丝数）。
	CountsOf(ctx context.Context, id int64) (following, follower int32, err error)
}

// VerifyCodeStore 短信验证码存储。
//
// 抽象成接口而非直接用 cache：验证码有「一次性消费」与「发送频率限制」
// 两个语义，放在一个接口里比让 biz 自己拼 Redis key 更内聚。
type VerifyCodeStore interface {
	// Save 保存验证码。
	Save(ctx context.Context, scene, target, code string, ttl time.Duration) error
	// Consume 校验并立即失效。
	//
	// 必须是「校验+删除」的原子操作：若先校验后删除，
	// 并发的两个请求可能都通过校验，同一验证码被用两次。
	Consume(ctx context.Context, scene, target, code string) (bool, error)
	// AcquireSendQuota 申请发送配额。
	//
	// 返回 retryAfter > 0 表示被频率限制拦下，调用方据此告知用户等待秒数。
	// daily 超限时返回 ErrDailyQuotaExceeded。
	AcquireSendQuota(ctx context.Context, scene, target string) (retryAfter int, err error)
	// CountFailures 记录并返回校验失败次数，用于拦截暴力猜码。
	CountFailures(ctx context.Context, scene, target string) (int64, error)
	// ClearFailures 校验成功后清空失败计数。
	ClearFailures(ctx context.Context, scene, target string) error
}

// SMSSender 短信发送。
type SMSSender interface {
	SendVerifyCode(ctx context.Context, phone, code string, ttl time.Duration) error
}

// EmailSender 邮件发送。
type EmailSender interface {
	// SendCampusAuthLink 发送校园认证链接。
	SendCampusAuthLink(ctx context.Context, email, link string, expireAt time.Time) error
}

// WechatProfile 微信授权换取到的用户标识。
type WechatProfile struct {
	UnionID   string
	OpenID    string
	Nickname  string
	AvatarURL string
}

// WechatClient 微信开放平台/小程序的授权码兑换。
type WechatClient interface {
	// ExchangeWeb 用网站应用的 code 换取用户标识。
	ExchangeWeb(ctx context.Context, code string) (*WechatProfile, error)
	// ExchangeMiniApp 用小程序的 code 换取用户标识。
	ExchangeMiniApp(ctx context.Context, code string) (*WechatProfile, error)
}

// TokenStore 令牌吊销名单。
type TokenStore interface {
	// Revoke 吊销令牌，ttl 应设为该令牌的剩余有效期——
	// 超过有效期后令牌自然失效，名单里再留着只是浪费内存。
	Revoke(ctx context.Context, tokenID string, ttl time.Duration) error
	// IsRevoked 实现 pkg/auth.Revoker。
	IsRevoked(ctx context.Context, tokenID string) (bool, error)
}

// EventPublisher 领域事件发布。
//
// biz 只声明「发生了什么」，不关心谁消费、走什么队列。
type EventPublisher interface {
	Publish(ctx context.Context, topic string, payload []byte) error
}

// 本服务发出的事件主题。
const (
	// TopicUserRegistered 新用户注册。
	TopicUserRegistered = "user.registered"
	// TopicCampusAuthPassed 校园认证通过，product/search 可据此放开发布权限。
	TopicCampusAuthPassed = "user.campus_auth_passed"
	// TopicCampusAuthSubmitted 提交人工认证，review-service 生成工单。
	TopicCampusAuthSubmitted = "user.campus_auth_submitted"
	// TopicUserBlocked 拉黑发生，im-service 可据此清理会话缓存。
	TopicUserBlocked = "user.blocked"
)
