package biz

import (
	"encoding/base64"
	"testing"
)

// 配额与交错逻辑是「同校优先」的核心，且完全不依赖外部组件，
// 因此用纯单测覆盖其全部分支。

func TestAllocateQuota(t *testing.T) {
	cases := map[int][laneCount]int{
		// 20 条：6:3:1 → 12/6/2
		20: {12, 6, 2},
		// 10 条：整除
		10: {6, 3, 1},
		// 余数补给同校区（产品意图所在）
		7: {5, 2, 0},
		// limit 很小时整数除法让后两路取整为 0，余数全给同校区。
		// 这是合理的：只有 1~3 个位置时，优先展示同校区最符合产品意图。
		1: {1, 0, 0},
		3: {3, 0, 0},
	}
	for limit, want := range cases {
		got := allocateQuota(limit)
		if got != want {
			t.Errorf("allocateQuota(%d) = %v, want %v", limit, got, want)
		}
		sum := got[0] + got[1] + got[2]
		if sum != limit {
			t.Errorf("allocateQuota(%d) 总和为 %d，应等于 limit", limit, sum)
		}
	}
}

func briefs(ids ...int64) []*ProductBrief {
	out := make([]*ProductBrief, 0, len(ids))
	for _, id := range ids {
		out = append(out, &ProductBrief{ProductID: id})
	}
	return out
}

func idsOf(items []*ProductBrief) []int64 {
	out := make([]int64, 0, len(items))
	for _, it := range items {
		out = append(out, it.ProductID)
	}
	return out
}

// 三路都充足时，严格按 6:3:1 取。
func TestMergeLanesRespectsQuotaWhenAbundant(t *testing.T) {
	var lanes [laneCount][]*ProductBrief
	lanes[laneSameCampus] = briefs(1, 2, 3, 4, 5, 6, 7, 8)
	lanes[laneSameSchool] = briefs(101, 102, 103, 104)
	lanes[laneGlobal] = briefs(201, 202, 203)

	got, consumed := mergeLanes(lanes, 10)
	if len(got) != 10 {
		t.Fatalf("应返回 10 条，实际 %d", len(got))
	}
	if consumed != [laneCount]int{6, 3, 1} {
		t.Fatalf("消耗数应为 6/3/1，实际 %v", consumed)
	}
	// 顺序：同校区在前，其次同校，最后全局。
	want := []int64{1, 2, 3, 4, 5, 6, 101, 102, 103, 201}
	for i, id := range want {
		if got[i].ProductID != id {
			t.Fatalf("第 %d 位应为 %d，实际 %d（完整：%v）", i, id, got[i].ProductID, idsOf(got))
		}
	}
}

// 同校区数据不足时必须向后回填，否则页面会莫名变短，
// 用户会误以为「没有更多了」。
func TestMergeLanesBackfillsWhenLaneShort(t *testing.T) {
	var lanes [laneCount][]*ProductBrief
	lanes[laneSameCampus] = briefs(1, 2) // 只有 2 条，配额 6
	lanes[laneSameSchool] = briefs(101, 102, 103, 104, 105)
	lanes[laneGlobal] = briefs(201, 202, 203, 204)

	got, consumed := mergeLanes(lanes, 10)
	if len(got) != 10 {
		t.Fatalf("应回填至 10 条，实际 %d: %v", len(got), idsOf(got))
	}
	if consumed[laneSameCampus] != 2 {
		t.Fatalf("同校区只有 2 条可用，实际消耗 %d", consumed[laneSameCampus])
	}
	// 回填优先从同校取，其次全局。
	total := consumed[0] + consumed[1] + consumed[2]
	if total != 10 {
		t.Fatalf("消耗总数应为 10，实际 %d（%v）", total, consumed)
	}
}

// 全部数据不足时返回现有全部，不报错也不补空位。
func TestMergeLanesAllShort(t *testing.T) {
	var lanes [laneCount][]*ProductBrief
	lanes[laneSameCampus] = briefs(1)
	lanes[laneSameSchool] = briefs(101)
	lanes[laneGlobal] = nil

	got, consumed := mergeLanes(lanes, 10)
	if len(got) != 2 {
		t.Fatalf("应返回 2 条，实际 %d", len(got))
	}
	if consumed != [laneCount]int{1, 1, 0} {
		t.Fatalf("消耗数错误: %v", consumed)
	}
}

