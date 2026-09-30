// Package errs 统一平台的错误表达。
//
// 职责边界（见 doc.md §5.1）：
//   - 本包只提供「错误如何分类、如何转换」的机制，不定义任何业务错误原因。
//     业务原因由各服务在自己的 errors.proto 中声明，经 protoc-gen-go-errors 生成。
//   - 本包负责把基础设施层的错误（数据库、缓存）归一成带正确 HTTP 状态码的
//     kratos 错误，避免把 SQL 细节泄漏给客户端。
package errs

import (
	"context"
	"errors"
	"net/http"
	"strings"

	kratoserrors "github.com/go-kratos/kratos/v3/errors"
)

// 平台级通用 reason。业务专属 reason 一律在各服务的 errors.proto 中定义，
// 不在此处堆积。
const (
	// ReasonInternal 未归类的服务端错误。
	ReasonInternal = "INTERNAL_ERROR"
	// ReasonInvalidArgument 参数校验失败。
	ReasonInvalidArgument = "INVALID_ARGUMENT"
	// ReasonUnauthenticated 未登录或令牌无效。
	ReasonUnauthenticated = "UNAUTHENTICATED"
	// ReasonPermissionDenied 已登录但无权访问该资源。
	ReasonPermissionDenied = "PERMISSION_DENIED"
	// ReasonNotFound 资源不存在。
	ReasonNotFound = "NOT_FOUND"
	// ReasonAlreadyExists 资源已存在（唯一键冲突）。
	ReasonAlreadyExists = "ALREADY_EXISTS"
	// ReasonConflict 并发冲突或状态不允许该操作。
	ReasonConflict = "CONFLICT"
	// ReasonRateLimited 触发限流。
	ReasonRateLimited = "RATE_LIMITED"
	// ReasonUnavailable 依赖不可用。
	ReasonUnavailable = "SERVICE_UNAVAILABLE"
	// ReasonTimeout 处理超时。
	ReasonTimeout = "TIMEOUT"
)

// ErrNotFound 供数据层返回的哨兵错误。
// 数据层不应直接构造 kratos 错误——那是传输层的关注点。
var ErrNotFound = errors.New("resource not found")

// ErrAlreadyExists 唯一键冲突的哨兵错误。
var ErrAlreadyExists = errors.New("resource already exists")

// ErrConflict 状态冲突的哨兵错误，如商品已被他人预订。
var ErrConflict = errors.New("resource state conflict")

// New 按 HTTP 状态码构造错误。
func New(code int, reason, message string) *kratoserrors.Error {
	return kratoserrors.New(code, reason, message)
}

// Internal 500。
func Internal(message string) *kratoserrors.Error {
	return kratoserrors.New(http.StatusInternalServerError, ReasonInternal, message)
}

// InvalidArgument 400。
func InvalidArgument(message string) *kratoserrors.Error {
	return kratoserrors.New(http.StatusBadRequest, ReasonInvalidArgument, message)
}

// Unauthenticated 401。
func Unauthenticated(message string) *kratoserrors.Error {
	return kratoserrors.New(http.StatusUnauthorized, ReasonUnauthenticated, message)
}

// PermissionDenied 403。
func PermissionDenied(message string) *kratoserrors.Error {
	return kratoserrors.New(http.StatusForbidden, ReasonPermissionDenied, message)
}

// NotFound 404。
func NotFound(message string) *kratoserrors.Error {
	return kratoserrors.New(http.StatusNotFound, ReasonNotFound, message)
}

// AlreadyExists 409。
func AlreadyExists(message string) *kratoserrors.Error {
	return kratoserrors.New(http.StatusConflict, ReasonAlreadyExists, message)
}

// Conflict 409。与 AlreadyExists 同码不同 reason：
// 前者表示「创建时重复」，后者表示「当前状态不允许此操作」。
func Conflict(message string) *kratoserrors.Error {
	return kratoserrors.New(http.StatusConflict, ReasonConflict, message)
}

// RateLimited 429。
func RateLimited(message string) *kratoserrors.Error {
	return kratoserrors.New(http.StatusTooManyRequests, ReasonRateLimited, message)
}

// Unavailable 503。
func Unavailable(message string) *kratoserrors.Error {
	return kratoserrors.New(http.StatusServiceUnavailable, ReasonUnavailable, message)
}

// Timeout 504。
func Timeout(message string) *kratoserrors.Error {
	return kratoserrors.New(http.StatusGatewayTimeout, ReasonTimeout, message)
}

// WithMeta 附加结构化元数据，便于客户端做精确处理（如 product_id）。
func WithMeta(e *kratoserrors.Error, kv map[string]string) *kratoserrors.Error {
	return e.WithMetadata(kv)
}

// isUniqueViolation 判断是否为唯一键冲突。
//
// 这里用字符串匹配而非依赖具体驱动的错误类型，是刻意的：公共库不应为了
// 识别错误码而引入 mysql / pq 等驱动依赖，否则任何使用本包的服务都会被迫
// 编译进所有数据库驱动。代价是匹配较为宽松，故仅用于归类而不用于控制流分支。
func isUniqueViolation(err error) bool {
	s := strings.ToLower(err.Error())
	switch {
	case strings.Contains(s, "duplicate entry"): // MySQL 1062
		return true
	case strings.Contains(s, "duplicate key value"): // PostgreSQL 23505
		return true
	case strings.Contains(s, "unique constraint"):
		return true
	default:
		return false
	}
}

// FromDataError 把数据层错误归一为可直接返回给客户端的 kratos 错误。
//
// 归一规则（见 doc.md §12）：
//   - ErrNotFound            → 404
//   - ErrAlreadyExists、唯一键冲突 → 409 ALREADY_EXISTS
//   - ErrConflict            → 409 CONFLICT
//   - context 超时/取消      → 504
//   - 其余                   → 500，且原始信息不下发给客户端
//
// notFoundMsg 用于定制 404 文案，留空时使用通用文案。
func FromDataError(err error, notFoundMsg string) error {
	if err == nil {
		return nil
	}
	// 已经是 kratos 错误说明上游已明确表达语义，原样透传。
	var ke *kratoserrors.Error
	if errors.As(err, &ke) {
		return ke
	}
	switch {
	case errors.Is(err, ErrNotFound):
		if notFoundMsg == "" {
			notFoundMsg = "资源不存在"
		}
		return NotFound(notFoundMsg)
	case errors.Is(err, ErrAlreadyExists), isUniqueViolation(err):
		return AlreadyExists("资源已存在")
	case errors.Is(err, ErrConflict):
		return Conflict("当前状态不允许该操作")
	case errors.Is(err, context.DeadlineExceeded):
		return Timeout("处理超时，请稍后重试")
	case errors.Is(err, context.Canceled):
		// 客户端主动断开，不是服务端故障，用 499 语义最贴切，
		// 但标准库无该常量，沿用 kratos 的 ClientClosed。
		return kratoserrors.ClientClosed("CLIENT_CLOSED", "请求已被取消")
	default:
		// 原始错误只进日志，不下发。
		return Internal("服务内部错误")
	}
}
