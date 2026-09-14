#!/bin/sh
set -eu
REPO_ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
cd "$REPO_ROOT"
# 默认日志目录及前台参数由生命周期协议测试核验，避免绑定脚本文本实现。
go test ./internal/lifecycle -run TestDefaultsPreserveDailyLogAndForeground -count=1
