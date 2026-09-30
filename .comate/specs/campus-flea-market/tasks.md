# 校园跳蚤市场（campus-flea-market）实施任务计划

> 执行原则：先打通一条最小垂直切片（Task 1~9：环境 → 契约 → 公共库 → 建表 → user-service → 网关 → 前端登录），再按服务横向铺开。每个顶层任务结束时代码必须可编译、可启动、可验证。
>
> 任务与 doc.md 章节对应关系标注在任务标题后的括号中。

- [ ] Task 1: 工程骨架与本机环境就绪（doc §4、§15）
    - 1.1: `git init` 并写入 `.gitignore`（Go / Node / IDE / 产物目录）
    - 1.2: 安装缺失工具：`brew install bufbuild/buf/buf`、`corepack enable pnpm`（修改本机环境，执行前向用户确认）
    - 1.3: 创建顶层 `Makefile`，显式 export `GOROOT=/Users/v_lidongze/go/go1.23.2` 与 PATH，避免依赖 shell 配置
    - 1.4: 创建 `docker-compose.yml`：mysql8 / redis7 / etcd3.5 / meilisearch / minio，含健康检查与初始化卷
    - 1.5: 创建 `go.work` 骨架（后续每加一个 module 追加 use 条目）
    - 1.6: 创建目录占位：`api/ pkg/ app/ cmd/ deploy/ docs/ web/`
    - 1.7: 验证：`make env-check` 输出全部工具版本，`docker compose up -d` 后各容器 healthy

- [x] Task 2: 校验 kratos 模板并锁定目录布局（doc §4、§15）
    - 2.1: 在临时目录执行 `kratos new` 生成样例服务，记录 v3.0.0 CLI 实际产出结构
    - 2.2: 对比 doc §4 假设布局，确认 `internal/{conf,server,service,biz,data}` 分层是否一致
    - 2.3: 若不一致，以实际模板为准并回改 doc §4，不强行套用文档布局
    - 2.4: 确认 kratos 框架版本锁定 v2.9.2，wire 与 protoc 插件版本写入 `Makefile` 的 `init` 目标
    - 2.5: 产出 `scripts/new-service.sh`：基于校验后的布局创建新服务，避免 9 个服务手工拼目录

- [x] Task 3: proto 契约工具链与文档化（doc §6）
    - 3.1: 创建 `buf.work.yaml` 与 `api/buf.yaml`（module 名 `buf.build/fleamarket/api`，lint STANDARD、breaking FILE）
    - 3.2: 声明远程依赖 `googleapis`、`protovalidate`，执行 `buf dep update` 生成 `buf.lock`
    - 3.3: 编写 `api/buf.gen.yaml`：go / go-grpc / go-http / go-errors / openapi 五个插件
    - 3.4: 编写 `api/buf.gen.ts.yaml`：生成前端 TS client 到 `web/packages/api-sdk`
    - 3.5: 定义 `api/common/v1`：`Pagination`、`Cursor`、`Money`、`Campus`、`ImageRef`、通用枚举
    - 3.6: 定义 `api/user/v1` 作为试点（含详尽注释与 `buf.validate` 约束），验证注释能正确进入 OpenAPI
    - 3.7: `make api` 一键生成；`make docs` 产出 `docs/openapi.yaml` 与 Redoc 静态站
    - 3.8: 验证免拷贝生成：在空目录中仅凭 `buf.gen.yaml` 执行 `buf generate <module>` 成功产出客户端代码
    - 3.9: 配置 `buf build -o docs/descriptor.binpb` 产出自包含描述符（供不用 buf 的客户端）
    - 3.10: 编写 `make api-lint` / `make api-breaking`，接入 CI 脚本

