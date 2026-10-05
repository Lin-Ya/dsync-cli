// Package shellinit 生成 `eval "$(dsync shell-init zsh)"` 的输出，
// 取代 v1 ~/.zshenv 中的 GEO / 代理 / 镜像 env / WSL ssh-agent 桥接逻辑（§1–3、§5）。
// PATH 属于用户配置，仍由用户的 .zshenv 自行维护。
package shellinit

import (
	"fmt"
	"strings"

	"github.com/Lin-Ya/dsync-cli/internal/config"
	"github.com/Lin-Ya/dsync-cli/internal/mirrors"
	"github.com/Lin-Ya/dsync-cli/internal/platform"
)

// Shells 是支持的 shell。
var Shells = []string{"zsh", "bash"}

// Options 是生成所需的全部输入（纯数据，便于 golden 测试）。
type Options struct {
	Shell    string
	Geo      mirrors.Geo
	Platform platform.Platform
	// Proxy 来自 local.toml；为 nil 且 V1ProxyFile 非空时回退 source v1 的 proxy.env。
	Proxy       *config.Proxy
	V1ProxyFile string
}

// WSLHostToken 是代理配置中可用的占位符，WSL 下展开为 Windows 宿主 IP。
const WSLHostToken = "${WSL_HOST_IP}"

// Generate 生成 shell 脚本。
func Generate(o Options) (string, error) {
	if !supported(o.Shell) {
		return "", fmt.Errorf("不支持的 shell %q（可选: %s）", o.Shell, strings.Join(Shells, " | "))
	}
	env, err := mirrors.EnvFor(o.Geo)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# dsync shell-init (%s, geo=%s, %s)\n", o.Shell, o.Geo, o.Platform.Label())

	// 1) GEO
	fmt.Fprintf(&b, "export DSYNC_GEO=%s\n", sq(string(o.Geo)))
	fmt.Fprintf(&b, "export %s=%s\n", config.ShellInitMarker, sq("shell-init"))

	// 2) 代理（先于镜像，与 v1 顺序一致）
	writeProxy(&b, o)

	// 3) 镜像环境变量
	var unset []string
	for _, s := range env {
		if s.Value == "" {
			unset = append(unset, s.Name)
			continue
		}
		fmt.Fprintf(&b, "export %s=%s\n", s.Name, sq(s.Value))
	}
	if len(unset) > 0 {
		fmt.Fprintf(&b, "unset %s\n", strings.Join(unset, " "))
	}

	// 4) WSL：复用 Windows SSH Agent
	if o.Platform.WSL {
		b.WriteString(wslSSHAgent)
	}
	return b.String(), nil
}

func supported(shell string) bool {
	for _, s := range Shells {
		if s == shell {
			return true
		}
	}
	return false
}

func writeProxy(b *strings.Builder, o Options) {
	if o.Proxy == nil {
		if o.V1ProxyFile != "" {
			fmt.Fprintf(b, "[ -f %[1]s ] && . %[1]s\n", sq(o.V1ProxyFile))
		}
		return
	}
	p := o.Proxy
	vals := []struct {
		names []string
		v     string
	}{
		{[]string{"http_proxy", "HTTP_PROXY"}, p.HTTP},
		{[]string{"https_proxy", "HTTPS_PROXY"}, p.HTTPS},
		{[]string{"all_proxy", "ALL_PROXY"}, p.All},
		{[]string{"no_proxy", "NO_PROXY"}, p.NoProxy},
	}
	needHost := false
	for _, x := range vals {
		if strings.Contains(x.v, WSLHostToken) {
			needHost = true
		}
	}
	if needHost {
		if o.Platform.WSL {
			b.WriteString("export WSL_HOST_IP=\"$(ip route show default 2>/dev/null | awk '{print $3; exit}')\"\n")
		} else {
			// 非 WSL 下没有 Windows 宿主：回退本机回环，保证同一份 local.toml 可跨平台复用
			b.WriteString("export WSL_HOST_IP='127.0.0.1'\n")
		}
	}
	for _, x := range vals {
		if x.v == "" {
			continue
		}
		val := dqWithHost(x.v)
		for _, n := range x.names {
			fmt.Fprintf(b, "export %s=%s\n", n, val)
		}
	}
}

// sq 生成 POSIX 单引号字面量。
func sq(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// dqWithHost 生成双引号字符串：除 ${WSL_HOST_IP} 外的一切都按字面量转义。
func dqWithHost(s string) string {
	parts := strings.Split(s, WSLHostToken)
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "$", `\$`, "`", "\\`")
	for i, p := range parts {
		parts[i] = r.Replace(p)
	}
	return `"` + strings.Join(parts, WSLHostToken) + `"`
}

// wslSSHAgent 移植自 v1 dot_zshenv.tmpl §5（行为保持一致）：
// 用 socat + npiperelay 把 Windows OpenSSH Agent 的命名管道桥接为 unix socket。
const wslSSHAgent = `export SSH_AUTH_SOCK="$HOME/.ssh/agent.sock"
if command -v socat >/dev/null 2>&1 && [ -x "$HOME/.local/bin/npiperelay.exe" ]; then
  if [ -S "$SSH_AUTH_SOCK" ] && command -v ss >/dev/null 2>&1 && ss -xl 2>/dev/null | grep -q "agent.sock"; then
    :
  else
    mkdir -p "$HOME/.ssh"
    rm -f "$SSH_AUTH_SOCK"
    echo "[$(date '+%F %T')] dsync: 启动 socat（残留检测重建）..." >>"$HOME/.ssh/agent-start.log"
    nohup socat UNIX-LISTEN:"$SSH_AUTH_SOCK",fork \
      EXEC:"$HOME/.local/bin/npiperelay.exe -ei -s //./pipe/openssh-ssh-agent",nofork \
      >>"$HOME/.ssh/agent-start.log" 2>&1 &
  fi
fi
`
