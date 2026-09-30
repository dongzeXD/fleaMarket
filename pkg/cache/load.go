package cache

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// JSONCodec 默认编解码器。
//
// 选 JSON 而非 protobuf 作为默认：缓存内容常常是拼装后的视图对象而非
// 某个具体的 proto message，JSON 无需为每种组合定义 message。
// 需要极致性能的热点路径可自行传入其他 Codec。
type JSONCodec struct{}

func (JSONCodec) Marshal(v any) ([]byte, error)   { return json.Marshal(v) }
func (JSONCodec) Unmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

// NullSetter 能写入空值占位的缓存。RedisCache 已实现。
type NullSetter interface {
	SetNull(ctx context.Context, key string) error
}

// LoadOptions Load 的可选参数。
type LoadOptions struct {
	// TTL 正常数据的缓存时长。必填。
	TTL time.Duration
	// LockTTL 回源期间持锁的时长，默认 3s。
	// 应略大于回源的预期耗时，又不能太长——持锁进程崩溃时其他请求要等它过期。
	LockTTL time.Duration
	// LockWait 等待他人回源完成的最长时间，默认 2s。
	LockWait time.Duration
	// Codec 序列化方式，默认 JSONCodec。
	Codec Codec
	// CacheNull 是否缓存空结果以防穿透，默认 true。
	//
	// 需要关闭的场景：查询条件由用户完全控制且合法取值空间巨大时，
	// 缓存空值反而会挤占内存。
	CacheNull *bool
}

func (o *LoadOptions) setDefaults() {
	if o.LockTTL <= 0 {
		o.LockTTL = 3 * time.Second
	}
	if o.LockWait <= 0 {
		o.LockWait = 2 * time.Second
	}
	if o.Codec == nil {
		o.Codec = JSONCodec{}
	}
	if o.CacheNull == nil {
		t := true
		o.CacheNull = &t
	}
}

// Load 读缓存，未命中则加锁回源。
//
// 完整流程：
//  1. 读缓存，命中则直接返回。
//  2. 未命中，尝试取分布式锁。
//     - 取到锁：二次读缓存（可能在等锁期间已被他人填充），仍未命中则调用 fn 回源，
//     写入缓存后返回。这一步收敛了缓存击穿——同一 key 同时只有一个请求落到数据库。
//     - 未取到锁：说明他人正在回源，等待后重读缓存；若仍无结果则直接回源，
//     不再阻塞。宁可多一次数据库查询，也不能让请求无限等待。
//  3. fn 返回 ErrNotFound 时写入空值占位，挡住后续穿透。
//
// 关于「未取到锁就直接回源」的取舍：严格的做法是持续等待直到拿到锁或超时报错，
// 但那会把缓存故障放大成请求失败。缓存是优化手段而非正确性依赖，
// 降级为直连数据库是正确的选择。
func Load[T any](ctx context.Context, c Cacher, key string,
	fn func(context.Context) (T, error), opts LoadOptions) (T, error) {

	var zero T
	opts.setDefaults()
	if opts.TTL <= 0 {
		return zero, errors.New("cache: LoadOptions.TTL must be positive")
	}

	// 1. 首次读缓存
	if v, err := getDecoded[T](ctx, c, key, opts.Codec); err == nil {
		return v, nil
	} else if !errors.Is(err, ErrNotFound) {
		// 缓存故障不应导致请求失败，降级为直接回源。
		return loadAndStore(ctx, c, key, fn, opts)
	}

	locker, ok := c.(Locker)
	if !ok {
		return loadAndStore(ctx, c, key, fn, opts)
	}

	// 2. 取锁
	unlock, err := locker.Lock(ctx, "load:"+key, opts.LockTTL, opts.LockWait)
	if err != nil {
		if errors.Is(err, ErrLockNotAcquired) {
			// 他人已回源完毕的概率很高，先重读一次。
			if v, err2 := getDecoded[T](ctx, c, key, opts.Codec); err2 == nil {
				return v, nil
			}
		}
		// 等不到锁或缓存仍为空，降级直连回源，不再等待。
		return loadAndStore(ctx, c, key, fn, opts)
	}
	defer func() { _ = unlock(ctx) }()

	// 3. 二次读（double check）：等锁期间可能已被填充
	if v, err := getDecoded[T](ctx, c, key, opts.Codec); err == nil {
		return v, nil
	}

	return loadAndStore(ctx, c, key, fn, opts)
}

func loadAndStore[T any](ctx context.Context, c Cacher, key string,
	fn func(context.Context) (T, error), opts LoadOptions) (T, error) {

	var zero T
	v, err := fn(ctx)
	if err != nil {
		if errors.Is(err, ErrNotFound) && *opts.CacheNull {
			if ns, ok := c.(NullSetter); ok {
				_ = ns.SetNull(ctx, key)
			}
		}
		return zero, err
	}

	b, mErr := opts.Codec.Marshal(v)
	if mErr == nil {
		// 缓存写入失败不影响本次返回：数据已经拿到了。
		_ = c.Set(ctx, key, b, opts.TTL)
	}
	return v, nil
}

func getDecoded[T any](ctx context.Context, c Cacher, key string, codec Codec) (T, error) {
	var zero T
	b, err := c.Get(ctx, key)
	if err != nil {
		return zero, err
	}
	var v T
	if err := codec.Unmarshal(b, &v); err != nil {
		// 反序列化失败通常意味着缓存里是旧版本结构的数据，
		// 视作未命中并清掉，让下一次回源重建。
		_ = c.Del(ctx, key)
		return zero, ErrNotFound
	}
	return v, nil
}
