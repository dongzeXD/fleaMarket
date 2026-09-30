import type { Metadata, Viewport } from 'next';

import { AppShell } from '@/components/app-shell';

import './globals.css';
import { Providers } from './providers';

export const metadata: Metadata = {
  title: {
    default: '校园跳蚤市场',
    template: '%s | 校园跳蚤市场',
  },
  description: '同校同学之间的二手闲置交易平台，线下面交，安全可靠。',
};

/**
 * 移动优先的视口设置。
 *
 * maximumScale 不设上限、userScalable 不禁用：禁止缩放会让视力障碍用户
 * 无法阅读，是常见但严重的无障碍问题（doc.md §10.2 的可访问性要求）。
 */
export const viewport: Viewport = {
  width: 'device-width',
  initialScale: 1,
  themeColor: [
    { media: '(prefers-color-scheme: light)', color: '#ffffff' },
    { media: '(prefers-color-scheme: dark)', color: '#1a1a1a' },
  ],
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="zh-CN">
      <body className="min-h-dvh bg-surface">
        <Providers>
          <AppShell>{children}</AppShell>
        </Providers>
      </body>
    </html>
  );
}
