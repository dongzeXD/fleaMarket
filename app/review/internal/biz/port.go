package biz

import (
	"context"
	"time"
)

// 领域对象与端口声明。

// 处置级别。与 pkg/sensitive.Level 对齐（Warn=0/Review=1/Block=2），
// 但这里用 1/2/3 与 proto、数据库保持一致，转换在 data 层完成。
const (
	LevelWarn   uint8 = 1
	LevelReview uint8 = 2
	LevelBlock  uint8 = 3
)

// 审核对象类型。
const (
	TargetTypeProduct     uint8 = 1
	TargetTypeWanted      uint8 = 2
	TargetTypeMessage     uint8 = 3
	TargetTypeUserProfile uint8 = 4
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

// reportActionThreshold 同一目标被多少个不同用户举报后自动临时下架。
//
// 3 是经验阈值：单个举报可能是误报或恶意，但三个互不相识的用户
// 举报同一目标，属实的概率已足够高，值得先下架再由人工复核，
// 而不是等人工逐条看完（doc.md §9.6）。
const reportActionThreshold = 3

// ModerationResult 自动审核结论。
type ModerationResult struct {
	Level    uint8
	HitWords []string
	// Passed 是否放行。WARN 及以下放行。
	Passed bool
}

// ModerationTask 审核工单。
type ModerationTask struct {
	ID           int64
	TargetType   uint8
	TargetID     int64
	OwnerID      int64
	Status       uint8
	AutoLevel    uint8
	HitWords     []string
	Content      string
	ReviewerID   int64
	RejectReason string
	CreatedAt    time.Time
	DecidedAt    *time.Time
}

// Report 举报。
type Report struct {
	ID           int64
	ReporterID   int64
	TargetType   uint8
	TargetID     int64
	Reason       uint8
	Description  string
	ImageKeys    []string
	Status       uint8
	HandledBy    int64
	HandleResult string
	CreatedAt    time.Time
	HandledAt    *time.Time
}

// SensitiveWord 敏感词。
type SensitiveWord struct {
	ID        int64
	Word      string
	Category  uint8
	Level     uint8
	CreatedAt time.Time
}

// Matcher 敏感词匹配器。
//
// 抽象成接口而非直接用 pkg/sensitive.Matcher：biz 需要在测试中
// 注入可控的匹配结果，且这层接口隔离了 pkg 的 Level 编码（0/1/2）
// 与本服务的 1/2/3。
type Matcher interface {
	// Match 返回文本命中的最高处置级别与命中词。
	Match(text string) (level uint8, words []string)
	// Reload 用新词库重建匹配器。
	Reload(words []SensitiveWord)
}

// TaskRepo 审核工单仓储。
type TaskRepo interface {
	Create(ctx context.Context, t *ModerationTask) (*ModerationTask, error)
	GetByID(ctx context.Context, id int64) (*ModerationTask, error)
	// UpdateDecision 人工处置：置状态、审核人、驳回原因。
	// 带 fromStatus 条件，防止并发重复处置。
	UpdateDecision(ctx context.Context, id int64, fromStatus, toStatus uint8,
		reviewerID int64, reason string) (bool, error)
	List(ctx context.Context, status uint8, targetType uint8, page, pageSize int) ([]*ModerationTask, int64, error)
}

// ReportRepo 举报仓储。
type ReportRepo interface {
	// Create 创建举报。返回 false 表示该用户已举报过同一目标（幂等）。
	Create(ctx context.Context, r *Report) (created bool, err error)
	GetByID(ctx context.Context, id int64) (*Report, error)
	UpdateStatus(ctx context.Context, id int64, fromStatus, toStatus uint8,
		handledBy int64, result string) (bool, error)
	List(ctx context.Context, status uint8, page, pageSize int) ([]*Report, int64, error)
	// CountDistinctReporters 统计某目标被多少个不同用户举报（仅计待处理的）。
	CountDistinctReporters(ctx context.Context, targetType uint8, targetID int64) (int64, error)
}

// WordRepo 敏感词仓储。
type WordRepo interface {
	ListAll(ctx context.Context) ([]SensitiveWord, error)
	List(ctx context.Context, category uint8, page, pageSize int) ([]*SensitiveWord, int64, error)
	Create(ctx context.Context, w *SensitiveWord) (*SensitiveWord, error)
	Delete(ctx context.Context, id int64) error
}

// WordChangeNotifier 词库变更通知。
//
// 敏感词更新后，通过 Pub/Sub 通知所有实例重载词库（doc.md §9.6），
// 使多实例部署时词库变更能即时生效，而非等进程重启。
type WordChangeNotifier interface {
	// Publish 广播词库已变更。
	Publish(ctx context.Context) error
	// Subscribe 订阅变更，回调中重载词库。阻塞直到 ctx 取消。
	Subscribe(ctx context.Context, onChange func()) error
}

// ProductModerationGateway 把审核结论回写 product-service。
type ProductGateway interface {
	// ApplyModeration 回写商品审核结论。
	ApplyModeration(ctx context.Context, productID int64, passed bool,
		reason string, hitWords []string) error
	// TakeOffline 临时下架商品（举报达阈值时）。
	TakeOffline(ctx context.Context, productID int64) error
}

// EventPublisher 领域事件发布。
type EventPublisher interface {
	Publish(ctx context.Context, topic string, payload []byte) error
}

// 消费的事件主题。
const (
	// TopicProductCreated 商品发布，触发送审。
	//
	// 注意：product-service 已在发布时同步调用 SubmitProduct，
	// 这里再订阅是为了兜底——同步调用失败时，事件消费能补上送审，
	// 使商品不会因一次网络抖动而永久卡在待审核。
	TopicProductCreated = "product.created"
)
