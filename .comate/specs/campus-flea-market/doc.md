# 校园跳蚤市场（campus-flea-market）设计文档

## 0. 文档定位与阅读顺序

本文档是整个系统的**唯一设计基线**，后续 `tasks.md` 的每一条任务都必须能追溯到本文档的某一节。

- 第 1 节：需求拆分（R1~R8），确定"做什么、不做什么"
- 第 2 节：技术选型与被否决方案，确定"为什么这么做"
- 第 3~4 节：总体架构与工程目录，确定"代码放哪里"
- 第 5~8 节：四项强制性技术要求的落地方案（公共库、proto 契约分发、建表脚本、etcd + RPC）
- 第 9 节：微服务职责与数据表设计
- 第 10 节：前端方案（响应式 + 小程序演进）
- 第 11~14 节：关键实现细节、数据流、边界与异常、安全风险
- 第 15~16 节：预期结果、环境前置条件

## 1. 需求范围与拆分

### 1.1 需求全景

构建面向高校的校园二手交易平台，Web 端优先，后期追加微信小程序。交易以**线下当面交易**为主，平台只做信息撮合与信任背书，不做资金托管。

### 1.2 子需求拆分

复杂度较高，拆分为 8 个子需求域，每个域对应独立的服务边界与数据所有权：

| 编号 | 子需求 | 核心能力 | 归属服务 |
|------|--------|----------|----------|
| R1 | 用户与身份 | 手机号/微信登录、校园身份认证（学号/校园邮箱）、个人主页 | user-service |
| R2 | 商品发布与管理 | 多图上传、商品 CRUD、状态机、分类体系 | product-service |
| R3 | 浏览与搜索 | 信息流（时间/热度/距离）、分类浏览、关键词搜索、多维筛选、同校优先 | search-service + product-service |
| R4 | 交易与沟通 | 站内即时聊天、砍价、预订/下单、约面交 | im-service + trade-service |
| R5 | 信任与安全 | 双向评价、信誉分、举报拉黑、内容审核（违禁品过滤） | trade-service + review-service |
| R6 | 消息与通知 | 咨询/预订/评价推送、降价与上新提醒 | notify-service |
| R7 | 辅助功能 | 收藏关注、求购帖、校园地图选点 | product-service + user-service |
| R8 | 运营管理平台 | 活动配置、首页轮播图、公告、审核工单 | admin-service |

### 1.3 明确不做（Out of Scope）

- **线上支付与资金托管**：校园二手场景下引入支付意味着需要处理退款纠纷、资金合规、备付金问题，且极易被用作洗钱通道。首版仅支持线下当面交易 + 平台侧"预订锁定"机制。预留 `trade-service` 的支付扩展点（订单表含 `pay_channel` 字段但仅允许 `OFFLINE`）。
- **物流配送**：校园场景无需求。
- **实名认证对接公安接口**：以校园身份认证（学号/校园邮箱）替代。

## 2. 技术选型

### 2.1 选型总表

| 层次 | 选型 | 版本 | 理由 |
|------|------|------|------|
| 服务端框架 | go-kratos | **v3.0.0**（`github.com/go-kratos/kratos/v3`） | 本机 CLI v3.0.0 生成的官方 layout 即针对 v3；跟随官方模板而非与 CLI 对抗 |
| Go 版本 | 本机 Go 1.23.2，**实际编译使用 1.25.7** | - | 官方 v3 layout 的 `go.mod` 要求 `go 1.25.7`；`GOTOOLCHAIN=auto` 已自动下载并实测 `go build` 成功 |
| 服务注册发现 | etcd v3.5 | - | 需求指定；kratos 官方 `contrib/registry/etcd` 直接可用 |
| 服务间通讯 | gRPC + protobuf | proto3 | 需求指定 |
| 契约管理 | Buf CLI + 独立 proto 仓库（buf module） | buf v1.4x | 客户端通过 `buf.yaml` 远程依赖拉取，**无需拷贝 proto 文件** |
| API 文档 | protoc-gen-openapi + Redoc 静态站 | - | protobuf 注释直接产出 OpenAPI 3，达到"文档效果" |
| 数据库 | MySQL 8.0 | - | 校园规模数据量，关系型建模清晰，运维成熟 |
| ORM / 建表 | GORM v2 + 自研 `pkg/orm` entity registry | - | 满足"根据实体对象创建数据库表"；AutoMigrate + SQL 快照导出 |
| 缓存 | Redis 7 | - | 会话、热度计数、IM 在线状态、限流 |
| 搜索 | Meilisearch | v1.x | 中文分词开箱可用、部署极轻（单二进制），相比 ES 运维成本低一个数量级 |
| 消息队列 | Redis Stream（首版）→ 可换 Kafka | - | 通知、审核、索引同步的异步解耦；`pkg/mq` 做接口抽象，避免绑死实现 |
| 对象存储 | S3 兼容（MinIO 本地 / 云 OSS 生产） | - | 多图上传走前端直传 + 后端签名，不经过业务服务 |
| 前端框架 | Next.js 15 App Router + React 19 + TypeScript | - | 成熟度最高、SSR 利好商品详情页 SEO 与首屏 |
| 样式 | Tailwind CSS v4 + shadcn/ui | - | 移动优先响应式，断点即类名，无运行时开销 |
| 前端数据层 | TanStack Query + 由 proto 生成的 TS SDK | - | 与后端契约强一致，接口变更编译期暴露 |
| 小程序（后期） | Taro 4 + React | - | 复用 monorepo 中的 TS SDK 与业务 hooks 层 |
| 仓库形态 | pnpm workspace（前端）+ Go workspace（后端），单仓多应用 | - | 需求要求"工程文件夹下多应用管理" |

### 2.2 关键选型的权衡说明

**前端为何不直接用 Taro 一套代码编译 H5 + 小程序？**
Taro 能一套代码多端，但代价是：H5 端产物体积大、SEO 能力弱（无 SSR）、样式受小程序模型约束（不能自由用 CSS Grid/伪元素）、调试链路长。本项目 Web 端是主战场且商品详情页需要被搜索引擎收录，因此 Web 端用 Next.js 做到最佳体验；小程序期新建 Taro 应用，**复用 `packages/api-sdk`（生成物）与 `packages/core`（纯 TS 业务逻辑/校验/格式化，零 DOM 依赖）**，真正重写的只有 UI 层。这是"共享逻辑、分别实现视图"的策略，比"一套代码两端都将就"更可控。

**搜索为何选 Meilisearch 而非 Elasticsearch？**
校园跳蚤市场的商品量级通常在十万级，ES 的分片/JVM 调优成本无法被数据量正当化。Meilisearch 单二进制、内置中文分词、原生支持 `filter` + `sort` + 分面统计，恰好覆盖"价格区间/成色/校区筛选 + 时间/热度排序"。若后期需要复杂聚合，`search-service` 内部的 `SearchEngine` 接口可替换实现，业务代码不变。

**"距离排序"如何实现？**
不做真实 GPS 地理围栏。校园场景的"距离"实质是**校区 + 交易地点（校内 POI）** 的层级关系：同校区 > 同校不同校区 > 邻校。POI 表存经纬度，用于地图选点展示与同校区内的粗排（Haversine 计算，数据量小可在 Meilisearch 的 `_geo` 字段上做）。

## 3. 总体架构

