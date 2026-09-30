package errs

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	kratoserrors "github.com/go-kratos/kratos/v3/errors"
)

func codeOf(t *testing.T, err error) int {
	t.Helper()
	var ke *kratoserrors.Error
	if !errors.As(err, &ke) {
		t.Fatalf("期望 kratos 错误，实际 %T: %v", err, err)
	}
	return int(ke.Code)
}

func reasonOf(t *testing.T, err error) string {
	t.Helper()
	var ke *kratoserrors.Error
	if !errors.As(err, &ke) {
		t.Fatalf("期望 kratos 错误，实际 %T: %v", err, err)
	}
	return ke.Reason
}

func TestFromDataErrorNil(t *testing.T) {
	if err := FromDataError(nil, ""); err != nil {
		t.Fatalf("nil 应返回 nil，实际 %v", err)
	}
}

func TestFromDataErrorSentinels(t *testing.T) {
	cases := []struct {
		name       string
		in         error
		wantCode   int
		wantReason string
	}{
		{"not found", ErrNotFound, http.StatusNotFound, ReasonNotFound},
		{"already exists", ErrAlreadyExists, http.StatusConflict, ReasonAlreadyExists},
		{"conflict", ErrConflict, http.StatusConflict, ReasonConflict},
		{"deadline", context.DeadlineExceeded, http.StatusGatewayTimeout, ReasonTimeout},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := FromDataError(c.in, "")
			if code := codeOf(t, got); code != c.wantCode {
				t.Errorf("code = %d, want %d", code, c.wantCode)
			}
			if r := reasonOf(t, got); r != c.wantReason {
				t.Errorf("reason = %q, want %q", r, c.wantReason)
			}
		})
	}
}

// 包装过的哨兵错误同样要能被识别，否则数据层加一层 fmt.Errorf 就会退化成 500。
func TestFromDataErrorRecognizesWrappedSentinels(t *testing.T) {
	wrapped := fmt.Errorf("query product: %w", ErrNotFound)
	if code := codeOf(t, FromDataError(wrapped, "商品不存在")); code != http.StatusNotFound {
		t.Fatalf("包装后的 ErrNotFound 应仍为 404，实际 %d", code)
	}
}

func TestFromDataErrorCustomNotFoundMessage(t *testing.T) {
	err := FromDataError(ErrNotFound, "商品不存在")
	var ke *kratoserrors.Error
	if !errors.As(err, &ke) {
		t.Fatal("类型断言失败")
	}
	if ke.Message != "商品不存在" {
		t.Fatalf("message = %q，期望自定义文案", ke.Message)
	}
	// 留空时应有通用兜底文案，而不是空串。
	err = FromDataError(ErrNotFound, "")
	_ = errors.As(err, &ke)
	if ke.Message == "" {
		t.Fatal("未提供文案时应有默认文案")
	}
}

// 驱动层的唯一键冲突必须映射为 409，而不是 500。
// 这是 doc.md §12 明确要求的行为。
func TestUniqueViolationMapsTo409(t *testing.T) {
	for _, raw := range []string{
		"Error 1062 (23000): Duplicate entry '13800138000' for key 't_user.uk_phone_hash'",
		`pq: duplicate key value violates unique constraint "t_user_phone_hash_key"`,
		"UNIQUE constraint failed: t_user.phone_hash",
	} {
		got := FromDataError(errors.New(raw), "")
		if code := codeOf(t, got); code != http.StatusConflict {
			t.Errorf("唯一键冲突应为 409，实际 %d (输入: %s)", code, raw)
		}
	}
}

// 未知错误不得把原始信息下发给客户端——避免泄漏 SQL、表名、内网地址。
func TestUnknownErrorDoesNotLeakDetails(t *testing.T) {
	raw := "dial tcp 10.0.3.17:3306: connect: connection refused"
	got := FromDataError(errors.New(raw), "")
	if code := codeOf(t, got); code != http.StatusInternalServerError {
		t.Fatalf("未知错误应为 500，实际 %d", code)
	}
	var ke *kratoserrors.Error
	_ = errors.As(got, &ke)
	if strings.Contains(ke.Message, "10.0.3.17") || strings.Contains(ke.Message, "3306") {
		t.Fatalf("内部细节泄漏到响应: %q", ke.Message)
	}
}

// 上游已明确表达语义的 kratos 错误应原样透传，不能被降级成 500。
func TestFromDataErrorPassesThroughKratosError(t *testing.T) {
	orig := Conflict("商品已被预订")
	got := FromDataError(orig, "")
	if code := codeOf(t, got); code != http.StatusConflict {
		t.Fatalf("code = %d, want 409", code)
	}
	if r := reasonOf(t, got); r != ReasonConflict {
		t.Fatalf("reason = %q, want %q", r, ReasonConflict)
	}
}

func TestConstructorCodes(t *testing.T) {
	cases := map[int]error{
		http.StatusInternalServerError: Internal("x"),
		http.StatusBadRequest:          InvalidArgument("x"),
		http.StatusUnauthorized:        Unauthenticated("x"),
		http.StatusForbidden:           PermissionDenied("x"),
		http.StatusNotFound:            NotFound("x"),
		http.StatusTooManyRequests:     RateLimited("x"),
		http.StatusServiceUnavailable:  Unavailable("x"),
		http.StatusGatewayTimeout:      Timeout("x"),
	}
	for want, err := range cases {
		if got := codeOf(t, err); got != want {
			t.Errorf("code = %d, want %d (%v)", got, want, err)
		}
	}
}

func TestWithMetaCarriesStructuredContext(t *testing.T) {
	err := WithMeta(Conflict("商品已被预订"), map[string]string{"product_id": "123"})
	if err.Metadata["product_id"] != "123" {
		t.Fatalf("metadata 丢失: %+v", err.Metadata)
	}
}
