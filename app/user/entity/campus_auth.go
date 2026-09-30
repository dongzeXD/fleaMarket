package entity

import (
	"time"

	"gorm.io/gorm"
)

// 校园认证状态。与 api/user/v1 的 CampusAuthStatus 枚举保持同序。
const (
	CampusAuthStatusNone     uint8 = 1 // 从未提交
	CampusAuthStatusPending  uint8 = 2 // 待人工审核
	CampusAuthStatusPassed   uint8 = 3 // 通过
	CampusAuthStatusRejected uint8 = 4 // 驳回
	CampusAuthStatusExpired  uint8 = 5 // 已过期（如已毕业）
)

// 认证方式。
const (
	CampusAuthMethodEduEmail    uint8 = 1 // 校园邮箱，自动审核
	CampusAuthMethodStudentCard uint8 = 2 // 学号+学生证，人工审核
)

// School 学校字典。
type School struct {
	ID       int64  `gorm:"primaryKey;autoIncrement:false"`
	Name     string `gorm:"type:varchar(64);not null;uniqueIndex:uk_name"`
	Province string `gorm:"type:varchar(32);index"`
	// EmailDomains 认可的校园邮箱域名后缀，逗号分隔，如 "stu.pku.edu.cn,pku.edu.cn"。
	//
	// 用逗号分隔的字符串而非独立子表：一所学校通常只有 1~3 个域名，
	// 且只在认证时被读取一次做后缀匹配，建子表得不偿失。
	EmailDomains string `gorm:"type:varchar(512);not null;default:'';comment:认可的邮箱域名后缀,逗号分隔"`
	Enabled      bool   `gorm:"not null;default:true"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func (School) TableName() string { return "t_school" }

// Campus 校区字典。校区是「同校优先」与「距离」的基本单位。
type Campus struct {
	ID        int64   `gorm:"primaryKey;autoIncrement:false"`
	SchoolID  int64   `gorm:"not null;index:idx_school;comment:所属学校"`
	Name      string  `gorm:"type:varchar(64);not null"`
	Lat       float64 `gorm:"type:decimal(10,7);comment:校区中心纬度"`
	Lng       float64 `gorm:"type:decimal(10,7);comment:校区中心经度"`
	Enabled   bool    `gorm:"not null;default:true"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (Campus) TableName() string { return "t_campus" }

// CampusAuth 校园认证记录。
//
// 一个用户可以有多条记录（驳回后重新提交、毕业后重新认证），
// 当前有效的那条由 Status=Passed 且未过期界定。
type CampusAuth struct {
	ID       int64 `gorm:"primaryKey;autoIncrement:false"`
	UserID   int64 `gorm:"not null;index:idx_user_status,priority:1;comment:用户ID"`
	SchoolID int64 `gorm:"not null;index:idx_school_student,priority:1"`
	CampusID int64 `gorm:"not null"`

	Method uint8 `gorm:"not null;comment:1-校园邮箱 2-学号学生证"`
	Status uint8 `gorm:"not null;default:2;index:idx_user_status,priority:2;comment:见 CampusAuthStatus*"`

	// StudentNo 学号。与 SchoolID 构成唯一索引，防止同一学号被多个账号认证——
	// 这是校园应用防小号的关键约束。
	StudentNo string `gorm:"type:varchar(32);index:idx_school_student,priority:2;comment:学号"`
	RealName  string `gorm:"type:varchar(32);comment:真实姓名"`
	EduEmail  string `gorm:"type:varchar(128);comment:校园邮箱"`

	// ImageKeys 学生证照片的对象 key，逗号分隔。
	//
	// 隐私要求（doc.md §13）：认证结论产出 30 天后由定时任务清空此字段
	// 并删除对象存储中的原图，仅保留认证结果。
	ImageKeys string `gorm:"type:varchar(1024);comment:学生证照片key,逗号分隔,30天后清理"`
	// ImagePurgedAt 照片清理时间。非空表示已按隐私要求删除。
	ImagePurgedAt *time.Time `gorm:"comment:照片清理时间"`

	RejectReason string     `gorm:"type:varchar(255);comment:驳回原因"`
	ReviewerID   int64      `gorm:"comment:审核人,0表示系统自动"`
	VerifiedAt   *time.Time `gorm:"comment:通过时间"`
	// ExpireAt 认证过期时间。NULL 表示不过期。
	ExpireAt *time.Time `gorm:"index:idx_expire_at;comment:过期时间"`

	CreatedAt time.Time `gorm:"index"`
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`
}

func (CampusAuth) TableName() string { return "t_campus_auth" }
