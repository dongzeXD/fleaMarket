package transport

import (
	"context"
	"time"

	kratoserrors "github.com/go-kratos/kratos/v3/errors"
	"github.com/go-kratos/kratos/v3/middleware"
	"github.com/go-kratos/kratos/v3/transport"
)

// baseBackoff 首次重试前的等待时间。
//
// 用指数退避而非固定间隔：下游过载时，固定间隔的重试会形成稳定的
// 额外压力，让它更难恢复；退避则给了下游喘息的窗口。
const baseBackoff = 20 * time.Millisecond

// Retry 返回只对幂等方法生效的重试中间件。
//
// 判定「可重试」的条件比「失败了」更严格，只有两类错误会触发重试：
//   - 503 服务不可用：通常是实例正在重启或熔断，换一个实例就能成功。
//   - 504 网关超时：下游可能只是偶发抖动。
//
// 刻意不重试的情况：
//   - 500 内部错误：多半是确定性的代码缺陷或数据问题，重试只是浪费。
//   - 4xx：请求本身有问题，重试结果必然相同。
//   - context 超时或取消：上游已经不等了，再试毫无意义。
func Retry(idempotentOperations []string, maxRetries int) middleware.Middleware {
	allow := make(map[string]struct{}, len(idempotentOperations))
	for _, op := range idempotentOperations {
		allow[op] = struct{}{}
	}

	return func(handler middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req any) (any, error) {
			info, ok := transport.FromClientContext(ctx)
			if !ok {
				return handler(ctx, req)
			}
			if _, permitted := allow[info.Operation()]; !permitted {
				return handler(ctx, req)
			}

			var lastErr error
			backoff := baseBackoff
			for attempt := 0; attempt <= maxRetries; attempt++ {
				if attempt > 0 {
					select {
					case <-ctx.Done():
						return nil, ctx.Err()
					case <-time.After(backoff):
					}
					backoff *= 2
				}
				reply, err := handler(ctx, req)
				if err == nil {
					return reply, nil
				}
				lastErr = err
				if !retryable(ctx, err) {
					return nil, err
				}
			}
			return nil, lastErr
		}
	}
}

func retryable(ctx context.Context, err error) bool {
	// 上游已放弃等待，不再重试。
	if ctx.Err() != nil {
		return false
	}
	if kratoserrors.IsServiceUnavailable(err) || kratoserrors.IsGatewayTimeout(err) {
		return true
	}
	return false
}