```
                      ┌──────────────┐   ┌──────────────┐
   浏览器 (Next.js)   │  管理后台    │   │ 微信小程序   │
        │             │ (Next.js)    │   │  (Taro,后期) │
        └──────┬──────┴──────┬───────┘   └──────┬───────┘
               │ HTTPS/JSON  │                  │
               ▼             ▼                  ▼
        ┌───────────────────────────────────────────────┐
        │   api-gateway (kratos HTTP)  统一入口 BFF     │
        │  · JWT 鉴权 / 限流 / CORS / 请求聚合          │
        │  · grpc-gateway 风格：proto 定义 http 注解    │
        │  · WebSocket 反向代理 → im-service            │
        └───────────────────────┬───────────────────────┘
                                │ gRPC (etcd discovery)
   ┌───────────┬───────────┬────┴──────┬───────────┬───────────┬──────────┐
   ▼           ▼           ▼           ▼           ▼           ▼          ▼
┌──────┐  ┌────────┐  ┌────────┐  ┌────────┐  ┌────────┐  ┌────────┐ ┌───────┐
│user  │  │product │  │search  │  │trade   │  │im      │  │review  │ │admin  │
│svc   │  │svc     │  │svc     │  │svc     │  │svc     │  │svc     │ │svc    │
└──┬───┘  └───┬────┘  └───┬────┘  └───┬────┘  └───┬────┘  └───┬────┘ └───┬───┘
   │          │           │           │           │           │          │
   └──────────┴───────────┴─────┬─────┴───────────┴───────────┴──────────┘
                                │
      ┌────────┬────────┬───────┼────────┬──────────┬──────────┐
      ▼        ▼        ▼       ▼        ▼          ▼          ▼
   MySQL8   Redis7   etcd3  Meilisearch  S3/MinIO  notify-svc(消费MQ)
```

### 3.1 服务清单（8 个应用）

| 应用 | 端口(HTTP/gRPC) | 对外 | 职责 |
|------|------|------|------|
| `api-gateway` | 8000 / — | 是 | 唯一 HTTP 入口，鉴权、聚合、WS 代理 |
| `user-service` | 8001 / 9001 | 否 | 账号、登录、校园认证、个人主页、关注/拉黑、信誉分 |
| `product-service` | 8002 / 9002 | 否 | 商品、分类、图片元数据、收藏、求购帖、校园 POI |
| `search-service` | 8003 / 9003 | 否 | 索引同步、信息流、关键词搜索、筛选排序 |
| `trade-service` | 8004 / 9004 | 否 | 预订/订单、状态机、双向评价 |
| `im-service` | 8005 / 9005 | 是(WS) | WebSocket 长连接、会话、消息持久化、砍价卡片 |
| `review-service` | 8006 / 9006 | 否 | 内容审核（敏感词+违禁品）、举报工单 |
| `notify-service` | 8007 / 9007 | 否 | 站内信、降价/上新提醒、消费 MQ 事件 |
| `admin-service` | 8008 / 9008 | 否 | 轮播图、公告、活动专区、运营配置、工单处置 |

> `api-gateway` 与 `admin-service` 是两个不同层次：前者是流量入口，后者是运营配置的领域服务；管理后台前端的请求同样经 `api-gateway`，由网关按 `/admin/*` 路由并校验 `role=ADMIN`。

## 4. 工程目录结构

遵循 kratos 官方"单仓多应用"（monorepo layout，`kratos new -r` 生成的 `app/` 布局）方案。

```
/Users/v_lidongze/dev/fleaMarket/
├── go.work                          # Go workspace，聚合 pkg 与各 app
├── Makefile                         # 顶层：make api / make build / make migrate / make docs
├── docker-compose.yml               # mysql / redis / etcd / meilisearch / minio
├── buf.work.yaml                    # buf 工作区，指向 api/
│
├── api/                             # ★ proto 契约唯一来源（独立 buf module，可单独 push 到 BSR）
│   ├── buf.yaml                     # name: buf.build/fleamarket/api
│   ├── buf.gen.yaml                 # 生成 go / go-grpc / http / errors / openapi
│   ├── buf.gen.ts.yaml              # 生成前端 TS SDK（connect-es）
│   ├── common/v1/                   # 跨服务公共 message：Pagination / Money / Campus / Enum
│   ├── user/v1/{user.proto,auth.proto,campus_auth.proto}
│   ├── product/v1/{product.proto,category.proto,favorite.proto,wanted.proto,poi.proto}
│   ├── search/v1/search.proto
│   ├── trade/v1/{order.proto,review.proto}
│   ├── im/v1/{conversation.proto,message.proto}
│   ├── review/v1/{moderation.proto,report.proto}
│   ├── notify/v1/notification.proto
│   └── admin/v1/{banner.proto,announcement.proto,activity.proto}
│
├── pkg/                             # ★ 公共库：泛用性高、零业务依赖（见第 5 节）
│   ├── go.mod                       # module github.com/fleamarket/pkg —— 独立模块，禁止 import 任何 app
│   ├── orm/                         # GORM 封装 + entity registry + migrator
│   ├── cache/                       # Redis 抽象：Cacher 接口、缓存击穿保护、分布式锁
│   ├── mq/                          # Producer/Consumer 接口 + Redis Stream 实现
│   ├── registry/                    # etcd registry 构造器（含 TLS、租约参数）
│   ├── auth/                        # JWT 签发校验、Claims 载体、kratos middleware
│   ├── idgen/                       # Snowflake ID
│   ├── storage/                      # 对象存储接口 + S3 实现 + 预签名 URL
│   ├── paging/                      # 游标分页 / 偏移分页的泛型工具
│   ├── errs/                        # 错误码规范、kratos errors 转换
│   ├── validate/                    # 请求校验 middleware（protovalidate）
│   ├── log/                         # 日志（zap）+ traceID 注入
│   ├── trace/                       # OpenTelemetry 初始化
│   ├── sensitive/                   # 敏感词匹配算法（AC 自动机）—— 词库由业务注入
│   └── transport/                   # gRPC client 工厂（连接池、超时、熔断、重试）
│
├── app/
│   ├── user/
│   │   ├── cmd/user/main.go         # 入口，wire_gen
│   │   ├── configs/config.yaml
│   │   └── internal/                # ★ 业务代码全部在 internal 下，跨服务无法 import
│   │       ├── conf/                # 配置 proto 生成物
│   │       ├── server/              # grpc.go / http.go
│   │       ├── service/             # proto service 实现（DTO ↔ 领域对象）
│   │       ├── biz/                 # 领域逻辑 + Repo 接口定义
│   │       ├── data/                # Repo 实现、entity（GORM model）、外部服务 client
│   │       └── entity/              # ★ 表实体，向 pkg/orm registry 注册
│   ├── product/  ... 同构
│   ├── search/   ...
│   ├── trade/    ...
│   ├── im/       ...
│   ├── review/   ...
│   ├── notify/   ...
│   ├── admin/    ...
│   └── gateway/                     # api-gateway，仅 internal/server + internal/proxy
│
├── cmd/
│   └── dbctl/                       # ★ 建表脚本（见第 7 节）：migrate / dump / drop
│
├── deploy/                          # k8s manifest / Dockerfile 模板
├── docs/                            # 生成的 openapi.yaml + Redoc 静态站
└── web/                             # pnpm workspace
    ├── pnpm-workspace.yaml
    ├── apps/web/                    # Next.js 用户端
    ├── apps/admin/                  # Next.js 管理后台
    ├── apps/miniapp/                # Taro 小程序（后期）
    └── packages/
        ├── api-sdk/                 # buf 生成的 TS client（不手写）
        ├── core/                    # 纯 TS：校验、金额/时间格式化、状态机文案（零 DOM）
        └── ui/                      # 共享 React 组件（web/admin 复用，小程序不用）
```

