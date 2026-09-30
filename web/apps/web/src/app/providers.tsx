'use client';

import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { useEffect, useState } from 'react';

import { bootstrapSession } from '@/lib/api';
import { isApiError } from '@fleamarket/api-sdk';

/**
 * 全局 Provider。
 *
 * 除了挂载 TanStack Query，还负责应用启动时的会话恢复：
 * access_token 只存内存，刷新页面后必须用 httpOnly Cookie 里的
 * refresh_token 换一次，否则用户每次刷新都会"被登出"。
 */
export function Providers({ children }: { children: React.ReactNode }) {
  const [queryClient] = useState(
    () =>
      new QueryClient({
        defaultOptions: {
          queries: {
            // 校园二手的商品信息变化不快，30 秒内复用缓存足够，
            // 又不至于让刚发布的商品长时间看不到。
            staleTime: 30_000,
            // 4xx 重试没有意义，请求本身有问题。
            // 只对 5xx 与网络错误重试，且最多一次。
            retry: (failureCount, error) => {
              if (failureCount >= 1) return false;
              if (isApiError(error)) return error.status >= 500;
              return true;
            },
            refetchOnWindowFocus: false,
          },
        },
      }),
  );

  useEffect(() => {
    void bootstrapSession();
  }, []);

  return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}
