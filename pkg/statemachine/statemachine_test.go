package statemachine

import (
	"errors"
	"reflect"
	"testing"
)

// 用商品状态流转作为被测用例——注意这些常量定义在测试文件里，
// 而非公共库中，正是 doc.md §5.1「公共库零业务语义」的体现。
type productStatus int

const (
	pendingReview productStatus = iota
	onSale
	reserved
	sold
	offline
	rejected
)

type productEvent int

const (
	pass productEvent = iota
	reject
	reserve
	cancel
	complete
	takeOffline
	republish
)

func productMachine() *Machine[productStatus, productEvent] {
	return New[productStatus, productEvent]().
		Allow(pendingReview, pass, onSale).
		Allow(pendingReview, reject, rejected).
		Allow(onSale, reserve, reserved).
		Allow(onSale, takeOffline, offline).
		Allow(reserved, cancel, onSale).
		Allow(reserved, complete, sold).
		Allow(offline, republish, pendingReview)
}

func TestLegalTransitions(t *testing.T) {
	m := productMachine()
	cases := []struct {
		from  productStatus
		event productEvent
		want  productStatus
	}{
		{pendingReview, pass, onSale},
		{pendingReview, reject, rejected},
		{onSale, reserve, reserved},
		{reserved, complete, sold},
		{reserved, cancel, onSale},
		{offline, republish, pendingReview},
	}
	for _, c := range cases {
		got, err := m.Next(c.from, c.event)
		if err != nil {
			t.Fatalf("Next(%v, %v) 意外报错: %v", c.from, c.event, err)
		}
		if got != c.want {
			t.Fatalf("Next(%v, %v) = %v, want %v", c.from, c.event, got, c.want)
		}
	}
}

func TestIllegalTransitionReturnsTypedError(t *testing.T) {
	m := productMachine()
	// 已售出是终态，不允许任何流转。
	_, err := m.Next(sold, reserve)
	var ie *ErrIllegalTransition
	if !errors.As(err, &ie) {
		t.Fatalf("应返回 *ErrIllegalTransition，实际 %T: %v", err, err)
	}
	// 已在售的商品不能再次「审核通过」。
	if _, err := m.Next(onSale, pass); err == nil {
		t.Fatal("onSale + pass 应为非法流转")
	}
	// 未声明过的状态。
	if _, err := m.Next(productStatus(99), pass); err == nil {
		t.Fatal("未声明状态应为非法流转")
	}
}

func TestCan(t *testing.T) {
	m := productMachine()
	if !m.Can(onSale, reserve) {
		t.Fatal("在售商品应可被预订")
	}
	if m.Can(sold, reserve) {
		t.Fatal("已售出商品不应可被预订")
	}
}

func TestAllowedEventsIsStable(t *testing.T) {
	m := productMachine()
	first := m.AllowedEvents(reserved)
	if len(first) != 2 {
		t.Fatalf("reserved 状态应有 2 个合法事件，实际 %d", len(first))
	}
	// 多次调用结果必须一致，否则前端按钮顺序会抖动。
	for i := 0; i < 20; i++ {
		if got := m.AllowedEvents(reserved); !reflect.DeepEqual(got, first) {
			t.Fatalf("AllowedEvents 结果不稳定: %v vs %v", got, first)
		}
	}
	if got := m.AllowedEvents(sold); len(got) != 0 {
		t.Fatalf("终态应无合法事件，实际 %v", got)
	}
}

func TestIsTerminal(t *testing.T) {
	m := productMachine()
	if !m.IsTerminal(sold) {
		t.Fatal("sold 应为终态")
	}
	if !m.IsTerminal(rejected) {
		t.Fatal("rejected 未声明出边，应为终态")
	}
	if m.IsTerminal(onSale) {
		t.Fatal("onSale 不应为终态")
	}
}

func TestAllowOverridesDuplicate(t *testing.T) {
	m := New[productStatus, productEvent]().
		Allow(onSale, reserve, reserved).
		Allow(onSale, reserve, sold) // 后声明覆盖前者
	got, err := m.Next(onSale, reserve)
	if err != nil {
		t.Fatal(err)
	}
	if got != sold {
		t.Fatalf("重复声明应以最后一次为准，got %v", got)
	}
}

// 验证泛型对字符串类型状态同样可用（订单服务可能用字符串枚举）。
func TestWorksWithStringStates(t *testing.T) {
	m := New[string, string]().
		Allow("CREATED", "confirm", "CONFIRMED").
		Allow("CONFIRMED", "finish", "DONE")
	got, err := m.Next("CREATED", "confirm")
	if err != nil || got != "CONFIRMED" {
		t.Fatalf("got %q err %v", got, err)
	}
	if _, err := m.Next("DONE", "finish"); err == nil {
		t.Fatal("DONE 为终态，应非法")
	}
}
