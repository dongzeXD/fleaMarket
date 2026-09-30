package biz

import (
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
)

// 同校优先的配额分配（doc.md §9.3）。
//
// 三路查询按固定比例交错填充，而非依赖搜索引擎的 boost 评分：
//   - 配额是确定性的，「同校区商品应占六成」这件事可被验证；
//   - 评分式 boost 会随关键词相关度波动，同一位置的构成不可预测，
//     用户会感觉「有时候全是本校的，有时候一条都没有」。
const (
	// laneSameCampus 同校区。
	laneSameCampus = 0
	// laneSameSchool 同校但不同校区。
	laneSameSchool = 1
	// laneGlobal 全局（其他学校）。
	laneGlobal = 2
	laneCount  = 3
)

// quotaRatio 三路的填充比例 6:3:1。
//
// 比例的依据是交易可达性：同校区步行可达，同校跨校区需骑车，
// 跨校则基本无法面交。给跨校保留一成是为了让小校区的用户不至于无内容可看。
var quotaRatio = [laneCount]int{6, 3, 1}

// allocateQuota 按比例把 limit 拆到三路。
//
// 余数补给第一路：宁可多给同校区，这是产品意图所在。
func allocateQuota(limit int) [laneCount]int {
	var q [laneCount]int
	total := quotaRatio[0] + quotaRatio[1] + quotaRatio[2]
	assigned := 0
	for i := 0; i < laneCount; i++ {
		q[i] = limit * quotaRatio[i] / total
		assigned += q[i]
	}
	q[0] += limit - assigned
	return q
}

// laneCursor 三路各自的偏移量。
//
// 每路独立维护偏移：它们是三次独立的引擎查询，共用一个偏移会错位。
type laneCursor struct {
	offsets [laneCount]int
}

func (c laneCursor) isZero() bool {
	return c.offsets[0] == 0 && c.offsets[1] == 0 && c.offsets[2] == 0
}

// encode 编成不透明字符串。
//
// base64 包装使游标对客户端不透明，保留服务端后续调整分路策略的自由——
// 若暴露成 "0:0:0" 这类可读格式，客户端可能会去解析甚至构造它。
func (c laneCursor) encode() string {
	if c.isZero() {
		return ""
	}
	raw := strconv.Itoa(c.offsets[0]) + ":" +
		strconv.Itoa(c.offsets[1]) + ":" +
		strconv.Itoa(c.offsets[2])
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// errInvalidCursor 游标格式非法。
var errInvalidCursor = errors.New("search: invalid cursor")

func decodeLaneCursor(s string) (laneCursor, error) {
	var c laneCursor
	if s == "" {
		return c, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return c, errInvalidCursor
	}
	parts := strings.Split(string(b), ":")
	if len(parts) != laneCount {
		return c, errInvalidCursor
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return c, errInvalidCursor
		}
		c.offsets[i] = n
	}
	return c, nil
}

// mergeLanes 按配额交错三路结果，并在某路不足时向后回填。
//
// 回填是必需的：若同校区只有 2 条商品而配额是 6，不回填的话
// 这一页只会返回 2+3+1=6 条而非 limit 条，用户会看到一个莫名的短页，
// 并误以为「没有更多了」。回填让页面始终填满，代价是比例在数据稀疏时偏离。
//
// 返回消耗掉的各路条数，供调用方推进游标。
func mergeLanes(lanes [laneCount][]*ProductBrief, limit int) ([]*ProductBrief, [laneCount]int) {
	quota := allocateQuota(limit)
	var consumed [laneCount]int
	out := make([]*ProductBrief, 0, limit)

	// 第一轮：各路按配额取。
	for lane := 0; lane < laneCount; lane++ {
		take := quota[lane]
		if take > len(lanes[lane]) {
			take = len(lanes[lane])
		}
		out = append(out, lanes[lane][:take]...)
		consumed[lane] = take
	}

	// 第二轮：未填满时按 同校区 → 同校 → 全局 的优先级回填。
	for lane := 0; lane < laneCount && len(out) < limit; lane++ {
		remaining := lanes[lane][consumed[lane]:]
		need := limit - len(out)
		if need > len(remaining) {
			need = len(remaining)
		}
		out = append(out, remaining[:need]...)
		consumed[lane] += need
	}

	return out, consumed
}

// dedupe 按商品 ID 去重，保留首次出现的顺序。
//
// 三路查询的过滤条件互斥（同校区 / 同校异校区 / 其他学校），
// 理论上不会重叠。但索引的 campus_id 与权威数据可能短暂不一致，
// 此时同一商品可能同时命中两路——重复卡片是用户能直接看见的缺陷，
// 因此加一道去重兜底。
func dedupe(items []*ProductBrief) []*ProductBrief {
	seen := make(map[int64]struct{}, len(items))
	out := make([]*ProductBrief, 0, len(items))
	for _, it := range items {
		if it == nil {
			continue
		}
		if _, dup := seen[it.ProductID]; dup {
			continue
		}
		seen[it.ProductID] = struct{}{}
		out = append(out, it)
	}
	return out
}
