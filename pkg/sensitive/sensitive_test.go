package sensitive

import (
	"sync"
	"testing"
)

func newLoaded(words ...Word) *Matcher {
	m := New()
	m.Load(words)
	return m
}

func TestBasicMatch(t *testing.T) {
	m := newLoaded(
		Word{Text: "代写作业", Level: LevelBlock, Category: "学术不端"},
		Word{Text: "管制刀具", Level: LevelBlock, Category: "违禁品"},
		Word{Text: "加微信", Level: LevelReview, Category: "导流"},
	)

	res := m.Match("有偿代写作业，详情加微信")
	if !res.Matched() {
		t.Fatal("应命中")
	}
	if res.MaxLevel != LevelBlock {
		t.Fatalf("最高级别应为 Block，实际 %v", res.MaxLevel)
	}
	words := res.Words()
	if len(words) != 2 {
		t.Fatalf("应命中 2 个词，实际 %v", words)
	}
}

func TestNoMatch(t *testing.T) {
	m := newLoaded(Word{Text: "代写作业", Level: LevelBlock})
	res := m.Match("出售二手高数教材，九成新")
	if res.Matched() {
		t.Fatalf("不应命中，实际 %v", res.Words())
	}
	if res.MaxLevel != LevelWarn {
		t.Fatalf("无命中时 MaxLevel 应为零值，实际 %v", res.MaxLevel)
	}
}

// AC 自动机的核心能力：一次扫描找出所有词，包括互相重叠的。
func TestOverlappingWords(t *testing.T) {
	m := newLoaded(
		Word{Text: "代写", Level: LevelReview},
		Word{Text: "代写作业", Level: LevelBlock},
		Word{Text: "写作业", Level: LevelWarn},
	)
	res := m.Match("我要代写作业")
	if len(res.Hits) != 3 {
		t.Fatalf("三个重叠词都应命中，实际 %d: %v", len(res.Hits), res.Words())
	}
	if res.MaxLevel != LevelBlock {
		t.Fatalf("应取最高级别 Block，实际 %v", res.MaxLevel)
	}
}

// 归一化必须能对抗最常见的几种绕过手法，否则敏感词过滤形同虚设。
func TestNormalizationDefeatsEvasion(t *testing.T) {
	m := newLoaded(Word{Text: "代写作业", Level: LevelBlock})
	cases := []string{
		"代写作业",    // 原样
		"代 写 作 业", // 插入空格
		"代-写-作-业", // 插入分隔符
		"代·写、作。业", // 中文标点
		"代　写　作　业", // 全角空格
		"代写作业",    // 重复以确保稳定
	}
	for _, s := range cases {
		if !m.Match(s).Matched() {
			t.Errorf("未命中绕过写法: %q", s)
		}
	}
}

func TestNormalizationCaseAndFullWidth(t *testing.T) {
	m := newLoaded(Word{Text: "daixie", Level: LevelBlock})
	for _, s := range []string{"daixie", "DaiXie", "DAIXIE", "ｄａｉｘｉｅ", "ＤＡＩＸＩＥ"} {
		if !m.Match(s).Matched() {
			t.Errorf("未命中: %q", s)
		}
	}
}

func TestWithoutNormalize(t *testing.T) {
	m := New(WithoutNormalize())
	m.Load([]Word{{Text: "代写作业", Level: LevelBlock}})
	if m.Match("代 写 作 业").Matched() {
		t.Fatal("关闭归一化后不应命中带空格的写法")
	}
	if !m.Match("代写作业").Matched() {
		t.Fatal("精确写法应命中")
	}
}

// Hit 回传的应是词库中的原始写法，而非归一化后的形式，
// 否则审核人员在工单里看到的词与词库对不上。
func TestHitReturnsOriginalWordText(t *testing.T) {
	m := newLoaded(Word{Text: "代 写 作 业", Level: LevelBlock, Category: "学术不端"})
	res := m.Match("提供代写作业服务")
	if !res.Matched() {
		t.Fatal("应命中")
	}
	if res.Hits[0].Word.Text != "代 写 作 业" {
		t.Fatalf("应回传原始写法，实际 %q", res.Hits[0].Word.Text)
	}
	if res.Hits[0].Word.Category != "学术不端" {
		t.Fatalf("Category 应原样回传，实际 %q", res.Hits[0].Word.Category)
	}
}