- [x] Task 4: 公共库基础层 pkg（doc §5.1、§5.2）
    - 4.1: 初始化 `pkg/go.mod`（module `github.com/fleamarket/pkg`），加入 `go.work`
    - 4.2: `pkg/log`：zap 适配 kratos logger，注入 traceID，敏感字段（phone/password/code/token）自动脱敏
    - 4.3: `pkg/errs`：错误码分段规范、kratos errors 转换、GORM/Redis 常见错误归一（唯一键冲突 → 409）
    - 4.4: `pkg/idgen`：Snowflake，workerID 从环境变量或 etcd 顺序租约获取
    - 4.5: `pkg/paging`：泛型 `Page[T]`、复合游标编解码（SortValue+ID 防同值跳页）
    - 4.6: `pkg/statemachine`：泛型状态机 `Machine[S,E]`，非法流转返回 `ErrIllegalTransition`
    - 4.7: `pkg/validate`：protovalidate middleware，校验失败转 400 并携带字段路径
    - 4.8: `pkg/trace`：OpenTelemetry 初始化（可关闭），HTTP/gRPC 双端埋点
    - 4.9: 为以上每个包编写单测（不依赖外部中间件），`go test ./...` 通过

- [x] Task 5: 公共库基础设施层 pkg（doc §5.2、§8.1）
    - 5.1: `pkg/orm`：GORM 初始化（连接池、慢查询日志、软删除约定）
    - 5.2: `pkg/orm`：`Entity` 接口 + 线程安全 `Registry`（按 group 分组）+ 包级 `Default`/`Register`
    - 5.3: `pkg/orm`：`Migrator` 的 `Migrate` / `DumpDDL` / `Drop` 三个方法
    - 5.4: `pkg/cache`：`Cacher` 接口 + Redis 实现、分布式锁、泛型 `Load` 防击穿防穿透
    - 5.5: `pkg/mq`：`Producer`/`Consumer` 接口 + Redis Stream 实现 + 死信处理 + 3 次重试
    - 5.6: `pkg/storage`：对象存储接口 + S3 实现 + 预签名 PUT/GET URL（限定 Content-Type 与大小）
    - 5.7: `pkg/registry`：etcd Registrar/Discovery 构造器（namespace 隔离、租约参数、账号密码）
    - 5.8: `pkg/transport`：gRPC client 工厂，内置 recovery/tracing/metrics/熔断/metadata 透传/幂等重试
    - 5.9: `pkg/sensitive`：AC 自动机敏感词匹配，词库由调用方注入，支持热替换
    - 5.10: 编写 `scripts/lint-pkg-purity.sh`：扫描 `pkg/` 禁止出现 `app/*` 依赖与业务名词，接入 `make lint`
    - 5.11: 针对 orm/cache/mq/storage 编写集成测试（依赖 docker-compose 起的中间件）

- [x] Task 6: 建表脚本 dbctl（doc §7）
    - 6.1: 确立 entity 放置约定：纯结构体在 `app/<svc>/entity/`（导出），业务逻辑留 `internal/`
    - 6.2: 初始化 `cmd/dbctl` module，通过 `go.work` 引用各 app module
    - 6.3: 实现 `migrate` 子命令：`--group` 过滤，默认全量 AutoMigrate，输出变更摘要
    - 6.4: 实现 `dump` 子命令：导出 `CREATE TABLE` 快照到 `deploy/sql/schema.sql`，纳入版本管理
    - 6.5: 实现 `drop` 子命令：强制校验 `APP_ENV=dev` 且需 `--yes`，否则非零退出
    - 6.6: 建立 `deploy/sql/manual/` 目录与规范说明（删列/改类型走人工脚本，dbctl 不承担）
    - 6.7: 接入 `make migrate` / `make db-dump`，用 user-service 的实体做首次端到端验证

