#!/usr/bin/env bash
# ============================================================================
# init.sh — dsync init 端到端测试（ubuntu:24.04 容器，真实下载 / 克隆 / 安装）
#
# 模拟全新 WSL：预装 git 与 sudo，普通用户 + 免密 sudo。两种模式各用一个容器：
#   clone  —— dsync init file:///tmp/dotfiles（fixture：e2e/fixtures/dotfiles-mini，v1 布局）
#   create —— dsync init --create（内置 starter 模板，v2 布局；并验证 .zshenv 经 shell-init 生效）
# 每种模式：init 后再执行一次无参数的 dsync init（幂等），最后 dsync status --json
# 必须无漂移、无缺失工具、无待办 provision。
#
# 用法（cli/ 目录）：bash e2e/init.sh [clone|create]...   镜像站不可用时：DSYNC_GEO=official bash e2e/init.sh
# 需要：docker、go。透传宿主 http(s)_proxy（下载 GitHub 资产）。
# ============================================================================
set -euo pipefail

CLI_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
IMAGE="${DSYNC_E2E_IMAGE:-ubuntu:24.04}"
GEO="${DSYNC_GEO:-cn}"
MODES=("$@")
[ ${#MODES[@]} -gt 0 ] || MODES=(clone create)

if [ -n "${http_proxy:-}${https_proxy:-}" ]; then
  export no_proxy="${no_proxy:+$no_proxy,}archive.ubuntu.com,security.ubuntu.com,mirrors.tuna.tsinghua.edu.cn"
fi

BIN="$(mktemp -d)"
trap 'rm -rf "$BIN"' EXIT
(cd "$CLI_DIR" && CGO_ENABLED=0 GOOS=linux go build -o "$BIN/dsync" ./cmd/dsync)
cp -r "$CLI_DIR/e2e/fixtures/dotfiles-mini" "$BIN/fixture"

cat > "$BIN/assert.sh" <<'INNER'
set -euo pipefail
fail() { echo "FAIL: $*"; exit 1; }
if [ "$MODE" = clone ]; then
  cp -r /e2e/fixture /tmp/dotfiles
  git -C /tmp/dotfiles init -q -b main
  git -C /tmp/dotfiles -c user.email=e2e@local -c user.name=e2e add -A
  git -C /tmp/dotfiles -c user.email=e2e@local -c user.name=e2e commit -qm init
  echo "== [clone] 第 1 次 dsync init（geo=$GEO）"
  dsync init file:///tmp/dotfiles --geo "$GEO"
else
  echo "== [create] 第 1 次 dsync init --create（geo=$GEO）"
  dsync init --create --geo "$GEO"
fi
echo "== [$MODE] 第 2 次 dsync init（无参数）"
dsync init

"$HOME/.local/bin/mise" --version >/dev/null || fail "mise 未安装"
"$HOME/.local/bin/chezmoi" --version >/dev/null || fail "chezmoi 未安装"
grep -q "geo = '$GEO'" "$HOME/.config/dsync/local.toml" || fail "local.toml 未记录 geo"
if [ "$MODE" = clone ]; then
  test -L "$HOME/.local/share/chezmoi" || fail "v1 布局应建立 ~/.local/share/chezmoi 软链"
  test "$(cat "$HOME/.config/dsync/geo")" = "$GEO" || fail "geo 文件不符"
else
  grep -q 'sourceDir' "$HOME/.config/chezmoi/chezmoi.toml" || fail "v2 布局应由 dsync 生成 chezmoi 配置"
  # starter 的 .zshenv：eval "$(dsync shell-init zsh)" 后镜像变量随 geo 生效
  got="$(env -i HOME="$HOME" PATH=/usr/local/bin:/usr/bin:/bin zsh -c 'echo "$DSYNC_GEO|$NPM_CONFIG_REGISTRY"')"
  want="$GEO|"; [ "$GEO" = cn ] && want="cn|https://registry.npmmirror.com"
  [ "$got" = "$want" ] || fail ".zshenv 中 shell-init 未生效：得到 $got，期望 $want"
fi
if [ "$GEO" = cn ]; then grep -q npmmirror "$HOME/.npmrc" || fail ".npmrc 应为 npmmirror"
else grep -q registry.npmjs.org "$HOME/.npmrc" || fail ".npmrc 应为官方源"; fi
PATH="$HOME/.local/bin:$PATH" mise which fzf >/dev/null || fail "fzf 未由 mise 安装"

PATH="$HOME/.local/bin:$PATH" dsync status --json > /tmp/status.json
cat /tmp/status.json
grep -q '"config_stale": false' /tmp/status.json || fail "status.config_stale 为 true（未收敛）"
for k in drift missing_tools provision_pending; do
  grep -q "\"$k\": \[\]" /tmp/status.json || fail "status.$k 非空（未收敛）"
done
echo "PASS: dsync init（$MODE）"
INNER
chmod 755 "$BIN" "$BIN/fixture" && chmod -R a+rX "$BIN"

for MODE in "${MODES[@]}"; do
  echo "################ 模式: $MODE ################"
  docker run --rm \
    -e http_proxy -e https_proxy -e no_proxy -e DEBIAN_FRONTEND=noninteractive \
    -v "$BIN/dsync:/usr/local/bin/dsync:ro" -v "$BIN:/e2e:ro" \
    "$IMAGE" bash -c "apt-get update -qq >/dev/null && apt-get install -y -qq git ca-certificates sudo >/dev/null &&
      useradd -m -s /bin/bash dev && echo 'dev ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/dev &&
      su - dev -c 'MODE=$MODE GEO=$GEO http_proxy=${http_proxy:-} https_proxy=${https_proxy:-} no_proxy=${no_proxy:-} bash /e2e/assert.sh'"
done
echo "== E2E 通过: dsync init ${MODES[*]}（geo=$GEO）"
