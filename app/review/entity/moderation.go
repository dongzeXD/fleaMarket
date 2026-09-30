package entity

import (
	"time"

	"gorm.io/gorm"
)

// 审核对象类型。与 api/review/v1 的 TargetType 同值。
const (
	TargetTypeProduct     uint8 = 1
	TargetTypeWanted      uint8 = 2
	TargetTypeMessage     uint8 = 3
	TargetTypeUserProfile uint8 = 4
)

// 处置级别。与敏感词的三档一致。
const (
	LevelWarn   uint8 = 1
	LevelReview uint8 = 2
	LevelBlock  uint8 = 3
)

// 工单状态。
const (
	TaskStatusAutoPassed     uint8 = 1
	TaskStatusAutoRejected   uint8 = 2
	TaskStatusPending        uint8 = 3
	TaskStatusManualPassed   uint8 = 4
	TaskStatusManualRejected uint8 = 5
)

// 举报状态。
const (
	ReportStatusPending   uint8 = 1
	ReportStatusActioned  uint8 = 2
	ReportStatusDismissed uint8 = 3
)

// 敏感词分类。
const (
	WordCategoryProhibited    uint8 = 1
	WordCategoryContact       uint8 = 2
	WordCategoryAcademic      uint8 = 3
	WordCategoryPoliticalPorn uint8 = 4
)

// ModerationTask 审核工单。
//
// 每次送审都落一条记录，无论自动通过还是转人工。
// 全量留存的意义：既是人工审核的工作队列，也是「某用户被审核驳回过几次」
// 这类风控统计的数据来源。
type ModerationTask struct {
	ID         int64 `gorm:"primaryKey;autoIncrement:false"`
	TargetType uint8 `gorm:"not null;index:idx_type_status,priority:1;comment:见 TargetType*"`
	TargetID   int64 `gorm:"not null;index:idx_target;comment:审核对象ID"`
	OwnerID    int64 `gorm:"not null;index:idx_owner;comment:内容所属用户"`

	Status uint8 `gorm:"not null;index:idx_type_status,priority:2;index:idx_status_created,priority:1;comment:见 TaskStatus*"`
	// AutoLevel 自动审核命中的最高处置级别。
	AutoLevel uint8 `gorm:"not null;default:0;comment:自动审核级别"`
	// HitWords 命中的违规词，逗号分隔。
	HitWords string `gorm:"type:varchar(512);comment:命中违规词"`
	// Content 送审内容摘要，供人工审核查看。
	//
	// 只存摘要而非全文：审核只需判断是否违规，全文由 target_id 回查即可，
	// 在审核表里存商品全部描述是重复且占空间的。
	Content string `gorm:"type:varchar(1000);comment:内容摘要"`

	ReviewerID   int64  `gorm:"comment:人工审核人,0为系统"`
	RejectReason string `gorm:"type:varchar(255);comment:驳回原因"`

	CreatedAt time.Time `gorm:"index:idx_status_created,priority:2"`
	UpdatedAt time.Time
	DecidedAt *time.Time `gorm:"comment:处置时间"`
}

func (ModerationTask) TableName() string { return "t_moderation_task" }

// Report 举报。
//
// idx_target_pending 支撑「同一目标被几个人举报」的统计——
// 达到阈值自动临时下架（doc.md §9.6）。
type Report struct {
	ID         int64 `gorm:"primaryKey;autoIncrement:false"`
	ReporterID int64 `gorm:"not null;uniqueIndex:uk_reporter_target,priority:1;comment:举报人"`

	TargetType uint8 `gorm:"not null;uniqueIndex:uk_reporter_target,priority:2;index:idx_target_status,priority:1;comment:举报对象类型"`
	TargetID   int64 `gorm:"not null;uniqueIndex:uk_reporter_target,priority:3;index:idx_target_status,priority:2;comment:举报对象ID"`

	Reason      uint8  `gorm:"not null;comment:举报原因"`
	Description string `gorm:"type:varchar(500);comment:举报说明"`
	ImageKeys   string `gorm:"type:varchar(1024);comment:证据图片key,逗号分隔"`

	Status       uint8  `gorm:"not null;default:1;index:idx_status_created,priority:1;comment:见 ReportStatus*"`
	HandledBy    int64  `gorm:"comment:处理人"`
	HandleResult string `gorm:"type:varchar(255);comment:处理结果"`

	CreatedAt time.Time `gorm:"index:idx_status_created,priority:2;index:idx_target_status,priority:3"`
	UpdatedAt time.Time
	HandledAt *time.Time `gorm:"comment:处理时间"`
}

func (Report) TableName() string { return "t_report" }

// SensitiveWord 敏感词。
//
// word 上有唯一索引：同一个词重复添加没有意义，且会让 AC 自动机
// 出现重复终结点。
type SensitiveWord struct {
	ID       int64  `gorm:"primaryKey;autoIncrement:false"`
	Word     string `gorm:"type:varchar(64);not null;uniqueIndex:uk_word;comment:敏感词"`
	Category uint8  `gorm:"not null;index:idx_category;comment:见 WordCategory*"`
	// Level 处置级别：1 告警 2 复审 3 拦截。
	Level     uint8 `gorm:"not null;comment:处置级别"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`
}

func (SensitiveWord) TableName() string { return "t_sensitive_word" }