- [x] Task 7: user-service 用户与身份（doc §9.1、R1）
    - 7.1: 用 `scripts/new-service.sh` 生成服务骨架，配置文件含 mysql/redis/etcd/jwt 段
    - 7.2: 定义 entity：`t_user`、`t_campus_auth`、`t_school`、`t_campus`、`t_user_follow`、`t_user_block`、`t_credit_log`，注册到 orm Registry
    - 7.3: `pkg/auth` 补充 JWT 签发校验与 Claims（user_id/role/campus_id），双 token（access 2h / refresh 30d）
    - 7.4: 手机号登录：验证码下发（60s 同号限流 + 每日上限）、校验、注册即登录；phone 用 AES-GCM 加密存储 + `phone_hash` 唯一索引
    - 7.5: 微信登录：Web 端开放平台扫码授权（code → access_token → unionid），以 unionid 归一账号
    - 7.6: 校园认证-邮箱：域名后缀白名单校验（`t_school.domain_suffix`）、签名一次性链接 30 分钟有效
    - 7.7: 校园认证-学号：提交学号+姓名+学生证照片，生成人工审核工单（先留接口，Task 12 接通 review-service）
    - 7.8: 个人主页聚合接口：基础信息 + 信誉分 + 发布商品数/交易数（商品与交易数据在 Task 8/10 接通前返回占位）
    - 7.9: 关注/取关、拉黑/取消拉黑，及批量查询拉黑关系的内部 RPC（供 im-service 调用）
    - 7.10: 信誉分：`t_credit_log` 流水 + 分值重算（基础 100，上下限 0~200），消费 `trade.reviewed` 事件
    - 7.11: 注册到 etcd 并暴露 gRPC + HTTP，`reflection.Register` 开启
    - 7.12: 单测（biz 层用 mock repo）+ 集成测试（登录全流程）
    - 7.13: **补缺口** —— 补 `CreateAvatarUploadURL` 与 `CreateCampusAuthImageUploadURL`。
      发现于 Task 10：biz 层用 `storage.Stat` 校验了学生证图片归属，
      但从未暴露申请预签名上传凭证的接口，客户端实际无法完成上传。

- [x] Task 8: api-gateway 统一入口（doc §3、§8、§13）
    - 8.1: 生成 gateway 骨架，仅含 `internal/server` 与 `internal/proxy`，不含业务逻辑
    - 8.2: JWT 鉴权 middleware：白名单路由放行，校验通过后将 user_id/role/campus_id 注入 gRPC metadata
    - 8.3: 基于 etcd discovery 的下游 client 管理（`discovery:///xxx-service`）
    - 8.4: 按 proto 的 `google.api.http` 注解暴露 HTTP 路由，透传至对应服务
    - 8.5: 限流 middleware：IP + 用户双维度令牌桶，写接口（发布/举报/发消息）单独配额
    - 8.6: CORS、请求体大小限制、panic recovery、统一错误响应格式
    - 8.7: `/docs` 路径托管 Redoc 静态站与 `openapi.yaml`
    - 8.8: `/admin/*` 路由前置校验 `role=ADMIN`
    - 8.9: refresh_token 的 httpOnly Cookie 处理（Web 走 Cookie，小程序走响应体）
    - 8.10: 验证：经网关完成一次完整的手机号注册登录，`etcdctl get --prefix /fleamarket` 可见实例
    - 说明：WebSocket 升级代理随 im-service 一并实现，见 Task 15.11，此处不留空壳代码

- [x] Task 9: 前端 monorepo 骨架与登录垂直切片（doc §10.1、§10.3、§10.4）
    - 9.1: 初始化 `web/` pnpm workspace，配置 TS base config、ESLint、Prettier
    - 9.2: `web/packages/api-sdk`：由 `docs/openapi.yaml` 生成类型 + openapi-fetch 客户端
    - 9.3: `web/packages/core`：金额/时间格式化、枚举文案映射、表单校验（零 DOM 依赖）
    - 9.4: Tailwind v4 主题 token 与暗色模式（`@theme` 块，无需 tailwind.config.js）
    - 9.5: `web/apps/web`：Next.js 15 App Router，配置 `next/image` 远端域名白名单
    - 9.6: 请求层：TanStack Query + SDK 拦截器，401 静默续期且并发共享单飞刷新
    - 9.7: 认证状态：access token 存内存、refresh token 走 httpOnly cookie，Zustand 管理
    - 9.8: 登录页、微信登录页、校园认证页（响应式）
    - 9.9: 布局骨架：移动端底部 TabBar / 桌面端顶部导航 + 左侧分类栏的响应式切换
    - 9.10: 验证：`pnpm -r typecheck` 与 `next build` 通过，dev server 渲染正确
    - 说明：`packages/ui` 延后到 Task 18 建立。当前只有一个消费方，
      提前抽公共组件包属于为假想需求做设计；届时 admin 后台会成为第二个消费方。

