package data

import (
	"log/slog"
	"time"

	"github.com/fleamarket/app/review/internal/conf"

	"github.com/fleamarket/pkg/cache"
	"github.com/fleamarket/pkg/orm"
	"github.com/google/wire"
	"gorm.io/gorm"
)

// ProviderSet 数据层的 wire provider 集合。
// 新增 Repo 实现后在此登记。
var ProviderSet = wire.NewSet(NewData)

// Data 持有各 Repo 共享的长生命周期客户端。
type Data struct {
	DB    *gorm.DB
	Cache *cache.RedisCache
}

// NewData 建立数据库与缓存连接。
//
// 注意此处不做建表：schema 变更统一由 cmd/dbctl 负责（doc.md §7），
// 服务启动时自动建表会让「谁在什么时候改了表结构」变得不可追溯。
func NewData(c *conf.Data, logger *slog.Logger) (*Data, func(), error) {
	db, dbCleanup, err := orm.Open(orm.Config{
		DSN:             c.GetDatabase().GetSource(),
		MaxOpenConns:    int(c.GetDatabase().GetMaxOpenConns()),
		MaxIdleConns:    int(c.GetDatabase().GetMaxIdleConns()),
		ConnMaxLifetime: c.GetDatabase().GetConnMaxLifetime().AsDuration(),
		SlowThreshold:   c.GetDatabase().GetSlowThreshold().AsDuration(),
		Debug:           c.GetDatabase().GetDebug(),
		Logger:          logger,
	})
	if err != nil {
		return nil, nil, err
	}

	rc, cacheCleanup, err := cache.NewRedis(cache.RedisConfig{
		Addr:         c.GetRedis().GetAddr(),
		Password:     c.GetRedis().GetPassword(),
		DB:           int(c.GetRedis().GetDb()),
		PoolSize:     int(c.GetRedis().GetPoolSize()),
		ReadTimeout:  orDefault(c.GetRedis().GetReadTimeout().AsDuration(), 200*time.Millisecond),
		WriteTimeout: orDefault(c.GetRedis().GetWriteTimeout().AsDuration(), 200*time.Millisecond),
		KeyPrefix:    c.GetRedis().GetKeyPrefix(),
	})
	if err != nil {
		dbCleanup()
		return nil, nil, err
	}

	cleanup := func() {
		cacheCleanup()
		dbCleanup()
	}
	return &Data{DB: db, Cache: rc}, cleanup, nil
}

func orDefault(v, def time.Duration) time.Duration {
	if v <= 0 {
		return def
	}
	return v
}
