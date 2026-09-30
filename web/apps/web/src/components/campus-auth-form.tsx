'use client';

import { campusAuthHint, isCampusVerified, validateEduEmail, firstError } from '@fleamarket/core';
import { useState } from 'react';

import { api } from '@/lib/api';
import { useAuthStore } from '@/lib/auth-store';

/**
 * 校园身份认证。
 *
 * 两条路径（doc.md §9.1）：
 *   - 校园邮箱：域名命中白名单后发一次性链接，点击即自动通过；
 *   - 学号 + 学生证：进人工审核队列。
 *
 * 默认展示邮箱路径，因为它能即时通过；学号路径折叠在下方，
 * 供没有校园邮箱的用户使用。
 */
export function CampusAuthForm() {
  const profile = useAuthStore((s) => s.profile);
  const [schoolId, setSchoolId] = useState('');
  const [campusId, setCampusId] = useState('');
  const [email, setEmail] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [sentTo, setSentTo] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  const verified = isCampusVerified(profile?.campusAuthStatus);
  const hint = campusAuthHint(profile?.campusAuthStatus);

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    const msg = firstError(validateEduEmail(email));
    if (msg) {
      setError(msg);
      return;
    }
    if (!schoolId || !campusId) {
      setError('请选择学校与校区');
      return;
    }
    setError(null);
    setSubmitting(true);
    try {
      const result = await api.POST('/v1/campus-auth/email', {
        body: { schoolId, campusId, eduEmail: email },
      });
      if (result.error || !result.data) {
        const body = result.error as { message?: string; metadata?: Record<string, string> };
        // 域名不匹配时后端会在 metadata 里回传该校允许的域名，
        // 直接展示比让用户猜要有用得多。
        const domains = body?.metadata?.['allowed_domains'];
        setError(
          domains ? `${body?.message ?? '邮箱不符合要求'}（可用域名：${domains}）` : (body?.message ?? '提交失败'),
        );
        return;
      }
      setSentTo(result.data.emailMasked ?? email);
    } catch {
      setError('网络异常，请稍后重试');
    } finally {
      setSubmitting(false);
    }
  }

  if (verified) {
    return (
      <div className="rounded-lg border border-brand-100 bg-brand-50 p-4">
        <p className="font-medium text-brand-700">已完成校园认证</p>
        <p className="mt-1 text-sm text-brand-600">你现在可以发布闲置与发起聊天。</p>
      </div>
    );
  }

  if (sentTo) {
    return (
      <div className="rounded-lg border border-border-subtle bg-surface-muted p-4">
        <p className="font-medium">认证邮件已发送</p>
        <p className="mt-1 text-sm text-gray-600">
          请查收 <span className="font-mono">{sentTo}</span> 的邮件并点击其中的链接。
          链接 30 分钟内有效，且仅可使用一次。
        </p>
        <p className="mt-3 text-xs text-gray-500">
          本地开发环境下，认证链接会直接打印在 user-service 的日志中。
        </p>
      </div>
    );
  }

  return (
    <>
      {hint && (
        <p className="mb-4 rounded-md bg-surface-muted p-3 text-sm text-gray-600">{hint}</p>
      )}

      {/* 桌面端两栏，移动端单栏。学校与校区是强关联的两个选择，
          并排放置能让用户看到它们的关系。 */}
      <form onSubmit={handleSubmit} className="space-y-4" noValidate>
        <div className="grid gap-4 sm:grid-cols-2">
          <div>
            <label htmlFor="school" className="mb-1.5 block text-sm font-medium">
              学校
            </label>
            <input
              id="school"
              value={schoolId}
              onChange={(e) => setSchoolId(e.target.value.replace(/\D/g, ''))}
              inputMode="numeric"
              placeholder="学校 ID（待接入选择器）"
              className="w-full rounded-md border border-border-subtle bg-surface px-3 py-2.5 outline-none focus:border-brand-500"
            />
          </div>
          <div>
            <label htmlFor="campus" className="mb-1.5 block text-sm font-medium">
              校区
            </label>
            <input
              id="campus"
              value={campusId}
              onChange={(e) => setCampusId(e.target.value.replace(/\D/g, ''))}
              inputMode="numeric"
              placeholder="校区 ID（待接入选择器）"
              className="w-full rounded-md border border-border-subtle bg-surface px-3 py-2.5 outline-none focus:border-brand-500"
            />
          </div>
        </div>

        <div>
          <label htmlFor="email" className="mb-1.5 block text-sm font-medium">
            校园邮箱
          </label>
          <input
            id="email"
            type="email"
            autoComplete="email"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            placeholder="xxx@stu.example.edu.cn"
            className="w-full rounded-md border border-border-subtle bg-surface px-3 py-2.5 outline-none focus:border-brand-500"
          />
          <p className="mt-1.5 text-xs text-gray-500">
            仅用于验证在校身份，不会对外展示
          </p>
        </div>

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
          {submitting ? '提交中…' : '发送认证邮件'}
        </button>
      </form>

      <details className="mt-6 rounded-md border border-border-subtle p-4">
        <summary className="cursor-pointer text-sm font-medium">没有校园邮箱？</summary>
        <p className="mt-2 text-sm text-gray-600">
          可提交学号与学生证照片进行人工审核，通常 1 个工作日内完成。
        </p>
        <p className="mt-2 text-xs text-gray-500">
          隐私说明：学生证照片仅用于人工核验，认证结论产出 30 天后自动删除，
          平台长期只保留认证结果。
        </p>
      </details>
    </>
  );
}
