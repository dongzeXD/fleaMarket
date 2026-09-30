package paging

import (
	"errors"
	"testing"
)

func TestCursorRoundTrip(t *testing.T) {
	cases := []Cursor{
		{SortValue: "1704067200000", ID: 123},
		{SortValue: "0", ID: 1},
		{SortValue: "98.5", ID: 9223372036854775807},
	}
	for _, want := range cases {
		got, err := DecodeCursor(want.Encode())
		if err != nil {
			t.Fatalf("DecodeCursor(%q) 报错: %v", want.Encode(), err)
		}
		if got != want {
			t.Fatalf("往返不一致: got %+v, want %+v", got, want)
		}
	}
}

// SortValue 自身可能包含分隔符（如带 "|" 的复合排序键），
// 解析必须从右侧切分，否则会把 SortValue 截断。
func TestCursorSortValueContainingSeparator(t *testing.T) {
	want := Cursor{SortValue: "a|b|c", ID: 42}
	got, err := DecodeCursor(want.Encode())
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestEmptyCursorMeansFirstPage(t *testing.T) {
	if got := (Cursor{}).Encode(); got != "" {
		t.Fatalf("零值游标应编码为空串，实际 %q", got)
	}
	got, err := DecodeCursor("")
	if err != nil {
		t.Fatalf("空游标不应报错: %v", err)
	}
	if !got.IsZero() {
		t.Fatalf("空游标应解析为零值，实际 %+v", got)
	}
}

func TestDecodeCursorRejectsMalformed(t *testing.T) {
	for _, s := range []string{
		"not-base64!!!",
		"YWJj",       // 合法 base64 但无分隔符 ("abc")
		"fDEyMw",     // "|123"，SortValue 为空
		"YWJjfHh5eg", // "abc|xyz"，ID 非数字
	} {
		if _, err := DecodeCursor(s); !errors.Is(err, ErrInvalidCursor) {
			t.Fatalf("DecodeCursor(%q) 应返回 ErrInvalidCursor，实际 %v", s, err)
		}
	}
}

func TestNormalizeLimit(t *testing.T) {
	cases := map[int]int{
		0:            DefaultLimit,
		-5:           DefaultLimit,
		1:            1,
		MaxLimit:     MaxLimit,
		MaxLimit + 1: MaxLimit,
		100000:       MaxLimit,
	}
	for in, want := range cases {
		if got := NormalizeLimit(in); got != want {
			t.Errorf("NormalizeLimit(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestNormalizePage(t *testing.T) {
	page, size, offset := NormalizePage(0, 0)
	if page != 1 || size != DefaultPageSize || offset != 0 {
		t.Fatalf("默认值错误: %d %d %d", page, size, offset)
	}
	page, size, offset = NormalizePage(3, 10)
	if page != 3 || size != 10 || offset != 20 {
		t.Fatalf("offset 计算错误: %d %d %d", page, size, offset)
	}
	_, size, _ = NormalizePage(1, MaxPageSize+50)
	if size != MaxPageSize {
		t.Fatalf("pageSize 未收敛到上限: %d", size)
	}
}

type row struct {
	id    int64
	score string
}

func TestBuildPageHasMore(t *testing.T) {
	limit := 3
	// 按约定多查一条（4 条）表示还有下一页。
	rows := []row{
		{1, "10"}, {2, "9"}, {3, "8"}, {4, "7"},
	}
	p := BuildPage(rows, limit, func(r row) Cursor {
		return Cursor{SortValue: r.score, ID: r.id}
	})

	if !p.HasMore {
		t.Fatal("多查到一条时 HasMore 应为 true")
	}
	if len(p.Items) != limit {
		t.Fatalf("应裁剪到 %d 条，实际 %d", limit, len(p.Items))
	}
	if p.Items[len(p.Items)-1].id != 3 {
		t.Fatalf("裁剪后末条应为 id=3，实际 %d", p.Items[len(p.Items)-1].id)
	}
	// 游标必须取自裁剪后的末条（id=3），而非被丢弃的那条（id=4），
	// 否则下一页会漏掉 id=3 之后的第一条记录。
	want := Cursor{SortValue: "8", ID: 3}.Encode()
	if p.NextCursor != want {
		t.Fatalf("NextCursor 应取裁剪后末条，got %q want %q", p.NextCursor, want)
	}
}

func TestBuildPageLastPage(t *testing.T) {
	rows := []row{{1, "10"}, {2, "9"}}
	p := BuildPage(rows, 3, func(r row) Cursor {
		return Cursor{SortValue: r.score, ID: r.id}
	})
	if p.HasMore {
		t.Fatal("未满一页时 HasMore 应为 false")
	}
	if p.NextCursor != "" {
		t.Fatalf("末页 NextCursor 应为空，实际 %q", p.NextCursor)
	}
	if len(p.Items) != 2 {
		t.Fatalf("不应裁剪，实际 %d 条", len(p.Items))
	}
}

func TestBuildPageEmpty(t *testing.T) {
	p := BuildPage([]row{}, 10, func(r row) Cursor { return Cursor{} })
	if p.HasMore || p.NextCursor != "" || len(p.Items) != 0 {
		t.Fatalf("空结果集处理错误: %+v", p)
	}
}