### 4.1 `internal` 保护机制如何生效

Go 语言原生规则：`app/user/internal/...` 只能被 `app/user/` 下的包导入。因此：

- `app/product` 想用用户信息，**只能**通过 `api/user/v1` 的 gRPC 接口，不可能直接 import `app/user/internal/biz`。这从编译器层面强制了服务边界。
- `pkg/` 是独立 Go module，其 `go.mod` 中**不允许出现任何 `github.com/fleamarket/app/*` 依赖**，CI 用 `make lint-pkg-purity` 脚本扫描并在违规时失败。

## 5. 公共库设计（pkg/）

### 5.1 设计铁律

1. **零业务语义**：`pkg` 中不得出现 `Product`、`Order`、`Campus` 等业务名词。反例：`pkg/product/status.go` 是错的；正例：`pkg/statemachine` 提供泛型状态机，商品状态在 `app/product/internal/biz` 中声明。
2. **接口在 pkg，实现可替换**：对外暴露接口 + 一个默认实现。如 `mq.Producer` 接口 + `mq/redisstream` 实现，后续加 Kafka 实现不改调用方。
3. **依赖倒置**：需要业务数据时通过参数/回调注入，不反向依赖。如 `sensitive.Matcher` 的词库由 `review-service` 在启动时 `Load([]string)` 注入。
4. **配置由调用方给**：`pkg` 不读文件、不读环境变量，只接受 struct 参数。
5. **可独立测试**：每个包必须有不依赖 MySQL/Redis 之外任何东西的单测。

### 5.2 核心包接口定义

> **实施修正（Task 4 产出）**：kratos v3 的能力比文档初稿假设的更完整，以下两处因此大幅简化——
> - `pkg/log`：**不引入 zap**。v3 的 `log` 包已基于 `log/slog`，且自带 `WithFilter` + `FilterKey` 脱敏与 `WithExtractor` 上下文注入。本包退化为「统一约定 + 声明平台级敏感字段清单」，约 100 行。
> - `pkg/validate`：**不自行实现中间件**。v3 已有 `middleware/validate.Validator(ValidatorFunc)` 挂载点，本包只提供 protovalidate 的 `ValidatorFunc` 与错误信息整形（多条违规压成单行）。
>
> 少写代码即少维护面，这两处都改为「配置官方能力」而非「另造一套」。


**pkg/orm —— 实体注册与建表能力（支撑第 7 节）**

```go
package orm

// Entity 是所有可建表对象必须实现的接口。泛用性体现在：
// 只要求"我叫什么表名"，不约束任何字段。
type Entity interface {
    TableName() string
}

// Registry 是进程内的实体注册中心，线程安全。
type Registry struct {
    mu       sync.RWMutex
    entities map[string][]Entity // key: 业务分组名(如 "user")，用于分库/分服务迁移
}

func NewRegistry() *Registry
func (r *Registry) Register(group string, es ...Entity)
func (r *Registry) Groups() []string
func (r *Registry) Entities(group string) []Entity

// Default 供各服务 entity 包在 init() 中注册，dbctl 统一读取
var Default = NewRegistry()
func Register(group string, es ...Entity) { Default.Register(group, es...) }

// Migrator 执行建表 / 导出 DDL
type Migrator struct{ db *gorm.DB }
func NewMigrator(db *gorm.DB) *Migrator
func (m *Migrator) Migrate(ctx context.Context, es []Entity) error      // AutoMigrate
func (m *Migrator) DumpDDL(ctx context.Context, es []Entity) (string, error) // 输出 CREATE TABLE 快照
func (m *Migrator) Drop(ctx context.Context, es []Entity) error         // 仅 dev 环境允许
```

**pkg/transport —— gRPC 客户端工厂**

```go
type ClientOptions struct {
    Endpoint    string        // "discovery:///user-service"
    Timeout     time.Duration // 默认 2s
    Registry    registry.Discovery
    Middlewares []middleware.Middleware
}
// NewGRPCConn 内置：tracing、metrics、recovery、metadata 透传、
// 指数退避重试（仅幂等方法，由 proto option 标记）
func NewGRPCConn(ctx context.Context, o ClientOptions) (*grpc.ClientConn, error)
```

**pkg/cache —— 防击穿的泛型缓存**

```go
// Load 语义：缓存命中直接返回；未命中则加分布式锁回源，
// 空结果写入短 TTL 空值占位（防穿透），返回 ErrNotFound。
func Load[T any](ctx context.Context, c Cacher, key string,
    ttl time.Duration, fn func(ctx context.Context) (T, error)) (T, error)
```

**pkg/paging —— 游标分页**

```go
type Cursor struct { SortValue string; ID int64 }  // 复合游标，避免同值跳页
func EncodeCursor(c Cursor) string
func DecodeCursor(s string) (Cursor, error)
type Page[T any] struct { Items []T; NextCursor string; HasMore bool }
```

**pkg/statemachine —— 泛型状态机**（商品状态、订单状态共用）

```go
type Machine[S comparable, E comparable] struct{ transitions map[S]map[E]S }
func New[S, E comparable]() *Machine[S, E]
func (m *Machine[S, E]) Allow(from S, event E, to S) *Machine[S, E]
func (m *Machine[S, E]) Next(from S, event E) (S, error) // 非法流转返回 ErrIllegalTransition
```

## 6. protobuf 契约：文档化 + 免拷贝生成客户端

这是需求中技术含量最高的一条，拆成三个独立能力来满足。

### 6.1 契约唯一来源：`api/` 作为 Buf Module

`api/buf.yaml`：

```yaml
version: v2
modules:
  - path: .
    name: buf.build/fleamarket/api
lint:
  use: [STANDARD]
  except: [PACKAGE_DIRECTORY_MATCH]
breaking:
  use: [FILE]      # CI 中对比 main 分支，破坏性变更直接卡住 MR
deps:
  - buf.build/googleapis/googleapis
  - buf.build/bufbuild/protovalidate
```

### 6.2 能力一：文档效果（proto 注释 → 可浏览的 API 文档站）

链路：proto 注释 → `protoc-gen-openapi` → `docs/openapi.yaml` → Redoc 静态页 → 由 `api-gateway` 在 `/docs` 路径托管。

```yaml
# api/buf.gen.yaml
version: v2
plugins:
  - local: protoc-gen-go
    out: ../
    opt: [paths=source_relative]
  - local: protoc-gen-go-grpc
    out: ../
    opt: [paths=source_relative]
  - local: protoc-gen-go-http        # kratos：由 http option 生成 HTTP 路由
    out: ../
    opt: [paths=source_relative]
  - local: protoc-gen-go-errors      # kratos：错误码枚举 → Go 错误构造器
    out: ../
    opt: [paths=source_relative]
  - local: protoc-gen-openapi        # ★ 文档
    out: ../docs
    opt: [naming=json, default_response=false]
```

