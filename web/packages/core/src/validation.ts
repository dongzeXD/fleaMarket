/**
 * 表单校验。
 *
 * 规则与 proto 中的 buf.validate 注解保持一致（见 api/user/v1）。
 * 前端校验的目的是即时反馈，不是安全边界——服务端会用同一套约束
 * 再校验一次，因此这里放过的输入不会造成数据问题。
 *
 * 为什么仍要写一遍：等一次网络往返才告诉用户「手机号格式不对」
 * 体验太差。这属于刻意的重复，代价是两处规则需同步维护，
 * 因此每条规则都在注释里指明其 proto 来源。
 */

/** 校验结果。ok 为 false 时 message 必定非空。 */
export type ValidationResult = { ok: true } | { ok: false; message: string };

const OK: ValidationResult = { ok: true };

function fail(message: string): ValidationResult {
  return { ok: false, message };
}

/** 手机号。对应 proto: `^1[3-9]\d{9}$`。 */
export function validatePhone(input: string): ValidationResult {
  const v = input.trim();
  if (v === '') return fail('请输入手机号');
  if (!/^1[3-9]\d{9}$/.test(v)) return fail('手机号格式不正确');
  return OK;
}

/** 短信验证码。对应 proto: `^\d{6}$`。 */
export function validateSmsCode(input: string): ValidationResult {
  const v = input.trim();
  if (v === '') return fail('请输入验证码');
  if (!/^\d{6}$/.test(v)) return fail('验证码为 6 位数字');
  return OK;
}

/** 校园邮箱。对应 proto 的 `string.email` 约束。 */
export function validateEduEmail(input: string): ValidationResult {
  const v = input.trim();
  if (v === '') return fail('请输入校园邮箱');
  // 不做严格 RFC 校验：过严会误伤合法地址，
  // 而域名是否属于该校由服务端按白名单判定（见 School.AllowsEmail）。
  if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(v)) return fail('邮箱格式不正确');
  return OK;
}

/** 昵称，2~20 字符。对应 proto: min_len 2, max_len 20。 */
export function validateNickname(input: string): ValidationResult {
  const v = input.trim();
  const len = [...v].length;
  if (len === 0) return fail('请输入昵称');
  if (len < 2) return fail('昵称至少 2 个字符');
  if (len > 20) return fail('昵称最多 20 个字符');
  return OK;
}

/** 学号，4~32 字符。对应 proto: min_len 4, max_len 32。 */
export function validateStudentNo(input: string): ValidationResult {
  const v = input.trim();
  if (v === '') return fail('请输入学号');
  if (v.length < 4) return fail('学号至少 4 位');
  if (v.length > 32) return fail('学号最多 32 位');
  return OK;
}

/** 真实姓名，2~20 字符。对应 proto: min_len 2, max_len 20。 */
export function validateRealName(input: string): ValidationResult {
  const v = input.trim();
  const len = [...v].length;
  if (len < 2) return fail('请输入真实姓名');
  if (len > 20) return fail('姓名最多 20 个字符');
  return OK;
}

/** 商品标题，2~60 字符。对应 proto: min_len 2, max_len 60。 */
export function validateProductTitle(input: string): ValidationResult {
  const v = input.trim();
  const len = [...v].length;
  if (len === 0) return fail('请输入标题');
  if (len < 2) return fail('标题至少 2 个字符');
  if (len > 60) return fail('标题最多 60 个字符');
  return OK;
}

/**
 * 逐条执行校验，返回第一个失败项。
 *
 * 只报第一个错误而非全部：表单一次弹出 5 条红字会让用户无从下手。
 */
export function firstError(...results: ValidationResult[]): string | null {
  for (const r of results) {
    if (!r.ok) return r.message;
  }
  return null;
}
