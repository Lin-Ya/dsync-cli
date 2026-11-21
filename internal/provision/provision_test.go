package provision

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Lin-Ya/dsync-cli/internal/config"
	"github.com/Lin-Ya/dsync-cli/internal/exec"
	"github.com/Lin-Ya/dsync-cli/internal/mirrors"
	"github.com/Lin-Ya/dsync-cli/internal/platform"
)

var (
	ubuntu = platform.Platform{OS: "linux", Arch: "amd64", DistroID: "ubuntu", Version: "24.04"}
	wsl    = platform.Platform{OS: "linux", Arch: "amd64", WSL: true, DistroID: "ubuntu", Version: "24.04"}
	mac    = platform.Platform{OS: "darwin", Arch: "arm64"}
)

const ubuntuSources = `Types: deb
URIs: http://archive.ubuntu.com/ubuntu/
Suites: noble noble-updates
Types: deb
URIs: http://security.ubuntu.com/ubuntu/
Suites: noble-security
`

const tunaSources = `Types: deb
URIs: http://mirrors.tuna.tsinghua.edu.cn/ubuntu/
Suites: noble noble-updates
Types: deb
URIs: http://mirrors.tuna.tsinghua.edu.cn/ubuntu/
Suites: noble-security
`

func newCtx(t *testing.T, p platform.Platform) (*Context, *exec.Recording) {
	t.Helper()
	rec := &exec.Recording{Paths: map[string]string{}}
	rec.OnStdout("", "") // 兜底（优先级最低）：未单独预设的命令一律成功、无输出
	return &Context{
		R: rec, Platform: p, Geo: mirrors.CN, Manifest: config.DefaultManifest(),
		Home: t.TempDir(), Root: t.TempDir(), UID: 1000, User: "kevin",
	}, rec
}

func writeFile(t *testing.T, p, content string, perm os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), perm); err != nil {
		t.Fatal(err)
	}
}

func installed(rec *exec.Recording, pkgs ...string) {
	for _, p := range pkgs {
		rec.OnStdout("dpkg-query -W '-f=${Status}' "+p, "install ok installed")
	}
}

func check(t *testing.T, s Step, x *Context) Result {
	t.Helper()
	r, err := s.Check(context.Background(), x)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func mutating(rec *exec.Recording) []string {
	var out []string
	for _, c := range rec.Calls {
		if c.Mutating {
			out = append(out, exec.Cmd{Name: c.Cmd.Name, Args: c.Cmd.Args}.String())
		}
	}
	return out
}

func TestRegistryMatchesManifestSchema(t *testing.T) {
	var ids []string
	for _, s := range Steps {
		ids = append(ids, s.ID())
	}
	if !reflect.DeepEqual(ids, config.KnownProvisionSteps) {
		t.Errorf("注册表 %v 与 config.KnownProvisionSteps %v 不一致", ids, config.KnownProvisionSteps)
	}
}

// ---------- apt ----------

func TestApt_MirrorAndMissingWithSudo(t *testing.T) {
	x, rec := newCtx(t, ubuntu)
	rec.Paths["sudo"] = "/usr/bin/sudo"
	src := filepath.Join(x.Root, "etc/apt/sources.list.d/ubuntu.sources")
	writeFile(t, src, ubuntuSources, 0o644)
	x.Manifest.Packages.Apt = []string{"curl", "zsh", "socat"}
	installed(rec, "curl")

	r := check(t, aptStep{}, x)
	if r.State != Pending || r.Detail != "切换 apt 源到 TUNA 镜像；安装 zsh socat" {
		t.Fatalf("%+v", r)
	}
	rec.Calls = nil
	if err := (aptStep{}).Apply(context.Background(), x); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"sudo cp " + src + " " + src + ".bak.dsync",
		`sudo sed -i -e 's@//archive\.ubuntu\.com/ubuntu@//mirrors.tuna.tsinghua.edu.cn/ubuntu@g' -e 's@//security\.ubuntu\.com/ubuntu@//mirrors.tuna.tsinghua.edu.cn/ubuntu@g' ` + src,
		"sudo apt-get update -y",
		"sudo env DEBIAN_FRONTEND=noninteractive apt-get install -y zsh socat",
	}
	if got := mutating(rec); !reflect.DeepEqual(got, want) {
		t.Errorf("命令序列:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestApt_RootNoSudoAndConvergence(t *testing.T) {
	x, rec := newCtx(t, ubuntu)
	x.UID = 0
	rec.Paths["sudo"] = "/usr/bin/sudo"
	x.Manifest.Packages.Apt = []string{"curl"}
	cases := map[string]struct {
		sources string
		geo     mirrors.Geo
	}{
		"已换源":         {"URIs: http://mirrors.tuna.tsinghua.edu.cn/ubuntu/\n", mirrors.CN},
		"official 不换": {ubuntuSources, mirrors.Official},
		"Debian 源不匹配": {"deb http://deb.debian.org/debian bookworm main\n", mirrors.CN},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			writeFile(t, filepath.Join(x.Root, "etc/apt/sources.list.d/ubuntu.sources"), c.sources, 0o644)
			x.Geo = c.geo
			if r := check(t, aptStep{}, x); r.Detail != "安装 curl" {
				t.Errorf("不应换源（否则永不收敛）: %+v", r)
			}
		})
	}
	rec.Calls = nil
	if err := (aptStep{}).Apply(context.Background(), x); err != nil {
		t.Fatal(err)
	}
	if got := mutating(rec); got[0] != "apt-get update -y" {
		t.Errorf("root 下不应加 sudo: %v", got)
	}
	installed(rec, "curl")
	if r := check(t, aptStep{}, x); r.State != Done {
		t.Errorf("全部已装应为 done: %+v", r)
	}
}