proto 中用注释和 `openapi.v3` 扩展写文档，示例：

```protobuf
// ProductService 提供闲置商品的发布、检索与生命周期管理。
//
// 状态机：ON_SALE ──reserve──> RESERVED ──complete──> SOLD
//                  └──offline──> OFFLINE ──republish──> ON_SALE
service ProductService {
  // 发布闲置商品。
  //
  // 图片需先调用 `StorageService.CreateUploadURL` 直传对象存储，
  // 此处只提交返回的 object_key 列表（最多 9 张，第 1 张为封面）。
  // 发布后商品进入 `PENDING_REVIEW`，审核通过才对外可见。
  rpc CreateProduct(CreateProductRequest) returns (CreateProductReply) {
    option (google.api.http) = { post: "/v1/products", body: "*" };
  }
}

message CreateProductRequest {
  // 商品标题，2~60 字符，禁止联系方式。
  string title = 1 [(buf.validate.field).string = {min_len: 2, max_len: 60}];
  // 价格，单位：分。0 表示"免费赠送"。
  int64 price_cents = 2 [(buf.validate.field).int64.gte = 0];
  // 成色等级。
  Condition condition = 3 [(buf.validate.field).enum.defined_only = true];
  // 图片 object_key 列表，1~9 个。
  repeated string image_keys = 4 [(buf.validate.field).repeated = {min_items: 1, max_items: 9}];
}
```

`buf.validate` 注解一石二鸟：既是文档中的字段约束说明，又被 `pkg/validate` 的 protovalidate middleware 在运行时强制执行，**校验规则不用在 Go 和 TS 里各写一遍**。

### 6.3 能力二：其他客户端无需拷贝 proto 即可生成代码

三条并行路径，覆盖不同类型的消费者：

**路径 A（推荐，面向开发者）—— Buf 远程模块依赖。**
CI 在 `api/` 变更合并后执行 `buf push`，将模块推送到 BSR（`buf.build/fleamarket/api`，或自建 `bufbuild/buf` registry 服务部署在内网）。任何外部客户端只需在自己仓库写 `buf.gen.yaml` 与一行依赖声明，然后：

```bash
buf generate buf.build/fleamarket/api --path product/v1
```

proto 文件始终留在服务端仓库，客户端本地**没有任何 .proto 文件**。

**路径 B（面向不用 buf 的团队）—— gRPC Server Reflection。**
所有服务的 gRPC Server 注册 `reflection.Register(srv)`。客户端用 `grpcurl` / `buf curl` 直接从运行中的服务拉取描述符：

```bash
grpcurl -plaintext localhost:9002 describe product.v1.ProductService
buf curl --schema localhost:9002 --protocol grpc ...
```

同时 CI 产出 `docs/descriptor.binpb`（`buf build -o`），这是一个自包含的 FileDescriptorSet，客户端下载这一个文件即可 `protoc --descriptor_set_in` 生成任意语言代码。

**路径 C（面向前端/移动端）—— 预生成 SDK 包。**
CI 跑 `buf generate --template buf.gen.ts.yaml` 产出 TS client，发布为 `@fleamarket/api-sdk` 到私有 npm registry；Go SDK 则因 `api/` 在同仓库而天然可 `import`。前端 `package.json` 里装包即用，完全不接触 protobuf 工具链。

### 6.4 能力三：契约演进安全

- `buf breaking --against '.git#branch=main'` 在 CI 阻断破坏性变更。
- 所有 package 带 `v1` 版本号，不兼容变更走 `v2` 新包并行期。
- `buf lint` 强制命名规范与注释完整性。

## 7. 建表脚本：根据实体对象创建数据库表

### 7.1 机制

三步：实体声明 → init 注册 → `dbctl` 统一执行。

**Step 1 —— 服务内声明实体**（`app/product/internal/entity/product.go`）：

```go
package entity

import (
    "time"
    "gorm.io/gorm"
    "github.com/fleamarket/pkg/orm"
)

const Group = "product"   // 该服务的实体分组

type Product struct {
    ID          int64          `gorm:"primaryKey;autoIncrement:false;comment:雪花ID"`
    SellerID    int64          `gorm:"not null;index:idx_seller_status,priority:1;comment:卖家ID"`
    Title       string         `gorm:"type:varchar(60);not null;comment:标题"`
    Description string         `gorm:"type:varchar(2000);comment:描述"`
    PriceCents  int64          `gorm:"not null;index:idx_campus_price,priority:2;comment:价格(分)"`
    Condition   uint8          `gorm:"not null;comment:成色 1-全新 2-几乎全新 3-轻微使用 4-明显使用"`
    CategoryID  int32          `gorm:"not null;index;comment:分类ID"`
    CampusID    int32          `gorm:"not null;index:idx_campus_price,priority:1;comment:校区ID"`
    POIID       *int64         `gorm:"comment:交易地点POI"`
    Status      uint8          `gorm:"not null;default:0;index:idx_seller_status,priority:2;comment:0-待审 1-在售 2-已预订 3-已售出 4-下架 5-审核驳回"`
    CoverKey    string         `gorm:"type:varchar(255);comment:封面对象key"`
    ViewCount   int64          `gorm:"not null;default:0"`
    FavCount    int32          `gorm:"not null;default:0"`
    HeatScore   float64        `gorm:"not null;default:0;index;comment:热度分,定时任务重算"`
    CreatedAt   time.Time      `gorm:"index"`
    UpdatedAt   time.Time
    DeletedAt   gorm.DeletedAt `gorm:"index"`
}

func (Product) TableName() string { return "t_product" }

func init() {
    orm.Register(Group,
        Product{}, ProductImage{}, Category{}, Favorite{},
        WantedPost{}, CampusPOI{}, PriceHistory{},
    )
}
```

**Step 2 —— dbctl 聚合所有分组**（`cmd/dbctl/main.go`）：

```go
// 关键：blank import 各服务的 entity 包，触发其 init() 注册。
// dbctl 位于仓库根的 cmd/ 下，与 app/*/internal 同属一个 module 树？—— 不是。
// 因此实体注册采用"每个服务自带 migrate 子命令"+ "dbctl 通过 go:generate 汇总"两种方式，
// 实际落地选择：dbctl 作为独立 module，通过 go.work 引用各 app module，
// 而 entity 包上移到 app/<svc>/entity（非 internal），使 dbctl 可导入；
// 业务逻辑仍全部留在 internal 下受保护。
import (
    _ "github.com/fleamarket/app/product/entity"
    _ "github.com/fleamarket/app/user/entity"
    // ... 其余服务
)
```

> **设计决策说明**：`internal` 保护与"统一建表脚本"存在天然张力。解法是把**纯数据结构定义**（entity，只有字段+tag+TableName，无任何行为）放在 `app/<svc>/entity/`（导出），而 **Repo 实现、领域逻辑、service** 全部留在 `app/<svc>/internal/`。这样其他服务即使 import 了 entity 也拿不到任何可执行的业务能力，不构成边界泄漏；同时满足需求"业务功能代码用 internal 保护"。

**Step 3 —— 命令行**：

```bash
make migrate                       # 全部分组 AutoMigrate
go run ./cmd/dbctl migrate --group product,user
go run ./cmd/dbctl dump --out deploy/sql/schema.sql   # 导出 DDL 快照，纳入版本管理
go run ./cmd/dbctl drop --group product --yes         # 仅 APP_ENV=dev 时允许
```

