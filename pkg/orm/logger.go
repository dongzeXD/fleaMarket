package orm

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"gorm.io/gorm/logger"
)

// slogLogger 把 GORM 的日志接口桥接到 slog，使 SQL 日志与业务日志同源、
// 共享 traceID 与脱敏规则。
type slogLogger struct {
	l             *slog.Logger
	level         logger.LogLevel
	slowThreshold time.Duration
}

func newSlogLogger(l *slog.Logger, level logger.LogLevel, slow time.Duration) logger.Interface {
	return &slogLogger{l: l, level: level, slowThreshold: slow}
}

func (s *slogLogger) LogMode(level logger.LogLevel) logger.Interface {
	c := *s
	c.level = level
	return &c
}

func (s *slogLogger) Info(ctx context.Context, msg string, args ...any) {
	if s.level >= logger.Info {
		s.l.InfoContext(ctx, "gorm: "+sprintf(msg, args...))
	}
}

func (s *slogLogger) Warn(ctx context.Context, msg string, args ...any) {
	if s.level >= logger.Warn {
		s.l.WarnContext(ctx, "gorm: "+sprintf(msg, args...))
	}
}

func (s *slogLogger) Error(ctx context.Context, msg string, args ...any) {
	if s.level >= logger.Error {
		s.l.ErrorContext(ctx, "gorm: "+sprintf(msg, args...))
	}
}

// Trace 记录每条 SQL 的执行情况。
//
// 三种情况分级处理：
//   - 出错且非「记录不存在」：error 级。ErrRecordNotFound 是正常的业务分支
//     （查不到就是一种结果），不该污染错误日志。
//   - 超过慢查询阈值：warn 级，附带耗时与影响行数。
//   - 其余：仅在 Debug 模式下以 debug 级输出。
func (s *slogLogger) Trace(ctx context.Context, begin time.Time,
	fc func() (string, int64), err error) {

	if s.level <= logger.Silent {
		return
	}
	elapsed := time.Since(begin)
	sql, rows := fc()

	attrs := []any{
		slog.String("sql", sql),
		slog.Int64("rows", rows),
		slog.Duration("elapsed", elapsed),
	}

	switch {
	case err != nil && !errors.Is(err, logger.ErrRecordNotFound):
		s.l.ErrorContext(ctx, "gorm: query failed", append(attrs, slog.Any("error", err))...)
	case s.slowThreshold > 0 && elapsed > s.slowThreshold:
		s.l.WarnContext(ctx, "gorm: slow query", attrs...)
	case s.level >= logger.Info:
		s.l.DebugContext(ctx, "gorm: query", attrs...)
	}
}

// sprintf 仅在确实要输出时才被调用，因此格式化开销不会浪费在被级别过滤掉的日志上。
func sprintf(msg string, args ...any) string {
	if len(args) == 0 {
		return msg
	}
	return fmt.Sprintf(msg, args...)
}
