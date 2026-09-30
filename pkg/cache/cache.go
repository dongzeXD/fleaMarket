// Package cache 提供缓存抽象与 Redis 实现。
//
// 除基础读写外，重点解决两个高并发下的经典问题：
//   - 缓存击穿：热点 key 失效瞬间大量请求同时回源，用分布式锁收敛为一次回源。
//   - 缓存穿透：查询不存在的数据每次都落到数据库，用空值占位挡住。
//
// 泛用性约束（doc.md §5.1）：本包不知道缓存的是什么，key 的命名规则由调用方决定。
package cache

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound 缓存未命中，或命中了空值占位。
//
// 命中空值占位也返回此错误，是刻意的设计：对调用方而言
// 「数据库里没有」与「缓存记录了数据库里没有」是同一件事，
// 不应让调用方去区分这种实现细节。
var ErrNotFound = errors.New("cache: not found")

// ErrLockNotAcquired 未取得锁。
var ErrLockNotAcquired = errors.New("cache: lock not acquired")

// Cacher 缓存读写接口。
//
// 只定义字节级操作，序列化格式由上层决定——这样 JSON、protobuf、
// msgpack 都能用同一套缓存实现。
type Cacher interface {
	// Get 读取。未命中返回 ErrNotFound。
	Get(ctx context.Context, key string) ([]byte, error)
	// Set 写入并设置过期时间。ttl 必须为正，不接受永不过期：
	// 没有 TTL 的缓存最终会变成一份无人负责的脏数据副本。
	Set(ctx context.Context, key string, val []byte, ttl time.Duration) error
	// Del 删除一个或多个 key。
	Del(ctx context.Context, keys ...string) error
	// Incr 原子自增并返回新值，用于浏览量、收藏数等计数场景。
	Incr(ctx context.Context, key string, delta int64) (int64, error)
	// Expire 重设过期时间。
	Expire(ctx context.Context, key string, ttl time.Duration) error
	// SetNX 仅当 key 不存在时写入，返回是否写入成功。
	// 这是分布式锁与限流的基础操作。
	SetNX(ctx context.Context, key string, val []byte, ttl time.Duration) (bool, error)
	// Eval 执行 Lua 脚本，用于需要原子性的复合操作（如解锁时校验持有者）。
	Eval(ctx context.Context, script string, keys []string, args ...any) (any, error)
}

// Locker 分布式锁接口。
type Locker interface {
	// Lock 尝试获取锁。
	//
	// ttl 是锁的自动释放时间，必须设置：持锁进程崩溃时若锁不过期，
	// 该资源将永久不可操作。
	// wait 为等待获取锁的最长时间，为 0 时立即返回 ErrLockNotAcquired。
	Lock(ctx context.Context, key string, ttl, wait time.Duration) (Unlock, error)
}

// Unlock 释放锁。实现必须校验持有者身份，避免释放他人的锁
// （本进程锁超时自动释放后，锁可能已被其他进程获取）。
type Unlock func(context.Context) error

// Codec 序列化编解码器，供 Load 使用。
type Codec interface {
	Marshal(v any) ([]byte, error)
	Unmarshal(data []byte, v any) error
}
