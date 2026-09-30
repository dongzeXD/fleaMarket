'use client';

import { create } from 'zustand';

/**
 * 登录态。
 *
 * 令牌存放策略（doc.md §10.4）：
 *   - access_token 只存内存（本 store），刷新页面即丢失，靠 refresh 重建。
 *     不存 localStorage 是因为那会让 XSS 能直接读走它。
 *   - refresh_token 由网关写入 httpOnly Cookie，前端代码完全接触不到。
 *
 * 代价是每次刷新页面要多一次 /v1/auth/refresh 往返。这个代价是值得的：
 * 令牌一旦落到 JS 可读的存储里，任何一个第三方脚本的漏洞都能导致账号被盗。
 */

export interface UserProfile {
  userId: string;
  nickname: string;
  creditScore: number;
  campusAuthStatus: string;
  phoneMasked?: string;
  avatarUrl?: string;
}

interface AuthState {
  accessToken: string | null;
  profile: UserProfile | null;
  /** 首次 refresh 尝试是否已完成。用于区分「未登录」与「还不知道」。 */
  initialized: boolean;

  setSession: (token: string, profile: UserProfile | null) => void;
  setProfile: (profile: UserProfile | null) => void;
  clear: () => void;
  markInitialized: () => void;
}

export const useAuthStore = create<AuthState>((set) => ({
  accessToken: null,
  profile: null,
  initialized: false,

  setSession: (token, profile) => set({ accessToken: token, profile, initialized: true }),
  setProfile: (profile) => set({ profile }),
  clear: () => set({ accessToken: null, profile: null, initialized: true }),
  markInitialized: () => set({ initialized: true }),
}));

/**
 * 在 React 之外读写令牌。
 *
 * API 客户端的中间件运行在渲染树之外，拿不到 hook。
 * zustand 的 getState/setState 正好填补这个缺口——
 * 这是选它而非 Context 的主要原因。
 */
export const authBridge = {
  getAccessToken: () => useAuthStore.getState().accessToken,
  setSession: (token: string, profile: UserProfile | null) =>
    useAuthStore.getState().setSession(token, profile),
  clear: () => useAuthStore.getState().clear(),
};

/** 是否已登录。initialized 为 false 时应显示加载态而非登录按钮。 */
export function useIsLoggedIn(): boolean {
  return useAuthStore((s) => s.accessToken !== null);
}
