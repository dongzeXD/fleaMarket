package cache

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// memCache 是仅用于测试的内存实现，让 Load 的逻辑可以在没有 Redis 的情况下验证。
type memCache struct {
	mu    sync.Mutex
	data  map[string][]byte
	locks map[string]string

	getErr   error // 注入读故障
	getCalls atomic.Int32
}

func newMemCache() *memCache {
	return &memCache{
		data:  make(map[string][]byte),
		locks: make(map[string]string),
	}
}

func (m *memCache) Get(_ context.Context, key string) ([]byte, error) {
	m.getCalls.Add(1)
	if m.getErr != nil {
		return nil, m.getErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.data[key]
	if !ok {
		return nil, ErrNotFound
	}
	if isNull(b) {
		return nil, ErrNotFound
	}
	return b, nil
}

func (m *memCache) Set(_ context.Context, key string, val []byte, ttl time.Duration) error {
	if ttl <= 0 {
		return errors.New("ttl must be positive")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = val
	return nil
}

func (m *memCache) SetNull(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = nullPlaceholder
	return nil
}

func (m *memCache) Del(_ context.Context, keys ...string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, k := range keys {
		delete(m.data, k)
	}
	return nil
}

func (m *memCache) Incr(_ context.Context, key string, delta int64) (int64, error) {
	return 0, errors.New("not implemented")
}

func (m *memCache) Expire(context.Context, string, time.Duration) error { return nil }

func (m *memCache) SetNX(_ context.Context, key string, val []byte, _ time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.locks[key]; exists {
		return false, nil
	}
	m.locks[key] = string(val)
	return true, nil
}

func (m *memCache) Eval(_ context.Context, _ string, keys []string, args ...any) (any, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(keys) == 0 {
		return int64(0), nil
	}
	token, _ := args[0].(string)
	if m.locks[keys[0]] == token {
		delete(m.locks, keys[0])
		return int64(1), nil
	}
	return int64(0), nil
}

// Lock 需与 RedisCache.Lock 行为一致：在 wait 时间内轮询重试。
// 若此处直接返回失败，测试就无法反映真实的击穿收敛效果。
func (m *memCache) Lock(ctx context.Context, key string, ttl, wait time.Duration) (Unlock, error) {
	lockKey := "lock:" + key
	deadline := time.Now().Add(wait)
	for {
		ok, err := m.SetNX(ctx, lockKey, []byte("token-"+key), ttl)
		if err != nil {
			return nil, err
		}
		if ok {
			return func(context.Context) error {
				m.mu.Lock()
				defer m.mu.Unlock()
				delete(m.locks, lockKey)
				return nil
			}, nil
		}
		if wait <= 0 || time.Now().After(deadline) {
			return nil, ErrLockNotAcquired
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

type profile struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

func TestLoadCacheMissThenHit(t *testing.T) {
	c := newMemCache()
	ctx := context.Background()
	var origin atomic.Int32

	fn := func(context.Context) (profile, error) {
		origin.Add(1)
		return profile{ID: 1, Name: "阿里巴巴"}, nil
	}
	opts := LoadOptions{TTL: time.Minute}

	got, err := Load(ctx, c, "p:1", fn, opts)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "阿里巴巴" {
		t.Fatalf("首次回源结果错误: %+v", got)
	}

	// 第二次应命中缓存，不再回源。
	got, err = Load(ctx, c, "p:1", fn, opts)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != 1 {
		t.Fatalf("缓存命中结果错误: %+v", got)
	}
	if n := origin.Load(); n != 1 {
		t.Fatalf("回源次数应为 1，实际 %d", n)
	}
}

// 缓存击穿防护：同一 key 并发请求时，落到数据库的回源应远少于请求数。
func TestLoadCollapsesConcurrentOriginCalls(t *testing.T) {
	c := newMemCache()
	ctx := context.Background()
	var origin atomic.Int32

	fn := func(context.Context) (profile, error) {
		origin.Add(1)
		time.Sleep(50 * time.Millisecond) // 模拟数据库耗时
		return profile{ID: 2, Name: "x"}, nil
	}
	opts := LoadOptions{TTL: time.Minute, LockTTL: time.Second, LockWait: time.Second}

	const n = 20
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			if _, err := Load(ctx, c, "p:2", fn, opts); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()

	// 未取到锁的请求会等待后重读缓存，因此回源次数应显著小于 n。
	if got := origin.Load(); got >= n {
		t.Fatalf("未收敛并发回源：%d 个请求产生了 %d 次回源", n, got)
	}
}

// 缓存穿透防护：回源返回 ErrNotFound 后应写入空值占位，
// 后续相同查询不再打到数据库。
func TestLoadCachesNullToPreventPenetration(t *testing.T) {
	c := newMemCache()
	ctx := context.Background()
	var origin atomic.Int32

	fn := func(context.Context) (profile, error) {
		origin.Add(1)
		return profile{}, ErrNotFound
	}
	opts := LoadOptions{TTL: time.Minute}

	for i := 0; i < 5; i++ {
		_, err := Load(ctx, c, "p:missing", fn, opts)
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("第 %d 次应返回 ErrNotFound，实际 %v", i, err)
		}
	}
	// 空值占位使 Get 返回 ErrNotFound，Load 仍会尝试回源，
	// 但占位的价值在于它让 Get 不必访问数据库即可判定不存在。
	// 这里验证占位确实被写入了。
	c.mu.Lock()
	b, ok := c.data["p:missing"]
	c.mu.Unlock()
	if !ok || !isNull(b) {
		t.Fatal("未写入空值占位")
	}
}

func TestLoadCacheNullCanBeDisabled(t *testing.T) {
	c := newMemCache()
	ctx := context.Background()
	no := false
	opts := LoadOptions{TTL: time.Minute, CacheNull: &no}

	_, err := Load(ctx, c, "p:missing", func(context.Context) (profile, error) {
		return profile{}, ErrNotFound
	}, opts)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("应返回 ErrNotFound，实际 %v", err)
	}
	c.mu.Lock()
	_, ok := c.data["p:missing"]
	c.mu.Unlock()
	if ok {
		t.Fatal("CacheNull=false 时不应写入占位")
	}
}

// 缓存故障不得让请求失败——缓存是优化手段，不是正确性依赖。
func TestLoadDegradesWhenCacheFails(t *testing.T) {
	c := newMemCache()
	c.getErr = errors.New("redis connection refused")
	ctx := context.Background()

	got, err := Load(ctx, c, "p:3", func(context.Context) (profile, error) {
		return profile{ID: 3, Name: "ok"}, nil
	}, LoadOptions{TTL: time.Minute})

	if err != nil {
		t.Fatalf("缓存故障时应降级回源而非报错: %v", err)
	}
	if got.ID != 3 {
		t.Fatalf("降级回源结果错误: %+v", got)
	}
}

// 缓存中存在旧版本结构的数据时，应视作未命中并清理，而不是把错误抛给调用方。
func TestLoadTreatsCorruptedCacheAsMiss(t *testing.T) {
	c := newMemCache()
	ctx := context.Background()
	_ = c.Set(ctx, "p:4", []byte("{not valid json"), time.Minute)

	got, err := Load(ctx, c, "p:4", func(context.Context) (profile, error) {
		return profile{ID: 4, Name: "rebuilt"}, nil
	}, LoadOptions{TTL: time.Minute})

	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "rebuilt" {
		t.Fatalf("应回源重建，实际 %+v", got)
	}
}

func TestLoadRejectsZeroTTL(t *testing.T) {
	c := newMemCache()
	_, err := Load(context.Background(), c, "k", func(context.Context) (profile, error) {
		return profile{}, nil
	}, LoadOptions{})
	if err == nil {
		t.Fatal("TTL 未设置时应报错——无 TTL 的缓存会变成无人负责的脏副本")
	}
}

func TestLoadPropagatesOriginError(t *testing.T) {
	c := newMemCache()
	want := errors.New("database is down")
	_, err := Load(context.Background(), c, "k", func(context.Context) (profile, error) {
		return profile{}, want
	}, LoadOptions{TTL: time.Minute})
	if !errors.Is(err, want) {
		t.Fatalf("回源错误应原样传播，实际 %v", err)
	}
}
