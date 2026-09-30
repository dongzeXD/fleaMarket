// Package paging 提供分页工具。
//
// 同时支持两种分页：
//   - 偏移分页（Offset）：适用于总数有限、需要跳页的场景，如管理后台列表。
//   - 游标分页（Cursor）：适用于信息流等持续下拉场景。
//
// 为何信息流必须用游标分页：偏移分页在「边浏览边有新商品上架」时，
// 第 2 页会重复出现第 1 页末尾的数据（数据整体后移），用户体验上表现为重复卡片。
// 游标分页以「上一条的排序值」为锚点，天然免疫插入导致的错位。
package paging

import (
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
)

// 默认与上限。上限存在的意义是防止调用方（尤其是外部客户端）
// 用超大 limit 把数据库拖垮。
const (
	DefaultLimit = 20
	MaxLimit     = 50

	DefaultPageSize = 20
	MaxPageSize     = 100
)

// ErrInvalidCursor 游标格式非法。
var ErrInvalidCursor = errors.New("paging: invalid cursor")

// Cursor 复合游标。
//
// 只用排序字段值做游标是不够的：当排序字段存在重复值（如同一秒内上架的多个商品、
// 热度分相同）时，会出现漏项或重复。因此附加单调唯一的 ID 作为第二排序键，
// 查询条件形如 (sort_value, id) < (?, ?)。
type Cursor struct {
	// SortValue 主排序字段的值，以字符串承载以兼容时间戳、价格、热度分等不同类型。
	SortValue string
	// ID 记录主键，作为同值时的决胜键。
	ID int64
}

// IsZero 判断是否为首页请求（空游标）。
func (c Cursor) IsZero() bool { return c.SortValue == "" && c.ID == 0 }

// Encode 编码为不透明字符串。
//
// 用 base64 包装而非直接暴露 "值:ID"，目的是让游标对客户端不透明——
// 避免客户端自行构造或依赖其内部格式，从而保留服务端后续更换排序实现的自由。
func (c Cursor) Encode() string {
	if c.IsZero() {
		return ""
	}
	raw := c.SortValue + "|" + strconv.FormatInt(c.ID, 10)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// DecodeCursor 解析游标。空串表示首页，返回零值 Cursor 且不报错。
func DecodeCursor(s string) (Cursor, error) {
	if s == "" {
		return Cursor{}, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return Cursor{}, ErrInvalidCursor
	}
	// SortValue 自身可能含 "|"，因此从右侧切分。
	raw := string(b)
	i := strings.LastIndex(raw, "|")
	if i <= 0 {
		return Cursor{}, ErrInvalidCursor
	}
	id, err := strconv.ParseInt(raw[i+1:], 10, 64)
	if err != nil {
		return Cursor{}, ErrInvalidCursor
	}
	return Cursor{SortValue: raw[:i], ID: id}, nil
}

// NormalizeLimit 收敛游标分页的 limit 到 [1, MaxLimit]。
func NormalizeLimit(limit int) int {
	switch {
	case limit <= 0:
		return DefaultLimit
	case limit > MaxLimit:
		return MaxLimit
	default:
		return limit
	}
}

// NormalizePage 收敛偏移分页参数，返回规范化后的 page、pageSize 与 SQL offset。
func NormalizePage(page, pageSize int) (int, int, int) {
	if page <= 0 {
		page = 1
	}
	switch {
	case pageSize <= 0:
		pageSize = DefaultPageSize
	case pageSize > MaxPageSize:
		pageSize = MaxPageSize
	}
	return page, pageSize, (page - 1) * pageSize
}

// Page 游标分页结果。
type Page[T any] struct {
	Items      []T
	NextCursor string
	HasMore    bool
}

// BuildPage 依据「多查一条」的约定构造分页结果。
//
// 调用方应以 limit+1 条查询，本函数据此判断是否还有下一页，
// 并裁掉多出的那条。cursorOf 负责从末条记录提取游标。
//
// 这样做避免了为「是否有下一页」单独执行一次 COUNT 查询。
func BuildPage[T any](rows []T, limit int, cursorOf func(T) Cursor) Page[T] {
	limit = NormalizeLimit(limit)
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	p := Page[T]{Items: rows, HasMore: hasMore}
	if hasMore && len(rows) > 0 {
		p.NextCursor = cursorOf(rows[len(rows)-1]).Encode()
	}
	return p
}