- [x] Task 10: product-service 商品发布与管理（doc §9.2、R2、R7）
    - 10.1: 定义 `api/product/v1` proto（product/category/favorite/wanted/poi）含注释与校验约束
    - 10.2: 定义 entity 并注册：`t_product`、`t_product_image`、`t_category`、`t_favorite`、`t_wanted_post`、`t_campus_poi`、`t_price_history`
    - 10.3: 分类体系初始数据脚本（两级分类，含教材书籍/数码电子/日用百货/服饰鞋包/自行车交通/票券卡券等）
    - 10.4: 图片上传：`CreateUploadURL` 返回 S3 预签名 URL；提交时校验 key 属主前缀、HEAD 确认存在、校验文件魔数
    - 10.5: 用 `pkg/statemachine` 声明商品状态机（PENDING_REVIEW/ON_SALE/RESERVED/SOLD/OFFLINE/REJECTED）
    - 10.6: 商品 CRUD + 状态流转接口，更新与删除强制校验 `seller_id == caller`
    - 10.7: `LockForOrder` 内部 RPC：`UPDATE ... WHERE id=? AND status=ON_SALE`，以影响行数判定抢占
    - 10.8: 改价写 `t_price_history` 并发 `product.price_dropped` 事件
    - 10.9: 收藏/取消收藏、我的收藏列表、`FavCount` 计数（Redis 计数 + 定时回写）
    - 10.10: 求购帖 CRUD（含预算上限、过期时间）
    - 10.11: 校园 POI 查询接口（按校区，供地图选点）
    - 10.12: 发布后调 review-service 送审（先留接口，Task 13 接通）；发 `product.created/updated/status_changed` 事件
    - 10.13: 单测（状态机、抢占并发）+ 集成测试（发布到上架全流程）

- [x] Task 11: search-service 浏览与搜索（doc §9.3、R3）
    - 11.1: 定义 `api/search/v1` proto（信息流、搜索、筛选、分面统计）
    - 11.2: `SearchEngine` 接口抽象 + Meilisearch 实现（实现可替换，业务代码不变）
    - 11.3: 索引 schema：filterable（category/campus/school/condition/price/status）、sortable（created_at/price/heat/_geoPoint）
    - 11.4: 消费 `product.*` 事件增量维护索引，失败进死信并可重投
    - 11.5: 关键词搜索 + 多维筛选（价格区间、成色、校区）+ 排序（时间/热度/距离）
    - 11.6: 同校/同校区优先：三路查询（同校区 / 同校异校区 / 全局）按 6:3:1 配额确定性交错归并
    - 11.7: 热度分定时任务：`log10(view+1)*1.0 + fav*3 + inquiry*5 - 时间衰减`，每 10 分钟重算近 7 天商品
    - 11.8: `ReindexAll` 管理接口：新索引别名切换，重建期间搜索不中断
    - 11.9: 降级：Meilisearch 不可用时回落 MySQL LIKE + 分类过滤，响应标记 `degraded=true`
    - 11.10: 集成测试：造 100 条多校区商品验证优先级配额与筛选正确性

- [x] Task 12: trade-service 交易与评价（doc §9.4、R4、R5）
    - 12.1: 定义 `api/trade/v1` proto（order、review）
    - 12.2: 定义 entity 并注册：`t_order`、`t_review`
    - 12.3: 订单状态机 + `pay_channel` 仅允许 `OFFLINE`（保留字段不开放线上支付）
    - 12.4: 预订流程：Redis 分布式锁 → 调 `product.LockForOrder` → 创建订单 → 失败则释放
    - 12.5: 卖家确认/拒绝预订；24h 未确认的定时任务回滚商品为 ON_SALE
    - 12.6: 约面交：绑定 POI 与时间，变更需双方可见
    - 12.7: 完成交易：任一方点完成 + 另一方确认，或 72h 后自动完成，商品置 SOLD
    - 12.8: 双向评价：7 天互评期，提交后不可修改，发 `trade.reviewed` 事件
    - 12.9: 交易记录查询（买家视角/卖家视角）
    - 12.10: 集成测试：并发 10 个用户抢同一商品，验证只有 1 单成功

