#!/usr/bin/env bash
# ============================================================================
# 强制 pkg/ 公共库的「泛用性」约束（doc.md §5.1）。
#
# 检查两件事：
#   1. 依赖纯净：pkg 不得 import 任何 app/* 或 api/*。
#      —— app/* 会造成公共库反向依赖业务；
#      —— api/* 是业务契约（user.v1 等），引入后公共库就绑定了具体业务领域。
#   2. 命名纯净：pkg 的源码中不得出现业务名词。
#      公共库出现 Product / Order 这类词，说明抽象没做对。
#
# 测试文件（*_test.go）豁免命名检查：用商品状态机做状态机的测试用例是合理的，
# 那是在演示通用能力如何被业务使用，而非公共库自身持有业务语义。
# ============================================================================
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PKG="${ROOT}/pkg"
FAIL=0

if [[ ! -d "${PKG}" ]]; then
  echo "SKIP: pkg/ 尚不存在"
  exit 0
fi

echo ">> 检查 1/3: pkg/go.mod 不得依赖 app/* 或 api/*"
if grep -nE "github\.com/fleamarket/(app|api)" "${PKG}/go.mod" 2>/dev/null; then
  echo "   FAIL: pkg/go.mod 出现业务模块依赖" >&2
  FAIL=1
else
  echo "   OK"
fi

echo ">> 检查 2/3: pkg 源码不得 import app/* 或 api/*"
HITS="$(grep -rnE "\"github\.com/fleamarket/(app|api)/" "${PKG}" --include='*.go' 2>/dev/null || true)"
if [[ -n "${HITS}" ]]; then
  echo "${HITS}" >&2
  echo "   FAIL: 公共库反向依赖了业务代码" >&2
  FAIL=1
else
  echo "   OK"
fi

echo ">> 检查 3/3: pkg 非测试源码不得出现业务名词"
# 业务名词清单。刻意不含 User —— pkg/auth 需要 UserID 这样的中性身份概念，
# 它在任何有登录态的系统里都通用，不算本平台特有的业务语义。
BUSINESS_WORDS='Product|Order|Campus|School|Student|Seller|Buyer|Favorite|Wanted|Bargain|Notification|Moderation|Banner|Announcement|Credit|Merchandise|Goods'
BAD=""
while IFS= read -r f; do
  [[ "${f}" == *_test.go ]] && continue
  # 只看代码，不看注释：注释里用"商品状态机"举例说明用途是必要的文档，
  # 标识符里出现业务名词才是真正的抽象泄漏。
  code="$(sed -e 's#//.*##' "${f}" | grep -vE '^\s*(\*|/\*)' || true)"
  hit="$(printf '%s' "${code}" | grep -nE "\b(${BUSINESS_WORDS})" || true)"
  if [[ -n "${hit}" ]]; then
    BAD="${BAD}\n${f}:\n${hit}"
  fi
done < <(find "${PKG}" -name '*.go' -type f)

if [[ -n "${BAD}" ]]; then
  printf '%b\n' "${BAD}" >&2
  echo "   FAIL: 公共库标识符中出现业务名词" >&2
  FAIL=1
else
  echo "   OK"
fi

if [[ "${FAIL}" != "0" ]]; then
  echo ""
  echo "pkg 纯净性检查未通过。修复方式：把业务语义下移到 app/<svc>/internal，"
  echo "公共库只保留与业务无关的机制（接口、算法、封装）。" >&2
  exit 1
fi

echo ""
echo "PASS: pkg 公共库纯净性检查通过"
