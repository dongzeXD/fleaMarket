'use client';

import Link from 'next/link';
import { usePathname } from 'next/navigation';

import { useAuthStore } from '@/lib/auth-store';

/**
 * 响应式外壳。
 *
 * 导航形态随宽度切换（doc.md §10.2）：
 *   - < 768px：底部 TabBar（固定 5 项，拇指可达）
 *   - ≥ 768px：顶部导航
 *   - ≥ 1024px：顶部导航 + 左侧分类栏
 *
 * 实现方式是两套 DOM 用 `hidden md:flex` 互斥显示，而非 JS 判断宽度。
 * 原因：JS 判断需要等客户端 hydration，会在首屏出现布局跳变；
 * 纯 CSS 的方案在 SSR 阶段就是正确的。
 */
export function AppShell({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex min-h-dvh flex-col">
      <TopNav />
      <div className="flex flex-1">
        <CategorySidebar />
        <main className="flex-1 pb-[calc(var(--spacing-safe-bottom-bar)+env(safe-area-inset-bottom,0px))] md:pb-0">
          {children}
        </main>
      </div>
      <BottomTabBar />
    </div>
  );
}

const navItems = [
  { href: '/', label: '首页', icon: '🏠' },
  { href: '/search', label: '搜索', icon: '🔍' },
  { href: '/publish', label: '发布', icon: '➕' },
  { href: '/messages', label: '消息', icon: '💬' },
  { href: '/me', label: '我的', icon: '👤' },
] as const;

function TopNav() {
  const profile = useAuthStore((s) => s.profile);
  const initialized = useAuthStore((s) => s.initialized);

  return (
    <header className="sticky top-0 z-20 hidden border-b border-border-subtle bg-surface/95 backdrop-blur md:block">
      <nav className="mx-auto flex h-14 max-w-7xl items-center gap-6 px-4">
        <Link href="/" className="text-lg font-semibold text-brand-600">
          校园跳蚤市场
        </Link>
        <div className="flex flex-1 items-center gap-1">
          {navItems.slice(0, 4).map((item) => (
            <NavLink key={item.href} href={item.href} label={item.label} />
          ))}
        </div>
        {/* initialized 为 false 时不渲染登录按钮，避免已登录用户
            在会话恢复完成前先看到一次"登录"再闪回昵称。 */}
        {!initialized ? (
          <div className="h-8 w-20 animate-pulse rounded bg-surface-muted" aria-hidden />
        ) : profile ? (
          <Link
            href="/me"
            className="tap-target flex items-center gap-2 rounded px-3 text-sm hover:bg-surface-muted"
          >
            <span className="max-w-24 truncate">{profile.nickname}</span>
          </Link>
        ) : (
          <Link
            href="/login"
            className="rounded-md bg-brand-600 px-4 py-2 text-sm font-medium text-white hover:bg-brand-700"
          >
            登录
          </Link>
        )}
      </nav>
    </header>
  );
}

function NavLink({ href, label }: { href: string; label: string }) {
  const pathname = usePathname();
  const active = pathname === href;
  return (
    <Link
      href={href}
      aria-current={active ? 'page' : undefined}
      className={`rounded px-3 py-2 text-sm transition-colors ${
        active ? 'bg-brand-50 font-medium text-brand-700' : 'hover:bg-surface-muted'
      }`}
    >
      {label}
    </Link>
  );
}

/** 桌面端的左侧分类栏，1024px 以下隐藏。 */
function CategorySidebar() {
  const categories = [
    '教材书籍',
    '数码电子',
    '日用百货',
    '服饰鞋包',
    '自行车/交通',
    '票券卡券',
    '运动器材',
    '乐器',
    '美妆个护',
    '其他',
  ];

  return (
    <aside className="hidden w-48 shrink-0 border-r border-border-subtle p-4 lg:block">
      <h2 className="mb-3 text-sm font-semibold text-brand-700">分类浏览</h2>
      <ul className="space-y-1">
        {categories.map((c) => (
          <li key={c}>
            <Link
              href={`/search?category=${encodeURIComponent(c)}`}
              className="block rounded px-2 py-1.5 text-sm hover:bg-surface-muted"
            >
              {c}
            </Link>
          </li>
        ))}
      </ul>
    </aside>
  );
}

/** 移动端底部 TabBar，768px 以上隐藏。 */
function BottomTabBar() {
  const pathname = usePathname();

  return (
    <nav
      aria-label="主导航"
      className="pb-safe fixed bottom-0 left-0 right-0 z-20 border-t border-border-subtle bg-surface md:hidden"
    >
      <ul className="flex">
        {navItems.map((item) => {
          const active = pathname === item.href;
          return (
            <li key={item.href} className="flex-1">
              <Link
                href={item.href}
                aria-current={active ? 'page' : undefined}
                className={`tap-target flex flex-col items-center justify-center gap-0.5 py-2 text-xs ${
                  active ? 'text-brand-600' : 'text-gray-500'
                }`}
              >
                <span aria-hidden className="text-lg leading-none">
                  {item.icon}
                </span>
                <span>{item.label}</span>
              </Link>
            </li>
          );
        })}
      </ul>
    </nav>
  );
}
