package main

import (
	"context"
	"flag"
	"log/slog"
	"os"

	"github.com/fleamarket/app/search/internal/conf"
	"github.com/fleamarket/app/search/internal/data"

	pkglog "github.com/fleamarket/pkg/log"
	pkgreg "github.com/fleamarket/pkg/registry"
	pkgtrace "github.com/fleamarket/pkg/trace"
	"github.com/go-kratos/kratos/contrib/otel/v3/tracing"
	"github.com/go-kratos/kratos/v3"
	"github.com/go-kratos/kratos/v3/config"
	"github.com/go-kratos/kratos/v3/config/env"
	"github.com/go-kratos/kratos/v3/config/file"
	"github.com/go-kratos/kratos/v3/registry"
	"github.com/go-kratos/kratos/v3/transport/grpc"
	"github.com/go-kratos/kratos/v3/transport/http"

	_ "go.uber.org/automaxprocs"
)

// 通过 -ldflags "-X main.Version=x.y.z" 注入。
var (
	Name     = "search-service"
	Version  string
	flagconf string

	id, _ = os.Hostname()
)

func init() {
	flag.StringVar(&flagconf, "conf", "../../configs", "配置文件路径")
}

// newApp 组装应用。
//
// 事件消费者作为 transport.Server 挂载：这样它的生命周期由 kratos 管理，
// 优雅停机时会先停止拉取新消息，而不是在进程退出瞬间丢掉正在处理的事件。
func newApp(logger *slog.Logger, gs *grpc.Server, hs *http.Server,
	consumer *data.Consumer, r registry.Registrar) *kratos.App {
	return kratos.New(
		kratos.ID(id),
		kratos.Name(Name),
		kratos.Version(Version),
		kratos.Metadata(map[string]string{}),
		kratos.Logger(logger),
		kratos.Server(gs, hs, consumer),
		// 注册到 etcd，并在优雅停机时自动注销（doc.md §8.2）。
		kratos.Registrar(r),
	)
}

func main() {
	flag.Parse()

	c := config.New(config.WithSource(
		file.NewSource(flagconf),
		// 环境变量优先级高于文件，使密钥可以不落盘。
		env.NewSource("KRATOS"),
	))
	defer c.Close()

	if err := c.Load(); err != nil {
		panic(err)
	}
	var bc conf.Bootstrap
	if err := c.Scan(&bc); err != nil {
		panic(err)
	}

	logger := pkglog.New(pkglog.Config{
		ServiceID:      id,
		ServiceName:    Name,
		ServiceVersion: Version,
		Level:          bc.GetObservability().GetLog().GetLevel(),
		Format:         logFormat(bc.GetObservability().GetLog().GetFormat()),
		AddSource:      bc.GetObservability().GetLog().GetAddSource(),
		// TraceAttrs 把 traceID 注入每条日志，实现日志与链路互查。
		Extractors: []pkglog.Extractor{tracing.TraceAttrs},
	})
	pkglog.SetDefault(logger)

	ctx := context.Background()
	shutdownTrace, err := pkgtrace.Init(ctx, pkgtrace.Config{
		Enabled:        bc.GetObservability().GetTrace().GetEnabled(),
		Endpoint:       bc.GetObservability().GetTrace().GetEndpoint(),
		ServiceName:    Name,
		ServiceVersion: Version,
		Environment:    bc.GetObservability().GetEnvironment(),
		SampleRatio:    bc.GetObservability().GetTrace().GetSampleRatio(),
		Insecure:       bc.GetObservability().GetTrace().GetInsecure(),
	})
	if err != nil {
		panic(err)
	}
	defer func() { _ = shutdownTrace(ctx) }()

	reg, regCleanup, err := pkgreg.New(pkgreg.Config{
		Endpoints:   bc.GetRegistry().GetEtcd().GetEndpoints(),
		DialTimeout: bc.GetRegistry().GetEtcd().GetDialTimeout().AsDuration(),
		Username:    bc.GetRegistry().GetEtcd().GetUsername(),
		Password:    bc.GetRegistry().GetEtcd().GetPassword(),
		Namespace:   bc.GetRegistry().GetEtcd().GetNamespace(),
		TTL:         bc.GetRegistry().GetEtcd().GetTtl().AsDuration(),
	})
	if err != nil {
		panic(err)
	}
	defer regCleanup()

	app, cleanup, err := wireApp(
		bc.Server, bc.Data, bc.Meilisearch, bc.Downstream, bc.Business,
		reg, reg, logger,
	)
	if err != nil {
		panic(err)
	}
	defer cleanup()

	if err := app.Run(); err != nil {
		panic(err)
	}
}

func logFormat(s string) pkglog.Format {
	if s == "json" {
		return pkglog.FormatJSON
	}
	return pkglog.FormatText
}
