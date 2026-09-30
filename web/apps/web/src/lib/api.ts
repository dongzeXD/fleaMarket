'use client';

import { createApiClient, type TokenProvider } from '@fleamarket/api-sdk';

import { authBridge, type UserProfile } from './auth-store';

const BASE_URL = process.env.NEXT_PUBLIC_API_BASE_URL ?? 'http://127.0.0.1:8000';

/**
 * 刷新令牌的单飞控制。
 *
 * 为什么必须单飞：refresh 令牌是轮换的（用一次即吊销，见 doc.md §9.1）。
 * 若页面同时发出 5 个请求并同时收到 401，它们会各自发起刷新——
 * 第一个成功并吊销旧令牌，其余 4 个携带已吊销的令牌，全部失败。
 * 用户看到的现象是「偶尔随机被登出」，且极难复现。
 *
 * 这里用一个共享的 Promise 把并发刷新收敛为一次。
 */
let refreshInflight: Promise<boolean> | null = null;

async function doRefresh(): Promise<boolean> {
  try {
    const res = await fetch(`${BASE_URL}/v1/auth/refresh`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', 'X-Client': 'web' },
      // refresh_token 在 httpOnly Cookie 里，必须让浏览器带上。
      credentials: 'include',
      body: JSON.stringify({}),
    });
    if (!res.ok) return false;

    const data = (await res.json()) as {
      accessToken?: string;
      profile?: Record<string, unknown>;
    };
    if (!data.accessToken) return false;

    authBridge.setSession(data.accessToken, toProfile(data.profile));
    return true;
  } catch {
    // 网络错误与刷新失败不做区分：两者对调用方的后续动作相同。
    return false;
  }
}

const tokens: TokenProvider = {
  getAccessToken: () => authBridge.getAccessToken(),

  refresh: () => {
    if (refreshInflight) return refreshInflight;
    refreshInflight = doRefresh().finally(() => {
      refreshInflight = null;
    });
    return refreshInflight;
  },

  onAuthFailure: () => {
    authBridge.clear();
  },
};

/**
 * 全局 API 客户端。
 *
 * 做成模块级单例而非每次渲染新建：客户端内部持有中间件与刷新状态，
 * 重复创建会让单飞控制失效。
 */
export const api = createApiClient({ baseUrl: BASE_URL, tokens, client: 'web' });

/** 应用启动时尝试恢复会话。失败是正常情况（未登录）。 */
export async function bootstrapSession(): Promise<void> {
  const ok = await tokens.refresh();
  if (!ok) {
    authBridge.clear();
  }
}

/** 把后端返回的 profile 转成前端模型。 */
export function toProfile(raw: Record<string, unknown> | undefined): UserProfile | null {
  if (!raw) return null;
  return {
    // proto 的 int64 在 JSON 中以字符串传输，保持字符串避免精度丢失——
    // 雪花 ID 超过 Number.MAX_SAFE_INTEGER，转成 number 会静默错位。
    userId: String(raw['userId'] ?? ''),
    nickname: String(raw['nickname'] ?? ''),
    creditScore: Number(raw['creditScore'] ?? 0),
    campusAuthStatus: String(raw['campusAuthStatus'] ?? ''),
    ...(raw['phoneMasked'] ? { phoneMasked: String(raw['phoneMasked']) } : {}),
  };
}
