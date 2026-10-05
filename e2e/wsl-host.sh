#!/usr/bin/env bash
# ============================================================================
# wsl-host.sh — 真实 WSL 端到端：在临时发行版中验证 host setup 的 wsl-init 步骤
#
# 只能在 WSL 内运行（经 interop 调用 Windows 侧 wsl.exe / 测试程序），需要 docker。
# 流程：docker 导出 ubuntu:24.04 根文件系统 → wsl --import 临时发行版（无需管理员）
#       → 预装 git / sudo（模拟全新 WSL，不写代理）→ 运行 internal/host 的 TestRealWSL_WSLInit
#       （宿主有 https_proxy 时经 --proxy 'http://${WSL_HOST_IP}:<端口>' 传入，验证运行时推出宿主 IP）
#       → 注销临时发行版并删除文件。不触碰日常使用的发行版与 Docker Desktop 配置。
#
# 用法（cli/ 目录）：bash e2e/wsl-host.sh
#   WIN_DIR   Windows 侧工作目录（WSL 路径，默认 /mnt/c/Users/$USER/dsync-e2e）
#   DISTRO    临时发行版名（默认 dsync-e2e）
# ============================================================================
set -euo pipefail

CLI_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DISTRO="${DISTRO:-dsync-e2e}"
WIN_DIR="${WIN_DIR:-/mnt/c/Users/${USER}/dsync-e2e}"
command -v wsl.exe >/dev/null || { echo "需要在 WSL 内运行" >&2; exit 1; }
if wsl.exe --list --quiet | tr -d '\0\r' | grep -qx "$DISTRO"; then
  echo "发行版 $DISTRO 已存在，为避免误删请先确认后手动 wsl.exe --unregister $DISTRO" >&2; exit 1
fi

win() { wslpath -w "$1"; }
cleanup() {
  wsl.exe --unregister "$DISTRO" >/dev/null 2>&1 || true
  rm -rf "$WIN_DIR"
}
trap cleanup EXIT

rm -rf "$WIN_DIR" && mkdir -p "$WIN_DIR/src" "$WIN_DIR/vhd"
echo "== 构建 dsync（linux）与 host 测试程序（windows）"
(cd "$CLI_DIR" && CGO_ENABLED=0 GOOS=linux go build -o "$WIN_DIR/dsync_linux_amd64" ./cmd/dsync)
(cd "$CLI_DIR" && GOOS=windows go test -c -o "$WIN_DIR/host.test.exe" ./internal/host/)

echo "== 准备 dotfiles 仓库（e2e/fixtures/dotfiles-mini）"
cp -r "$CLI_DIR/e2e/fixtures/dotfiles-mini/." "$WIN_DIR/src/"
git -C "$WIN_DIR/src" init -q -b main
git -C "$WIN_DIR/src" -c user.email=e2e@local -c user.name=e2e add -A
git -C "$WIN_DIR/src" -c user.email=e2e@local -c user.name=e2e commit -qm init

echo "== 导入临时发行版 $DISTRO"
cid="$(docker create ubuntu:24.04)"
docker export "$cid" > "$WIN_DIR/rootfs.tar"
docker rm "$cid" >/dev/null
wsl.exe --import "$DISTRO" "$(win "$WIN_DIR/vhd")" "$(win "$WIN_DIR/rootfs.tar")"

echo "== 模拟全新 WSL：预装 git / sudo（不写任何代理配置，由 --proxy 在运行时提供）"
wsl.exe -d "$DISTRO" --exec bash -c "export DEBIAN_FRONTEND=noninteractive; apt-get update -qq >/dev/null && apt-get install -y -qq git sudo ca-certificates iproute2 >/dev/null"
proxy=""
if [ -n "${https_proxy:-}" ]; then
  port="${https_proxy##*:}"; port="${port%%/*}"
  proxy="http://\${WSL_HOST_IP}:$port"   # 宿主代理端口；IP 在 WSL 内由默认网关推出
fi

echo "== 运行 TestRealWSL_WSLInit"
cd "$WIN_DIR"
DSYNC_E2E_WSL_DISTRO="$DISTRO" DSYNC_E2E_SOURCE="$(win "$WIN_DIR/src")" DSYNC_E2E_LINUX_BINARY="$(win "$WIN_DIR/dsync_linux_amd64")" \
  DSYNC_E2E_PROXY="$proxy" WSLENV="DSYNC_E2E_WSL_DISTRO:DSYNC_E2E_SOURCE:DSYNC_E2E_LINUX_BINARY:DSYNC_E2E_PROXY" \
  ./host.test.exe -test.v -test.run TestRealWSL_WSLInit -test.timeout 30m </dev/null
echo "== 真实 WSL 端到端通过"
