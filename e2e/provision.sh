#!/usr/bin/env bash
# ============================================================================
# provision.sh — dsync provision 端到端测试（ubuntu:24.04 容器，真实执行）
#
# 两个场景：
#   root  —— 容器默认用户，不需要 sudo
#   sudo  —— 普通用户 + 免密 sudo（模拟 WSL 下的个人账户）
# 每个场景：provision 执行两次，第二次后 `provision --check --json` 不得有待办项
# （验证幂等与收敛）。
#
# 用法（宿主机，cli/ 目录）：bash e2e/provision.sh [root|sudo]...   默认两个都跑
#   DSYNC_GEO=official bash e2e/provision.sh   # 不换 apt 源（镜像站不可用时）
# 需要：docker、go。透传宿主的 http(s)_proxy（oh-my-zsh 需访问 GitHub）与 DSYNC_GEO。
# ============================================================================
set -euo pipefail

CLI_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
IMAGE="${DSYNC_E2E_IMAGE:-ubuntu:24.04}"
SCENARIOS=("$@")
[ ${#SCENARIOS[@]} -gt 0 ] || SCENARIOS=(root sudo)

# apt 源直连（境内镜像 / 官方源走代理常被拒或不稳），只有 GitHub 等走代理
if [ -n "${http_proxy:-}${https_proxy:-}" ]; then
  export no_proxy="${no_proxy:+$no_proxy,}archive.ubuntu.com,security.ubuntu.com,mirrors.tuna.tsinghua.edu.cn"
fi

BIN="$(mktemp -d)"
trap 'rm -rf "$BIN"' EXIT
(cd "$CLI_DIR" && CGO_ENABLED=0 GOOS=linux go build -o "$BIN/dsync" ./cmd/dsync)

# 容器内执行的断言脚本（以目标用户身份运行）
cat > "$BIN/assert.sh" <<'INNER'
set -euo pipefail
export PATH="$HOME/.local/bin:$PATH"
echo "== 第 1 次 provision（$(id -un)）"
dsync provision
echo "== 第 2 次 provision（应无待办）"
dsync provision
dsync provision --check --json > /tmp/plan.json
cat /tmp/plan.json
if grep -q '"state": "pending"' /tmp/plan.json; then
  echo "FAIL: 第 2 次执行后仍有待办项（不收敛）"; exit 1
fi
test "$(getent passwd "$(id -un)" | cut -d: -f7)" = "$(command -v zsh)" || { echo "FAIL: 默认 shell 未切换"; exit 1; }
test -d "$HOME/.oh-my-zsh/custom/plugins/zsh-autosuggestions" || { echo "FAIL: 插件未安装"; exit 1; }
if [ "${DSYNC_GEO:-cn}" = "cn" ]; then
  grep -q mirrors.tuna.tsinghua.edu.cn /etc/apt/sources.list.d/ubuntu.sources || { echo "FAIL: apt 未换源"; exit 1; }
else
  ! grep -q mirrors.tuna.tsinghua.edu.cn /etc/apt/sources.list.d/ubuntu.sources || { echo "FAIL: official 不应换源"; exit 1; }
fi
echo "PASS: $(id -un)"
INNER
chmod 755 "$BIN" && chmod 644 "$BIN/assert.sh"   # mktemp -d 为 0700，容器内普通用户需可读

run() {
  local scenario="$1" setup
  # shellcheck disable=SC2016  # 有意单引号：sudo 分支中的变量在容器内展开
  case "$scenario" in
    root) setup='bash /e2e/assert.sh' ;;
    sudo) setup='apt-get update -qq >/dev/null && apt-get install -y -qq sudo >/dev/null &&
                 useradd -m -s /bin/bash dev && echo "dev ALL=(ALL) NOPASSWD:ALL" > /etc/sudoers.d/dev &&
                 su - dev -c "DSYNC_GEO=${DSYNC_GEO:-} http_proxy=${http_proxy:-} https_proxy=${https_proxy:-} no_proxy=${no_proxy:-} bash /e2e/assert.sh"' ;;
    *) echo "未知场景: $scenario" >&2; return 2 ;;
  esac
  echo "################ 场景: $scenario ################"
  docker run --rm \
    -e http_proxy -e https_proxy -e no_proxy -e DSYNC_GEO -e DEBIAN_FRONTEND=noninteractive \
    -v "$BIN/dsync:/usr/local/bin/dsync:ro" -v "$BIN:/e2e:ro" \
    "$IMAGE" bash -c "$setup"
}

for s in "${SCENARIOS[@]}"; do run "$s"; done
echo "== E2E 全部通过: ${SCENARIOS[*]}"
