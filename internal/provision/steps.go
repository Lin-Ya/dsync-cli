package provision

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/Lin-Ya/dsync-cli/internal/exec"
	"github.com/Lin-Ya/dsync-cli/internal/mirrors"
	"github.com/Lin-Ya/dsync-cli/internal/platform"
)

func unix(p platform.Platform) bool { return p.OS == "linux" || p.OS == "darwin" }

// ---------- apt（移植 run_once_10，仅 Ubuntu/Debian） ----------

type aptStep struct{}

var aptSources = []string{"etc/apt/sources.list.d/ubuntu.sources", "etc/apt/sources.list"}

func (aptStep) ID() string     { return "apt" }
func (aptStep) Optional() bool { return false }
func (aptStep) Applicable(p platform.Platform) bool {
	return p.OS == "linux" && (p.DistroID == "ubuntu" || p.DistroID == "debian")
}

// mirrorTargets 返回需要换成 TUNA 镜像的源文件。只认 Ubuntu 官方源地址，
// 换过即不再匹配，保证收敛（geo=official 时不回退，与 v1 一致）。
func (aptStep) mirrorTargets(x *Context) []string {
	if x.Geo != mirrors.CN {
		return nil
	}
	host, _ := mirrors.URL("apt", mirrors.CN)
	var files []string
	for _, rel := range aptSources {
		b, err := os.ReadFile(x.path(rel))
		if err != nil {
			continue
		}
		s := string(b)
		if strings.Contains(s, host) {
			return nil
		}
		if strings.Contains(s, "//archive.ubuntu.com/ubuntu") || strings.Contains(s, "//security.ubuntu.com/ubuntu") {
			files = append(files, x.path(rel))
		}
	}
	return files
}

func (aptStep) missing(ctx context.Context, x *Context) []string {
	var out []string
	for _, pkg := range x.Manifest.Packages.Apt {
		res, err := x.R.Query(ctx, exec.Cmd{Name: "dpkg-query", Args: []string{"-W", "-f=${Status}", pkg}})
		if err != nil || !strings.Contains(string(res.Stdout), "install ok installed") {
			out = append(out, pkg)
		}
	}
	return out
}

func (s aptStep) Check(ctx context.Context, x *Context) (Result, error) {
	var todo []string
	if len(s.mirrorTargets(x)) > 0 {
		todo = append(todo, "切换 apt 源到 TUNA 镜像")
	}
	if m := s.missing(ctx, x); len(m) > 0 {
		todo = append(todo, "安装 "+strings.Join(m, " "))
	}
	if len(todo) == 0 {
		return Result{State: Done}, nil
	}
	return Result{State: Pending, Detail: strings.Join(todo, "；")}, nil
}

func (s aptStep) Apply(ctx context.Context, x *Context) error {
	host, _ := mirrors.URL("apt", mirrors.CN)
	for _, f := range s.mirrorTargets(x) {
		if err := x.run(ctx, x.sudoCmd("cp", f, f+".bak.dsync")); err != nil {
			return err
		}
		if err := x.run(ctx, x.sudoCmd("sed", "-i",
			"-e", `s@//archive\.ubuntu\.com/ubuntu@//`+host+`/ubuntu@g`,
			"-e", `s@//security\.ubuntu\.com/ubuntu@//`+host+`/ubuntu@g`, f)); err != nil {
			return err
		}
	}
	missing := s.missing(ctx, x)
	if len(missing) == 0 {
		return nil
	}
	if err := x.run(ctx, x.sudoCmd("apt-get", "update", "-y")); err != nil {
		return err
	}
	return x.run(ctx, x.sudoCmd("env", append([]string{"DEBIAN_FRONTEND=noninteractive", "apt-get", "install", "-y"}, missing...)...))
}

// ---------- wsl-ssh-agent（移植 run_once_15：下载 npiperelay） ----------

type wslSSHAgentStep struct{}

const npiperelayURL = "https://github.com/jstarks/npiperelay/releases/download/v0.1.0/npiperelay_windows_%s.zip"

func (wslSSHAgentStep) ID() string                          { return "wsl-ssh-agent" }
func (wslSSHAgentStep) Optional() bool                      { return true } // GitHub 被墙时不阻断核心步骤
func (wslSSHAgentStep) Applicable(p platform.Platform) bool { return p.WSL }

func (wslSSHAgentStep) target(x *Context) string {
	return filepath.Join(x.Home, ".local", "bin", "npiperelay.exe")
}

func (s wslSSHAgentStep) Check(_ context.Context, x *Context) (Result, error) {
	if st, err := os.Stat(s.target(x)); err == nil && st.Mode()&0o111 != 0 {
		return Result{State: Done}, nil
	}
	return Result{State: Pending, Detail: "下载 npiperelay v0.1.0 到 ~/.local/bin（桥接 Windows ssh-agent）"}, nil
}

