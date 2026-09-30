package trace

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
)

// 关闭追踪时仍必须安装 Propagator，否则 traceID 无法在服务间透传，
// 日志也就失去串联能力——这正是 Config.Enabled 注释所强调的行为。
func TestDisabledStillInstallsPropagator(t *testing.T) {
	shutdown, err := Init(context.Background(), Config{
		Enabled:     false,
		ServiceName: "user-service",
	})
	if err != nil {
		t.Fatal(err)
	}
	if shutdown == nil {
		t.Fatal("Shutdown 不应为 nil，调用方需能无条件 defer")
	}
	defer func() {
		if err := shutdown(context.Background()); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	}()

	p := otel.GetTextMapPropagator()
	if p == nil {
		t.Fatal("Propagator 未安装")
	}
	fields := p.Fields()
	var hasTraceparent, hasBaggage bool
	for _, f := range fields {
		switch f {
		case "traceparent":
			hasTraceparent = true
		case "baggage":
			hasBaggage = true
		}
	}
	if !hasTraceparent {
		t.Errorf("缺少 W3C traceparent，实际字段: %v", fields)
	}
	if !hasBaggage {
		t.Errorf("缺少 baggage，实际字段: %v", fields)
	}
}

func TestDisabledDoesNotSample(t *testing.T) {
	shutdown, err := Init(context.Background(), Config{Enabled: false, ServiceName: "svc"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = shutdown(context.Background()) }()

	_, span := otel.Tracer("test").Start(context.Background(), "op")
	defer span.End()
	if span.SpanContext().IsSampled() {
		t.Fatal("Enabled=false 时不应采样")
	}
	// 即便不采样，SpanContext 仍需有效，才能把 traceID 写入日志与下游请求头。
	if !span.SpanContext().IsValid() {
		t.Fatal("SpanContext 应有效以支持 traceID 透传")
	}
}

func TestSamplerBoundaries(t *testing.T) {
	if got := sampler(0).Description(); got != "AlwaysOffSampler" {
		t.Errorf("ratio=0 应为 AlwaysOff，实际 %s", got)
	}
	if got := sampler(-1).Description(); got != "AlwaysOffSampler" {
		t.Errorf("ratio<0 应为 AlwaysOff，实际 %s", got)
	}
	if got := sampler(1).Description(); got != "AlwaysOnSampler" {
		t.Errorf("ratio=1 应为 AlwaysOn，实际 %s", got)
	}
	if got := sampler(2).Description(); got != "AlwaysOnSampler" {
		t.Errorf("ratio>1 应为 AlwaysOn，实际 %s", got)
	}
	if got := sampler(0.5).Description(); got == "AlwaysOnSampler" || got == "AlwaysOffSampler" {
		t.Errorf("ratio=0.5 应为比例采样，实际 %s", got)
	}
}
