package entity

import (
	"time"

	"gorm.io/gorm"
)

// UserStatus 账号状态。用 uint8 而非字符串枚举以节省索引空间。
const (
	UserStatusNormal  uint8 = 1 // 正常
	UserStatusBanned  uint8 = 2 // 封禁
	UserStatusDeleted uint8 = 3 // 已注销（保留记录以维持历史交易的引用完整性）
)

// User 账号主表。
//
// 手机号的存储方式（doc.md §13）：
//   - PhoneCipher 存 AES-GCM 密文，用于展示时解密后脱敏；
//   - PhoneHash 存 HMAC-SHA256 摘要，承担唯一索引与登录检索。
//
// 为何不直接对密文建唯一索引：AES-GCM 每次加密使用随机 nonce，
// 同一手机号两次加密的密文不同，无法用于等值查询与唯一约束。
type User struct {
	ID          int64  `gorm:"primaryKey;autoIncrement:false;comment:雪花ID"`
	PhoneCipher string `gorm:"type:varbinary(255);comment:手机号AES-GCM密文"`
	PhoneHash   string `gorm:"type:char(64);uniqueIndex:uk_phone_hash;comment:手机号HMAC摘要,用于唯一约束与检索"`
	// WxUnionID 是跨端归一的依据：同一微信用户在 Web 与小程序的 openid 不同，
	// 但 unionid 相同（需同一开放平台账号下）。
	WxUnionID    string `gorm:"type:varchar(64);uniqueIndex:uk_wx_unionid;comment:微信unionid"`
	WxOpenIDWeb  string `gorm:"type:varchar(64);comment:网站应用openid"`
	WxOpenIDMini string `gorm:"type:varchar(64);comment:小程序openid"`

	Nickname  string `gorm:"type:varchar(32);not null;default:'';comment:昵称"`
	AvatarKey string `gorm:"type:varchar(255);comment:头像对象key"`
	Status    uint8  `gorm:"not null;default:1;index:idx_status;comment:1-正常 2-封禁 3-已注销"`

	// CreditScore 信誉分。冗余在此以避免每次展示都聚合 t_credit_log，
	// 权威流水仍在 t_credit_log 中，二者由同一事务维护。
	CreditScore int32 `gorm:"not null;default:100;comment:信誉分 0-200"`

	// CampusAuthStatus / CampusID 同样是冗余字段，来源为 t_campus_auth。
	// 冗余的理由：几乎每个接口都要判断「是否已认证」与「属于哪个校区」，
	// 若每次都联表会让最热的查询路径多一次 JOIN。
	CampusAuthStatus uint8 `gorm:"not null;default:1;comment:认证状态,见 CampusAuthStatus*"`
	SchoolID         int64 `gorm:"index:idx_school_campus,priority:1;comment:学校ID"`
	CampusID         int64 `gorm:"index:idx_school_campus,priority:2;comment:校区ID"`

	LastLoginAt time.Time `gorm:"comment:最近登录时间"`
	CreatedAt   time.Time `gorm:"index:idx_created_at"`
	UpdatedAt   time.Time
	DeletedAt   gorm.DeletedAt `gorm:"index"`
}

func (User) TableName() string { return "t_user" }

// FollowRelation 关注关系。
//
// 采用联合主键而非自增 ID：关注关系的唯一性就是 (follower, followee)，
// 额外的自增主键既浪费空间又给不了任何查询帮助。
//
// idx_followee_created 支撑「查某人的粉丝列表并按时间倒序」。
type FollowRelation struct {
	FollowerID int64     `gorm:"primaryKey;comment:关注者"`
	FolloweeID int64     `gorm:"primaryKey;index:idx_followee_created,priority:1;comment:被关注者"`
	CreatedAt  time.Time `gorm:"index:idx_followee_created,priority:2"`
}

func (FollowRelation) TableName() string { return "t_user_follow" }

// BlockRelation 拉黑关系。
type BlockRelation struct {
	UserID        int64 `gorm:"primaryKey;comment:操作者"`
	BlockedUserID int64 `gorm:"primaryKey;comment:被拉黑者"`
	Reason        uint8 `gorm:"not null;default:0;comment:拉黑原因,用于风控统计"`
	CreatedAt     time.Time
}

func (BlockRelation) TableName() string { return "t_user_block" }

// CreditLog 信誉分变更流水。
//
// 存在的意义是可追溯与可回滚：信誉分直接影响用户能否交易，
// 若只保留最终分值，出现争议时无法解释「为什么被扣分」，也无法撤销误判。
type CreditLog struct {
	ID     int64 `gorm:"primaryKey;autoIncrement:false"`
	UserID int64 `gorm:"not null;uniqueIndex:uk_user_reason_ref,priority:1;index:idx_user_created,priority:1;comment:用户ID"`
	// Delta 本次变更值，可正可负。
	Delta int32 `gorm:"not null;comment:分值变化"`
	// After 变更后的分值，便于对账时发现冗余字段与流水不一致。
	After int32 `gorm:"not null;comment:变更后分值"`
	// UserID + ReasonCode + RefID 构成幂等键：同一用户、同一原因、同一业务对象
	// 只应加减一次分。
	//
	// user_id 必须在键里：一次交易完成会同时给买卖双方各加一分，
	// 两条流水的 reason 与 ref_id（订单 ID）相同，仅 user_id 不同。
	// 若键不含 user_id，第二个人的加分会被误判为重复而丢失。
	ReasonCode uint8     `gorm:"not null;uniqueIndex:uk_user_reason_ref,priority:2;comment:变更原因"`
	RefID      int64     `gorm:"not null;uniqueIndex:uk_user_reason_ref,priority:3;comment:关联业务对象ID"`
	Remark     string    `gorm:"type:varchar(255);comment:备注"`
	CreatedAt  time.Time `gorm:"index:idx_user_created,priority:2"`
}

func (CreditLog) TableName() string { return "t_credit_log" }
