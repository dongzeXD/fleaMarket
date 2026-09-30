package cache

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// nullPlaceholder 空值占位的内容。
//
// 用一个不可能与正常序列化结果冲突的字节序列，而非空切片：
// 空切片无法与「缓存了一个空对象」区分。
var nullPlaceholder = []byte("\x00__cache_null__")

// RedisConfig Redis 连接配置。
type RedisConfig struct {
	Addr     string
	Password string
	DB       int
	// PoolSize 连接池大小，默认 20。
	PoolSize int
	// ReadTimeout 读超时，默认 200ms。
	//
	// 缓存的超时必须设得比数据库更短：缓存的意义是快，
	// 一个慢到接近数据库的缓存查询只会让总耗时更糟。
	ReadTimeout time.Duration
	// WriteTimeout 写超时，默认 200ms。
	WriteTimeout time.Duration
	// KeyPrefix 全局 key 前缀，用于多环境共用一个 Redis 实例时隔离。
	KeyPrefix string
	// NullTTL 空值占位的过期时间，默认 60s。
	//
	// 刻意远短于正常数据的 TTL：空值占位是为了挡住恶意或错误的穿透查询，
	// 而一旦数据真的被创建，过长的空值会导致新数据长时间不可见。
	NullTTL time.Duration
}

func (c *RedisConfig) setDefaults() {
	if c.PoolSize <= 0 {
		c.PoolSize = 20
	}
	if c.ReadTimeout <= 0 {
		c.ReadTimeout = 200 * time.Millisecond
	}
	if c.WriteTimeout <= 0 {
		c.WriteTimeout = 200 * time.Millisecond
	}
	if c.NullTTL <= 0 {
		c.NullTTL = 60 * time.Second
	}
}

// RedisCache 基于 Redis 的 Cacher 与 Locker 实现。
type RedisCache struct {
	rdb     *redis.Client
	prefix  string
	nullTTL time.Duration
}

var (
	_ Cacher = (*RedisCache)(nil)
	_ Locker = (*RedisCache)(nil)
)

// NewRedis 创建 Redis 缓存客户端。
func NewRedis(c RedisConfig) (*RedisCache, func(), error) {
	c.setDefaults()
	if c.Addr == "" {
		return nil, nil, fmt.Errorf("cache: empty redis addr")
	}
	rdb := redis.NewClient(&redis.Options{
		Addr:         c.Addr,
		Password:     c.Password,
		DB:           c.DB,
		PoolSize:     c.PoolSize,
		ReadTimeout:  c.ReadTimeout,
		WriteTimeout: c.WriteTimeout,
	})
	rc := &RedisCache{rdb: rdb, prefix: c.KeyPrefix, nullTTL: c.NullTTL}
	return rc, func() { _ = rdb.Close() }, nil
}

// Client 暴露底层客户端，供需要 Redis 特有能力的场景使用（如 Stream、Pub/Sub）。
// 这是刻意开的逃生口：接口不可能覆盖所有 Redis 能力，
// 与其让调用方绕过本包重新建连接，不如让它复用同一个连接池。
func (r *RedisCache) Client() *redis.Client { return r.rdb }

func (r *RedisCache) k(key string) string {
	if r.prefix == "" {
		return key
	}
	return r.prefix + ":" + key
}

func (r *RedisCache) Get(ctx context.Context, key string) ([]byte, error) {
	b, err := r.rdb.Get(ctx, r.k(key)).Bytes()
	switch {
	case errors.Is(err, redis.Nil):
		return nil, ErrNotFound
	case err != nil:
		return nil, err
	}
	if isNull(b) {
		return nil, ErrNotFound
	}
	return b, nil
}

func (r *RedisCache) Set(ctx context.Context, key string, val []byte, ttl time.Duration) error {
	if ttl <= 0 {
		return fmt.Errorf("cache: ttl must be positive, got %v", ttl)
	}
	return r.rdb.Set(ctx, r.k(key), val, ttl).Err()
}

// SetNull 写入空值占位，用于防穿透。
func (r *RedisCache) SetNull(ctx context.Context, key string) error {
	return r.rdb.Set(ctx, r.k(key), nullPlaceholder, r.nullTTL).Err()
}

func (r *RedisCache) Del(ctx context.Context, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	full := make([]string, len(keys))
	for i, k := range keys {
		full[i] = r.k(k)
	}
	return r.rdb.Del(ctx, full...).Err()
}

func (r *RedisCache) Incr(ctx context.Context, key string, delta int64) (int64, error) {
	return r.rdb.IncrBy(ctx, r.k(key), delta).Result()
}

func (r *RedisCache) Expire(ctx context.Context, key string, ttl time.Duration) error {
	return r.rdb.Expire(ctx, r.k(key), ttl).Err()
}

func (r *RedisCache) SetNX(ctx context.Context, key string, val []byte, ttl time.Duration) (bool, error) {
	return r.rdb.SetNX(ctx, r.k(key), val, ttl).Result()
}

func (r *RedisCache) Eval(ctx context.Context, script string, keys []string, args ...any) (any, error) {
	full := make([]string, len(keys))
	for i, k := range keys {
		full[i] = r.k(k)
	}
	return r.rdb.Eval(ctx, script, full, args...).Result()
}

// unlockScript 只在锁的持有者标识匹配时才删除。
//
// 为什么必须用 Lua 而不是「先 GET 比对再 DEL」：两条命令之间锁可能因 TTL 到期
// 而被其他进程获取，此时 DEL 会误删他人的锁。Lua 保证了比对与删除的原子性。
const unlockScript = `
if redis.call("GET", KEYS[1]) == ARGV[1] then
	return redis.call("DEL", KEYS[1])
end
return 0
`

// Lock 获取分布式锁。
//
// 采用轮询重试而非 Redis 的阻塞原语：阻塞式等待会占用连接，
// 在锁竞争激烈时可能耗尽连接池。轮询间隔随等待时间递增以降低无效请求。
func (r *RedisCache) Lock(ctx context.Context, key string, ttl, wait time.Duration) (Unlock, error) {
	if ttl <= 0 {
		return nil, fmt.Errorf("cache: lock ttl must be positive")
	}
	token, err := randomToken()
	if err != nil {
		return nil, err
	}
	lockKey := "lock:" + key

	deadline := time.Now().Add(wait)
	interval := 20 * time.Millisecond

	for {
		ok, err := r.SetNX(ctx, lockKey, []byte(token), ttl)
		if err != nil {
			return nil, err
		}
		if ok {
			return func(ctx context.Context) error {
				_, err := r.Eval(ctx, unlockScript, []string{lockKey}, token)
				return err
			}, nil
		}
		if wait <= 0 || time.Now().After(deadline) {
			return nil, ErrLockNotAcquired
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
		if interval < 200*time.Millisecond {
			interval *= 2
		}
	}
}

func isNull(b []byte) bool {
	if len(b) != len(nullPlaceholder) {
		return false
	}
	for i := range b {
		if b[i] != nullPlaceholder[i] {
			return false
		}
	}
	return true
}

func randomToken() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("cache: generate lock token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