- [ ] Task 13: review-service 内容审核与举报（doc §9.6、R5）
    - 13.1: 定义 `api/review/v1` proto（moderation、report）
    - 13.2: 定义 entity 并注册：`t_moderation_task`、`t_report`、`t_sensitive_word`
    - 13.3: 违禁品与学术不端词库初始数据（管制刀具、药品、代写代考、账号租借、饭卡余额转卖、黄牛票等）
    - 13.4: 自动审核：`pkg/sensitive` AC 自动机匹配，BLOCK 直接驳回 / REVIEW 转人工 / 无命中自动通过
    - 13.5: 词库热更新：变更后 Redis Pub/Sub 通知各实例重载
    - 13.6: 同步审核接口（供 im-service 调用，300ms 超时）与异步审核接口（供商品发布）
    - 13.7: 审核结果通过 MQ 回写 product-service，不做反向同步 RPC
    - 13.8: 举报提交与处置；同一目标被 3 个不同用户举报则自动临时下架并升级高优工单
    - 13.9: 人工审核工单列表与处置接口（供 admin 后台）
    - 13.10: 单测：AC 自动机命中准确性、变形词与空格绕过场景

- [ ] Task 14: notify-service 消息与通知（doc §9.7、R6）
    - 14.1: 定义 `api/notify/v1` proto
    - 14.2: 定义 entity 并注册：`t_notification`、`t_notify_setting`
    - 14.3: 消费事件：商品被咨询、被预订、被评价、审核结果
    - 14.4: 按 `t_notify_setting` 过滤后写站内信；用户在线时经 im-service WS 通道实时下发
    - 14.5: 收藏商品降价提醒：消费 `product.price_dropped`，扇出给收藏用户
    - 14.6: 上新提醒：用户关注「分类 + 校区」组合，新商品过审后分页批量扇出（避免长事务）
    - 14.7: 未读数查询与已读标记（含全部已读）
    - 14.8: 通知渠道抽象：预留微信订阅消息 sender 实现位（小程序期只加实现不改业务）

- [ ] Task 15: im-service 站内即时聊天（doc §9.5、R4）
    - 15.1: 定义 `api/im/v1` proto（conversation、message）
    - 15.2: 定义 entity 并注册：`t_conversation`、`t_message`
    - 15.3: WebSocket 服务：连接鉴权（网关已校验 JWT）、30s 心跳、优雅断连
    - 15.4: 在线状态与路由表存 Redis（`im:online:{uid} → nodeID`）
    - 15.5: 多节点消息转发：Redis Pub/Sub，支持水平扩展
    - 15.6: 消息类型实现：TEXT / IMAGE / BARGAIN（砍价卡片含接受拒绝）/ POI（面交地点卡片）/ SYSTEM
    - 15.7: 发送前置校验：双方非拉黑、发送方已校园认证、内容同步过审（超时 300ms 放行 + 异步复审）
    - 15.8: 会话列表、未读数、增量拉取离线消息（客户端带 `last_msg_id`）
    - 15.9: 首次会话弹出防诈骗提示；拦截「扫码付款/加微信转账」类诱导话术
    - 15.10: 被拉黑用户发起会话时静默拦截（返回成功但不投递，不告知对方已拉黑）
    - 15.11: 打通网关 WS 代理，端到端验证两个浏览器实时收发

- [ ] Task 16: admin-service 运营管理（doc §9.8、R8）
    - 16.1: 定义 `api/admin/v1` proto（banner、announcement、activity）
    - 16.2: 定义 entity 并注册：`t_banner`、`t_announcement`、`t_activity`、`t_admin_user`、`t_admin_op_log`
    - 16.3: 管理员独立账号体系（不复用 `t_user`）与角色权限
    - 16.4: 首页轮播图 CRUD（排序、生效时间区间、校区范围）
    - 16.5: 公告 CRUD（Markdown 内容、置顶、级别、生效区间）
    - 16.6: 活动专区：`product_filter_json` 动态圈选商品，新增活动无需写代码
    - 16.7: 审核工单与举报处置的管理入口（转调 review-service）
    - 16.8: 全量管理操作强制写 `t_admin_op_log` 审计日志（middleware 统一拦截）

