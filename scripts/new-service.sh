#!/usr/bin/env bash
# ============================================================================
# scripts/new-service.sh —— 基于 kratos v3 官方 layout 创建单仓内的新服务
#
# 官方模板产出的结构可用，但与本项目有两处刻意偏离（见 doc.md §15.2）：
#   1. 数据层用 GORM，不用模板内置的 Ent  -> 剥离 internal/data/ent/**
#   2. 契约集中在根 api/，不在服务内      -> 剥离服务内的 api/ 与 buf 配置
#
# 用法: scripts/new-service.sh <service-name> <http-port> <grpc-port>
#   例: scripts/new-service.sh user 8001 9001
# ============================================================================
set -euo pipefail

SVC="${1:?用法: new-service.sh <service-name> <http-port> <grpc-port>}"
HTTP_PORT="${2:?缺少 http 端口}"
GRPC_PORT="${3:?缺少 grpc 端口}"

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MODULE="github.com/fleamarket/app/${SVC}"
TARGET="${ROOT}/app/${SVC}"
LAYOUT_REPO="https://github.com/go-kratos/kratos-layout"

export GOROOT="${GOROOT:-/Users/v_lidongze/go/go1.23.2}"
export PATH="${GOROOT}/bin:${HOME}/go/bin:/opt/homebrew/bin:${PATH}"

if [[ -d "${TARGET}" ]]; then
  echo "!! app/${SVC} 已存在，拒绝覆盖" >&2
  exit 1
fi

echo ">> 1/6 由官方 layout 生成骨架 (必须带 -r，否则 v3 CLI 在非 TTY 下 panic)"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT
(cd "${TMP}" && kratos new "${SVC}" -r "${LAYOUT_REPO}" -t 120s >/dev/null)

mkdir -p "${ROOT}/app"
mv "${TMP}/${SVC}" "${TARGET}"

echo ">> 2/8 剥离模板的 Ent 数据层（本项目用 GORM）"
rm -rf "${TARGET}/internal/data/ent"
rm -f  "${TARGET}/internal/data/todo.go" "${TARGET}/internal/data/todo_test.go"
rm -f  "${TARGET}/internal/biz/todo.go"
rm -f  "${TARGET}/internal/service/todo.go" "${TARGET}/internal/service/todo_test.go"

echo ">> 3/8 剥离服务内契约与 buf 配置（契约集中在根 api/）"
rm -rf "${TARGET}/api"
rm -f  "${TARGET}/buf.yaml" "${TARGET}/buf.lock" \
       "${TARGET}/buf.gen.yaml" "${TARGET}/buf.gen.config.yaml" \
       "${TARGET}/openapi.yaml"

echo ">> 4/8 剥离模板自带的杂项文件（统一由仓库根管理）"
rm -f "${TARGET}/LICENSE" "${TARGET}/README.md" \
      "${TARGET}/AGENTS.md" "${TARGET}/CLAUDE.md" \
      "${TARGET}/.gitignore" "${TARGET}/.gitattributes" \
      "${TARGET}/Makefile"
rm -f "${TARGET}"/internal/*/README.md

echo ">> 5/8 改写 module 路径与端口"
# 模板以服务名作为 module 名，替换为本项目命名空间
find "${TARGET}" -type f \( -name '*.go' -o -name '*.proto' -o -name '*.mod' \) -print0 \
  | xargs -0 sed -i '' "s#\"${SVC}/internal#\"${MODULE}/internal#g"
sed -i '' "s#^module ${SVC}\$#module ${MODULE}#" "${TARGET}/go.mod"
sed -i '' -e "s#0.0.0.0:8000#0.0.0.0:${HTTP_PORT}#" \
          -e "s#0.0.0.0:9000#0.0.0.0:${GRPC_PORT}#" \
  "${TARGET}/configs/config.yaml"

echo ">> 6/8 生成 GORM 版数据层与空 ProviderSet"
# 模板的 data.go / biz.go / service.go 都引用了已被删除的 Ent 与 Todo 示例，
# 直接重写为本项目的骨架，避免留下无法编译的残骸。
bash "${ROOT}/scripts/internal/gen-service-files.sh" "${SVC}" "${MODULE}" "${TARGET}"

echo ">> 7/8 加入 go.work"
if ! grep -q "use ./app/${SVC}" "${ROOT}/go.work"; then
  printf '\nuse ./app/%s\n' "${SVC}" >> "${ROOT}/go.work"
fi

echo ">> 8/8 生成配置结构体并整理依赖"
(cd "${ROOT}" && make conf-"${SVC}" >/dev/null 2>&1) || \
  echo "   !! conf 生成失败，稍后手工执行 make conf"
(cd "${TARGET}" && go mod tidy >/dev/null 2>&1 || true)

cat <<EOF

完成: app/${SVC}  (module ${MODULE})
  HTTP ${HTTP_PORT} / gRPC ${GRPC_PORT}

后续需手工完成:
  - app/${SVC}/entity/ 定义表实体（纯结构体，导出）并向 pkg/orm 注册
  - internal/biz、internal/data、internal/service 实现业务逻辑
  - cmd/${SVC}/wire.go 接入所需 provider 后执行 make wire-${SVC}
EOF
