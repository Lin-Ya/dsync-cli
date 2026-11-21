#!/usr/bin/env bash
# 启动一个"新设备"容器，以普通用户 ubuntu 进入交互 shell，手动执行 dsync init 等命令。
#   bash e2e/manual/run.sh [镜像]     默认 dsync-fresh:24.04（每次都是全新状态，退出即删除）
# 每次运行都会重新编译 dsync 并只读挂载到 /usr/local/bin/dsync（不经过 Release 下载）。
# GitHub token：优先 $GITHUB_TOKEN，否则 gh auth token；挂载为文件，不进镜像、不进容器环境变量。
#   （dsync-cli 已公开，这个 token 只为克隆你的 dotfiles 仓库）
set -euo pipefail

CLI_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
IMAGE="${1:-dsync-fresh:24.04}"
NAME=dsync-manual

if [ "$IMAGE" = dsync-fresh:24.04 ]; then
  docker build -q -t "$IMAGE" "$CLI_DIR/e2e/manual" >/dev/null
fi

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
(cd "$CLI_DIR" && mise x go@1.25 -- env CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o "$WORK/dsync" ./cmd/dsync)

token="${GITHUB_TOKEN:-$(gh auth token 2>/dev/null || true)}"
[ -n "$token" ] || { echo "需要 GITHUB_TOKEN 或已登录的 gh（用于克隆 dotfiles 仓库；仓库公开时可去掉这一步）" >&2; exit 2; }
(umask 077 && printf '%s' "$token" > "$WORK/github_token")

if [ -n "${http_proxy:-}${https_proxy:-}" ]; then
  export no_proxy="${no_proxy:+$no_proxy,}archive.ubuntu.com,security.ubuntu.com,mirrors.tuna.tsinghua.edu.cn"
fi

echo "== $IMAGE  容器名 $NAME  dsync $("$WORK/dsync" version)"
docker run --rm -it --name "$NAME" --hostname "$NAME" \
  -e http_proxy -e https_proxy -e no_proxy -e TERM \
  -v "$WORK/dsync:/usr/local/bin/dsync:ro" \
  -v "$WORK/github_token:/run/secrets/github_token:ro" \
  "$IMAGE" sh -c 'exec "$(getent passwd "$(id -un)" | cut -d: -f7)" -l'
