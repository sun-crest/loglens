#!/usr/bin/env bash
# 一键发版：校验 → 打包 → 打远端 tag → 发 GitHub release → 推进下一开发版本。
#
# 用法:
#   ./scripts/release.sh              # 交互确认后执行（发版动作不可逆）
#   ./scripts/release.sh --dry-run    # 只打印将要执行的步骤，不做任何改动
#   ./scripts/release.sh --yes        # 跳过发布前的确认提示（CI / 你已确信时用）
#
# 版本号（cmd/loglens/main.go 为唯一真相源）:
#   开发期: x.y.z-dev   （日志/version 输出标记非发布态，本脚本直接拒绝）
#   发布态: x.y.z       （纯版本号，配合 annotated tag vx.y.z 一起发）
#
# 与 package.sh 的分工：
#   - 本脚本是"人/CI"的一键发版流水线，串起 bump 之后的全部动作；
#   - package.sh 只做"构建+打包+冒烟"，纯函数化，不触碰 git/gh。
#   - tag 与 bump 是显式语义动作：脚本不代打 tag、不代去 -dev。
#
# 依赖: bash 4+, go, tar, sha256sum, sed, grep, mktemp, git, gh。
set -euo pipefail

DRY_RUN=0
YES=0
usage() {
  sed -n '2,11p' "$0" | sed 's/^# \{0,1\}//'
  exit ${1:-1}
}
for arg in "$@"; do
  case "$arg" in
    --dry-run) DRY_RUN=1 ;;
    --yes)     YES=1 ;;
    -h|--help) usage 0 ;;
    *) echo "error: 未知参数 '$arg'" >&2; usage ;;
  esac
done

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$ROOT"

# ---------- 1. 版本号: 唯一真相源 = main.go const version ----------
VERSION="$(sed -n 's/^const version = "\(.*\)"$/\1/p' cmd/loglens/main.go)"
if [[ "$VERSION" == *-* ]]; then
  echo "error: 当前版本 '${VERSION}' 是开发版本号（含 - 后缀）。" >&2
  echo "       发版前请把 cmd/loglens/main.go 的 const version 改为纯 x.y.z，提交后打 tag 再重跑。" >&2
  exit 1
fi
if ! [[ "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "error: 无法从 cmd/loglens/main.go 确定发版版本号 (当前='${VERSION}')" >&2
  exit 1
fi
TAG="v${VERSION}"

# ---------- 2. 工作区必须干净：发版产物必须与某个提交一一对应 ----------
if [ -n "$(git status --porcelain)" ]; then
  echo "error: git 工作区不干净。请先提交 bump 再发版：" >&2
  echo "       git add cmd/loglens/main.go && git commit -m \"chore: bump version to ${VERSION}\"" >&2
  exit 1
fi

# ---------- 3. annotated tag 必须已打到 HEAD（tag 是显式语义动作，不代打） ----------
tagged="$(git rev-parse -q --verify "refs/tags/${TAG}^{commit}" 2>/dev/null || true)"
if [ "$tagged" != "$(git rev-parse HEAD)" ]; then
  echo "error: 缺少指向 HEAD 的标签 ${TAG}（或标签指向旧提交）。" >&2
  echo "       请先打 annotated tag 再重跑本脚本：" >&2
  echo "       git tag -a ${TAG} -m \"loglens ${VERSION}\"" >&2
  exit 1
fi

# ---------- 4. 打印计划（dry-run 到此为止，不做任何写操作） ----------
next_dev() {  # 发 x.y.0 -> x.(y+1).0-dev；发 x.y.z(z>0) -> x.y.(z+1)-dev
  local major minor patch
  IFS=. read -r major minor patch <<<"${VERSION}"
  if [ "$patch" = 0 ]; then
    echo "${major}.$((minor + 1)).0-dev"
  else
    echo "${major}.${minor}.$((patch + 1))-dev"
  fi
}
NEXT="$(next_dev)"

echo "===== 发版计划: loglens ${VERSION} (tag ${TAG}) ====="
echo "  1. git push origin ${TAG}          # 把本地 annotated tag 推送到远端"
echo "  2. ./scripts/package.sh            # 打包 dist/*.tar.gz + .sha256，冒烟自校验"
echo "  3. gh release create ${TAG} ... --generate-notes"
echo "  4. main.go -> ${NEXT} 并提交       # 发布完立刻推进下一开发版本"
if [ "$DRY_RUN" = 1 ]; then
  echo "===== dry-run: 以上为将要执行的步骤，未做任何改动 ====="
  exit 0
fi

# ---------- 5. 确认（-dev 已被拦，这里再拦一次人） ----------
if [ "$YES" = 0 ]; then
  read -r -p "确认发布并推送? [y/N] " ans
  case "$ans" in
    y|Y|yes|YES) ;;
    *) echo "已取消"; exit 1 ;;
  esac
fi

# ---------- 6. 起手前最后校验 ----------
if ! command -v gh >/dev/null 2>&1; then
  echo "error: 未找到 gh。先装 GitHub CLI 并 gh auth login，或手动 gh release create。" >&2
  exit 1
fi
if gh release view "$TAG" --json id >/dev/null 2>&1; then
  echo "error: ${TAG} 已在 GitHub 发布过（gh release view ${TAG} 可见），重复发布请先删旧 release。" >&2
  exit 1
fi

echo "[1/4] 推送 tag ${TAG} ..."
git push origin "$TAG"

echo "[2/4] 打包 ..."
./scripts/package.sh

echo "[3/4] 发 GitHub release ${TAG} ..."
assets=(
  "dist/loglens-${VERSION}-linux-amd64.tar.gz"
  "dist/loglens-${VERSION}-linux-amd64.tar.gz.sha256"
)
for f in "${assets[@]}"; do
  [ -f "$f" ] || { echo "error: 缺少发版产物 $f" >&2; exit 1; }
done
gh release create "$TAG" "${assets[@]}" --generate-notes

echo "[4/4] 推进开发版本 ${NEXT} ..."
sed -i "s/^const version = \"${VERSION}\"$/const version = \"${NEXT}\"/" cmd/loglens/main.go
grep -q "^const version = \"${NEXT}\"$" cmd/loglens/main.go || { echo "error: bump 写入失败" >&2; exit 1; }
git add cmd/loglens/main.go
git commit -m "chore: bump version to ${NEXT}"

echo "===== done. release: https://github.com/sun-crest/loglens/releases/tag/${TAG} ====="
git status --short