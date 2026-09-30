/**
 * @fleamarket/core
 *
 * 纯 TypeScript 业务逻辑，零 DOM 与框架依赖。
 *
 * 存在的理由（doc.md §10.5）：后期追加微信小程序时，UI 层需要用 Taro 重写，
 * 但金额计算、时间格式化、表单校验、枚举文案这些逻辑与渲染无关，
 * 可以原样复用。把它们隔离在本包中，使「共享逻辑、分别实现视图」
 * 成为可强制的边界——这里一旦出现 window 或 React 的引用，构建就会失败。
 */
export * from './money';
export * from './time';
export * from './validation';
export * from './enums';