func TestEmptyMatcherAndEmptyInput(t *testing.T) {
	m := New()
	if m.Match("任意文本").Matched() {
		t.Fatal("空词库不应命中任何内容")
	}
	m.Load([]Word{{Text: "违禁", Level: LevelBlock}})
	if m.Match("").Matched() {
		t.Fatal("空文本不应命中")
	}
}

// 词库中的脏数据不应阻断整个加载过程。
func TestLoadSkipsInvalidWords(t *testing.T) {
	m := New()
	m.Load([]Word{
		{Text: "", Level: LevelBlock},
		{Text: "   ", Level: LevelBlock},
		{Text: "---", Level: LevelBlock},
		{Text: "违禁品", Level: LevelBlock},
	})
	if !m.Match("出售违禁品").Matched() {
		t.Fatal("有效词应正常生效")
	}
	// 纯分隔符的词被归一化为空后跳过，不应把任意文本都判为命中。
	if m.Match("正常的商品描述").Matched() {
		t.Fatal("脏数据被错误地加载成了通配词")
	}
}

// Load 整体替换词库，旧词应立即失效。
func TestLoadReplacesWordList(t *testing.T) {
	m := newLoaded(Word{Text: "旧词", Level: LevelBlock})
	if !m.Match("含旧词的文本").Matched() {
		t.Fatal("旧词应命中")
	}
	m.Load([]Word{{Text: "新词", Level: LevelBlock}})
	if m.Match("含旧词的文本").Matched() {
		t.Fatal("Load 后旧词应失效")
	}
	if !m.Match("含新词的文本").Matched() {
		t.Fatal("新词应生效")
	}
}

// 匹配器需支持并发读，因为它会被所有请求共享。
func TestConcurrentMatchAndReload(t *testing.T) {
	m := newLoaded(
		Word{Text: "违禁品", Level: LevelBlock},
		Word{Text: "代写", Level: LevelReview},
	)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				m.Match("出售违禁品并提供代写服务")
			}
		}()
	}
	// 并发读的同时热更新词库。
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < 20; j++ {
			m.Load([]Word{{Text: "违禁品", Level: LevelBlock}})
		}
	}()
	wg.Wait()
}

func TestMatchLevel(t *testing.T) {
	m := newLoaded(
		Word{Text: "加微信", Level: LevelReview},
		Word{Text: "代考", Level: LevelBlock},
	)
	if got := m.MatchLevel("详情加微信"); got != LevelReview {
		t.Fatalf("应为 Review，实际 %v", got)
	}
	if got := m.MatchLevel("提供代考服务"); got != LevelBlock {
		t.Fatalf("应为 Block，实际 %v", got)
	}
	if got := m.MatchLevel("九成新教材"); got != LevelWarn {
		t.Fatalf("无命中应为零值，实际 %v", got)
	}
}

func TestWordsDeduplicates(t *testing.T) {
	m := newLoaded(Word{Text: "代写", Level: LevelBlock})
	res := m.Match("代写、代写、代写")
	if len(res.Hits) != 3 {
		t.Fatalf("应有 3 次命中，实际 %d", len(res.Hits))
	}
	if got := res.Words(); len(got) != 1 {
		t.Fatalf("Words 应去重为 1 个，实际 %v", got)
	}
}

func BenchmarkMatch(b *testing.B) {
	words := make([]Word, 0, 5000)
	for i := 0; i < 5000; i++ {
		words = append(words, Word{Text: "违禁词" + string(rune('a'+i%26)) + string(rune('0'+i%10)), Level: LevelBlock})
	}
	words = append(words, Word{Text: "代写作业", Level: LevelBlock})
	m := New()
	m.Load(words)

	text := "出售二手高数教材九成新，可小刀，另提供代写作业服务，详情私聊，校内可面交，位置在图书馆东门"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.Match(text)
	}
}
