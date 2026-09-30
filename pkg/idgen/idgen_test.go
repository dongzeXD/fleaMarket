package idgen

import (
	"sync"
	"testing"
	"time"
)

func TestNewRejectsOutOfRangeNode(t *testing.T) {
	for _, node := range []int64{-1, 1024, 99999} {
		if _, err := New(node); err == nil {
			t.Fatalf("node=%d 应被拒绝", node)
		}
	}
	if _, err := New(0); err != nil {
		t.Fatalf("node=0 应被接受: %v", err)
	}
	if _, err := New(1023); err != nil {
		t.Fatalf("node=1023 应被接受: %v", err)
	}
}

func TestNextIDIsMonotonicAndUnique(t *testing.T) {
	g, err := New(7)
	if err != nil {
		t.Fatal(err)
	}
	const n = 10000
	seen := make(map[int64]struct{}, n)
	var prev int64
	for i := 0; i < n; i++ {
		id, err := g.NextID()
		if err != nil {
			t.Fatal(err)
		}
		if id <= prev {
			t.Fatalf("ID 非单调递增: prev=%d cur=%d", prev, id)
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("ID 重复: %d", id)
		}
		seen[id] = struct{}{}
		prev = id
	}
}

func TestNextIDConcurrentUnique(t *testing.T) {
	g, err := New(1)
	if err != nil {
		t.Fatal(err)
	}
	const goroutines, per = 16, 500

	var mu sync.Mutex
	seen := make(map[int64]struct{}, goroutines*per)

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			ids := make([]int64, 0, per)
			for j := 0; j < per; j++ {
				id, err := g.NextID()
				if err != nil {
					t.Error(err)
					return
				}
				ids = append(ids, id)
			}
			mu.Lock()
			defer mu.Unlock()
			for _, id := range ids {
				if _, dup := seen[id]; dup {
					t.Errorf("并发下 ID 重复: %d", id)
					return
				}
				seen[id] = struct{}{}
			}
		}()
	}
	wg.Wait()

	if len(seen) != goroutines*per {
		t.Fatalf("期望 %d 个唯一 ID，实际 %d", goroutines*per, len(seen))
	}
}

// 序列位只有 12 位（4096/毫秒），本测试验证序列耗尽时会正确等待下一毫秒
// 而非产生重复 ID。
func TestSequenceExhaustionWaitsForNextMillis(t *testing.T) {
	g, err := New(3)
	if err != nil {
		t.Fatal(err)
	}
	// 冻结时钟，强制所有 ID 落在同一毫秒，从而触发序列回绕。
	frozen := time.Now().UnixMilli()
	calls := 0
	g.now = func() int64 {
		calls++
		// 前 maxSeq+1 次返回同一毫秒，之后推进，模拟真实时钟前进。
		if calls <= maxSeq+1 {
			return frozen
		}
		return frozen + 1
	}

	seen := make(map[int64]struct{}, maxSeq+10)
	for i := 0; i < maxSeq+10; i++ {
		id, err := g.NextID()
		if err != nil {
			t.Fatal(err)
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("序列耗尽后出现重复 ID: %d (第 %d 次)", id, i)
		}
		seen[id] = struct{}{}
	}
}

func TestClockBackwardsWithinToleranceRecovers(t *testing.T) {
	g, err := New(5)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UnixMilli()
	step := 0
	g.now = func() int64 {
		step++
		switch {
		case step == 1:
			return base + 100
		case step == 2:
			return base + 98 // 回拨 2ms，在容忍范围内
		default:
			return base + 100 + int64(step) // 自旋后时钟恢复
		}
	}
	if _, err := g.NextID(); err != nil {
		t.Fatal(err)
	}
	if _, err := g.NextID(); err != nil {
		t.Fatalf("小幅回拨应自旋等待而非报错: %v", err)
	}
}

func TestClockBackwardsBeyondToleranceFails(t *testing.T) {
	g, err := New(5)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UnixMilli()
	step := 0
	g.now = func() int64 {
		step++
		if step == 1 {
			return base
		}
		return base - ToleranceMillis - 1
	}
	if _, err := g.NextID(); err != nil {
		t.Fatal(err)
	}
	if _, err := g.NextID(); err == nil {
		t.Fatal("大幅时钟回拨应返回错误以触发告警")
	}
}

func TestTimeOfAndNodeOfRoundTrip(t *testing.T) {
	const node int64 = 511
	g, err := New(node)
	if err != nil {
		t.Fatal(err)
	}
	before := time.Now().Truncate(time.Millisecond)
	id, err := g.NextID()
	if err != nil {
		t.Fatal(err)
	}
	after := time.Now()

	if got := NodeOf(id); got != node {
		t.Fatalf("NodeOf = %d, 期望 %d", got, node)
	}
	ts := TimeOf(id)
	if ts.Before(before) || ts.After(after.Add(time.Millisecond)) {
		t.Fatalf("TimeOf = %v，不在 [%v, %v] 区间内", ts, before, after)
	}
}