func (s wslSSHAgentStep) Apply(ctx context.Context, x *Context) error {
	arch := "amd64" // 官方 release 只有 amd64 / 386
	if x.Platform.Arch == "386" {
		arch = "386"
	}
	bin := filepath.Dir(s.target(x))
	cache := filepath.Join(x.Home, ".cache", "dsync")
	zip := filepath.Join(cache, "npiperelay.zip")
	for _, c := range []exec.Cmd{
		{Name: "mkdir", Args: []string{"-p", bin, cache}},
		{Name: "curl", Args: []string{"-fsSL", "-o", zip, fmt.Sprintf(npiperelayURL, arch)}},
		{Name: "unzip", Args: []string{"-o", zip, "npiperelay.exe", "-d", bin}},
		{Name: "chmod", Args: []string{"+x", s.target(x)}},
		{Name: "rm", Args: []string{"-f", zip}},
	} {
		if err := x.run(ctx, c); err != nil {
			return fmt.Errorf("%w（GitHub 不可达时请配置代理后重跑 dsync provision --only wsl-ssh-agent）", err)
		}
	}
	return nil
}

// ---------- homebrew（移植 run_once_20，仅 macOS） ----------

type homebrewStep struct{}

const brewInstallURL = "https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh"

func (homebrewStep) ID() string                          { return "homebrew" }
func (homebrewStep) Optional() bool                      { return false }
func (homebrewStep) Applicable(p platform.Platform) bool { return p.OS == "darwin" }

// brewPath 查找 brew：非交互环境下 PATH 里可能还没有它。
func brewPath(x *Context) string {
	if p, err := x.R.LookPath("brew"); err == nil {
		return p
	}
	for _, p := range []string{"/opt/homebrew/bin/brew", "/usr/local/bin/brew"} {
		if exists(x.path(p)) {
			return p
		}
	}
	return ""
}

func (homebrewStep) missing(ctx context.Context, x *Context, brew string) []string {
	if brew == "" {
		return append([]string(nil), x.Manifest.Packages.Brew...)
	}
	var out []string
	for _, pkg := range x.Manifest.Packages.Brew {
		if _, err := x.R.Query(ctx, exec.Cmd{Name: brew, Args: []string{"list", "--formula", pkg}}); err != nil {
			out = append(out, pkg)
		}
	}
	return out
}

func (s homebrewStep) Check(ctx context.Context, x *Context) (Result, error) {
	if _, err := x.R.Query(ctx, exec.Cmd{Name: "xcode-select", Args: []string{"-p"}}); err != nil {
		return Result{State: Manual, Detail: "需要 Xcode Command Line Tools：运行 xcode-select --install，完成后重跑 dsync provision"}, nil
	}
	brew := brewPath(x)
	var todo []string
	if brew == "" {
		todo = append(todo, "安装 Homebrew")
	}
	if m := s.missing(ctx, x, brew); len(m) > 0 {
		todo = append(todo, "安装 "+strings.Join(m, " "))
	}
	if len(todo) == 0 {
		return Result{State: Done}, nil
	}
	return Result{State: Pending, Detail: strings.Join(todo, "；")}, nil
}

func (s homebrewStep) Apply(ctx context.Context, x *Context) error {
	env := append([]string{"NONINTERACTIVE=1", "HOMEBREW_INSTALL_FROM_API=1"}, x.mirrorEnv()...)
	brew := brewPath(x)
	if brew == "" {
		if err := x.run(ctx, exec.Cmd{Name: "/bin/bash", Args: []string{"-c",
			`script="$(curl -fsSL ` + brewInstallURL + `)" && /bin/bash -c "$script"`}, Env: env, Interactive: true}); err != nil {
			return err
		}
		if brew = brewPath(x); brew == "" {
			brew = "brew" // dry-run 下并未真正安装
		}
	}
	missing := s.missing(ctx, x, brew)
	if len(missing) == 0 {
		return nil
	}
	return x.run(ctx, exec.Cmd{Name: brew, Args: append([]string{"install"}, missing...), Env: env, Interactive: true})
}

// ---------- ohmyzsh（移植 run_once_30 的安装部分） ----------

type ohmyzshStep struct{}

const ohmyzshInstallURL = "https://raw.githubusercontent.com/ohmyzsh/ohmyzsh/master/tools/install.sh"

func (ohmyzshStep) ID() string                          { return "ohmyzsh" }
func (ohmyzshStep) Optional() bool                      { return true } // 依赖 GitHub，失败不阻断
func (ohmyzshStep) Applicable(p platform.Platform) bool { return unix(p) }

func (ohmyzshStep) dir(x *Context) string { return filepath.Join(x.Home, ".oh-my-zsh") }

func (s ohmyzshStep) missingPlugins(x *Context) []string {
	var out []string
	for _, repo := range x.Manifest.Shell.OhMyZshPlugins {
		if !exists(filepath.Join(s.dir(x), "custom", "plugins", path.Base(repo))) {
			out = append(out, repo)
		}
	}
	return out
}

func (s ohmyzshStep) Check(_ context.Context, x *Context) (Result, error) {
	var todo []string
	if !exists(s.dir(x)) {
		todo = append(todo, "安装 oh-my-zsh")
	}
	if m := s.missingPlugins(x); len(m) > 0 {
		todo = append(todo, "安装插件 "+strings.Join(m, " "))
	}
	if len(todo) == 0 {
		return Result{State: Done}, nil
	}
	return Result{State: Pending, Detail: strings.Join(todo, "；")}, nil
}