func TestMergeLanesEmpty(t *testing.T) {
	var lanes [laneCount][]*ProductBrief
	got, consumed := mergeLanes(lanes, 20)
	if len(got) != 0 {
		t.Fatalf("空输入应返回空，实际 %d 条", len(got))
	}
	if consumed != [laneCount]int{0, 0, 0} {
		t.Fatalf("消耗数应全为 0，实际 %v", consumed)
	}
}

// 同一商品在两路重复出现时必须去重——重复卡片是用户能直接看见的缺陷。
func TestDedupeRemovesDuplicatesKeepingOrder(t *testing.T) {
	items := briefs(1, 2, 1, 3, 2, 4)
	got := dedupe(items)
	want := []int64{1, 2, 3, 4}
	if len(got) != len(want) {
		t.Fatalf("去重后应有 %d 条，实际 %d: %v", len(want), len(got), idsOf(got))
	}
	for i, id := range want {
		if got[i].ProductID != id {
			t.Fatalf("第 %d 位应为 %d，实际 %d", i, id, got[i].ProductID)
		}
	}
}

func TestDedupeSkipsNil(t *testing.T) {
	items := []*ProductBrief{{ProductID: 1}, nil, {ProductID: 2}}
	got := dedupe(items)
	if len(got) != 2 {
		t.Fatalf("应跳过 nil，实际 %d 条", len(got))
	}
}

// --- 游标 ---

func TestLaneCursorRoundTrip(t *testing.T) {
	cases := []laneCursor{
		{offsets: [laneCount]int{12, 6, 2}},
		{offsets: [laneCount]int{0, 0, 1}},
		{offsets: [laneCount]int{1000, 500, 100}},
	}
	for _, want := range cases {
		got, err := decodeLaneCursor(want.encode())
		if err != nil {
			t.Fatalf("decode(%v) 报错: %v", want, err)
		}
		if got != want {
			t.Fatalf("往返不一致: got %v want %v", got, want)
		}
	}
}

func TestLaneCursorZeroMeansFirstPage(t *testing.T) {
	var zero laneCursor
	if zero.encode() != "" {
		t.Fatalf("零值游标应编码为空串，实际 %q", zero.encode())
	}
	got, err := decodeLaneCursor("")
	if err != nil {
		t.Fatalf("空游标不应报错: %v", err)
	}
	if !got.isZero() {
		t.Fatal("空游标应解析为零值")
	}
}

func TestDecodeLaneCursorRejectsMalformed(t *testing.T) {
	bad := []string{
		"not-base64!!!",
		encodeRaw("1:2"),     // 段数不足
		encodeRaw("1:2:3:4"), // 段数过多
		encodeRaw("a:b:c"),   // 非数字
		encodeRaw("-1:0:0"),  // 负偏移
	}
	for _, s := range bad {
		if _, err := decodeLaneCursor(s); err == nil {
			t.Errorf("%q 应被拒绝", s)
		}
	}
}

// 游标必须对客户端不透明，避免它去解析或构造。
func TestLaneCursorIsOpaque(t *testing.T) {
	c := laneCursor{offsets: [laneCount]int{12, 6, 2}}
	encoded := c.encode()
	if encoded == "12:6:2" {
		t.Fatal("游标不应是可读格式")
	}
	if encoded == "" {
		t.Fatal("非零游标不应为空")
	}
}

// --- 索引状态判定 ---

// 只索引对外可见的状态：待审核与驳回的商品不该出现在搜索结果里。
func TestShouldIndex(t *testing.T) {
	cases := map[uint8]bool{
		statusPendingReview: false,
		statusOnSale:        true,
		statusReserved:      true,
		statusSold:          true,
		statusOffline:       false,
		statusRejected:      false,
	}
	for status, want := range cases {
		if got := shouldIndex(status); got != want {
			t.Errorf("shouldIndex(%d) = %v, want %v", status, got, want)
		}
	}
}

// encodeRaw 构造非法游标用于测试解析健壮性。
func encodeRaw(s string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(s))
}
