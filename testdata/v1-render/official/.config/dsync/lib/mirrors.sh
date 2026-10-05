#!/usr/bin/env bash
# ============================================================================
# mirrors.sh — 统一镜像源映射（纯函数，无副作用，可单测）
# 单一事实源：所有镜像 URL 只在这里定义一次，禁止在其他文件散落硬编码
#
# 用法（先 source 本文件）:
#   source mirrors.sh
#   mirror_url <component> <geo>     # 输出 URL 到 stdout；official 输出空串=用官方默认
#   apply_mirror_env <geo>           # 按 geo 导出(cn)/清除(official)镜像环境变量
#   dsync_geo                        # 输出当前 geo（DSYNC_GEO 环境变量 > geo 文件；默认 cn）
#
# 组件清单（供完整性测试用）
# ============================================================================

MIRROR_COMPONENTS="apt brew_api brew_bottle brew_brew brew_core npm node pip pyenv rustup_dist rustup_root cargo docker"

# --- cn：国内镜像 URL（默认） ---
_mirror_cn() {
  case "$1" in
    apt)         echo "mirrors.tuna.tsinghua.edu.cn" ;;
    brew_api)    echo "https://mirrors.tuna.tsinghua.edu.cn/homebrew-bottles/api" ;;
    brew_bottle) echo "https://mirrors.tuna.tsinghua.edu.cn/homebrew-bottles" ;;
    brew_brew)   echo "https://mirrors.tuna.tsinghua.edu.cn/git/homebrew/brew.git" ;;
    brew_core)   echo "https://mirrors.tuna.tsinghua.edu.cn/git/homebrew/homebrew-core.git" ;;
    npm)         echo "https://registry.npmmirror.com" ;;
    node)        echo "https://npmmirror.com/mirrors/node/" ;;
    pip)         echo "https://pypi.tuna.tsinghua.edu.cn/simple" ;;
    pyenv)       echo "https://gitee.com/mirrors/pyenv.git" ;;
    rustup_dist) echo "https://rsproxy.cn" ;;
    rustup_root) echo "https://rsproxy.cn/rustup" ;;
    cargo)       echo "https://rsproxy.cn" ;;
    docker)      echo "https://docker.m.daocloud.io" ;;
    *) return 1 ;;
  esac
}

# --- official：输出空串 = 使用工具默认（官方）源 ---
_mirror_official() {
  case "$1" in
    apt|brew_api|brew_bottle|brew_brew|brew_core|npm|node|pip|pyenv|rustup_dist|rustup_root|cargo|docker)
      return 0 ;;
    *) return 1 ;;
  esac
}

# --- 对外主函数：mirror_url <component> <geo> ---
mirror_url() {
  local component="$1" geo="$2"
  case "$geo" in
    cn)       _mirror_cn "$component" ;;
    official) _mirror_official "$component" ;;
    *) echo "mirror_url: unknown geo '$geo'" >&2; return 1 ;;
  esac
}

# --- 当前 geo（优先级：DSYNC_GEO 环境变量 > geo 文件；默认 cn） ---
dsync_geo() {
  case "${DSYNC_GEO:-}" in
    cn|official) echo "$DSYNC_GEO" ;;
    *)
      if [ -f "${DSYNC_GEO_FILE:-$HOME/.config/dsync/geo}" ]; then
        tr -d '[:space:]' < "${DSYNC_GEO_FILE:-$HOME/.config/dsync/geo}"
      else
        echo "cn"
      fi
      ;;
  esac
}

# --- 按 geo 导出/清除镜像环境变量（source 本文件后调用） ---
apply_mirror_env() {
  local geo="${1:-$(dsync_geo)}"
  case "$geo" in
    cn)
      export MISE_NODE_MIRROR_URL="$(mirror_url node cn)"
      export PIP_INDEX_URL="$(mirror_url pip cn)"
      export UV_DEFAULT_INDEX="$(mirror_url pip cn)"
      export UV_INDEX_URL="$(mirror_url pip cn)"
      export NPM_CONFIG_REGISTRY="$(mirror_url npm cn)"
      export RUSTUP_DIST_SERVER="$(mirror_url rustup_dist cn)"
      export RUSTUP_UPDATE_ROOT="$(mirror_url rustup_root cn)"
      export MISE_PYENV_REPO="$(mirror_url pyenv cn)"
      export HOMEBREW_API_DOMAIN="$(mirror_url brew_api cn)"
      export HOMEBREW_BOTTLE_DOMAIN="$(mirror_url brew_bottle cn)"
      export HOMEBREW_BREW_GIT_REMOTE="$(mirror_url brew_brew cn)"
      export HOMEBREW_CORE_GIT_REMOTE="$(mirror_url brew_core cn)"
      ;;
    official)
      unset MISE_NODE_MIRROR_URL PIP_INDEX_URL UV_DEFAULT_INDEX UV_INDEX_URL \
        NPM_CONFIG_REGISTRY RUSTUP_DIST_SERVER RUSTUP_UPDATE_ROOT MISE_PYENV_REPO \
        HOMEBREW_API_DOMAIN HOMEBREW_BOTTLE_DOMAIN HOMEBREW_BREW_GIT_REMOTE HOMEBREW_CORE_GIT_REMOTE
      ;;
    *) echo "apply_mirror_env: unknown geo '$geo'" >&2; return 1 ;;
  esac
}