### 7.2 安全约束

- `drop` 子命令读取 `APP_ENV`，非 `dev` 直接拒绝执行并返回非零退出码。
- `migrate` 默认只做**增量加列/加索引**，GORM AutoMigrate 本身不删列、不改窄字段类型，规避数据丢失。
- 需要删列/改类型时，走 `deploy/sql/manual/NNN_xxx.sql` 人工审核脚本，`dbctl` 不承担。
- `dump` 的产出纳入 git，任何 schema 变更在 code review 中可见——这是"实体驱动建表"最容易失控的点，用快照 diff 来兜住。

## 8. etcd 服务注册发现 + gRPC 通讯

### 8.1 公共库封装（`pkg/registry`）

```go
package registry

type Config struct {
    Endpoints   []string
    DialTimeout time.Duration // 默认 5s
    Username    string
    Password    string
    Namespace   string        // 默认 "/microservices"，多环境隔离用 "/fleamarket/{env}"
}

// New 返回 kratos 的 Registrar 与 Discovery（etcd 实现同时满足两者）
func New(c Config) (*etcd.Registry, error)
```

### 8.2 服务端注册（每个 app 的 `cmd/<svc>/main.go`）

```go
reg, err := pkgreg.New(pkgreg.Config{
    Endpoints: bc.Registry.Etcd.Endpoints,
    Namespace: "/fleamarket/" + env,
})
app := kratos.New(
    kratos.ID(id), kratos.Name(Name), kratos.Version(Version),
    kratos.Metadata(map[string]string{"region": "campus-main"}),
    kratos.Server(gs, hs),
    kratos.Registrar(reg),          // ★ 自动注册 + 优雅下线时注销
)
```

etcd key 形如 `/fleamarket/prod/product-service/{instanceID}`，value 为实例 JSON（含 `grpc://ip:9002`、`http://ip:8002`），带 lease 心跳，进程崩溃后 TTL 到期自动摘除。

### 8.3 客户端发现（跨服务调用）

```go
// app/trade/internal/data/product_client.go
func NewProductClient(d registry.Discovery, l log.Logger) (productv1.ProductServiceClient, error) {
    conn, err := transport.NewGRPCConn(context.Background(), transport.ClientOptions{
        Endpoint: "discovery:///product-service",   // ★ kratos 解析 discovery scheme
        Registry: d,
        Timeout:  2 * time.Second,
    })
    if err != nil { return nil, err }
    return productv1.NewProductServiceClient(conn), nil
}
```

`pkg/transport` 内置的 middleware 链：`recovery → tracing → metrics → circuitbreaker → metadata 透传（traceID/userID）`，并对标记为幂等的方法做 3 次指数退避重试。

### 8.4 服务依赖关系（避免循环调用）

```
gateway → 所有服务
product → user(查卖家摘要)、review(送审)
trade   → product(校验商品状态+锁定)、user(信誉分变更)
search  → 无同步依赖（消费 product 的 MQ 事件建索引）
im      → user(校验拉黑)、product(会话绑定的商品卡片)
notify  → 无同步依赖（纯 MQ 消费）
review  → 无同步依赖（审核结果通过 MQ 回写 product）
admin   → product / review（运营干预）
```

原则：**同步 RPC 只允许单向**，需要反向通知的场景一律走 MQ 事件。这保证不存在 A→B→A 的调用环。

## 9. 各服务职责与数据表设计

### 9.1 user-service（R1、R7 部分）

| 表 | 关键字段 | 说明 |
|----|----------|------|
| `t_user` | id, phone(唯一,加密存储), wx_openid, wx_unionid, nickname, avatar_key, status, credit_score, created_at | 账号主表；phone 用 AES-GCM 加密 + `phone_hash` 列做唯一索引与检索 |
| `t_campus_auth` | id, user_id, school_id, campus_id, student_no, edu_email, method(STUDENT_NO/EDU_EMAIL), status(PENDING/PASSED/REJECTED), verified_at, expire_at | 校园认证记录；支持毕业后过期 |
| `t_school` / `t_campus` | id, name, province, domain_suffix / school_id, name, lat, lng | 学校与校区字典；`domain_suffix` 用于校验校园邮箱 |
| `t_user_follow` | follower_id, followee_id, created_at | 关注关系 |
| `t_user_block` | user_id, blocked_user_id, reason, created_at | 拉黑；IM 与商品咨询处双向拦截 |
| `t_credit_log` | user_id, delta, reason_code, ref_id, created_at | 信誉分流水，保证分值可追溯可回滚 |

**登录方式**：
- 手机号：短信验证码（`pkg/cache` 存 5 分钟，同号 60 秒限流 + 每日上限），验证成功签发双 token（access 2h / refresh 30d）。
- 微信一键登录：Web 端走微信开放平台扫码授权（`code` → `access_token` → `unionid`）；小程序期走 `wx.login` 的 `code2session`。两端以 `wx_unionid` 归一到同一账号。

**校园认证**：
- 校园邮箱：向 `xxx@stu.school.edu.cn` 发送带签名的一次性链接（30 分钟有效），域名后缀必须命中 `t_school.domain_suffix` 白名单。
- 学号：学号 + 姓名 + 学生证照片，进人工审核工单（`review-service` 处理）。
- 未认证用户：可浏览，**不可发布商品、不可发起聊天**。这是防诈骗的第一道闸门。

### 9.2 product-service（R2、R7）

| 表 | 关键字段 |
|----|----------|
| `t_product` | 见 7.1 代码 |
| `t_product_image` | id, product_id, object_key, sort, width, height |
| `t_category` | id, parent_id, name, icon_key, sort, enabled |
| `t_favorite` | user_id, product_id, created_at（联合主键） |
| `t_wanted_post` | id, user_id, title, desc, budget_max_cents, category_id, campus_id, status, expire_at |
| `t_campus_poi` | id, campus_id, name, lat, lng, type(GATE/DORM/CANTEEN/LIBRARY) |
| `t_price_history` | id, product_id, old_price_cents, new_price_cents, created_at |

**分类体系初始数据**：教材书籍、数码电子、日用百货、服饰鞋包、自行车/交通、票券卡券、运动器材、乐器、美妆个护、其他（两级，二级如"教材书籍 → 专业教材/考研资料/课外读物"）。

**商品状态机**（用 `pkg/statemachine` 声明）：
```
PENDING_REVIEW --pass--> ON_SALE        --reserve--> RESERVED
PENDING_REVIEW --reject--> REJECTED     RESERVED --cancel--> ON_SALE
ON_SALE --offline--> OFFLINE            RESERVED --complete--> SOLD
OFFLINE --republish--> PENDING_REVIEW   SOLD (终态)
```

**多图上传**：前端调 `StorageService.CreateUploadURL` 拿到 S3 预签名 PUT URL 直传，图片不经过业务服务（省带宽、省内存）。发布时只提交 `object_key`。服务端校验 key 前缀属于该用户的临时目录、且通过 HEAD 确认对象存在与 `Content-Type` 为图片。

**降价触发提醒**：改价时写 `t_price_history` 并发 `product.price_dropped` 事件，`notify-service` 消费后向收藏该商品的用户推送。

### 9.3 search-service（R3）

