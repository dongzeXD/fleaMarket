import type { Metadata } from 'next';

import { CampusAuthForm } from '@/components/campus-auth-form';

export const metadata: Metadata = {
  title: '校园身份认证',
  robots: { index: false, follow: false },
};

export default function CampusAuthPage() {
  return (
    <div className="mx-auto max-w-2xl px-4 py-8">
      <h1 className="text-2xl font-semibold">校园身份认证</h1>
      <p className="mt-2 text-sm text-gray-500">
        认证后才能发布闲置与发起聊天。这是平台防范诈骗的第一道措施，
        也是同学之间互相信任的基础。
      </p>

      <div className="mt-8">
        <CampusAuthForm />
      </div>
    </div>
  );
}