- [ ] Task 17: Web 用户端完整页面（doc §10.2、§10.3）
    - 17.1: 首页信息流（SSR + 客户端无限滚动）：移动 2 列瀑布流 / 平板 3 列 / 桌面 4~5 列网格
    - 17.2: 商品详情页（SSR + ISR 60s）：移动单列 + 底部固定操作条 / 桌面左图右信息双栏 + 吸顶卖家卡片
    - 17.3: 搜索与筛选页（CSR）：移动底部抽屉 / 平板顶部下拉 / 桌面左侧常驻面板
    - 17.4: 分类浏览页与分类导航
    - 17.5: 发布商品页：多图上传（直传预签名 URL、拖拽排序、封面标记）、分步表单、桌面端右侧实时预览
    - 17.6: 聊天页：移动全屏会话 / 平板双栏 / 桌面三栏；砍价卡片与面交地点卡片交互
    - 17.7: 订单与交易流程页：预订、确认、约面交、完成、双向评价
    - 17.8: 个人主页与他人主页：发布商品、信誉评分、交易记录、关注/拉黑
    - 17.9: 收藏页、求购帖发布与列表、消息通知中心
    - 17.10: 校园地图选点组件（POI 展示与选择）
    - 17.11: 活动专区页与首页轮播图渲染
    - 17.12: 用容器查询（`@container`）实现卡片组件自适应；触控目标 ≥44px；底部条适配 `safe-area-inset-bottom`
    - 17.13: 无障碍：语义化标签、aria-label、键盘可达与抽屉焦点陷阱、对比度 ≥4.5:1、商品图 alt 兜底
    - 17.14: 验证：375/768/1440 三档布局正确，Lighthouse 移动端性能 ≥85、可访问性 ≥95

- [ ] Task 18: 管理后台前端（doc §10.1、R8）
    - 18.1: `web/apps/admin` 初始化（纯 CSR），复用 `packages/ui` 与 `api-sdk`
    - 18.2: 管理员登录与角色权限路由守卫
    - 18.3: 轮播图、公告、活动配置管理页
    - 18.4: 审核工单队列与处置页（含命中词高亮）
    - 18.5: 举报处置页与用户封禁操作
    - 18.6: 敏感词库管理页（增删改 + 热更新触发）
    - 18.7: 操作审计日志查询页

- [ ] Task 19: 可观测性、部署与工程收尾（doc §8、§12、§13）
    - 19.1: 各服务统一接入 `pkg/log` / `pkg/trace` / metrics，健康检查与就绪探针
    - 19.2: 降级与熔断验收：逐个停掉 Meilisearch / review-service / etcd，验证 doc §12 的降级行为符合预期
    - 19.3: MQ 死信可见性与手动重投能力验证
    - 19.4: 各服务 Dockerfile 与 `deploy/` k8s manifest 模板
    - 19.5: `make build` / `make run` / `make test` / `make lint` 全部打通，CI 脚本串联 api-lint、api-breaking、lint-pkg-purity、test
    - 19.6: 密钥管理：手机号加密密钥、JWT 密钥、微信 AppSecret 走环境变量，代码与配置文件中零明文
    - 19.7: 隐私合规：注册流程隐私政策、学生证照片 30 天自动清理任务、账号注销彻底清除

- [ ] Task 20: 端到端验收与小程序演进准备（doc §14、§10.4）
    - 20.1: 端到端主链路：注册 → 校园认证 → 发布商品 → 过审 → 搜索到 → 聊天砍价 → 预订 → 完成 → 双向评价 → 信誉分变更 → 收到通知
    - 20.2: 逐条核对 doc §14 的 7 项预期结果，未达成项建任务补齐
    - 20.3: 空目录中验证 `buf generate <module>` 免拷贝生成客户端代码
    - 20.4: 发布 `@fleamarket/api-sdk` 到私有 npm registry 的流程与文档
    - 20.5: 梳理小程序需重写清单（登录、上传、WS、地图）并确认后端零改动
    - 20.6: 编写 README：架构图、本地启动步骤、新增服务流程、契约变更流程
