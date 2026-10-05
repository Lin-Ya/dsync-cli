# ============================================================================
# ~/.zshenv — 由 chezmoi 管理，请勿手改
# 本文件在所有 zsh 会话启动时最先加载（含非交互会话）
# ============================================================================

# 1) 镜像源开关：cn(国内镜像,默认) | official(官方源)
#    运行时以 ~/.config/dsync/geo 文件为准；切换后重新打开终端生效
export DSYNC_GEO="${DSYNC_GEO:-cn}"
if [ -f "$HOME/.config/dsync/geo" ]; then
  export DSYNC_GEO="$(tr -d '[:space:]' < "$HOME/.config/dsync/geo")"
fi

# 2) 本地代理（可选）：由 create_ 模板生成，每台机器各自填写
[ -f "$HOME/.config/dsync/proxy.env" ] && source "$HOME/.config/dsync/proxy.env"

# 3) 加载镜像映射并导出对应环境变量
if [ -f "$HOME/.config/dsync/lib/mirrors.sh" ]; then
  source "$HOME/.config/dsync/lib/mirrors.sh"
  apply_mirror_env "$DSYNC_GEO"
fi

# 4) mise：常驻 shims 与本地 bin 到 PATH（后 export 者在前，故 shims 优先于 local/bin）
#    保证非交互 zsh（脚本/tmux/cron/git hooks/VS Code 任务）也能直接使用 mise 管理的工具
#    交互 shell 另由 ~/.zshrc 的 `mise activate zsh` 提供 hook/环境变量
[ -d "$HOME/.local/bin" ] && export PATH="$HOME/.local/bin:$PATH"
[ -d "$HOME/.local/share/mise/shims" ] && export PATH="$HOME/.local/share/mise/shims:$PATH"

# 5) WSL：复用 Windows SSH Agent（密钥全程留在 Windows，见 docs/ssh-agent.md）
#    仅 WSL 生效（/proc/version 含 microsoft）；macOS 用系统 Keychain 无需处理
#    所有 zsh 会话（含非交互/git hooks/VS Code 任务）都能拿到 SSH_AUTH_SOCK

if grep -qi microsoft /proc/version 2>/dev/null; then
  export SSH_AUTH_SOCK="$HOME/.ssh/agent.sock"
  if command -v socat >/dev/null 2>&1 && [ -x "$HOME/.local/bin/npiperelay.exe" ]; then
    mkdir -p "$HOME/.ssh"
    # 残留 socket（WSL 重启/进程被杀后遗留）会让 ssh-add 报 Connection refused 且不再自动拉起。
    # 规则：仅当 ss 确认确有活跃监听才保留；否则视为残留 → 删除重建（幂等、开销小）。
    # 无 ss 时回退为「一律重建」。重建后回读监听状态并写日志（~/.ssh/agent-start.log），便于排查。
    LOG="$HOME/.ssh/agent-start.log"
    if [ -S "$SSH_AUTH_SOCK" ] && command -v ss >/dev/null 2>&1 && ss -xl 2>/dev/null | grep -q "agent.sock"; then
      : # 活跃监听，保持不动
    else
      rm -f "$SSH_AUTH_SOCK"
      echo "[$(date '+%F %T')] 启动 socat（残留检测重建）..." >>"$LOG"
      # nohup：避免 shell 初始化阶段/终端退出时 SIGHUP 把 socat 杀掉；输出进日志便于排障
      nohup socat UNIX-LISTEN:"$SSH_AUTH_SOCK",fork \
        EXEC:"$HOME/.local/bin/npiperelay.exe -ei -s //./pipe/openssh-ssh-agent",nofork \
        >>"$LOG" 2>&1 &
      sleep 0.3
      if command -v ss >/dev/null 2>&1 && ! ss -xl 2>/dev/null | grep -q "agent.sock"; then
        echo "  !!! WARN: 启动后 socket 仍无监听（见上方 socat 输出）" >>"$LOG"
      fi
    fi
  fi
fi

