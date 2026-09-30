// Package log 提供各服务统一的日志初始化。
//
// kratos v3 的 log 包已基于标准库 log/slog 提供了
// 上下文属性提取（WithExtractor）与敏感字段脱敏（WithFilter + FilterKey），
// 因此本包不重新实现日志器，只负责：
//   - 统一各服务的格式、级别、输出目标等约定
//   - 声明平台级的敏感字段清单，保证任何服务都不会把手机号、令牌写进日志
//
// 设计约束（见 doc.md §5.1）：本包不读取配置文件、不读环境变量，
// 全部参数由调用方以 Config 传入。
package log

import (
	"context"
	"io"
	"log/slog"
	"os"

	kratoslog "github.com/go-kratos/kratos/v3/log"
)

// SensitiveKeys 是平台级的敏感字段清单，其值在落盘前会被替换为掩码。
//
// 之所以放在公共库而不是各服务自行声明：脱敏遗漏属于一次就不可挽回的事故
// （日志一旦落盘或进入采集链路就难以撤回），必须默认全量生效而非依赖各服务自觉。
// 同时列出了 snake_case 与 camelCase 两种写法，因为 HTTP 层 JSON 与 gRPC 层
// 字段命名不一致。
var SensitiveKeys = []string{
	"phone", "phone_number", "phoneNumber",
	"password", "passwd",
	"code", "sms_code", "smsCode",
	"token", "access_token", "accessToken",
	"refresh_token", "refreshToken",
	"id_card", "idCard",
	"student_no", "studentNo",
	"edu_email", "eduEmail",
	"openid", "unionid",
	"secret", "app_secret", "appSecret",
	"authorization", "Authorization", "cookie", "Cookie",
}

// Format 日志编码格式。
type Format int

const (
	// FormatText 文本编码，便于本地开发肉眼阅读。
	FormatText Format = iota
	// FormatJSON JSON 编码，便于生产环境采集与检索。
	FormatJSON
)

// Extractor 从上下文提取日志属性，用于注入 traceID 等。
//
// 定义为类型别名而非新类型：调用方可以直接传入
// kratos contrib 的 tracing.TraceAttrs，无需为了类型匹配而
// 在业务代码里 import kratos 的 log 包。
type Extractor = func(context.Context) []slog.Attr

// Config 日志配置。
type Config struct {
	// ServiceID 实例标识，通常取 hostname。
	ServiceID string
	// ServiceName 服务名，如 "user-service"。
	ServiceName string
	// ServiceVersion 构建版本。
	ServiceVersion string
	// Level 最低输出级别，取值 debug/info/warn/error/fatal，大小写不敏感。
	// 留空时默认 info。
	Level string
	// Format 编码格式，默认 FormatText。
	Format Format
	// AddSource 是否记录调用位置。生产环境建议关闭以降低开销。
	AddSource bool
	// Writer 输出目标，留空时默认 os.Stdout。
	Writer io.Writer
	// Extractors 额外的上下文属性提取器，用于注入 traceID 等。
	// 调用方传入 tracing.TraceAttrs 即可完成链路 ID 注入。
	Extractors []Extractor
	// ExtraSensitiveKeys 业务追加的敏感字段，与 SensitiveKeys 合并生效。
	ExtraSensitiveKeys []string
}

// New 构造服务级 logger。
//
// 返回的 logger 已带上 service.id / service.name / service.version 三个固定属性，
// 并对 SensitiveKeys 中的字段做了脱敏。
func New(c Config) *slog.Logger {
	writer := c.Writer
	if writer == nil {
		writer = os.Stdout
	}

	format := kratoslog.FormatText
	if c.Format == FormatJSON {
		format = kratoslog.FormatJSON
	}

	level := kratoslog.LevelInfo
	if c.Level != "" {
		level = kratoslog.ParseLevel(c.Level)
	}

	keys := make([]string, 0, len(SensitiveKeys)+len(c.ExtraSensitiveKeys))
	keys = append(keys, SensitiveKeys...)
	keys = append(keys, c.ExtraSensitiveKeys...)

	opts := []kratoslog.Option{
		kratoslog.WithWriter(writer),
		kratoslog.WithFormat(format),
		kratoslog.WithLevel(level),
		kratoslog.WithAddSource(c.AddSource),
		kratoslog.WithFilter(kratoslog.FilterKey(keys...)),
	}
	if len(c.Extractors) > 0 {
		ex := make([]kratoslog.Extractor, 0, len(c.Extractors))
		for _, e := range c.Extractors {
			ex = append(ex, e)
		}
		opts = append(opts, kratoslog.WithExtractor(ex...))
	}

	return kratoslog.NewLogger(kratoslog.NewHandler(opts...)).With(
		slog.String("service.id", c.ServiceID),
		slog.String("service.name", c.ServiceName),
		slog.String("service.version", c.ServiceVersion),
	)
}

// SetDefault 将 logger 设为进程默认，使 slog 包级函数与 kratos 内部日志同源。
func SetDefault(l *slog.Logger) { kratoslog.SetDefault(l) }
