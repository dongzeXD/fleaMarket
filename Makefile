# ============================================================================
# 校园跳蚤市场 campus-flea-market —— 顶层构建入口
#
# 本机 go 不在默认 PATH 中，此处显式声明，无需改动 shell 配置即可 make 全流程。
# ============================================================================
export GOROOT   := /Users/v_lidongze/go/go1.23.2
export GOBIN    := $(HOME)/go/bin
export PATH     := $(GOROOT)/bin:$(GOBIN):/opt/homebrew/bin:$(PATH)

GO      := $(GOROOT)/bin/go
BUF     := buf
ROOT    := $(shell pwd)
APPS    := gateway user product search trade im review notify admin

.DEFAULT_GOAL := help

# ---------------------------------------------------------------------------
# 环境
# ---------------------------------------------------------------------------
.PHONY: env-check
env-check: ## 检查本机工具链版本
	@echo "ROOT        : $(ROOT)"
	@printf "go          : "; $(GO) version           || echo "MISSING"
	@printf "GOPROXY     : "; $(GO) env GOPROXY
	@printf "kratos      : "; kratos --version 2>/dev/null || echo "MISSING"
	@printf "protoc      : "; protoc --version         || echo "MISSING"
	@printf "buf         : "; $(BUF) --version         || echo "MISSING (brew install bufbuild/buf/buf)"
	@printf "node        : "; node -v                  || echo "MISSING"
	@printf "pnpm        : "; pnpm -v                  || echo "MISSING (corepack enable pnpm)"
	@printf "docker      : "; docker -v                || echo "MISSING"
	@printf "docker dmn  : "; docker info --format '{{.ServerVersion}}' 2>/dev/null || echo "NOT RUNNING"

.PHONY: init
init: ## 安装 protoc 插件与代码生成工具（kratos v3 线）
	$(GO) install google.golang.org/protobuf/cmd/protoc-gen-go@latest
	$(GO) install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
	$(GO) install github.com/go-kratos/kratos/cmd/protoc-gen-go-http/v3@latest
	$(GO) install github.com/go-kratos/kratos/cmd/protoc-gen-go-errors/v3@latest
	$(GO) install github.com/google/gnostic/cmd/protoc-gen-openapi@latest
	$(GO) install github.com/google/wire/cmd/wire@latest

.PHONY: infra-up infra-down infra-logs
infra-up: ## 启动本地依赖中间件
	docker compose up -d
infra-down: ## 停止本地依赖中间件
	docker compose down
infra-logs:
	docker compose logs -f --tail=100

# ---------------------------------------------------------------------------
# 契约 / 代码生成
# ---------------------------------------------------------------------------
.PHONY: api api-ts docs api-lint api-breaking api-descriptor api-push api-verify-nocopy
api: ## 生成 Go 侧 proto 产物（grpc/http/errors/openapi）
	cd api && $(BUF) generate
api-ts: ## 生成前端 TS SDK
	cd api && $(BUF) generate --template buf.gen.ts.yaml
docs: api api-descriptor ## 生成 OpenAPI 文档站（docs/openapi.yaml + Redoc）
	@python3 scripts/fix-openapi-errors.py docs/openapi.yaml
	@test -f docs/redoc.standalone.js || \
		curl -fsSL -o docs/redoc.standalone.js \
		https://cdn.redoc.ly/redoc/latest/bundles/redoc.standalone.js || \
		echo "!! redoc 资源下载失败，文档站将无法离线渲染；可手工放置 docs/redoc.standalone.js"
	@echo "文档已生成: docs/openapi.yaml + docs/index.html"
api-descriptor: ## 产出自包含 FileDescriptorSet，供不使用 buf 的客户端免拷贝生成代码
	cd api && $(BUF) build -o ../docs/descriptor.binpb
api-lint:
	cd api && $(BUF) lint
api-breaking: ## 对比 main 分支阻断破坏性变更
	cd api && $(BUF) breaking --against '../.git#branch=main,subdir=api'
