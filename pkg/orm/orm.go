// Package orm 封装 GORM 的初始化、实体注册与建表能力。
//
// 本包是「脚本根据实体对象创建数据库表」这一需求的底座（见 doc.md §7）。
// 设计上刻意分成三件互不耦合的事：
//   - Open：把配置变成一个可用的 *gorm.DB
//   - Registry：收集各服务声明的表实体
//   - Migrator：把实体变成数据库中的表
//
// 泛用性约束（doc.md §5.1）：Entity 接口只要求实体能说出自己的表名，
// 不对字段做任何假设，因此任何项目的任何表都能接入。
package orm

import (
	"fmt"
	"log/slog"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
)

// Config 数据库连接配置。
type Config struct {
	// DSN 数据源名称。凭据不应硬编码在配置文件中，由环境变量注入。
	DSN string
	// MaxOpenConns 最大连接数，默认 50。
	MaxOpenConns int
	// MaxIdleConns 最大空闲连接数，默认 10。
	MaxIdleConns int
	// ConnMaxLifetime 连接最长存活时间，默认 1 小时。
	//
	// 必须设置：云数据库与中间层代理通常有空闲连接回收策略，
	// 若客户端不主动淘汰，会周期性撞上「连接已被对端关闭」的错误。
	ConnMaxLifetime time.Duration
	// SlowThreshold 慢查询阈值，默认 200ms。超过则以 warn 级别打日志。
	SlowThreshold time.Duration
	// Debug 打印全部 SQL。仅开发环境开启。
	Debug bool
	// TablePrefix 表名前缀。本项目实体已在 TableName() 中写明完整表名，
	// 一般留空。
	TablePrefix string
	// Logger 日志器，用于输出慢查询与错误。留空时使用 slog 默认实例。
	Logger *slog.Logger
}

func (c *Config) setDefaults() {
	if c.MaxOpenConns <= 0 {
		c.MaxOpenConns = 50
	}
	if c.MaxIdleConns <= 0 {
		c.MaxIdleConns = 10
	}
	if c.ConnMaxLifetime <= 0 {
		c.ConnMaxLifetime = time.Hour
	}
	if c.SlowThreshold <= 0 {
		c.SlowThreshold = 200 * time.Millisecond
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
}

// Open 建立数据库连接并完成连接池与日志配置。
//
// 返回的 cleanup 需在进程退出前调用。
func Open(c Config) (*gorm.DB, func(), error) {
	c.setDefaults()

	if c.DSN == "" {
		return nil, nil, fmt.Errorf("orm: empty DSN")
	}

	level := logger.Warn
	if c.Debug {
		level = logger.Info
	}

	db, err := gorm.Open(mysql.Open(c.DSN), &gorm.Config{
		Logger: newSlogLogger(c.Logger, level, c.SlowThreshold),
		NamingStrategy: schema.NamingStrategy{
			TablePrefix:   c.TablePrefix,
			SingularTable: true,
		},
		// 关闭默认事务：GORM 默认把每条单独的写操作包进事务，
		// 对于本项目大量的单行插入/更新是额外开销。需要事务的地方显式 Transaction。
		SkipDefaultTransaction: true,
		// 批量预编译，减少重复 SQL 的解析开销。
		PrepareStmt: true,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("orm: open database: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, nil, fmt.Errorf("orm: get sql.DB: %w", err)
	}
	sqlDB.SetMaxOpenConns(c.MaxOpenConns)
	sqlDB.SetMaxIdleConns(c.MaxIdleConns)
	sqlDB.SetConnMaxLifetime(c.ConnMaxLifetime)

	cleanup := func() {
		if err := sqlDB.Close(); err != nil {
			c.Logger.Error("orm: close database", slog.Any("error", err))
		}
	}
	return db, cleanup, nil
}
