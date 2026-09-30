/**
 * 时间格式化。
 *
 * 后端统一以 Unix 秒传递时间戳（见各 proto 的 created_at 等字段），
 * 避免时区与字符串格式的歧义。本模块负责转成人类可读文本。
 */

/** 后端时间戳（Unix 秒）转 Date。0 视为「无值」。 */
export function fromUnix(seconds: number | undefined | null): Date | null {
  if (!seconds) return null;
  return new Date(seconds * 1000);
}

const MINUTE = 60;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;

/**
 * 相对时间描述，如「3 分钟前」。
 *
 * 校园二手场景下，「多久前发布」比精确时刻更有意义——
 * 用户关心的是这条商品还新不新鲜。超过 7 天才退回绝对日期。
 *
 * now 参数可注入以便测试，默认取当前时间。
 */
export function relativeTime(seconds: number | undefined | null, now = Date.now()): string {
  const date = fromUnix(seconds);
  if (!date) return '';

  const diff = Math.floor((now - date.getTime()) / 1000);

  // 时钟漂移或服务端时间略快时 diff 会是负数，
  // 显示「-2 分钟前」很怪，统一按「刚刚」处理。
  if (diff < MINUTE) return '刚刚';
  if (diff < HOUR) return `${Math.floor(diff / MINUTE)} 分钟前`;
  if (diff < DAY) return `${Math.floor(diff / HOUR)} 小时前`;
  if (diff < 7 * DAY) return `${Math.floor(diff / DAY)} 天前`;

  return formatDate(seconds);
}

/** 绝对日期，如 "2026-09-30"。 */
export function formatDate(seconds: number | undefined | null): string {
  const date = fromUnix(seconds);
  if (!date) return '';
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`;
}

/** 绝对时刻，如 "2026-09-30 15:04"。 */
export function formatDateTime(seconds: number | undefined | null): string {
  const date = fromUnix(seconds);
  if (!date) return '';
  return `${formatDate(seconds)} ${pad(date.getHours())}:${pad(date.getMinutes())}`;
}

/**
 * 倒计时的剩余秒数转 mm:ss。
 *
 * 用于验证码重发倒计时与预订超时提示。
 */
export function countdown(remainingSeconds: number): string {
  const s = Math.max(0, Math.floor(remainingSeconds));
  return `${pad(Math.floor(s / 60))}:${pad(s % 60)}`;
}

function pad(n: number): string {
  return String(n).padStart(2, '0');
}
