'use client';

import { validatePhone, validateSmsCode, countdown, firstError } from '@fleamarket/core';
import { isApiError } from '@fleamarket/api-sdk';
import { useRouter } from 'next/navigation';
import { useEffect, useState } from 'react';

import { api, toProfile } from '@/lib/api';
import { authBridge } from '@/lib/auth-store';

/**
 * 手机号验证码登录。
 *
 * 响应式策略：单列表单在所有断点下都是最优形态（表单本身是线性流程），
 * 因此只做容器宽度约束，不切换布局。
 */
export function PhoneLoginForm() {
  const router = useRouter();
  const [phone, setPhone] = useState('');
  const [code, setCode] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [sending, setSending] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  /** 距离可重新发送的剩余秒数。来自后端返回的 retryAfterSeconds。 */
  const [cooldown, setCooldown] = useState(0);

  useEffect(() => {
    if (cooldown <= 0) return;
    const t = setInterval(() => setCooldown((s) => Math.max(0, s - 1)), 1000);
    return () => clearInterval(t);
  }, [cooldown]);

  async function handleSendCode() {
    const msg = firstError(validatePhone(phone));
    if (msg) {
      setError(msg);
      return;
    }
    setError(null);
    setSending(true);
    try {
      const result = await api.POST('/v1/auth/sms-code', {
        body: { phone, scene: 'SMS_SCENE_LOGIN' },
      });
      if (result.error || !result.data) {
        // 限流错误里带着 retry_after，用它驱动倒计时，
        // 而不是让用户盲目重试。
        const e = toApiError(result.error);
        setError(e.message);
        if (e.retryAfterSeconds) setCooldown(e.retryAfterSeconds);
        return;
      }
      setCooldown(result.data.retryAfterSeconds ?? 60);
    } catch {
      setError('网络异常，请检查连接后重试');
    } finally {
      setSending(false);
    }
  }

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    const msg = firstError(validatePhone(phone), validateSmsCode(code));
    if (msg) {
      setError(msg);
      return;
    }
    setError(null);
    setSubmitting(true);
    try {
      const result = await api.POST('/v1/auth/login/phone', {
        body: { phone, code },
      });
      if (result.error || !result.data) {
        setError(toApiError(result.error).message);
        return;
      }
      const { accessToken, profile, isNewUser } = result.data;
      if (!accessToken) {
        setError('登录失败，请重试');
        return;
      }
      authBridge.setSession(accessToken, toProfile(profile));

      // 新用户直接引导去校园认证：未认证账号不能发布商品也不能聊天，
      // 此时让他回首页只会在下一步操作时被挡住，不如一次说清。
      router.replace(isNewUser ? '/campus-auth' : '/');
    } catch {
      setError('网络异常，请检查连接后重试');
    } finally {
      setSubmitting(false);
    }
  }

  const canSend = cooldown === 0 && !sending;

  return (
    <form onSubmit={handleSubmit} className="space-y-4" noValidate>
      <div>
        <label htmlFor="phone" className="mb-1.5 block text-sm font-medium">
          手机号
        </label>
        <input
          id="phone"
          type="tel"
          inputMode="numeric"
          autoComplete="tel"
          maxLength={11}
          value={phone}
          onChange={(e) => setPhone(e.target.value.replace(/\D/g, ''))}
          placeholder="请输入 11 位手机号"
          className="w-full rounded-md border border-border-subtle bg-surface px-3 py-2.5 text-base outline-none focus:border-brand-500"
        />
      </div>

      <div>
        <label htmlFor="code" className="mb-1.5 block text-sm font-medium">
          验证码
        </label>
        <div className="flex gap-2">
          <input
            id="code"
            type="text"
            inputMode="numeric"
            autoComplete="one-time-code"
            maxLength={6}
            value={code}
            onChange={(e) => setCode(e.target.value.replace(/\D/g, ''))}
            placeholder="6 位数字"
            className="min-w-0 flex-1 rounded-md border border-border-subtle bg-surface px-3 py-2.5 text-base outline-none focus:border-brand-500"
          />
          <button
            type="button"
            onClick={handleSendCode}
            disabled={!canSend}
            className="tap-target shrink-0 rounded-md border border-brand-600 px-3 text-sm font-medium text-brand-600 disabled:border-border-subtle disabled:text-gray-400"
          >
            {cooldown > 0 ? countdown(cooldown) : sending ? '发送中' : '获取验证码'}
          </button>
        </div>
      </div>

      {/* role=alert 让屏幕阅读器在错误出现时立即播报，
          否则视障用户不会知道提交失败了。 */}
      {error && (
        <p role="alert" className="text-sm text-red-600">
          {error}
        </p>
      )}

      <button
        type="submit"
        disabled={submitting}
        className="tap-target w-full rounded-md bg-brand-600 py-3 text-base font-medium text-white hover:bg-brand-700 disabled:bg-gray-300"
      >
        {submitting ? '登录中…' : '登录 / 注册'}
      </button>

      <p className="text-center text-xs text-gray-500">
        未注册的手机号将自动创建账号
      </p>
    </form>
  );
}

/**
 * 把后端错误体归一为 { message, retryAfterSeconds }。
 *
 * 不直接读 message 而保留 metadata：限流响应把等待秒数放在
 * metadata.retry_after 里（见 pkg/errs 的用法），倒计时依赖它。
 */
function toApiError(raw: unknown): { message: string; retryAfterSeconds: number | null } {
  if (isApiError(raw)) {
    return { message: raw.message, retryAfterSeconds: raw.retryAfterSeconds };
  }
  const body = raw as { message?: string; metadata?: Record<string, string> } | undefined;
  const retry = Number(body?.metadata?.['retry_after']);
  return {
    message: body?.message ?? '请求失败',
    retryAfterSeconds: Number.isFinite(retry) && retry > 0 ? retry : null,
  };
}
