package server

import (
	"net/http"
	"os"
	"path/filepath"

	"github.com/fleamarket/app/gateway/internal/conf"
	"github.com/fleamarket/app/gateway/internal/proxy"

	userv1 "github.com/fleamarket/api/user/v1"
	"github.com/fleamarket/pkg/auth"
	"github.com/fleamarket/pkg/validate"
	"github.com/go-kratos/kratos/v3/middleware"
	"github.com/go-kratos/kratos/v3/middleware/metadata"
	"github.com/go-kratos/kratos/v3/middleware/recovery"
	khttp "github.com/go-kratos/kratos/v3/transport/http"
	"github.com/google/wire"
)

// ProviderSet 传输层的 wire provider 集合。
var ProviderSet = wire.NewSet(NewHTTPServer)

// NewHTTPServer 创建网关的 HTTP 服务端。
//
// 网关只有 HTTP 端口，没有 gRPC 端口：它的职责是把外部 HTTP 流量翻译成
// 内部 gRPC 调用，自身不对内提供 gRPC 接口。
func NewHTTPServer(
	c *conf.Server,
	docsCfg *conf.Docs,
	rlCfg *conf.RateLimit,
	jwtMgr *auth.Manager,
	userProxy *proxy.UserProxy,
) (*khttp.Server, error) {

	validator, err := validate.Middleware()
	if err != nil {
		return nil, err
	}

	// 中间件顺序是有意义的：
	//  1. recovery 最外层，否则后续中间件 panic 无人兜底；
	//  2. metadata 先于 auth，因为 auth 要往 metadata 写身份；
	//  3. auth 先于 ratelimit，因为按用户限流需要先知道是谁；
	//  4. adminOnly 紧随 auth，尽早拦截越权；
	//  5. validate 最靠内，非法参数不必浪费限流配额。
	ms := []middleware.Middleware{
		recovery.Recovery(),
		metadata.Server(),
		auth.ServerFromToken(jwtMgr, isPublic),
		AdminOnly(),
		RateLimit(rlCfg),
		validator,
	}

	opts := []khttp.ServerOption{
		khttp.Middleware(ms...),
		khttp.Filter(
			corsFilter(c.GetCors()),
			bodyLimitFilter(c.GetHttp().GetMaxBodyBytes()),
		),
	}
	if n := c.GetHttp().GetNetwork(); n != "" {
		opts = append(opts, khttp.Network(n))
	}
	if addr := c.GetHttp().GetAddr(); addr != "" {
		opts = append(opts, khttp.Address(addr))
	}
	if t := c.GetHttp().GetTimeout(); t != nil {
		opts = append(opts, khttp.Timeout(t.AsDuration()))
	}

	srv := khttp.NewServer(opts...)

	// 路由由 proto 的 google.api.http 注解生成，此处只做挂载。
	userv1.RegisterAuthServiceHTTPServer(srv, userProxy)
	userv1.RegisterUserServiceHTTPServer(srv, userProxy)
	userv1.RegisterCampusAuthServiceHTTPServer(srv, userProxy)

	mountDocs(srv, docsCfg)
	mountHealth(srv)

	return srv, nil
}

// mountDocs 挂载 OpenAPI 文档站（doc.md §6.2）。
//
// 文档内容完全来自 proto 注释生成的 openapi.yaml，
// 因此不存在「文档与实现不一致」的可能——改文档的唯一方式是改 proto。
func mountDocs(srv *khttp.Server, c *conf.Docs) {
	if !c.GetEnabled() {
		return
	}
	dir := c.GetDir()
	if dir == "" {
		dir = "../../docs"
	}
	prefix := c.GetPath()
	if prefix == "" {
		prefix = "/docs"
	}

	abs, err := filepath.Abs(dir)
	if err != nil {
		return
	}
	if _, err := os.Stat(abs); err != nil {
		// 目录不存在时静默跳过而非报错：文档是辅助能力，
		// 不该因为它缺失而让整个网关起不来。
		return
	}

	fs := http.StripPrefix(prefix, http.FileServer(http.Dir(abs)))
	srv.HandlePrefix(prefix, fs)
}

// mountHealth 挂载健康检查。
//
// 不走 kratos 中间件链：健康检查若被限流或鉴权拦住，
// 编排系统会误判实例不可用并反复重启它。
func mountHealth(srv *khttp.Server) {
	srv.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
}
