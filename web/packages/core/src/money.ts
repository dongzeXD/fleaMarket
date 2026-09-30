/**
 * 金额处理。
 *
 * 全链路以「分」为单位的整数传递（见 api/common/v1 的 Money 定义）。
 * 前端绝不用浮点数做金额运算——0.1 + 0.2 !== 0.3 这类误差
 * 在标价与砍价场景下会直接变成对不上的账。
 */

/** 分转为展示用的元字符串，如 1050 → "10.50"。 */
export function centsToYuan(cents: number): string {
  const sign = cents < 0 ? '-' : '';
  const abs = Math.abs(Math.trunc(cents));
  const yuan = Math.floor(abs / 100);
  const fen = abs % 100;
  return `${sign}${yuan}.${String(fen).padStart(2, '0')}`;
}

/**
 * 分转为带货币符号的展示文本。
 *
 * 0 显示「免费」而非「¥0.00」：平台允许免费赠送（见 proto 中 price_cents 的说明），
 * 显示 ¥0.00 会让用户以为是标价错误。
 */
export function formatPrice(cents: number): string {
  if (cents === 0) return '免费';
  return `¥${centsToYuan(cents)}`;
}

/**
 * 用户输入的元字符串转为分。
 *
 * 返回 null 表示输入非法，由调用方决定提示文案。
 * 刻意不抛异常：表单校验场景下，非法输入是常态而非异常。
 */
export function yuanToCents(input: string): number | null {
  const trimmed = input.trim();
  if (trimmed === '') return null;
  // 只接受最多两位小数。三位小数的金额在「分」的粒度下无法表示，
  // 静默四舍五入会让用户看到与输入不同的价格。
  if (!/^\d+(\.\d{1,2})?$/.test(trimmed)) return null;

  const [yuanPart, fenPart = ''] = trimmed.split('.');
  const yuan = Number(yuanPart);
  const fen = Number(fenPart.padEnd(2, '0'));
  if (!Number.isSafeInteger(yuan)) return null;
  return yuan * 100 + fen;
}

/** 计算降价幅度的百分比，用于「已降价 20%」这类提示。 */
export function discountPercent(oldCents: number, newCents: number): number | null {
  if (oldCents <= 0 || newCents >= oldCents) return null;
  return Math.round(((oldCents - newCents) / oldCents) * 100);
}
