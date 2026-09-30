import type { Metadata } from 'next';
import Link from 'next/link';

import { PhoneLoginForm } from '@/components/phone-login-form';

export const metadata: Metadata = {
  title: '登录',
  // 登录页不应被搜索引擎收录。
  robots: { index: false, follow: false },
};

export default function LoginPage() {
  return (
    <div className="mx-auto max-w-md px-4 py-8 md:py-16">
      <h1 className="text-2xl font-semibold">登录校园跳蚤市场</h1>
      <p className="mt-2 text-sm text-gray-500">
        登录后可发布闲置、与同学沟通
      </p>

      <div className="mt-8">
        <PhoneLoginForm />
      </div>

      <div className="mt-8 flex items-center gap-3">
        <span className="h-px flex-1 bg-border-subtle" />
        <span className="text-xs text-gray-400">其他方式</span>
        <span className="h-px flex-1 bg-border-subtle" />
      </div>

      {/* 微信登录需要开放平台 AppID，本地开发未配置时点击会得到明确报错，
          而不是静默失败——这比隐藏按钮更利于联调。 */}
      <Link
        href="/login/wechat"
        className="tap-target mt-4 flex w-full items-center justify-center gap-2 rounded-md border border-border-subtle py-3 text-base hover:bg-surface-muted"
      >
        <span aria-hidden>💬</span>
        微信一键登录
      </Link>

      <p className="mt-8 text-center text-xs text-gray-400">
        登录即表示同意
        <Link href="/legal/terms" className="mx-1 underline">
          用户协议
        </Link>
        与
        <Link href="/legal/privacy" className="mx-1 underline">
          隐私政策
        </Link>
      </p>
    </div>
  );
}
