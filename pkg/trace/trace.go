// Package trace 封装 OpenTelemetry 初始化。
//
// 与 pkg/log 的分工：本包负责建立链路（TracerProvider 与 Propagator），
// pkg/log 负责把链路 ID 写进日志。二者通过 kratos contrib 的
// tracing.TraceAttrs 这一 Extractor 衔接，调用方无需手工传递 traceID。
//
// 设计约束：不读配置文件、不读环境变量，参数全部由调用方传入；
// 且必须支持「关闭」——本地开发不应被迫启动一个 collector。
package trace

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// Config 链路追踪配置。
type Config struct {
	// Enabled 为 false 时安装一个不采样的 Provider。
	//
	// 注意这里不是「什么都不装」：仍然安装 Provider 与 Propagator，
	// 以保证 traceID 在服务间正常透传、日志里也有 traceID 可供串联，
	// 只是不向后端导出 span。否则关闭追踪会连带丢失日志关联能力。
	Enabled bool
	// Endpoint OTLP/HTTP 接收地址，如 "localhost:4318"。
	Endpoint string
	// ServiceName 服务名，作为 resource 属性上报。
	ServiceName string
	// ServiceVersion 构建版本。
	ServiceVersion string
	// Environment 部署环境，如 dev/staging/prod。
	Environment string
	// SampleRatio 采样率 0~1。<=0 视为不采样，>=1 视为全采样。
	SampleRatio float64
	// Insecure 是否使用明文 HTTP。本地与内网为 true。
	Insecure bool
}

// Shutdown 关闭函数，需在进程退出前调用以冲刷缓冲中的 span。
type Shutdown func(context.Context) error

// Init 初始化全局 TracerProvider 与 Propagator。
//
// 返回的 Shutdown 即使在 Enabled=false 时也非 nil，调用方可无条件 defer。
func Init(ctx context.Context, c Config) (Shutdown, error) {
	// W3C TraceContext + Baggage：跨服务、跨语言的标准透传格式。
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(c.ServiceName),
			semconv.ServiceVersion(c.ServiceVersion),
			attribute.String("deployment.environment", c.Environment),
		),
	)
	if err != nil {
		return noopShutdown, fmt.Errorf("trace: build resource: %w", err)
	}

	if !c.Enabled {
		tp := sdktrace.NewTracerProvider(
			sdktrace.WithSampler(sdktrace.NeverSample()),
			sdktrace.WithResource(res),
		)
		otel.SetTracerProvider(tp)
		return tp.Shutdown, nil
	}

	opts := []otlptracehttp.Option{otlptracehttp.WithEndpoint(c.Endpoint)}
	if c.Insecure {
		opts = append(opts, otlptracehttp.WithInsecure())
	}
	exporter, err := otlptracehttp.New(ctx, opts...)
	if err != nil {
		return noopShutdown, fmt.Errorf("trace: create otlp exporter: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		// ParentBased 保证同一条调用链的采样决策一致：
		// 若入口已决定采样，下游必须跟随，否则链路会出现断裂的片段。
		sdktrace.WithSampler(sdktrace.ParentBased(sampler(c.SampleRatio))),
	)
	otel.SetTracerProvider(tp)
	return tp.Shutdown, nil
}

func sampler(ratio float64) sdktrace.Sampler {
	switch {
	case ratio <= 0:
		return sdktrace.NeverSample()
	case ratio >= 1:
		return sdktrace.AlwaysSample()
	default:
		return sdktrace.TraceIDRatioBased(ratio)
	}
}

func noopShutdown(context.Context) error { return nil }
