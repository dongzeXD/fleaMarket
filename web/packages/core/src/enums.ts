/**
 * 枚举文案映射。
 *
 * 后端枚举值到中文展示文本的唯一映射来源。
 * 集中在此而非散落各组件：同一个状态在列表页和详情页显示不同措辞
 * 是最常见的文案不一致来源。
 */

/** 校园认证状态。对应 api/user/v1 的 CampusAuthStatus。 */
export const CampusAuthStatus = {
  UNSPECIFIED: 'CAMPUS_AUTH_STATUS_UNSPECIFIED',
  NONE: 'CAMPUS_AUTH_STATUS_NONE',
  PENDING: 'CAMPUS_AUTH_STATUS_PENDING',
  PASSED: 'CAMPUS_AUTH_STATUS_PASSED',
  REJECTED: 'CAMPUS_AUTH_STATUS_REJECTED',
  EXPIRED: 'CAMPUS_AUTH_STATUS_EXPIRED',
} as const;

export type CampusAuthStatusValue = (typeof CampusAuthStatus)[keyof typeof CampusAuthStatus];

const campusAuthStatusText: Record<string, string> = {
  [CampusAuthStatus.NONE]: '未认证',
  [CampusAuthStatus.PENDING]: '审核中',
  [CampusAuthStatus.PASSED]: '已认证',
  [CampusAuthStatus.REJECTED]: '认证未通过',
  [CampusAuthStatus.EXPIRED]: '认证已过期',
};

export function campusAuthStatusLabel(status: string | undefined): string {
  if (!status) return '未认证';
  return campusAuthStatusText[status] ?? '未认证';
}

/** 是否已完成认证。未认证用户不可发布商品、不可发起会话。 */
export function isCampusVerified(status: string | undefined): boolean {
  return status === CampusAuthStatus.PASSED;
}

/**
 * 未认证时的行为提示。
 *
 * 区分状态给出不同引导：审核中让用户等待，驳回引导重新提交，
 * 过期引导重新认证。统一提示「请先认证」会让审核中的用户反复提交。
 */
export function campusAuthHint(status: string | undefined): string {
  switch (status) {
    case CampusAuthStatus.PASSED:
      return '';
    case CampusAuthStatus.PENDING:
      return '认证审核中，通过后即可发布商品';
    case CampusAuthStatus.REJECTED:
      return '认证未通过，请检查材料后重新提交';
    case CampusAuthStatus.EXPIRED:
      return '认证已过期，请重新完成校园认证';
    default:
      return '完成校园认证后才能发布商品与发起聊天';
  }
}

/** 商品成色。对应 api/product/v1 的 Condition。 */
export const conditionLabels: Record<number, string> = {
  1: '全新',
  2: '几乎全新',
  3: '轻微使用痕迹',
  4: '明显使用痕迹',
};

export function conditionLabel(condition: number | undefined): string {
  if (!condition) return '';
  return conditionLabels[condition] ?? '';
}

/** 商品状态。对应 doc.md §9.2 的状态机。 */
export const productStatusLabels: Record<number, string> = {
  0: '待审核',
  1: '在售',
  2: '已预订',
  3: '已售出',
  4: '已下架',
  5: '审核未通过',
};

export function productStatusLabel(status: number | undefined): string {
  if (status === undefined) return '';
  return productStatusLabels[status] ?? '';
}

/** 拉黑原因。对应 api/user/v1 的 BlockReason。 */
export const blockReasons = [
  { value: 'BLOCK_REASON_HARASSMENT', label: '骚扰' },
  { value: 'BLOCK_REASON_FRAUD_SUSPECT', label: '疑似诈骗' },
  { value: 'BLOCK_REASON_SPAM', label: '垃圾信息' },
  { value: 'BLOCK_REASON_OTHER', label: '其他' },
] as const;