- 订阅 `product.created/updated/status_changed/deleted` 事件，维护 Meilisearch 索引 `products`。
- 索引字段：`id, title, description, price_cents, condition, category_id, campus_id, school_id, seller_id, status, created_at, heat_score, _geo`。
- `filterableAttributes`: category_id / campus_id / school_id / condition / price_cents / status；`sortableAttributes`: created_at / price_cents / heat_score / _geoPoint。
- **同校/同校区优先**：不依赖搜索引擎的 boost，而是分两路查询后归并——第一路 `filter: campus_id = me`，第二路 `filter: school_id = me AND campus_id != me`，第三路全局；按配额（如 6:3:1）交错填充分页。这样"优先"是确定性的、可解释的，不会因相关度评分波动而失效。
- **热度分**：`heat = log10(view+1)*1.0 + fav*3.0 + inquiry*5.0 - 时间衰减(小时数^1.5 * 0.05)`，定时任务每 10 分钟批量重算近 7 天商品。
- 全量重建索引：`ReindexAll` 管理接口，用别名切换（`products_v2` → swap）避免重建期间搜索不可用。

### 9.4 trade-service（R4 下单、R5 评价）

| 表 | 关键字段 |
|----|----------|
| `t_order` | id, product_id, buyer_id, seller_id, price_cents(成交价), status, pay_channel(固定OFFLINE), meet_poi_id, meet_time, reserved_at, completed_at, cancel_reason |
| `t_review` | id, order_id, from_user_id, to_user_id, role(BUYER/SELLER), score(1-5), tags, content, created_at |

**预订机制**（避免多人重复问）：
1. 买家发起预订 → `trade-service` 以 `product_id` 为键取 Redis 分布式锁（`pkg/cache.Lock`）。
2. 调 `ProductService.LockForOrder(product_id, order_id)`，该接口内部用 `UPDATE t_product SET status=RESERVED WHERE id=? AND status=ON_SALE`，**靠影响行数=1 判定抢占成功**，天然并发安全（分布式锁只是减少无效 DB 争抢）。
3. 卖家确认/拒绝；拒绝或超时 24h 未确认 → 定时任务回滚商品为 ON_SALE。
4. 面交完成后任一方点"完成"，另一方确认（或 72h 后自动完成）→ 商品 SOLD，双方进入互评期（7 天）。

**双向评价与信誉分**：评价提交后发 `trade.reviewed` 事件，`user-service` 消费并写 `t_credit_log`。信誉分 = 基础分 100 + Σ(好评 +2 / 中评 0 / 差评 -5) + 完成交易数加成 - 违规扣分，下限 0 上限 200。评价 7 天后不可修改。

### 9.5 im-service（R4 聊天）

| 表 | 关键字段 |
|----|----------|
| `t_conversation` | id, product_id, buyer_id, seller_id, last_msg_id, last_msg_at, buyer_unread, seller_unread |
| `t_message` | id, conversation_id, sender_id, type(TEXT/IMAGE/BARGAIN/SYSTEM/POI), content, extra_json, created_at, revoked |

- WebSocket 直连 `im-service`（`api-gateway` 仅做 TCP/HTTP 升级代理并完成 JWT 前置校验），连接建立后心跳 30s。
- 在线状态与路由表存 Redis（`im:online:{uid} → {nodeID}`）；多节点间用 Redis Pub/Sub 转发消息，实现水平扩展。
- 消息类型 `BARGAIN`（砍价卡片：出价金额 + 接受/拒绝按钮）、`POI`（约面交地点卡片，点击跳校园地图）。
- 发消息前校验：双方均非拉黑关系、发送方已完成校园认证、内容过 `review-service` 的敏感词同步检查（超时 300ms 则放行并异步复审，不因审核抖动阻断聊天）。
- 离线消息：拉取式（客户端带 `last_msg_id` 增量拉），不做推送重试队列，简化实现。

### 9.6 review-service（R5 安全审核）

| 表 | 关键字段 |
|----|----------|
| `t_moderation_task` | id, target_type(PRODUCT/WANTED/MESSAGE/USER_PROFILE), target_id, status, auto_result, auto_hit_words, reviewer_id, decided_at |
| `t_report` | id, reporter_id, target_type, target_id, reason_code, desc, image_keys, status, handled_by, handle_result |
| `t_sensitive_word` | id, word, category(违禁品/联系方式/学术不端/涉政涉黄), level(BLOCK/REVIEW/WARN) |

- 自动审核：`pkg/sensitive` 的 AC 自动机匹配（词库启动时从 `t_sensitive_word` 加载，变更时 Pub/Sub 热更新）。`BLOCK` 级直接驳回，`REVIEW` 级转人工，均无命中则自动通过。
- 违禁品类目内置词库：管制刀具、电子烟、药品、动物、代写作业/论文代写/考试代考、账号租借、转卖校园卡/饭卡余额、黄牛票、破解软件等。
- 学术不端专项：命中"代写/代考/答案/题库"等直接 BLOCK，这是校园应用的合规红线。
- 举报处置：同一目标被 3 个不同用户举报 → 自动临时下架并升级为高优工单。

### 9.7 notify-service（R6）

| 表 | 关键字段 |
|----|----------|
| `t_notification` | id, user_id, type, title, body, ref_type, ref_id, read_at, created_at |
| `t_notify_setting` | user_id, inquiry_on, reserve_on, review_on, price_drop_on, new_arrival_on |

消费事件 → 按用户设置过滤 → 写站内信 → 若在线则通过 `im-service` 的 WS 通道实时下发。小程序期追加微信订阅消息通道（`pkg/mq` 消费侧加一个 sender 实现即可，业务不变）。

「上新提醒」：用户可关注 `分类 + 校区` 组合，新商品审核通过后按组合扇出（关注数大时走批量分页扇出，避免长事务）。

### 9.8 admin-service（R8）

| 表 | 关键字段 |
|----|----------|
| `t_banner` | id, image_key, link_url, sort, start_at, end_at, enabled, campus_scope |
| `t_announcement` | id, title, content_md, level, pinned, start_at, end_at |
| `t_activity` | id, name, banner_key, rule_json, product_filter_json, start_at, end_at, enabled |
| `t_admin_user` / `t_admin_op_log` | 管理员账号（独立体系，不复用 t_user）/ 全量操作审计日志 |

活动专区可配置：通过 `product_filter_json`（分类/价格/校区/标签条件）动态圈选商品，无需为每个活动写代码。所有管理操作强制写 `t_admin_op_log`。

## 10. 前端方案

### 10.1 应用与路由

`web/apps/web`（用户端，Next.js App Router）：

```
/                       首页信息流（SSR 首屏 + 客户端无限滚动）
/c/[categorySlug]       分类浏览
/search?q=&price=&cond= 搜索结果（URL 即状态，可分享可回退）
/p/[productId]          商品详情（SSR + generateMetadata，利于收录）
/publish                发布/编辑商品（多步表单）
/u/[userId]             个人主页（在售商品 / 信誉分 / 交易记录）
/me/*                   我的：发布、收藏、订单、评价、认证、设置
/chat                   会话列表（响应式：移动端单栏切换，桌面端双栏）
/chat/[conversationId]  聊天窗口
/wanted                 求购帖广场
/activity/[id]          活动专区
/auth/*                 登录、微信回调、校园认证
```

`web/apps/admin`（管理后台）：轮播图、公告、活动、分类、敏感词、审核工单、举报处置、用户管理、数据看板。

