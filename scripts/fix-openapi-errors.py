#!/usr/bin/env python3
"""修正 OpenAPI 文档中的错误响应结构。

背景：protoc-gen-openapi 为默认错误响应生成的是 google.rpc.Status
（字段 code / message / details），但本项目实际返回的是 kratos 的错误体
（字段 code / reason / message / metadata，见 doc.md §12）。

不修正的后果：由文档生成的客户端在错误分支上拿不到 reason 与 metadata。
前端需要按 reason 分支处理（如 CAMPUS_AUTH_REQUIRED 跳认证页），
并从 metadata.retry_after 读取限流倒计时——这两个字段缺失会让调用方
退回用 message 做字符串匹配，而文案一改就静默失效。

本脚本由 `make docs` 自动调用，不需要手工执行。
"""

from __future__ import annotations

import sys
from pathlib import Path

try:
    import yaml
except ImportError:
    print("fix-openapi-errors: 需要 PyYAML，跳过错误结构修正", file=sys.stderr)
    print("  安装: python3 -m pip install --user pyyaml", file=sys.stderr)
    sys.exit(0)


KRATOS_ERROR_SCHEMA = {
    "type": "object",
    "description": (
        "统一错误响应。由 kratos errors 产出，全部接口的失败响应均为此结构。"
    ),
    "properties": {
        "code": {
            "type": "integer",
            "format": "int32",
            "description": "HTTP 状态码，与响应的 HTTP status 一致。",
        },
        "reason": {
            "type": "string",
            "description": (
                "机器可读的错误原因，如 PRODUCT_ALREADY_RESERVED。"
                "客户端应按此字段分支处理，不要匹配 message 文案。"
            ),
        },
        "message": {
            "type": "string",
            "description": "面向用户的中文提示，可直接展示。",
        },
        "metadata": {
            "type": "object",
            "additionalProperties": {"type": "string"},
            "description": (
                "结构化上下文。例如限流时携带 retry_after（剩余等待秒数），"
                "资源冲突时携带 product_id。"
            ),
        },
    },
    "required": ["code", "reason", "message"],
}


def main() -> int:
    if len(sys.argv) < 2:
        print("用法: fix-openapi-errors.py <openapi.yaml>", file=sys.stderr)
        return 1

    path = Path(sys.argv[1])
    if not path.exists():
        print(f"fix-openapi-errors: {path} 不存在，跳过", file=sys.stderr)
        return 0

    doc = yaml.safe_load(path.read_text(encoding="utf-8"))
    schemas = doc.setdefault("components", {}).setdefault("schemas", {})

    schemas["Error"] = KRATOS_ERROR_SCHEMA
    # google.rpc.Status 与其依赖的 Any 不再被引用，移除以免误导读者。
    schemas.pop("Status", None)
    schemas.pop("GoogleProtobufAny", None)

    replaced = 0
    for methods in doc.get("paths", {}).values():
        if not isinstance(methods, dict):
            continue
        for op in methods.values():
            if not isinstance(op, dict):
                continue
            default = op.get("responses", {}).get("default")
            if not isinstance(default, dict):
                continue
            content = default.get("content", {})
            for media in content.values():
                if isinstance(media, dict) and "schema" in media:
                    media["schema"] = {"$ref": "#/components/schemas/Error"}
                    replaced += 1
            default["description"] = "请求失败"

    path.write_text(
        yaml.safe_dump(doc, allow_unicode=True, sort_keys=False, width=120),
        encoding="utf-8",
    )
    print(f"fix-openapi-errors: 已修正 {replaced} 处错误响应结构")
    return 0


if __name__ == "__main__":
    sys.exit(main())
