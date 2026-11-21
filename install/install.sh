#!/bin/sh
# ============================================================================
# install.sh — 安装 dsync（从 GitHub Release 下载并校验 sha256），然后执行 dsync init
#
# 仓库公开，匿名即可安装（设置了 GITHUB_TOKEN 或已登录 gh 时会自动带上，用于提高 API 限额）：
#
#   curl -fsSL https://raw.githubusercontent.com/Lin-Ya/dsync-cli/main/install/install.sh \
#     | sh -s -- https://github.com/<you>/dotfiles.git      # 参数原样传给 dsync init
#
# 环境变量：
#   GITHUB_TOKEN       可选：提高 API 速率限制（未认证每小时 60 次）
#   DSYNC_VERSION      目标版本 tag（默认最新，含预发布）
#   DSYNC_INSTALL_DIR  安装目录（默认 ~/.local/bin）
#   DSYNC_GITHUB_API   GitHub API 地址（测试用）
# 不带参数时只安装 dsync，不执行 init。
# ============================================================================
set -eu

REPO="Lin-Ya/dsync-cli"
API="${DSYNC_GITHUB_API:-https://api.github.com}"
DIR="${DSYNC_INSTALL_DIR:-$HOME/.local/bin}"

die() { echo "dsync install: $*" >&2; exit 1; }

# Token 可选：公开仓库匿名也能下载；设置了就带上，提高 API 速率限制。
TOKEN="${GITHUB_TOKEN:-}"
if [ -z "$TOKEN" ] && command -v gh >/dev/null 2>&1; then
  TOKEN="$(gh auth token 2>/dev/null || true)"
fi

case "$(uname -s)" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) die "不支持的系统 $(uname -s)（Windows 请用 install.ps1）" ;;
esac
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) die "不支持的架构 $(uname -m)" ;;
esac
name="dsync_${os}_${arch}"

api() { # $1=URL $2=Accept $3=输出文件（可选，缺省输出到 stdout）
  if [ -n "$TOKEN" ]; then
    curl -fsSL -H "Authorization: Bearer $TOKEN" -H "Accept: $2" -H "X-GitHub-Api-Version: 2022-11-28" \
      ${3:+-o "$3"} "$1" || die "请求 $1 失败（网络受限，或 Token 无效 / 无仓库权限？）"
  else
    curl -fsSL -H "Accept: $2" -H "X-GitHub-Api-Version: 2022-11-28" \
      ${3:+-o "$3"} "$1" || die "请求 $1 失败（网络受限，或触发了 GitHub 匿名速率限制？设置 GITHUB_TOKEN 可提高限额）"
  fi
}

if [ -n "${DSYNC_VERSION:-}" ]; then
  json="$(api "$API/repos/$REPO/releases/tags/$DSYNC_VERSION" application/vnd.github+json)"
else
  # /releases/latest 不含预发布版本，取列表中最新的一个
  json="$(api "$API/repos/$REPO/releases?per_page=1" application/vnd.github+json)"
fi

# 拆成 "url <值>" / "name <值>" 序列（不依赖 jq）；资产对象中 url（.../releases/assets/<id>）在 name 之前
# shellcheck disable=SC2020  # 有意按字符把 , { } 都替换为换行
fields="$(printf '%s' "$json" | tr ',{}' '\n\n\n' |
  sed -n -e 's/^[[:space:]]*"url":[[:space:]]*"\(.*\)"[[:space:]]*$/url \1/p' \
         -e 's/^[[:space:]]*"name":[[:space:]]*"\(.*\)"[[:space:]]*$/name \1/p' \
         -e 's/^[[:space:]]*"tag_name":[[:space:]]*"\(.*\)"[[:space:]]*$/tag \1/p')"
tag="$(printf '%s\n' "$fields" | awk '$1 == "tag" { print $2; exit }')"
asset_url() {
  printf '%s\n' "$fields" | awk -v want="$1" '
    $1 == "url" && $2 ~ /\/releases\/assets\/[0-9]+$/ { u = $2; next }
    $1 == "name" && u != "" { if ($2 == want) { print u; exit } u = "" }'
}
bin_url="$(asset_url "$name")"
sums_url="$(asset_url checksums.txt)"
[ -n "$tag" ] || die "没有找到可用的 Release"
[ -n "$bin_url" ] || die "Release $tag 中没有 $name"
[ -n "$sums_url" ] || die "Release $tag 中没有 checksums.txt"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
api "$sums_url" application/octet-stream "$tmp/checksums.txt"
api "$bin_url" application/octet-stream "$tmp/$name"

want="$(awk -v n="$name" '$2 == n || $2 == "*" n { print $1; exit }' "$tmp/checksums.txt")"
[ -n "$want" ] || die "checksums.txt 中没有 $name"
if command -v sha256sum >/dev/null 2>&1; then
  got="$(sha256sum "$tmp/$name" | awk '{print $1}')"
else
  got="$(shasum -a 256 "$tmp/$name" | awk '{print $1}')"
fi
[ "$got" = "$want" ] || die "$name 校验失败（sha256 $got，期望 $want），已拒绝安装"

mkdir -p "$DIR"
chmod 755 "$tmp/$name"
mv -f "$tmp/$name" "$DIR/dsync"
echo "dsync $tag 已安装到 $DIR/dsync"

case ":$PATH:" in
  *":$DIR:"*) ;;
  *) echo "提示：$DIR 不在 PATH 中，可执行 export PATH=\"$DIR:\$PATH\"" ;;
esac

if [ $# -gt 0 ]; then
  exec "$DIR/dsync" init "$@"
fi
echo "下一步：$DIR/dsync init <dotfiles 仓库地址>"
