package transport

import (
	"context"
	"errors"
	"testing"
	"time"

	kratoserrors "github.com/go-kratos/kratos/v3/errors"
	"github.com/go-kratos/kratos/v3/transport"
)

type mockTransport struct {
	operation string
}

func (m *mockTransport) Kind() transport.Kind            { return transport.KindGRPC }
func (m *mockTransport) Endpoint() string                { return "discovery:///test-service" }
func (m *mockTransport) Operation() string               { return m.operation }
func (m *mockTransport) RequestHeader() transport.Header { return nil }
func (m *mockTransport) ReplyHeader() transport.Header   { return nil }

func clientCtx(op string) context.Context {
	return transport.NewClientContext(context.Background(), &mockTransport{operation: op})
}

const idempotentOp = "/user.v1.UserService/BatchGetUserBrief"
const mutatingOp = "/trade.v1.OrderService/CreateOrder"

func TestRetrySucceedsAfterTransientFailure(t *testing.T) {
	calls := 0
	h := Retry([]string{idempotentOp}, 2)(func(context.Context, any) (any, error) {
		calls++
		if calls < 3 {
			return nil, kratoserrors.ServiceUnavailable("UNAVAILABLE", "instance restarting")
		}
		return "ok", nil
	})

	got, err := h(clientCtx(idempotentOp), nil)
	if err != nil {
		t.Fatalf("重试后应成功: %v", err)
	}
	if got != "ok" {
		t.Fatalf("结果错误: %v", got)
	}
	if calls != 3 {
		t.Fatalf("应共调用 3 次（1 次原始 + 2 次重试），实际 %d", calls)
	}
}

// 非幂等方法绝不重试——重试会造成重复下单这类数据错误。
func TestRetrySkipsNonIdempotentOperations(t *testing.T) {
	calls := 0
	h := Retry([]string{idempotentOp}, 3)(func(context.Context, any) (any, error) {
		calls++
		return nil, kratoserrors.ServiceUnavailable("UNAVAILABLE", "down")
	})

	if _, err := h(clientCtx(mutatingOp), nil); err == nil {
		t.Fatal("应返回错误")
	}
	if calls != 1 {
		t.Fatalf("非幂等方法只应调用 1 次，实际 %d", calls)
	}
}

// 500 内部错误多为确定性缺陷，重试是浪费。
func TestRetrySkipsInternalError(t *testing.T) {
	calls := 0
	h := Retry([]string{idempotentOp}, 3)(func(context.Context, any) (any, error) {
		calls++
		return nil, kratoserrors.InternalServer("INTERNAL", "nil pointer")
	})

	if _, err := h(clientCtx(idempotentOp), nil); err == nil {
		t.Fatal("应返回错误")
	}
	if calls != 1 {
		t.Fatalf("500 不应重试，实际调用 %d 次", calls)
	}
}

func TestRetrySkipsClientError(t *testing.T) {
	calls := 0
	h := Retry([]string{idempotentOp}, 3)(func(context.Context, any) (any, error) {
		calls++
		return nil, kratoserrors.BadRequest("INVALID_ARGUMENT", "bad param")
	})
	if _, err := h(clientCtx(idempotentOp), nil); err == nil {
		t.Fatal("应返回错误")
	}
	if calls != 1 {
		t.Fatalf("4xx 不应重试，实际调用 %d 次", calls)
	}
}

func TestRetryRetriesGatewayTimeout(t *testing.T) {
	calls := 0
	h := Retry([]string{idempotentOp}, 2)(func(context.Context, any) (any, error) {
		calls++
		return nil, kratoserrors.GatewayTimeout("TIMEOUT", "slow downstream")
	})
	if _, err := h(clientCtx(idempotentOp), nil); err == nil {
		t.Fatal("应返回错误")
	}
	if calls != 3 {
		t.Fatalf("504 应重试至上限，实际调用 %d 次", calls)
	}
}

func TestRetryExhaustsAndReturnsLastError(t *testing.T) {
	want := kratoserrors.ServiceUnavailable("UNAVAILABLE", "still down")
	h := Retry([]string{idempotentOp}, 2)(func(context.Context, any) (any, error) {
		return nil, want
	})
	_, err := h(clientCtx(idempotentOp), nil)
	if !kratoserrors.IsServiceUnavailable(err) {
		t.Fatalf("应返回最后一次的错误，实际 %v", err)
	}
}

// 上游已放弃等待时不应继续重试。
func TestRetryStopsWhenContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(clientCtx(idempotentOp))
	calls := 0
	h := Retry([]string{idempotentOp}, 5)(func(context.Context, any) (any, error) {
		calls++
		if calls == 1 {
			cancel()
		}
		return nil, kratoserrors.ServiceUnavailable("UNAVAILABLE", "down")
	})
	if _, err := h(ctx, nil); err == nil {
		t.Fatal("应返回错误")
	}
	if calls > 2 {
		t.Fatalf("context 取消后应停止重试，实际调用 %d 次", calls)
	}
}

// 没有客户端 transport 上下文时（如直接单测业务层）中间件应透明放行。
func TestRetryPassesThroughWithoutTransportContext(t *testing.T) {
	calls := 0
	h := Retry([]string{idempotentOp}, 3)(func(context.Context, any) (any, error) {
		calls++
		return "ok", nil
	})
	got, err := h(context.Background(), nil)
	if err != nil || got != "ok" {
		t.Fatalf("应透明放行: got=%v err=%v", got, err)
	}
	if calls != 1 {
		t.Fatalf("应只调用 1 次，实际 %d", calls)
	}
}

// 退避必须真的产生等待，否则重试会在下游过载时形成瞬时风暴。
func TestRetryBacksOff(t *testing.T) {
	h := Retry([]string{idempotentOp}, 2)(func(context.Context, any) (any, error) {
		return nil, kratoserrors.ServiceUnavailable("UNAVAILABLE", "down")
	})
	start := time.Now()
	_, _ = h(clientCtx(idempotentOp), nil)
	elapsed := time.Since(start)
	// 两次重试的退避为 baseBackoff + 2*baseBackoff = 60ms。
	if elapsed < baseBackoff+2*baseBackoff {
		t.Fatalf("退避时间不足: %v", elapsed)
	}
}

func TestClientOptionsValidation(t *testing.T) {
	var o ClientOptions
	if err := o.setDefaults(); err == nil {
		t.Fatal("空 Endpoint 应报错")
	}
	o = ClientOptions{Endpoint: "discovery:///svc"}
	if err := o.setDefaults(); err != nil {
		t.Fatal(err)
	}
	if o.Timeout != 2*time.Second {
		t.Fatalf("默认超时应为 2s，实际 %v", o.Timeout)
	}
	if o.MaxRetries != 2 {
		t.Fatalf("默认重试次数应为 2，实际 %d", o.MaxRetries)
	}
}

func TestNewGRPCConnRejectsEmptyEndpoint(t *testing.T) {
	_, _, err := NewGRPCConn(context.Background(), ClientOptions{})
	if err == nil {
		t.Fatal("空 Endpoint 应报错")
	}
	var target error = errors.New("x")
	_ = target
}
