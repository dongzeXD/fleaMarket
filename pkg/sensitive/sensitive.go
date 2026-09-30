// Package sensitive 提供敏感词匹配。
//
// 采用 Aho-Corasick 自动机：一次扫描即可找出文本中所有命中的词，
// 时间复杂度 O(n + m)，与词库大小无关。相比逐词 strings.Contains
// （O(n × 词库大小)），在词库上万条时差距是数量级的。
//
// 泛用性约束（doc.md §5.1）：本包不持有任何词库内容。
// 违禁品、学术不端等具体词表由 review-service 从数据库加载后注入，
// 公共库只提供匹配算法。
package sensitive

import (
	"strings"
	"sync"
	"unicode"
)

// Level 命中后的处置级别。
//
// 级别本身是通用概念（拦截/复审/仅告警），任何内容安全场景都适用，
// 因此放在公共库；而「哪个词属于哪个级别」是业务数据，不在此处。
type Level int

const (
	// LevelWarn 仅记录，不干预。
	LevelWarn Level = iota
	// LevelReview 转人工复审。
	LevelReview
	// LevelBlock 直接拦截。
	LevelBlock
)

// Word 一个待匹配的词及其处置级别与分类。
type Word struct {
	Text string
	// Level 处置级别。
	Level Level
	// Category 业务自定义的分类标识，原样回传，公共库不解释其含义。
	Category string
}

// Hit 一次命中。
type Hit struct {
	// Word 命中的词。
	Word Word
	// Start 在归一化后文本中的起始字符下标。
	Start int
	// End 结束下标（不含）。
	End int
}

// Result 匹配结果。
type Result struct {
	// Hits 全部命中，按起始位置升序。
	Hits []Hit
	// MaxLevel 所有命中中的最高级别，调用方据此决定放行/复审/拦截。
	MaxLevel Level
}

// Matched 是否有任何命中。
func (r Result) Matched() bool { return len(r.Hits) > 0 }

// Words 返回命中的词文本，去重后按出现顺序排列。
// 用于在审核工单中展示「命中了哪些词」。
func (r Result) Words() []string {
	seen := make(map[string]struct{}, len(r.Hits))
	out := make([]string, 0, len(r.Hits))
	for _, h := range r.Hits {
		if _, dup := seen[h.Word.Text]; dup {
			continue
		}
		seen[h.Word.Text] = struct{}{}
		out = append(out, h.Word.Text)
	}
	return out
}

// node AC 自动机的一个节点。
type node struct {
	children map[rune]*node
	fail     *node
	// words 以该节点为终点的词。同一位置可能结束多个词（如 "代写" 与 "代写作业"）。
	words []Word
	depth int
}

func newNode(depth int) *node {
	return &node{children: make(map[rune]*node), depth: depth}
}

// Matcher 敏感词匹配器。
//
// 构建后只读，可被多个 goroutine 并发使用。
// 词库更新通过 Load 整体替换（写锁），而非增量修改单个节点——
// 增量修改 AC 自动机的 fail 指针极易出错，整体重建更可靠，
// 且词库更新是低频操作（doc.md §13.5 的 Pub/Sub 热更新）。
type Matcher struct {
	mu   sync.RWMutex
	root *node
	// normalize 是否做归一化（大小写、全角半角、忽略分隔符）。
	normalize bool
}

// Option 配置项。
type Option func(*Matcher)

// WithoutNormalize 关闭归一化。
//
// 默认开启归一化是因为绕过手法极其常见：「代 写 作 业」「代-写-作业」
// 「ＤＡＩＸＩＥ」都需要能命中。关闭仅用于需要精确匹配的场景。
func WithoutNormalize() Option {
	return func(m *Matcher) { m.normalize = false }
}

// New 创建匹配器。
func New(opts ...Option) *Matcher {
	m := &Matcher{root: newNode(0), normalize: true}
	for _, o := range opts {
		o(m)
	}
	return m
}

