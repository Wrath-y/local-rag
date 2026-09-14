#!/bin/sh
set -eu
SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
cd "$SCRIPT_DIR"
# 始终由同一 Go 入口处理参数、进程身份与 JSON 状态。
BIN="$SCRIPT_DIR/.run/bin/rag-server"
if ! command -v go >/dev/null 2>&1; then
    echo "缺少 Go，请先安装与 go.mod 匹配的 Go 工具链。" >&2
    exit 1
fi
mkdir -p "$SCRIPT_DIR/.run/bin"
go build -o "$BIN" ./cmd/server/
exec "$BIN" lifecycle start "$@"
