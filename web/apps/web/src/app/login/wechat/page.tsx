import type { Metadata } from 'next';
import Link from 'next/link';

export const metadata: Metadata = {
  title: '微信登录',
  robots: { index: false, follow: false },
};

/**
 * 微信扫码登录。
 *
 * 完整流程需要开放平台 AppID 与已备案的回调域名，本地开发无法完成。
 * 此处明确说明前置条件，而不是放一个点击后静默失败的二维码占位——
 * 后者会让联调时误以为是代码问题。
 */
export default function WechatLoginPage() {
  return (
    <div className="mx-auto max-w-md px-4 py-8 md:py-16">
      <h1 className="text-2xl font-semibold">微信一键登录</h1>

      <div className="mt-6 rounded-lg border border-border-subtle bg-surface-muted p-4 text-sm text-gray-600">
        <p className="font-medium text-gray-800">该功能需要以下配置后才能使用：</p>
        <ul className="mt-2 list-disc space-y-1 pl-5">
          <li>微信开放平台网站应用的 AppID 与 AppSecret</li>
          <li>已备案并配置为授权回调域的域名</li>
        </ul>
        <p className="mt-3">
          配置项位于 <code className="font-mono text-xs">app/user/configs/config.yaml</code> 的
          <code className="mx-1 font-mono text-xs">wechat.web</code> 段。
        </p>
        <p className="mt-2 text-xs">
          说明：微信与手机号登录通过 unionid 归一到同一账号，
          两种方式登录的是同一个用户。
        </p>
      </div>

      <Link
        href="/login"
        className="tap-target mt-6 flex w-full items-center justify-center rounded-md border border-border-subtle py-3 hover:bg-surface-muted"
      >
        返回手机号登录
      </Link>
    </div>
  );
}
