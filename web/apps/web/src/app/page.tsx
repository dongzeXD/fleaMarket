/**
 * 首页信息流。
 *
 * 当前为占位：商品数据依赖 product-service 与 search-service（Task 10/11）。
 * 此处只验证响应式外壳与会话恢复链路。
 */
export default function HomePage() {
  return (
    <div className="mx-auto max-w-7xl px-4 py-6">
      <h1 className="text-xl font-semibold md:text-2xl">同校闲置</h1>
      <p className="mt-2 text-sm text-gray-500">
        优先展示同校区的商品，线下面交更安全。
      </p>

      {/* 网格列数随断点变化：移动 2 列 / 平板 3 列 / 桌面 4~5 列
          （doc.md §10.2 的适配表）。 */}
      <div className="mt-6 grid grid-cols-2 gap-3 md:grid-cols-3 lg:grid-cols-4 xl:grid-cols-5">
        {Array.from({ length: 10 }).map((_, i) => (
          <div
            key={i}
            className="rounded-lg border border-border-subtle bg-surface-muted p-3"
          >
            {/* aspect-square 预留图片位，避免图片加载后布局跳变（CLS）。 */}
            <div className="aspect-square rounded bg-border-subtle" aria-hidden />
            <p className="mt-2 truncate text-sm">商品位（待 product-service 接入）</p>
            <p className="mt-1 text-sm font-medium text-brand-600">¥--</p>
          </div>
        ))}
      </div>
    </div>
  );
}
