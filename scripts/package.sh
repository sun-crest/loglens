#!/usr/bin/env bash
# 打包 LogLens 目录树工作区为一个自包含发行包。
#
# 用法:
#   ./scripts/package.sh            # 版本号从 cmd/loglens/main.go 唯一读取
#
# 不接受版本参数：版本号唯一真相源是 cmd/loglens/main.go 的 const version，
# 以保证包名与二进制内 "loglens version" 永远一致（发版流程：先 bump 该常量，
# 再 tag，再打包）。CI 复用同一脚本，一致性由该流程保证。
#   # GitHub Actions 片段
#   - run: ./scripts/package.sh
#   - run: gh release create "${GITHUB_REF_NAME}" $(ls dist/*.tar.gz dist/*.sha256)
#
# 依赖: bash 4+, go, tar (GNU), sha256sum, sed, grep, mktemp。
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$ROOT"

# ---------- 版本号: 唯一真相源 = main.go const version ----------
if [ $# -gt 0 ]; then
  echo "error: 不接受版本参数；版本号唯一真相源是 cmd/loglens/main.go（请先 bump 该常量）" >&2
  exit 1
fi
version="$(sed -n 's/^const version = "\(.*\)"$/\1/p' cmd/loglens/main.go)"
if ! [[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "error: 无法从 cmd/loglens/main.go 确定版本号 (当前='${version}')" >&2
  exit 1
fi

name="loglens-${version}-linux-amd64"
stage="$(mktemp -d)"
smoke="$(mktemp -d)"
trap 'rm -rf "$stage" "$smoke"' EXIT
root="$stage/loglens"

echo "[1/5] 静态构建 CGO_ENABLED=0 ..."
mkdir -p "$root/bin"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -ldflags "-s -w" -o "$root/bin/loglens" ./cmd/loglens

echo "[2/5] 组装目录树 {bin, config, logs} ..."
mkdir -p "$root/config" "$root/logs"
cp config/loglens.toml.example "$root/config/loglens.toml.example"

echo "[3/5] 打包 tar.gz + sha256 ..."
mkdir -p dist
dist="dist/${name}.tar.gz"
tar -czf "$dist" -C "$stage" loglens
sha256sum "$dist" > "$dist.sha256"

echo "[4/5] 冒烟: 解包产物校验自包含 ..."
tar -xzf "$dist" -C "$smoke"
got="$("$smoke/loglens/bin/loglens" version 2>/dev/null || true)"
if [ "$got" != "loglens ${version}" ]; then
  echo "error: 解包冒烟失败 version='${got:-<none>}', 期望 'loglens ${version}'" >&2
  exit 1
fi
[ -f "$smoke/loglens/config/loglens.toml.example" ] || { echo "error: 包内缺少配置模板" >&2; exit 1; }
[ -d "$smoke/loglens/logs" ] || { echo "error: 包内缺少 logs/ 目录" >&2; exit 1; }

echo "[5/5] done."
ls -l "$dist" "$dist.sha256"