func (s ohmyzshStep) Apply(ctx context.Context, x *Context) error {
	if !exists(s.dir(x)) {
		// KEEP_ZSHRC=yes：~/.zshrc 由 dsync/chezmoi 管理，禁止安装脚本改写；CHSH 交给 chsh 步骤
		if err := x.run(ctx, exec.Cmd{Name: "sh", Args: []string{"-c",
			`script="$(curl -fsSL ` + ohmyzshInstallURL + `)" && sh -c "$script" "" --unattended`},
			Env: []string{"RUNZSH=no", "CHSH=no", "KEEP_ZSHRC=yes"}, Interactive: true}); err != nil {
			return err
		}
	}
	plugins := filepath.Join(s.dir(x), "custom", "plugins")
	for _, repo := range s.missingPlugins(x) {
		if err := x.run(ctx, exec.Cmd{Name: "git", Args: []string{"clone", "--depth", "1",
			"https://github.com/" + repo, filepath.Join(plugins, path.Base(repo))}}); err != nil {
			return err
		}
	}
	return nil
}

// ---------- chsh（移植 run_once_30 的默认 shell 部分） ----------

type chshStep struct{}

func (chshStep) ID() string                          { return "chsh" }
func (chshStep) Optional() bool                      { return true } // PAM 认证失败等不阻断
func (chshStep) Applicable(p platform.Platform) bool { return unix(p) }

// loginShell 读取账户数据库中的登录 shell。不用 $SHELL：它是会话启动时的值，
// chsh 之后同一会话仍是旧值，会让检测永远不收敛。
func (chshStep) loginShell(ctx context.Context, x *Context) (string, error) {
	if x.Platform.OS == "darwin" {
		res, err := x.R.Query(ctx, exec.Cmd{Name: "dscl", Args: []string{".", "-read", "/Users/" + x.User, "UserShell"}})
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(res.Stdout)), "UserShell:")), nil
	}
	res, err := x.R.Query(ctx, exec.Cmd{Name: "getent", Args: []string{"passwd", x.User}})
	if err != nil {
		return "", err
	}
	f := strings.Split(strings.TrimSpace(string(res.Stdout)), ":")
	if len(f) < 7 {
		return "", fmt.Errorf("无法解析 passwd 条目: %q", res.Stdout)
	}
	return f[6], nil
}

func (s chshStep) Check(ctx context.Context, x *Context) (Result, error) {
	want := x.Manifest.Shell.Default
	if want == "" {
		return Result{State: Done}, nil
	}
	wantPath, err := x.R.LookPath(want)
	if err != nil {
		return Result{State: Manual, Detail: "未安装 " + want + "，无法设为默认 shell"}, nil
	}
	cur, err := s.loginShell(ctx, x)
	if err != nil {
		return Result{}, err
	}
	if filepath.Base(cur) == want {
		return Result{State: Done}, nil
	}
	return Result{State: Pending, Detail: fmt.Sprintf("默认 shell %s → %s", cur, wantPath)}, nil
}

func (chshStep) Apply(ctx context.Context, x *Context) error {
	wantPath, err := x.R.LookPath(x.Manifest.Shell.Default)
	if err != nil {
		return err
	}
	if err := x.run(ctx, x.sudoCmd("chsh", "-s", wantPath, x.User)); err != nil {
		return fmt.Errorf("%w（可手动执行: sudo chsh -s %s %s）", err, wantPath, x.User)
	}
	return nil
}

// ---------- docker-check（移植 run_once_40 与 run_once_20 的 OrbStack 部分） ----------

type dockerStep struct{}

func (dockerStep) ID() string                          { return "docker-check" }
func (dockerStep) Optional() bool                      { return true }
func (dockerStep) Applicable(p platform.Platform) bool { return unix(p) }

func (dockerStep) Check(_ context.Context, x *Context) (Result, error) {
	if x.has("docker") {
		return Result{State: Done}, nil
	}
	switch {
	case x.Platform.OS == "darwin" && brewPath(x) != "":
		return Result{State: Pending, Detail: "安装 OrbStack（brew install --cask orbstack；首次需手动打开完成初始化）"}, nil
	case x.Platform.OS == "darwin":
		return Result{State: Manual, Detail: "未找到 docker，安装 Homebrew 后重跑以安装 OrbStack"}, nil
	case x.Platform.WSL:
		return Result{State: Manual, Detail: "未找到 docker：确认 Windows 侧 Docker Desktop 已启动，并在 Settings → Resources → WSL Integration 勾选本发行版（仍不行见 dsync-cli 仓库 docs/docker.md 的兜底方案）"}, nil
	}
	return Result{State: Manual, Detail: "未找到 docker（见 docs/docker.md）"}, nil
}

func (dockerStep) Apply(ctx context.Context, x *Context) error {
	return x.run(ctx, exec.Cmd{Name: brewPath(x), Args: []string{"install", "--cask", "orbstack"}, Interactive: true})
}
