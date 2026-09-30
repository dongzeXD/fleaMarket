package orm

import (
	"testing"
)

type fakeA struct{}

func (fakeA) TableName() string { return "t_fake_a" }

type fakeB struct{}

func (fakeB) TableName() string { return "t_fake_b" }

type fakeC struct{}

func (fakeC) TableName() string { return "t_fake_c" }

type emptyName struct{}

func (emptyName) TableName() string { return "" }

func TestRegisterAndRead(t *testing.T) {
	r := NewRegistry()
	r.Register("alpha", fakeA{}, fakeB{})
	r.Register("beta", fakeC{})

	if got := r.Groups(); len(got) != 2 || got[0] != "alpha" || got[1] != "beta" {
		t.Fatalf("Groups 应排序返回 [alpha beta]，实际 %v", got)
	}
	if got := r.Entities("alpha"); len(got) != 2 {
		t.Fatalf("alpha 应有 2 个实体，实际 %d", len(got))
	}
	if got := r.Entities("unknown"); len(got) != 0 {
		t.Fatalf("未知分组应返回空，实际 %d", len(got))
	}
	if got := r.All(); len(got) != 3 {
		t.Fatalf("All 应返回 3 个实体，实际 %d", len(got))
	}
}

// 注册顺序必须保留：存在外键时调用方要靠声明顺序控制建表次序。
func TestEntitiesPreservesRegistrationOrder(t *testing.T) {
	r := NewRegistry()
	r.Register("g", fakeC{}, fakeA{}, fakeB{})
	got := r.Entities("g")
	want := []string{"t_fake_c", "t_fake_a", "t_fake_b"}
	for i, w := range want {
		if got[i].TableName() != w {
			t.Fatalf("第 %d 个实体应为 %s，实际 %s", i, w, got[i].TableName())
		}
	}
}

// 返回切片的修改不得影响注册中心内部状态。
func TestEntitiesReturnsCopy(t *testing.T) {
	r := NewRegistry()
	r.Register("g", fakeA{}, fakeB{})
	got := r.Entities("g")
	got[0] = fakeC{}
	if r.Entities("g")[0].TableName() != "t_fake_a" {
		t.Fatal("外部修改污染了注册中心内部状态")
	}
}

func TestRegisterDuplicateTablePanics(t *testing.T) {
	r := NewRegistry()
	r.Register("g", fakeA{})
	defer func() {
		if recover() == nil {
			t.Fatal("同分组重复表名应 panic，以便在启动时立刻暴露")
		}
	}()
	r.Register("g", fakeA{})
}

// 不同分组允许同名表（不同服务可以各自有 t_config 之类的表）。
func TestSameTableInDifferentGroupsIsAllowed(t *testing.T) {
	r := NewRegistry()
	r.Register("alpha", fakeA{})
	r.Register("beta", fakeA{}) // 不应 panic
	if len(r.All()) != 2 {
		t.Fatal("跨分组同名表应各自保留")
	}
}

func TestRegisterRejectsInvalidInput(t *testing.T) {
	cases := map[string]func(){
		"空分组名":        func() { NewRegistry().Register("", fakeA{}) },
		"nil 实体":      func() { NewRegistry().Register("g", nil) },
		"空 TableName": func() { NewRegistry().Register("g", emptyName{}) },
	}
	for name, fn := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatalf("%s 应 panic", name)
				}
			}()
			fn()
		})
	}
}

func TestSelect(t *testing.T) {
	r := NewRegistry()
	r.Register("alpha", fakeA{})
	r.Register("beta", fakeB{}, fakeC{})

	all, err := r.Select()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("空参数应返回全部，实际 %d", len(all))
	}

	sub, err := r.Select("beta")
	if err != nil {
		t.Fatal(err)
	}
	if len(sub) != 2 {
		t.Fatalf("beta 应有 2 个实体，实际 %d", len(sub))
	}
}

// 拼错分组名必须报错。若静默跳过，用户会看到「迁移成功」但实际什么都没做。
func TestSelectUnknownGroupErrors(t *testing.T) {
	r := NewRegistry()
	r.Register("product", fakeA{})
	_, err := r.Select("produc")
	if err == nil {
		t.Fatal("未知分组应返回错误")
	}
	// 错误信息需给出可用分组，方便用户自行纠正。
	if !contains(err.Error(), "product") {
		t.Fatalf("错误信息应列出可用分组，实际: %v", err)
	}
}

func TestTableNames(t *testing.T) {
	r := NewRegistry()
	r.Register("g", fakeA{}, fakeB{})
	names, err := r.TableNames("g")
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 || names[0] != "t_fake_a" || names[1] != "t_fake_b" {
		t.Fatalf("表名清单错误: %v", names)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