// Load 用给定词库整体替换当前词库，并重建自动机。
//
// 空词、纯分隔符的词会被跳过而不报错：词库通常来自数据库，
// 让一条脏数据阻断整个词库加载是不合适的。
func (m *Matcher) Load(words []Word) {
	root := newNode(0)

	for _, w := range words {
		text := w.Text
		if m.normalize {
			text = normalizeText(text)
		}
		if text == "" {
			continue
		}
		cur := root
		for _, r := range text {
			next, ok := cur.children[r]
			if !ok {
				next = newNode(cur.depth + 1)
				cur.children[r] = next
			}
			cur = next
		}
		stored := w
		stored.Text = w.Text // 回传原始写法，便于审核人员理解
		cur.words = append(cur.words, stored)
	}

	buildFailLinks(root)

	m.mu.Lock()
	m.root = root
	m.mu.Unlock()
}

// buildFailLinks 用 BFS 构建失配指针。
//
// 失配指针的含义：当前节点匹配失败时，应跳转到「当前已匹配后缀」所对应的
// 最长前缀节点，从而避免回退重扫。这是 AC 自动机线性复杂度的来源。
func buildFailLinks(root *node) {
	root.fail = root
	queue := make([]*node, 0, len(root.children))
	for _, child := range root.children {
		child.fail = root
		queue = append(queue, child)
	}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for r, child := range cur.children {
			// 沿失配链找到能接上字符 r 的最长后缀。
			f := cur.fail
			for {
				if next, ok := f.children[r]; ok && next != child {
					child.fail = next
					break
				}
				if f == root {
					child.fail = root
					break
				}
				f = f.fail
			}
			queue = append(queue, child)
		}
	}
}

// Match 扫描文本，返回全部命中。
func (m *Matcher) Match(text string) Result {
	m.mu.RLock()
	root := m.root
	normalize := m.normalize
	m.mu.RUnlock()

	if root == nil || len(root.children) == 0 || text == "" {
		return Result{}
	}

	var runes []rune
	if normalize {
		runes = []rune(normalizeText(text))
	} else {
		runes = []rune(text)
	}

	res := Result{}
	cur := root
	for i, r := range runes {
		// 沿失配链回退直到能接上当前字符。
		for {
			if next, ok := cur.children[r]; ok {
				cur = next
				break
			}
			if cur == root {
				break
			}
			cur = cur.fail
		}
		// 收集当前节点及其失配链上的所有词
		// （失配链上的节点代表当前位置结束的更短后缀词）。
		for n := cur; n != root; n = n.fail {
			for _, w := range n.words {
				res.Hits = append(res.Hits, Hit{
					Word:  w,
					Start: i - n.depth + 1,
					End:   i + 1,
				})
				if w.Level > res.MaxLevel {
					res.MaxLevel = w.Level
				}
			}
			if n.fail == nil {
				break
			}
		}
	}
	return res
}

// MatchLevel 只关心最高处置级别时的快捷方法。
func (m *Matcher) MatchLevel(text string) Level {
	return m.Match(text).MaxLevel
}

// normalizeText 归一化文本以对抗常见绕过手法：
//   - 统一转小写（ＤＡＩＸＩＥ / DaiXie → daixie）
//   - 全角转半角
//   - 剔除空白与常见分隔符（「代 写-作.业」→「代写作业」）
//
// 副作用：位置下标是归一化后的，与原文不一致。因此 Hit 的 Start/End
// 只适合用于判定与展示命中词，不适合用于在原文上做精确替换。
func normalizeText(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		r = toHalfWidth(r)
		if unicode.IsSpace(r) || isSeparator(r) {
			continue
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

func toHalfWidth(r rune) rune {
	switch {
	case r == 0x3000: // 全角空格
		return ' '
	case r >= 0xFF01 && r <= 0xFF5E: // 全角 ASCII
		return r - 0xFEE0
	default:
		return r
	}
}

func isSeparator(r rune) bool {
	switch r {
	case '-', '_', '.', ',', '|', '/', '\\', '*', '+', '~', '^',
		'·', '、', '，', '。', '　':
		return true
	default:
		return false
	}
}
