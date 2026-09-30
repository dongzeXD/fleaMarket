import createClient, { type Middleware } from 'openapi-fetch';

import type { paths } from './generated/schema';

/** 后端统一错误响应（由 kratos errors 产出，见 doc.md §12）。 */
export interface ApiErrorBody {
  code: number;
  reason: string;
  message: string;
  metadata?: Record<string, string>;
}

/**
 * ApiError 把后端的错误响应包装成可 catch 的异常。
 *
 * 保留 reason 与 metadata 而非只留 message：
 * 前端需要按 reason 分支处理（如 CAMPUS_AUTH_REQUIRED 要跳认证页），
 * 而 metadata 里带着 retry_after 这类倒计时所需的数据。
 * 只靠 message 做字符串匹配会在文案调整时静默失效。
 */
export class ApiError extends Error {
  readonly status: number;
  readonly reason: string;
  readonly metadata: Record<string, string>;

  constructor(status: number, body: Partial<ApiErrorBody> | undefined) {
    super(body?.message ?? '请求失败');
    this.name = 'ApiError';
    this.status = status;
    this.reason = body?.reason ?? '';
    this.metadata = body?.metadata ?? {};
  }

  /** 是否为未认证，前端据此跳转登录。 */
  get isUnauthenticated(): boolean {
    return this.status === 401;
  }

  /** 触发限流时距下次可重试的秒数，取自错误元数据。 */
  get retryAfterSeconds(): number | null {
    const v = this.metadata['retry_after'];
    if (!v) return null;
    const n = Number(v);
    return Number.isFinite(n) ? n : null;
  }
}

export function isApiError(e: unknown): e is ApiError {
  return e instanceof ApiError;
}

/** 访问令牌的读取与刷新。由上层的认证状态模块实现。 */
export interface TokenProvider {
  /** 当前内存中的 access_token，未登录时返回 null。 */
  getAccessToken(): string | null;
  /**
   * 刷新令牌。
   *
   * 实现方必须保证并发调用只触发一次真实刷新（单飞），
   * 否则页面上同时发出的多个请求会各自刷新一次，
   * 而 refresh 令牌是轮换的——后到的刷新会因旧令牌已吊销而失败，
   * 表现为随机登出。
   */
  refresh(): Promise<boolean>;
  /** 刷新失败或令牌失效时调用，用于清理登录态。 */
  onAuthFailure(): void;
}

export interface ApiClientOptions {
  baseUrl: string;
  tokens?: TokenProvider;
  /** 客户端标识。miniapp 时 refresh_token 走响应体而非 Cookie。 */
  client?: 'web' | 'miniapp';
}

export type ApiClient = ReturnType<typeof createApiClient>;

/**
 * 创建 API 客户端。
 *
 * 内置两个中间件：
 *   1. 注入 Authorization 与 X-Client 头；
 *   2. 拦截 401，静默刷新后重放一次原请求。
 */
export function createApiClient(opts: ApiClientOptions) {
  const clientKind = opts.client ?? 'web';

  const client = createClient<paths>({
    baseUrl: opts.baseUrl,
    // 必须带上 Cookie：Web 端的 refresh_token 在 httpOnly Cookie 里，
    // 刷新请求依赖浏览器自动携带它。
    credentials: 'include',
  });

  const authMiddleware: Middleware = {
    async onRequest({ request }) {
      request.headers.set('X-Client', clientKind);
      const token = opts.tokens?.getAccessToken();
      if (token) {
        request.headers.set('Authorization', `Bearer ${token}`);
      }
      return request;
    },

    async onResponse({ request, response }) {
      if (response.status !== 401 || !opts.tokens) {
        return response;
      }
      // 刷新接口自己返回 401 说明 refresh 令牌也失效了，
      // 此时再刷新会无限递归。
      if (new URL(request.url).pathname.endsWith('/v1/auth/refresh')) {
        opts.tokens.onAuthFailure();
        return response;
      }

      const refreshed = await opts.tokens.refresh();
      if (!refreshed) {
        opts.tokens.onAuthFailure();
        return response;
      }

      // 重放原请求。Request 只能消费一次，故需克隆——
      // 直接复用会因 body 已被读取而失败。
      const retry = request.clone();
      const token = opts.tokens.getAccessToken();
      if (token) {
        retry.headers.set('Authorization', `Bearer ${token}`);
      }
      return fetch(retry);
    },
  };

  client.use(authMiddleware);
  return client;
}

/**
 * 把 openapi-fetch 的 { data, error } 结果转成「成功返回值 / 抛异常」。
 *
 * TanStack Query 依赖抛异常来识别失败，而 openapi-fetch 默认把错误
 * 放在返回值里。这个适配放在 SDK 内部，使调用方不必每次手写 if (error) throw。
 */
export function unwrap<T>(result: { data?: T; error?: unknown; response: Response }): T {
  if (result.error !== undefined || result.data === undefined) {
    throw new ApiError(result.response.status, result.error as Partial<ApiErrorBody>);
  }
  return result.data;
}
