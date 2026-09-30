package data

import (
	"log/slog"
	"time"

	"github.com/fleamarket/app/search/internal/conf"

	"github.com/fleamarket/app/search/internal/engine"

	"github.com/fleamarket/pkg/cache"
	"github.com/google/wire"
)

// ProviderSet 数据层的 wire provider 集合。
var ProviderSet = wire.NewSet(
	NewData,
	NewEngine,
	NewProductConn,
	NewProductGateway,
	NewConsumer,
	NewDeadLetterCounter,
	NewIndexSyncState,
)

// NewEngine 构造检索引擎。
//
// 返回接口而非具体类型：biz 层只依赖 engine.Engine，
// 日后换 Elasticsearch 时只需在此处替换实现。
func NewEngine(c *conf.Meilisearch, logger *slog.Logger) (engine.Engine, func(), error) {
	return engine.NewMeili(engine.MeiliConfig{
		Host:   c.GetHost(),
		APIKey: c.GetApiKey(),
		Timeouts: engine.Timeouts{
			Search: c.GetSearchTimeout().AsDuration(),
			Index:  c.GetIndexTimeout().AsDuration(),
			Health: c.GetHealthTimeout().AsDuration(),
		},
	}, logger)
}

// Data 持有长生命周期客户端。
//
// 注意这里没有 *gorm.DB：search-service 不拥有任何数据库表。
// 索引在 Meilisearch，权威数据在 product-service，同步状态在 Redis。
//
// 骨架模板默认会连 MySQL，这里刻意去掉——保留一个用不到的连接会
// 让本服务在 MySQL 故障时启动失败，而它本来完全有能力继续提供
// 检索服务（甚至降级路径也只是转发给 product-service）。
// 不需要的依赖就是多余的故障点。
type Data struct {
	Cache *cache.RedisCache
}

// NewData 建立 Redis 连接。
func NewData(c *conf.Data, logger *slog.Logger) (*Data, func(), error) {
	rc, cleanup, err := cache.NewRedis(cache.RedisConfig{
		Addr:         c.GetRedis().GetAddr(),
		Password:     c.GetRedis().GetPassword(),
		DB:           int(c.GetRedis().GetDb()),
		PoolSize:     int(c.GetRedis().GetPoolSize()),
		ReadTimeout:  orDefault(c.GetRedis().GetReadTimeout().AsDuration(), 200*time.Millisecond),
		WriteTimeout: orDefault(c.GetRedis().GetWriteTimeout().AsDuration(), 200*time.Millisecond),
		KeyPrefix:    c.GetRedis().GetKeyPrefix(),
	})
	if err != nil {
		return nil, nil, err
	}
	return &Data{Cache: rc}, cleanup, nil
}

func orDefault(v, def time.Duration) time.Duration {
	if v <= 0 {
		return def
	}
	return v
}