### 10.2 响应式适配策略

移动优先（mobile-first），Tailwind 断点：`sm 640 / md 768 / lg 1024 / xl 1280 / 2xl 1536`。

| 区域 | 移动端 (<768) | 平板 (768~1024) | 桌面 (≥1024) |
|------|---------------|-----------------|--------------|
| 导航 | 底部 TabBar（首页/分类/发布/消息/我的） | 顶部导航 | 顶部导航 + 侧边分类树 |
| 商品列表 | 2 列瀑布流 | 3 列 | 4~5 列网格 |
| 商品详情 | 图片全宽轮播 + 底部固定操作栏 | 上下布局 | 左图右信息两栏 + 右侧推荐 |
| 聊天 | 列表与会话单栏互斥切换（路由驱动） | 单栏 | 双栏并列 |
| 筛选 | 底部抽屉（Sheet） | 顶部折叠面板 | 左侧常驻筛选栏 |
| 发布表单 | 单列分步 | 单列 | 两栏（左表单右预览） |

具体手段：
- 容器查询（`@container`）用于卡片组件，使同一卡片在不同容器宽度下自适应，而非依赖全局视口断点。
- 图片用 `next/image` + `sizes` 属性按断点请求不同尺寸；对象存储侧配图片处理参数生成 WebP 缩略图。
- 交互目标 ≥44×44px；表单控件在移动端使用原生输入类型（`type=tel/email/number`）触发对应键盘。
- `prefers-reduced-motion` 与 `prefers-color-scheme` 均尊重；深色模式用 Tailwind `dark:`。
- 可访问性：语义化标签、`aria-label`、键盘可达（Tab 顺序与 Esc 关闭）、焦点可见环、颜色对比度 ≥ 4.5:1。

### 10.3 数据层

- 所有请求走 `@fleamarket/api-sdk`（由 proto 生成，见 6.3 路径 C），不手写 fetch 封装。
- TanStack Query 管理缓存/重试/乐观更新；列表用 `useInfiniteQuery` 配合游标分页。
- 鉴权：access token 存内存 + refresh token 存 `httpOnly` cookie，401 时自动静默刷新一次并重放请求。
- WebSocket 用独立 `ChatSocketProvider`，断线指数退避重连，重连后按 `last_msg_id` 补拉。

### 10.4 小程序演进（后期）

新增 `web/apps/miniapp`（Taro 4 + React），复用：
- `packages/api-sdk`：把 transport 层从 `fetch` 换成 `Taro.request` 适配器，接口定义完全不动。
- `packages/core`：表单校验规则、金额/时间格式化、状态机文案、筛选参数序列化——全部零 DOM 依赖，直接复用。
- 不复用 `packages/ui`（DOM 组件），小程序 UI 层重写。
- 登录改为 `wx.login` + `code2session`，服务端 `user-service` 已按 `unionid` 归一账号，无需改动。

## 11. 关键数据流

**流 A：发布商品**
```
前端 → CreateUploadURL(gateway→product) → 返回预签名URL
前端 → S3 直传图片
前端 → CreateProduct(gateway→product)
product: 校验(已认证/字段/图片存在) → 写 t_product(status=PENDING_REVIEW) → 发 product.created
review  消费 → AC自动机匹配 → 无命中则 ModerationPassed 事件
product 消费 → status=ON_SALE → 发 product.status_changed
search  消费 → 写 Meilisearch 索引
notify  消费 → 向关注"该分类+校区"的用户推上新提醒
```

**流 B：买家预订**
```
前端 → CreateOrder(gateway→trade)
trade: Redis锁(product_id) → product.LockForOrder(乐观UPDATE影响行数=1)
     → 写 t_order(status=RESERVED) → 发 trade.order_created
im     消费 → 向会话插入 SYSTEM 消息"买家已预订"
notify 消费 → 通知卖家
search 消费 product.status_changed → 索引 status=RESERVED（列表标"已预订"）
```

**流 C：即时聊天**
```
客户端 WS → gateway(JWT校验) → im-service 节点A
im: 校验拉黑/认证 → review.CheckText(超时300ms放行) → 写 t_message
  → 更新 t_conversation(未读数+1) → 查 Redis 对端在线节点
  → 同节点直投 / 跨节点 Redis Pub/Sub 转发 / 离线则发 MQ 给 notify
```

## 12. 边界条件与异常处理

| 场景 | 处理策略 |
|------|----------|
| 短信验证码轰炸 | 同手机号 60s 间隔 + 单日 10 条；同 IP 单小时 30 条；超限返回 `429 TOO_MANY_REQUESTS` |
| 多人同时预订同一商品 | 乐观锁 `UPDATE ... WHERE status=ON_SALE`，失败方返回 `409 PRODUCT_ALREADY_RESERVED` |
| 预订后卖家失联 | 24h 未确认，定时任务自动取消并释放商品；买家侧信誉不受影响 |
| 面交后无人点完成 | 预订确认 72h 后自动置为已完成，进入评价期 |
| 图片直传后未提交发布 | 临时目录对象设 24h 生命周期规则自动清理 |
| 校园邮箱链接被转发 | 链接内含 `user_id + email + exp` 的 HMAC 签名，且仅当前登录用户可消费，一次性失效 |
| 毕业生身份过期 | `t_campus_auth.expire_at` 到期后降级为"未认证"，仅保留浏览与历史订单查看 |
| 敏感词服务不可用 | 商品发布：降级为强制人工审核（不放行）；聊天消息：降级放行 + 异步复审（可用性优先） |
| Meilisearch 不可用 | 搜索接口降级为 MySQL `LIKE` + 分类过滤（有损但可用），并在响应中标记 `degraded=true` |
| etcd 短暂不可用 | kratos discovery 保留最后一次实例快照继续服务，注册失败重试；不因注册中心抖动导致全站不可用 |
| MQ 消费失败 | 3 次重试后进死信 Stream，管理后台可见并支持手动重投；索引类事件另有全量重建兜底 |
| 跨服务 RPC 超时 | 默认 2s 超时；非关键路径（如展示卖家信誉分）失败时返回占位值不阻断主流程 |
| 数据库唯一键冲突 | 统一在 `pkg/errs` 中转为 `409 ALREADY_EXISTS`，不向客户端暴露 SQL 细节 |
| 未认证用户尝试发布/聊天 | 返回 `403 CAMPUS_AUTH_REQUIRED`，前端引导至认证页 |
| 被拉黑用户发起会话 | 返回成功但消息不投递（静默拦截），不告知对方已拉黑，避免激化冲突 |

统一错误响应（由 kratos errors + protoc-gen-go-errors 生成）：
```json
{ "code": 409, "reason": "PRODUCT_ALREADY_RESERVED", "message": "商品已被预订", "metadata": {"product_id":"123"} }
```

## 13. 安全与风险

