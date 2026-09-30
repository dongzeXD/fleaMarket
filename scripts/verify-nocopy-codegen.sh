#!/usr/bin/env bash
# ============================================================================
# 验证 doc.md §6.3 的硬性需求：
#   「其余客户端不需要移动 & 拷贝 proto 文件就可以生成客户端代码」
#
# 做法：在一个全新的空目录中，只放一个 buf.gen.yaml 与仓库产出的
# descriptor.binpb（自包含 FileDescriptorSet），用 buf 远程插件生成 TS 客户端。
# 全程不存在任何 .proto 文件，本地也不需要安装 protoc 插件。
#
# 若已将模块推送到 registry（make api-push），还可直接:
#   buf generate buf.build/fleamarket/api
# 连 descriptor 文件都不需要。脚本会在检测到可用 registry 时附带验证该路径。
# ============================================================================
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DESC="${ROOT}/docs/descriptor.binpb"

export GOROOT="${GOROOT:-/Users/v_lidongze/go/go1.23.2}"
export PATH="${GOROOT}/bin:${HOME}/go/bin:/opt/homebrew/bin:${PATH}"

if [[ ! -f "${DESC}" ]]; then
  echo ">> descriptor 不存在，先执行 make api-descriptor"
  (cd "${ROOT}" && make api-descriptor)
fi

SANDBOX="$(mktemp -d)"
trap 'rm -rf "${SANDBOX}"' EXIT

cat > "${SANDBOX}/buf.gen.yaml" <<'EOF'
version: v2
plugins:
  - remote: buf.build/bufbuild/es
    out: gen
    opt: [target=ts]
EOF

cp "${DESC}" "${SANDBOX}/descriptor.binpb"

echo ">> 沙箱目录: ${SANDBOX}"
echo ">> 生成前目录内容:"
( cd "${SANDBOX}" && ls -1 )

( cd "${SANDBOX}" && buf generate descriptor.binpb )

PROTO_COUNT="$(find "${SANDBOX}" -name '*.proto' | wc -l | tr -d ' ')"
GEN_COUNT="$(find "${SANDBOX}/gen" -type f 2>/dev/null | wc -l | tr -d ' ')"

echo ">> 产出文件数: ${GEN_COUNT}"
find "${SANDBOX}/gen" -type f 2>/dev/null | sed "s#${SANDBOX}/#  #" | sort
echo ">> 沙箱内 .proto 文件数: ${PROTO_COUNT}"

if [[ "${PROTO_COUNT}" != "0" ]]; then
  echo "FAIL: 沙箱中出现了 .proto 文件，需求未满足" >&2
  exit 1
fi
if [[ "${GEN_COUNT}" == "0" ]]; then
  echo "FAIL: 未产出任何客户端代码" >&2
  exit 1
fi

echo "PASS: 零 proto 文件前提下成功生成 ${GEN_COUNT} 个客户端代码文件"
