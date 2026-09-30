/**
 * @fleamarket/api-sdk
 *
 * 契约来源是 `api/**\/*.proto`，经 protoc-gen-openapi 产出 `docs/openapi.yaml`，
 * 再由 openapi-typescript 生成 `src/generated/schema.ts`。
 *
 * 因此本包的类型与后端路由同源（doc.md §6.2）：
 *   - 路径、方法、请求体、响应体的类型全部来自 proto 注解，无手写映射；
 *   - 后端改了字段，前端在 `pnpm typecheck` 时就会报错，而不是运行时才发现。
 *
 * 为什么不用 protobuf 原生的 TS client：对外暴露的是 kratos 依据
 * google.api.http 注解生成的 REST/JSON 接口，并非 Connect 或 gRPC-Web 协议。
 * 用 OpenAPI 路线才能让「路由」也来自同一份契约，而不是只共享消息类型。
 *
 * 重新生成：`pnpm --filter @fleamarket/api-sdk generate`
 * （需先在仓库根执行 `make docs` 刷新 openapi.yaml）
 */
export { createApiClient, ApiError, isApiError, unwrap } from './client';
export type { ApiClient, ApiClientOptions, TokenProvider } from './client';