api-push: ## 推送模块至 registry，供外部客户端免拷贝生成代码
	cd api && $(BUF) push
api-verify-nocopy: ## 验证「客户端无需拷贝 proto 即可生成代码」（doc §6.3）
	@bash scripts/verify-nocopy-codegen.sh

# ---------------------------------------------------------------------------
# 服务内配置结构体 / wire
# ---------------------------------------------------------------------------
.PHONY: conf wire
conf: ## 为全部服务生成 internal/conf 的 Go 结构体
	@for a in $(APPS); do [ -d "app/$$a" ] && $(MAKE) conf-$$a || true; done

conf-%: ## 为指定服务生成配置结构体，如 make conf-user
	@test -d app/$* || { echo "app/$* 不存在"; exit 1; }
	cd app/$* && protoc --proto_path=./internal \
		--go_out=paths=source_relative:./internal \
		./internal/conf/conf.proto

wire: ## 为全部服务重新生成依赖注入代码
	@for a in $(APPS); do [ -d "app/$$a" ] && $(MAKE) wire-$$a || true; done

wire-%: ## 为指定服务生成 wire_gen.go，如 make wire-user
	cd app/$*/cmd/$* && wire

# ---------------------------------------------------------------------------
# 数据库
# ---------------------------------------------------------------------------
.PHONY: migrate db-dump db-drop db-list seed seed-dev
db-list: ## 列出已注册的实体分组与表
	cd cmd/dbctl && $(GO) run . list
migrate: ## 按实体对象建表（全部分组）
	cd cmd/dbctl && $(GO) run . migrate
db-dump: ## 导出 DDL 快照到 deploy/sql/schema.sql
	cd cmd/dbctl && $(GO) run . dump --out ../../deploy/sql/schema.sql
db-drop: ## 删表（仅 APP_ENV=dev）
	cd cmd/dbctl && APP_ENV=dev $(GO) run . drop --yes

MYSQL_CLI := docker compose exec -T mysql mysql -ufleamarket -pfleamarket fleamarket

seed: ## 导入字典初始数据（分类体系等，幂等可重复执行）
	@for f in deploy/sql/seed/001_*.sql; do \
		echo ">> $$f"; $(MYSQL_CLI) < $$f || exit 1; \
	done

seed-dev: ## 导入开发演示数据（学校/校区/面交点），仅 APP_ENV=dev
	@test "$(APP_ENV)" = "dev" || { \
		echo "seed-dev 仅允许在 APP_ENV=dev 下执行，当前 APP_ENV=$(APP_ENV)"; exit 1; }
	@for f in deploy/sql/seed/*.sql; do \
		echo ">> $$f"; $(MYSQL_CLI) < $$f || exit 1; \
	done

# ---------------------------------------------------------------------------
# 构建 / 测试 / 检查
# ---------------------------------------------------------------------------
.PHONY: build test lint lint-pkg-purity tidy
build: ## 构建全部 Go 应用
	@for a in $(APPS); do \
		if [ -d "app/$$a" ]; then \
			echo ">> build $$a"; \
			(cd app/$$a && $(GO) build -o ../../bin/$$a ./cmd/$$a) || exit 1; \
		fi; \
	done
test: ## 运行全部 Go 单测
	$(GO) test ./... 2>/dev/null; cd pkg && $(GO) test ./...
lint: lint-pkg-purity api-lint ## 全部静态检查
lint-pkg-purity: ## 强制 pkg/ 公共库零业务依赖
	@bash scripts/lint-pkg-purity.sh
tidy:
	cd pkg && $(GO) mod tidy
	@for a in $(APPS); do [ -d "app/$$a" ] && (cd app/$$a && $(GO) mod tidy) || true; done

# ---------------------------------------------------------------------------
# 前端
# ---------------------------------------------------------------------------
.PHONY: web-install web-dev web-build
web-install:
	cd web && pnpm install
web-dev:
	cd web && pnpm --filter web dev
web-build:
	cd web && pnpm -r build

.PHONY: help
help:
	@grep -hE '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'
