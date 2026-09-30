// Package statemachine 提供泛型状态机。
//
// 商品状态（在售/已预订/已售出/下架）与订单状态共用本实现。
// 之所以抽到公共库：状态流转的「合法性校验」是纯粹的图可达性问题，
// 与具体是商品还是订单无关；而具体有哪些状态、允许哪些流转，
// 由各服务在自己的 internal/biz 中声明，公共库不持有任何业务状态常量。
package statemachine

import (
	"fmt"
	"sort"
)

// ErrIllegalTransition 非法流转。
type ErrIllegalTransition struct {
	From  string
	Event string
}

func (e *ErrIllegalTransition) Error() string {
	return fmt.Sprintf("statemachine: illegal transition: state=%s event=%s", e.From, e.Event)
}

// Machine 状态机。S 为状态类型，E 为事件类型，二者需可比较。
//
// 构造完成后应只读使用；Allow 仅在初始化阶段调用，因此不加锁。
type Machine[S comparable, E comparable] struct {
	transitions map[S]map[E]S
}

// New 创建空状态机。
func New[S comparable, E comparable]() *Machine[S, E] {
	return &Machine[S, E]{transitions: make(map[S]map[E]S)}
}

// Allow 声明一条流转规则：处于 from 状态时，发生 event 事件则转入 to 状态。
// 返回自身以支持链式声明。重复声明同一 (from, event) 会覆盖前者。
func (m *Machine[S, E]) Allow(from S, event E, to S) *Machine[S, E] {
	if m.transitions[from] == nil {
		m.transitions[from] = make(map[E]S)
	}
	m.transitions[from][event] = to
	return m
}

// Next 计算流转后的状态。非法流转返回 *ErrIllegalTransition。
func (m *Machine[S, E]) Next(from S, event E) (S, error) {
	var zero S
	events, ok := m.transitions[from]
	if !ok {
		return zero, &ErrIllegalTransition{From: fmt.Sprint(from), Event: fmt.Sprint(event)}
	}
	to, ok := events[event]
	if !ok {
		return zero, &ErrIllegalTransition{From: fmt.Sprint(from), Event: fmt.Sprint(event)}
	}
	return to, nil
}

// Can 判断流转是否合法。用于在返回给前端的数据中标注「哪些操作按钮可用」，
// 避免前端把状态规则复制一遍。
func (m *Machine[S, E]) Can(from S, event E) bool {
	_, err := m.Next(from, event)
	return err == nil
}

// AllowedEvents 返回某状态下所有合法事件，结果按字符串表示排序以保证稳定。
func (m *Machine[S, E]) AllowedEvents(from S) []E {
	events := m.transitions[from]
	out := make([]E, 0, len(events))
	for e := range events {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		return fmt.Sprint(out[i]) < fmt.Sprint(out[j])
	})
	return out
}

// IsTerminal 判断是否为终态（无任何出边），如商品的「已售出」。
func (m *Machine[S, E]) IsTerminal(s S) bool {
	return len(m.transitions[s]) == 0
}
