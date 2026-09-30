package orm

import (
	"fmt"
	"sort"
	"sync"
)

// Entity 是所有可建表对象必须实现的接口。
//
// 泛用性体现在这个接口极小：只要求实体能说出自己的表名，
// 对字段、主键、索引不做任何约束（那些由 GORM tag 表达）。
// 因此任何项目的任何表都能接入本机制，公共库无需了解业务。
type Entity interface {
	TableName() string
}

// Registry 实体注册中心，按分组（通常是服务名）组织。
//
// 分组存在的意义：dbctl 需要支持 `--group product` 只迁移某个服务的表，
// 以便在灰度发布时逐服务推进 schema 变更，而不是一次动全库。
//
// 并发安全：注册通常发生在 init() 阶段（单线程），但 dbctl 与服务进程
// 都会读取，故仍加读写锁。
type Registry struct {
	mu       sync.RWMutex
	entities map[string][]Entity
	// seen 用于拒绝同一分组内重复的表名。
	seen map[string]map[string]struct{}
}

// NewRegistry 创建空注册中心。
func NewRegistry() *Registry {
	return &Registry{
		entities: make(map[string][]Entity),
		seen:     make(map[string]map[string]struct{}),
	}
}

// Register 把实体登记到指定分组。
//
// 重复的表名会 panic 而非静默忽略：表名冲突意味着两个实体会互相覆盖对方的 schema，
// 这类错误若延迟到运行时才暴露，代价远高于启动即失败。
// 注册发生在 init() 阶段，panic 能在进程启动的第一时间暴露问题。
func (r *Registry) Register(group string, es ...Entity) {
	if group == "" {
		panic("orm: register with empty group")
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.seen[group] == nil {
		r.seen[group] = make(map[string]struct{})
	}
	for _, e := range es {
		if e == nil {
			panic(fmt.Sprintf("orm: register nil entity in group %q", group))
		}
		name := e.TableName()
		if name == "" {
			panic(fmt.Sprintf("orm: entity %T in group %q has empty TableName()", e, group))
		}
		if _, dup := r.seen[group][name]; dup {
			panic(fmt.Sprintf("orm: duplicate table %q in group %q", name, group))
		}
		r.seen[group][name] = struct{}{}
		r.entities[group] = append(r.entities[group], e)
	}
}

// Groups 返回全部分组名，已排序以保证迁移顺序可复现。
func (r *Registry) Groups() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]string, 0, len(r.entities))
	for g := range r.entities {
		out = append(out, g)
	}
	sort.Strings(out)
	return out
}

// Entities 返回指定分组的实体，保持注册顺序。
//
// 保持注册顺序而非排序，是为了让调用方能通过声明顺序控制建表次序
// （存在外键时被引用表需先建）。
func (r *Registry) Entities(group string) []Entity {
	r.mu.RLock()
	defer r.mu.RUnlock()

	src := r.entities[group]
	out := make([]Entity, len(src))
	copy(out, src)
	return out
}

// All 返回全部分组的实体，按分组名排序后依次拼接。
func (r *Registry) All() []Entity {
	var out []Entity
	for _, g := range r.Groups() {
		out = append(out, r.Entities(g)...)
	}
	return out
}

// Select 按分组名筛选实体。groups 为空时返回全部。
// 未知分组会返回错误而非静默跳过，避免 `--group produc` 这类拼写错误
// 表现为「迁移成功但什么都没做」。
func (r *Registry) Select(groups ...string) ([]Entity, error) {
	if len(groups) == 0 {
		return r.All(), nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()

	var out []Entity
	for _, g := range groups {
		es, ok := r.entities[g]
		if !ok {
			return nil, fmt.Errorf("orm: unknown group %q (available: %v)", g, r.groupsLocked())
		}
		out = append(out, es...)
	}
	return out, nil
}

func (r *Registry) groupsLocked() []string {
	out := make([]string, 0, len(r.entities))
	for g := range r.entities {
		out = append(out, g)
	}
	sort.Strings(out)
	return out
}

// TableNames 返回指定分组的表名清单，便于 dbctl 打印将要操作的对象。
func (r *Registry) TableNames(groups ...string) ([]string, error) {
	es, err := r.Select(groups...)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(es))
	for _, e := range es {
		out = append(out, e.TableName())
	}
	return out, nil
}

// Default 是进程级默认注册中心。
//
// 各服务的 entity 包在 init() 中调用包级 Register 写入此处，
// dbctl 通过 blank import 触发这些 init() 后即可读到全部实体。
var Default = NewRegistry()

// Register 向默认注册中心登记实体。
func Register(group string, es ...Entity) { Default.Register(group, es...) }