- **不做线上支付**：见 1.3。同时在商品描述、聊天中拦截"扫码付款/加微信转账"类诱导话术，并在首次聊天弹出防诈骗提示。
- **鉴权**：`api-gateway` 统一校验 JWT，将 `user_id`/`role`/`campus_id` 注入 gRPC metadata；下游服务信任 metadata 但**仍校验资源归属**（如改商品必须 `seller_id == caller`），不依赖网关单点。
- **数据保护**：手机号 AES-GCM 加密存储，密钥走环境变量/KMS；对外接口一律返回脱敏手机号；日志中间件对 `phone/password/code/token` 字段做自动脱敏。
- **越权防护**：所有 `Get/Update/Delete` 按 `owner_id` 过滤；管理接口二次校验 `role=ADMIN` 且写审计日志。
- **限流**：网关按 IP + 用户双维度令牌桶；发布、举报、发消息等写接口单独配额。
- **上传安全**：预签名 URL 限定 `Content-Type` 与最大 5MB，服务端校验真实魔数而非仅信任扩展名。
- **越界风险提示**：校园身份认证涉及学号与学生证照片，属敏感个人信息。需在注册流程提供隐私政策、明确留存期限（认证通过后照片 30 天内删除，仅保留认证结论），并支持账号注销时彻底清除。

## 14. 预期结果

完成后应达成：

1. `docker compose up` 拉起 MySQL/Redis/etcd/Meilisearch/MinIO，`make migrate` 一键建出全部表。
2. `make run` 启动 9 个 Go 应用，`etcdctl get --prefix /fleamarket` 可见全部实例注册信息。
3. 访问 `http://localhost:8000/docs` 看到由 proto 注释生成的完整 API 文档站。
4. 在一个**空目录**中仅凭 `buf.gen.yaml` 执行 `buf generate buf.build/fleamarket/api` 即产出可用客户端代码，全程无任何本地 proto 文件。
5. Web 端在 375px / 768px / 1440px 三档宽度下布局均正确可用，Lighthouse 移动端性能 ≥ 85、可访问性 ≥ 95。
6. 端到端可跑通：注册 → 校园认证 → 发布商品（过审核）→ 搜索到 → 聊天砍价 → 预订 → 完成 → 双向评价 → 信誉分变更 → 收到通知。
7. `pkg/` 中不存在任何业务名词，`make lint-pkg-purity` 通过；各服务业务代码均在 `internal/` 下。

## 15. 环境前置条件

当前工作区实测结果（已复核）：

| 项 | 状态 | 说明 |
|----|------|------|
| Go 工具链 | **已就绪** | `go1.23.2 darwin/arm64`，GOROOT=`/Users/v_lidongze/go/go1.23.2`，GOPATH=`/Users/v_lidongze/go` |
| Go module proxy | **已就绪** | `GOPROXY=https://goproxy.baidu-int.com,direct`，实测可拉取 `kratos v2.9.2` |
| kratos CLI | 已装 v3.0.0 | `~/go/bin/kratos` |
| protoc | 已装 | `/opt/homebrew/bin/protoc` |
| Node | 已装 v24.21.0 | corepack 0.36.0 可用 |
| Docker CLI | 已装 v24.0.2 | **daemon 未运行**，需启动 Docker Desktop 后才能起依赖中间件 |
| **buf** | **已装 v1.73.0** | Homebrew 安装卡死（疑似无 GitHub 直连），改用 `go install github.com/bufbuild/buf/cmd/buf@latest` 走内网 proxy 成功 |
| **pnpm** | **已装 v12.8.1** | `corepack enable pnpm` |
| git 仓库 | **已初始化** | `buf breaking --against '.git#branch=main'` 依赖 git |
| etcd / MySQL / Redis / Meilisearch / MinIO | 不需本机安装 | 由 docker-compose 提供 |

因 `go` 不在默认 PATH 中，顶层 `Makefile` 将显式声明：

```makefile
export GOROOT := /Users/v_lidongze/go/go1.23.2
export PATH   := $(GOROOT)/bin:$(HOME)/go/bin:$(PATH)
```

这样无需改动 shell 配置即可 `make` 全流程可复现。

> 关于 kratos 版本：**实测结论已推翻文档初稿的 v2.9.2 假设**。本机 CLI v3.0.0 生成的官方 layout 依赖 `github.com/go-kratos/kratos/v3 v3.0.0`，内网 proxy 可正常拉取，`go build ./...` 实测通过。因此框架统一采用 **v3**。详见 §15.2 模板实测记录。

### 15.2 kratos v3 官方模板实测记录（Task 2 产出）

命令（**必须带 `-r`**，v3 CLI 无 `-r` 时会尝试交互选择模板，在非 TTY 环境下 panic）：

```bash
kratos new <svc> -r https://github.com/go-kratos/kratos-layout -t 120s
```

实测产出与结论：

| 项 | 实测结果 | 对本项目的影响 |
|----|----------|----------------|
| 分层目录 | `internal/{conf,server,service,biz,data}` | **与 doc §4 假设一致**，无需调整 |
| 框架版本 | `github.com/go-kratos/kratos/v3 v3.0.0` | 推翻 v2.9.2，全局改用 v3 |
| Go 版本 | `go.mod` 声明 `go 1.25.7` | `GOTOOLCHAIN=auto` 自动下载，构建已验证通过 |
| 日志 | v3 改用标准库 `log/slog`（`*slog.Logger`），不再是 v2 的 `log.Logger` 接口 | **`pkg/log` 需重新设计为 slog Handler**，而非 zap→kratos 适配器 |
| 链路追踪 | `github.com/go-kratos/kratos/contrib/otel/v3` + `log.WithExtractor(tracing.TraceAttrs)` | `pkg/trace` 直接复用 contrib，traceID 注入由 slog extractor 完成 |
| 数据层 | 模板内置 **Ent** + Google AIP 资源风格（`go.einride.tech/aip`） | **本项目仍用 GORM**，`scripts/new-service.sh` 剥离 `internal/data/ent/**`，理由见下 |
| 契约工具 | 模板自带 `buf.yaml` / `buf.gen.yaml` 且 `api/` 在服务目录内，并生成单服务 `openapi.yaml` | **本项目改为集中式根 `api/`**，剥离各服务的 buf 文件，理由见下 |
| 配置 | `conf.proto` 仅有 `Server`/`Data`，**无 registry 段** | 需自行扩展 `Registry`（etcd）配置段 |
| etcd 注册 | `contrib/registry/etcd/v3 v3.0.0-20260626125723` 可用 | 满足需求，`pkg/registry` 基于它封装 |

**为何偏离模板的 Ent 改用 GORM**：需求明确要求"脚本根据实体对象创建数据库表"，且本项目是 9 服务、30+ 表的单仓。Ent 是按 schema 包各自 codegen 的（单个实体即产出约 50 个文件，且每个服务一套独立 client），做不到"一条命令建全库"；GORM + `pkg/orm` 集中 Registry 可以做到 `make migrate` 一次覆盖全部分组，与需求描述直接对应。代价是放弃 Ent 的编译期查询类型安全，用 Repo 接口层 + 集成测试补偿。

**为何偏离模板的单服务 buf 改用集中式 `api/`**：需求要求"其余客户端不需要移动/拷贝 proto 文件就可以生成客户端代码"。这要求全部契约构成**一个可推送的 buf module**；若按模板让每个服务各自持有 `api/` 与 `buf.yaml`，外部客户端就需要按服务逐个添加依赖，且跨服务复用的 `common/v1` 无处安放。

### 15.1 首批需安装的工具

```bash
brew install bufbuild/buf/buf
corepack enable pnpm
git init /Users/v_lidongze/dev/fleaMarket
# 并手动启动 Docker Desktop
```

安装 buf 与 pnpm 属于修改本机环境，执行前会向你确认。