// 一个源文件已换源、另一个仍是官方源：必须继续处理剩下的那个文件。
// 旧实现在任一文件里看到镜像地址就整体返回 nil，混合状态会被判为已完成且永不修复。
func TestApt_MixedSourcesConverge(t *testing.T) {
	x, rec := newCtx(t, ubuntu)
	x.UID = 0
	x.Geo = mirrors.CN
	x.Manifest.Packages.Apt = nil // 只看换源，不涉及装包
	mirrored := filepath.Join(x.Root, "etc/apt/sources.list.d/ubuntu.sources")
	official := filepath.Join(x.Root, "etc/apt/sources.list")
	writeFile(t, mirrored, tunaSources, 0o644)
	writeFile(t, official, ubuntuSources, 0o644)

	if r := check(t, aptStep{}, x); r.State != Pending || r.Detail != "切换 apt 源到 TUNA 镜像" {
		t.Fatalf("混合源应被识别为待办: %+v", r)
	}

	rec.Calls = nil
	if err := (aptStep{}).Apply(context.Background(), x); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"cp " + official + " " + official + ".bak.dsync",
		`sed -i -e 's@//archive\.ubuntu\.com/ubuntu@//mirrors.tuna.tsinghua.edu.cn/ubuntu@g' -e 's@//security\.ubuntu\.com/ubuntu@//mirrors.tuna.tsinghua.edu.cn/ubuntu@g' ` + official,
	}
	if got := mutating(rec); !reflect.DeepEqual(got, want) {
		t.Errorf("应只处理仍为官方源的 %s:\n%s\nwant:\n%s", official, strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	// sed 之后两个文件都是镜像地址：不再有待办。
	writeFile(t, official, tunaSources, 0o644)
	if r := check(t, aptStep{}, x); r.State != Done {
		t.Errorf("两个文件都换源后应收敛: %+v", r)
	}
}

func TestApt_Applicable(t *testing.T) {
	a := aptStep{}
	if !a.Applicable(ubuntu) || !a.Applicable(platform.Platform{OS: "linux", DistroID: "debian"}) {
		t.Error("应适用于 ubuntu/debian")
	}
	if a.Applicable(platform.Platform{OS: "linux", DistroID: "fedora"}) || a.Applicable(mac) {
		t.Error("不应适用于 fedora / macOS")
	}
}

// ---------- wsl-ssh-agent ----------

func TestWSLSSHAgent(t *testing.T) {
	x, rec := newCtx(t, wsl)
	s := wslSSHAgentStep{}
	if s.Applicable(ubuntu) || !s.Applicable(wsl) {
		t.Error("只适用于 WSL")
	}
	if r := check(t, s, x); r.State != Pending {
		t.Fatalf("%+v", r)
	}
	if err := s.Apply(context.Background(), x); err != nil {
		t.Fatal(err)
	}
	got := mutating(rec)
	if len(got) != 5 || !strings.Contains(got[1], "npiperelay_windows_amd64.zip") || !strings.HasPrefix(got[2], "unzip -o") {
		t.Errorf("命令序列: %v", got)
	}
	writeFile(t, filepath.Join(x.Home, ".local/bin/npiperelay.exe"), "", 0o755)
	if r := check(t, s, x); r.State != Done {
		t.Errorf("已存在可执行文件应为 done: %+v", r)
	}
	rec.On("curl", exec.Result{ExitCode: 22})
	if err := s.Apply(context.Background(), x); err == nil || !strings.Contains(err.Error(), "代理") || !s.Optional() {
		t.Errorf("下载失败应提示代理且为可选步骤: %v", err)
	}
}

// ---------- homebrew ----------

func TestHomebrew(t *testing.T) {
	x, rec := newCtx(t, mac)
	s := homebrewStep{}
	rec.On("xcode-select -p", exec.Result{ExitCode: 2})
	if r := check(t, s, x); r.State != Manual || !strings.Contains(r.Detail, "xcode-select --install") {
		t.Fatalf("缺 CLT 应为 manual: %+v", r)
	}

	rec.OnStdout("xcode-select -p", "/Library/Developer/CommandLineTools")
	if r := check(t, s, x); r.State != Pending || r.Detail != "安装 Homebrew；安装 ripgrep fd" {
		t.Fatalf("%+v", r)
	}
	rec.On("brew list --formula", exec.Result{ExitCode: 1}) // 刚装好的 brew 里没有任何包
	rec.Calls = nil
	if err := s.Apply(context.Background(), x); err != nil {
		t.Fatal(err)
	}
	got := mutating(rec)
	if len(got) != 2 || !strings.HasPrefix(got[0], "/bin/bash -c") || got[1] != "brew install ripgrep fd" {
		t.Errorf("命令序列: %v", got)
	}
	env := strings.Join(rec.Calls[len(rec.Calls)-1].Cmd.Env, " ")
	if !strings.Contains(env, "NONINTERACTIVE=1") || !strings.Contains(env, "HOMEBREW_BOTTLE_DOMAIN=https://mirrors.tuna.tsinghua.edu.cn/homebrew-bottles") {
		t.Errorf("brew 应带非交互与 cn 镜像环境: %s", env)
	}

	rec.Paths["brew"] = "/opt/homebrew/bin/brew"
	rec.OnStdout("/opt/homebrew/bin/brew list --formula", "")
	if r := check(t, s, x); r.State != Done {
		t.Errorf("已装 brew 与全部包应为 done: %+v", r)
	}
}

// ---------- ohmyzsh ----------

func TestOhMyZsh(t *testing.T) {
	x, rec := newCtx(t, ubuntu)
	s := ohmyzshStep{}
	writeFile(t, filepath.Join(x.Home, ".oh-my-zsh/custom/plugins/zsh-autosuggestions/x"), "", 0o644)
	r := check(t, s, x)
	if r.State != Pending || r.Detail != "安装插件 zsh-users/zsh-syntax-highlighting" {
		t.Fatalf("已装 oh-my-zsh 时只应补插件: %+v", r)
	}
	if err := s.Apply(context.Background(), x); err != nil {
		t.Fatal(err)
	}
	if got := mutating(rec); len(got) != 1 || !strings.HasPrefix(got[0], "git clone --depth 1 https://github.com/zsh-users/zsh-syntax-highlighting") {
		t.Errorf("命令序列: %v", got)
	}

	os.RemoveAll(filepath.Join(x.Home, ".oh-my-zsh"))
	rec.Calls = nil
	if err := s.Apply(context.Background(), x); err != nil {
		t.Fatal(err)
	}
	if env := strings.Join(rec.Calls[0].Cmd.Env, " "); !strings.Contains(env, "KEEP_ZSHRC=yes") || !strings.Contains(env, "CHSH=no") {
		t.Errorf("安装脚本不得改写 ~/.zshrc 或切换 shell: %s", env)
	}
}

// ---------- chsh ----------

func TestChsh(t *testing.T) {
	x, rec := newCtx(t, ubuntu)
	s := chshStep{}
	if r := check(t, s, x); r.State != Manual {
		t.Fatalf("未装 zsh 应为 manual: %+v", r)
	}
	rec.Paths["zsh"] = "/usr/bin/zsh"
	rec.Paths["sudo"] = "/usr/bin/sudo"
	rec.OnStdout("getent passwd kevin", "kevin:x:1000:1000::/home/kevin:/bin/bash\n")
	if r := check(t, s, x); r.State != Pending || r.Detail != "默认 shell /bin/bash → /usr/bin/zsh" {
		t.Fatalf("%+v", r)
	}
	if err := s.Apply(context.Background(), x); err != nil {
		t.Fatal(err)
	}
	if got := mutating(rec); !reflect.DeepEqual(got, []string{"sudo chsh -s /usr/bin/zsh kevin"}) {
		t.Errorf("%v", got)
	}
	rec.OnStdout("getent passwd kevin", "kevin:x:1000:1000::/home/kevin:/usr/bin/zsh\n")
	if r := check(t, s, x); r.State != Done {
		t.Errorf("%+v", r)
	}

	xm, recm := newCtx(t, mac)
	recm.Paths["zsh"] = "/bin/zsh"
	recm.OnStdout("dscl . -read /Users/kevin UserShell", "UserShell: /bin/zsh\n")
	if r := check(t, s, xm); r.State != Done {
		t.Errorf("macOS dscl 解析: %+v", r)
	}
}

// ---------- docker-check ----------

func TestDockerCheck(t *testing.T) {
	x, rec := newCtx(t, wsl)
	s := dockerStep{}
	if r := check(t, s, x); r.State != Manual || !strings.Contains(r.Detail, "WSL Integration") {
		t.Errorf("WSL 无 docker 应给出集成提示: %+v", r)
	} else if strings.Contains(r.Detail, "~/.config/dsync/scripts") || !strings.Contains(r.Detail, "docs/docker.md") {
		t.Errorf("提示不应依赖个人 dotfiles 仓库中的脚本，应指向 docs/docker.md: %s", r.Detail)
	}
	rec.Paths["docker"] = "/usr/bin/docker"
	if r := check(t, s, x); r.State != Done {
		t.Errorf("%+v", r)
	}
	xm, recm := newCtx(t, mac)
	recm.Paths["brew"] = "/opt/homebrew/bin/brew"
	if r := check(t, s, xm); r.State != Pending {
		t.Fatalf("macOS 有 brew 无 docker 应安装 OrbStack: %+v", r)
	}
	if err := s.Apply(context.Background(), xm); err != nil {
		t.Fatal(err)
	}
	if got := mutating(recm); !reflect.DeepEqual(got, []string{"/opt/homebrew/bin/brew install --cask orbstack"}) {
		t.Errorf("%v", got)
	}
}

// ---------- Plan / Apply ----------

func TestPlan_FiltersAndOnly(t *testing.T) {
	x, rec := newCtx(t, ubuntu)
	installed(rec, x.Manifest.Packages.Apt...)
	items, err := Plan(context.Background(), x, nil)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, it := range items {
		ids = append(ids, it.ID)
	}
	// 原生 Ubuntu：无 wsl-ssh-agent、无 homebrew
	if strings.Join(ids, " ") != "apt ohmyzsh chsh docker-check" {
		t.Errorf("ids = %v", ids)
	}
	if items[0].State != Done {
		t.Errorf("apt 应为 done: %+v", items[0])
	}

	items, err = Plan(context.Background(), x, []string{"ohmyzsh"})
	if err != nil || len(items) != 1 || items[0].ID != "ohmyzsh" {
		t.Errorf("--only: %+v %v", items, err)
	}
	if _, err := Plan(context.Background(), x, []string{"nix"}); err == nil {
		t.Error("--only 未知步骤应报错")
	}
	x.Manifest.Provision.Enable = []string{"apt"}
	items, _ = Plan(context.Background(), x, nil)
	if len(items) != 1 {
		t.Errorf("未启用的步骤不应出现: %+v", items)
	}
}

type fakeStep struct {
	id       string
	optional bool
	err      error
	applied  *bool
}

func (f fakeStep) ID() string                                    { return f.id }
func (f fakeStep) Optional() bool                                { return f.optional }
func (fakeStep) Applicable(platform.Platform) bool               { return true }
func (fakeStep) Check(context.Context, *Context) (Result, error) { return Result{State: Pending}, nil }
func (f fakeStep) Apply(context.Context, *Context) error {
	if f.applied != nil {
		*f.applied = true
	}
	return f.err
}

func TestApply_FailureSemantics(t *testing.T) {
	x, _ := newCtx(t, ubuntu)
	var later bool
	items := []Item{
		{ID: "manual", Result: Result{State: Manual, Detail: "do it"}},
		{ID: "opt", Result: Result{State: Pending}, step: fakeStep{id: "opt", optional: true, err: os.ErrPermission}},
		{ID: "done", Result: Result{State: Done}, step: fakeStep{id: "done", err: os.ErrInvalid}},
		{ID: "req", Result: Result{State: Pending}, step: fakeStep{id: "req", err: os.ErrClosed}},
		{ID: "later", Result: Result{State: Pending}, step: fakeStep{id: "later", applied: &later}},
	}
	warnings, err := Apply(context.Background(), x, items, func(string, ...any) {})
	if err == nil || !strings.HasPrefix(err.Error(), "req:") {
		t.Errorf("必选步骤失败应中止: %v", err)
	}
	if later {
		t.Error("中止后不应继续执行")
	}
	if len(warnings) != 2 || !strings.HasPrefix(warnings[0], "manual: do it") || !strings.Contains(warnings[1], "可选步骤") {
		t.Errorf("warnings = %v", warnings)
	}
	if ids := PendingIDs(items); strings.Join(ids, " ") != "opt req later" {
		t.Errorf("PendingIDs = %v", ids)
	}
}

// 步骤间有依赖（chsh 需要 apt 先装好 zsh）：执行前必须重新检测，单次运行即可收敛。
type flagStep struct {
	id    string
	ready *bool // 由前一步 Apply 置为 true
	set   *bool // 本步 Apply 时置为 true
}

func (f flagStep) ID() string                      { return f.id }
func (flagStep) Optional() bool                    { return false }
func (flagStep) Applicable(platform.Platform) bool { return true }
func (f flagStep) Check(context.Context, *Context) (Result, error) {
	if f.ready != nil && !*f.ready {
		return Result{State: Manual, Detail: "依赖未就绪"}, nil
	}
	return Result{State: Pending}, nil
}
func (f flagStep) Apply(context.Context, *Context) error {
	*f.set = true
	return nil
}

func TestApply_RechecksBeforeEachStep(t *testing.T) {
	x, _ := newCtx(t, ubuntu)
	var zshInstalled, shellChanged bool
	apt := flagStep{id: "apt", set: &zshInstalled}
	chsh := flagStep{id: "chsh", ready: &zshInstalled, set: &shellChanged}
	items := []Item{
		{ID: "apt", Result: Result{State: Pending}, step: apt},
		{ID: "chsh", Result: Result{State: Manual, Detail: "依赖未就绪"}, step: chsh}, // 计划阶段的旧结果
	}
	warnings, err := Apply(context.Background(), x, items, func(string, ...any) {})
	if err != nil || len(warnings) != 0 {
		t.Fatalf("err=%v warnings=%v", err, warnings)
	}
	if !shellChanged {
		t.Error("apt 完成后 chsh 应在同一次运行中执行")
	}
}
