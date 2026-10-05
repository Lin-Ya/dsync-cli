package workflow

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	dexec "github.com/Lin-Ya/dsync-cli/internal/exec"
)

// initFixture：底座工具齐全（不触发下载），chezmoi / mise / provision 相关命令全部成功。
func initFixture(t *testing.T) *fixture {
	t.Helper()
	t.Setenv("PATH", os.Getenv("PATH")) // Init 会改写进程 PATH，测试结束后恢复
	f := newFixture(t)
	f.tools("git", "mise", "chezmoi")
	f.rec.OnStdout("", "")                                         // 兜底：未单独预设的命令一律成功
	f.rec.OnStdout("chezmoi --source", `{"age":{"recipient":""}}`) // dump-config；init/apply 忽略输出
	f.rec.OnStdout("chezmoi source-path", "")
	f.rec.On("chezmoi source-path", dexec.Result{ExitCode: 1}) // 新机器上没有 chezmoi 源
	f.rec.On("git rev-parse --show-toplevel", dexec.Result{ExitCode: 128})
	f.rec.OnStdout("getent passwd", "u:x:1000:1000::/home/u:/usr/bin/zsh\n")
	f.tools("zsh")
	return f
}

func (f *fixture) lines(prefix string) []string {
	var out []string
	for _, l := range f.rec.Lines() {
		if strings.HasPrefix(l, prefix) {
			out = append(out, l)
		}
	}
	return out
}

func TestInit_FromSource(t *testing.T) {
	f := initFixture(t)
	f.write(t, "src/dots/.chezmoi.toml.tmpl", "")
	if err := f.Env().Init(context.Background(), InitOptions{Source: "~/src/dots", Geo: "official"}); err != nil {
		t.Fatalf("%v\n%s", err, f.out.String())
	}
	src := realpath(t, filepath.Join(f.home, "src", "dots"))

	local := readFile(t, filepath.Join(f.home, ".config", "dsync", "local.toml"))
	if !strings.Contains(local, "geo = 'official'") || !strings.Contains(local, "path = '"+src+"'") {
		t.Errorf("local.toml:\n%s", local)
	}
	if target, err := os.Readlink(filepath.Join(f.home, ".local", "share", "chezmoi")); err != nil || target != src {
		t.Errorf("应建立 ~/.local/share/chezmoi → 源目录: %q %v", target, err)
	}

	// chezmoi init 以 --geo 传入 DSYNC_GEO，且在 apply、provision、mise install 之前
	var initEnv string
	for _, c := range f.rec.Calls {
		if strings.HasPrefix(dexec.Cmd{Name: c.Cmd.Name, Args: c.Cmd.Args}.String(), "chezmoi --source "+src+" init") {
			initEnv = strings.Join(c.Cmd.Env, " ")
		}
	}
	if !strings.Contains(initEnv, "DSYNC_GEO=official") {
		t.Errorf("chezmoi init 环境 = %q", initEnv)
	}
	order := strings.Join(f.rec.Lines(), "\n")
	iInit := strings.Index(order, "chezmoi --source "+src+" init")
	iApply := strings.Index(order, "chezmoi --source "+src+" apply")
	iProv := strings.Index(order, "getent passwd")
	iMise := strings.Index(order, "\nmise install")
	if !(iInit >= 0 && iInit < iApply && iApply < iProv && iProv < iMise) {
		t.Errorf("执行顺序错误:\n%s", order)
	}
	// 无 lockfile：普通安装（不带 --locked）
	if got := f.lines("mise install"); len(got) != 1 || got[0] != "mise install" {
		t.Errorf("无 lockfile 时应 mise install: %v", got)
	}
	if len(f.lines("curl")) != 0 {
		t.Error("底座工具齐全时不应下载")
	}
}

func TestInit_RerunIsIdempotentAndUsesLockfile(t *testing.T) {
	f := initFixture(t)
	f.write(t, "dots/.chezmoi.toml.tmpl", "")
	if err := f.Env().Init(context.Background(), InitOptions{Source: filepath.Join(f.home, "dots")}); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(f.home, ".local", "share", "chezmoi")
	before, _ := os.Readlink(link)

	// 第二次：不带参数，沿用 local.toml 中的仓库；此时已有 lockfile
	f.write(t, ".config/mise/mise.lock", "lock\n")
	f.rec.Calls = nil
	if err := f.Env().Init(context.Background(), InitOptions{}); err != nil {
		t.Fatalf("重复运行应成功: %v", err)
	}
	if after, _ := os.Readlink(link); after != before {
		t.Error("重复运行不应改动软链")
	}
	if got := f.lines("mise install"); len(got) != 1 || got[0] != "mise install --locked" {
		t.Errorf("有 lockfile 时应严格锁定安装: %v", got)
	}
	if !strings.Contains(readFile(t, filepath.Join(f.home, ".config", "dsync", "local.toml")), "geo = 'cn'") {
		t.Error("未指定 geo 时默认 cn")
	}
}

func TestInit_CloneRepo(t *testing.T) {
	f := initFixture(t)
	dest := filepath.Join(f.home, DefaultCloneDir)
	const clone = "git clone https://github.com/me/dotfiles.git"
	f.rec.OnFunc(clone, func(c dexec.Cmd) dexec.Result {
		os.MkdirAll(c.Args[2], 0o755)
		os.WriteFile(filepath.Join(c.Args[2], ".chezmoi.toml.tmpl"), nil, 0o644)
		return dexec.Result{}
	})
	opts := InitOptions{Repo: "https://github.com/me/dotfiles.git"}
	if err := f.Env().Init(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if got := f.lines(clone); len(got) != 1 || got[0] != clone+" "+dest {
		t.Errorf("clone: %v", got)
	}
	f.rec.Calls = nil
	if err := f.Env().Init(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if len(f.lines(clone)) != 0 || !strings.Contains(f.out.String(), "已存在，跳过克隆") {
		t.Error("目标已存在时应跳过克隆")
	}
}

func TestInit_GeoPrecedence(t *testing.T) {
	f := initFixture(t)
	f.write(t, "dots/.chezmoi.toml.tmpl", "")
	f.env["DSYNC_GEO"] = "official"
	if err := f.Env().Init(context.Background(), InitOptions{Source: filepath.Join(f.home, "dots")}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readFile(t, filepath.Join(f.home, ".config", "dsync", "local.toml")), "geo = 'official'") {
		t.Error("环境变量 DSYNC_GEO 应生效")
	}
	if err := f.Env().Init(context.Background(), InitOptions{Geo: "cn"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readFile(t, filepath.Join(f.home, ".config", "dsync", "local.toml")), "geo = 'cn'") {
		t.Error("--geo 应优先于环境变量")
	}
	// 已记录的选择不被环境中的残留值覆盖：v1 的 .zshenv 会在每个 shell 导出 DSYNC_GEO，
	// geo set 之后同一终端里它仍是旧值（此处环境变量仍为 official，local.toml 为 cn）。
	if err := f.Env().Init(context.Background(), InitOptions{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readFile(t, filepath.Join(f.home, ".config", "dsync", "local.toml")), "geo = 'cn'") {
		t.Error("重复运行时 local.toml 应优先于环境变量")
	}
}

func TestInit_Errors(t *testing.T) {
	f := initFixture(t)
	ctx := context.Background()
	cases := map[string]struct {
		opts InitOptions
		want string
	}{
		"三选一":             {InitOptions{Repo: "x", Source: "y"}, "三选一"},
		"create+source":   {InitOptions{Create: true, Source: "y"}, "三选一"},
		"github 需 create": {InitOptions{GitHub: "me/dots"}, "--create"},
		"未指定仓库":           {InitOptions{}, "请指定 dotfiles 仓库"},
		"非仓库目录":           {InitOptions{Source: f.home}, "不是 dotfiles 仓库"},
		"非法 geo":          {InitOptions{Source: f.home, Geo: "mars"}, "未知 geo"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if err := f.Env().Init(ctx, c.opts); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want %q", err, c.want)
			}
		})
	}
	delete(f.rec.Paths, "git")
	if err := f.Env().Init(ctx, InitOptions{Source: f.home}); err == nil || !strings.Contains(err.Error(), "未找到 git") {
		t.Errorf("缺少 git: %v", err)
	}
}

func TestInit_DryRunDownloadsNothing(t *testing.T) {
	f := initFixture(t)
	delete(f.rec.Paths, "mise")
	delete(f.rec.Paths, "chezmoi")
	// 新机器上 chezmoi 尚未安装：任何 chezmoi 查询都会失败（dry-run 下也不应调用）
	f.rec.On("chezmoi", dexec.Result{ExitCode: 127, Stderr: []byte("chezmoi: command not found")})
	f.write(t, "dots/.chezmoi.toml.tmpl", "")
	e := f.Env()
	e.DryRun = true
	e.Runner = dexec.DryRun{Inner: f.rec, Out: &f.err}
	if err := e.Init(context.Background(), InitOptions{Source: filepath.Join(f.home, "dots")}); err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"mise", "chezmoi"} {
		if fileExists(filepath.Join(f.home, ".local", "bin", tool)) {
			t.Errorf("dry-run 不应安装 %s", tool)
		}
	}
	if !strings.Contains(f.err.String(), "[dry-run] 下载 https://github.com/jdx/mise/") || fileExists(filepath.Join(f.home, ".config", "dsync", "local.toml")) {
		t.Errorf("dry-run 应只打印:\n%s", f.err.String())
	}
	for _, d := range []string{".config", ".local"} {
		if _, err := os.Stat(filepath.Join(f.home, d)); err == nil {
			t.Errorf("dry-run 不应创建 ~/%s", d)
		}
	}
}

// 克隆路径的 dry-run 同样不得创建任何目录。
func TestInit_DryRunCloneCreatesNothing(t *testing.T) {
	f := initFixture(t)
	e := f.Env()
	e.DryRun = true
	e.Runner = dexec.DryRun{Inner: f.rec, Out: &f.err}
	if err := e.Init(context.Background(), InitOptions{Repo: "https://github.com/me/dotfiles.git"}); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(f.home); len(entries) != 0 {
		t.Errorf("dry-run 后 HOME 应保持为空: %v", entries)
	}
}

func TestInit_CreateFromStarter(t *testing.T) {
	needBin(t, "git")
	f := initFixture(t)
	t.Setenv("HOME", f.home)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	f.rec.Delegate = map[string]dexec.Runner{"git": dexec.Real{}}
	if err := f.Env().Init(context.Background(), InitOptions{Create: true, Geo: "official"}); err != nil {
		t.Fatalf("%v\n%s", err, f.out.String())
	}
	dest := filepath.Join(f.home, DefaultCloneDir)
	if !fileExists(filepath.Join(dest, "dsync.toml")) || !fileExists(filepath.Join(dest, "home", "dot_zshenv")) {
		t.Fatal("应写入 starter 模板")
	}
	out, err := exec.Command("git", "-C", dest, "log", "--format=%s %an").Output()
	if err != nil || strings.TrimSpace(string(out)) != "init: dsync starter dsync" {
		t.Errorf("初始提交: %q %v", out, err)
	}
	cfg := readFile(t, filepath.Join(f.home, ".config", "chezmoi", "chezmoi.toml"))
	if !strings.Contains(cfg, "sourceDir = '"+realpath(t, filepath.Join(dest, "home"))+"'") || !strings.Contains(cfg, "geo = 'official'") {
		t.Errorf("v2 布局应由 dsync 生成 chezmoi 配置:\n%s", cfg)
	}
	if _, err := os.Lstat(filepath.Join(f.home, ".local", "share", "chezmoi")); err == nil {
		t.Error("v2 布局不应创建 ~/.local/share/chezmoi 软链")
	}
	if !strings.Contains(f.out.String(), "仅本地仓库") {
		t.Error("未给 --github 时应提示如何关联远端")
	}
	for _, l := range f.rec.Lines() {
		if strings.HasPrefix(l, "chezmoi") && strings.Contains(l, " init") {
			t.Errorf("v2 布局不应执行 chezmoi init: %s", l)
		}
	}

	// 已存在时拒绝覆盖
	if err := f.Env().Init(context.Background(), InitOptions{Create: true}); err == nil || !strings.Contains(err.Error(), "不为空") {
		t.Errorf("重复 --create 应拒绝: %v", err)
	}
}

func TestInit_CreateWithGitHub(t *testing.T) {
	f := initFixture(t)
	if err := f.Env().Init(context.Background(), InitOptions{Create: true, GitHub: "me/dots"}); err == nil || !strings.Contains(err.Error(), "gh") {
		t.Errorf("没有 gh 应报错: %v", err)
	}
	f.tools("gh")
	f.rec.OnFunc("git", func(c dexec.Cmd) dexec.Result { return dexec.Result{} })
	f.rec.On("git rev-parse --show-toplevel", dexec.Result{ExitCode: 128})
	if err := f.Env().Init(context.Background(), InitOptions{Create: true, GitHub: "me/dots"}); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(f.home, DefaultCloneDir)
	want := dexec.Cmd{Name: "gh", Args: []string{"repo", "create", "me/dots", "--private", "--source", dest, "--push"}}.String()
	if got := f.lines("gh repo create"); len(got) != 1 || got[0] != want {
		t.Errorf("gh 调用: %v", got)
	}
}

// git 身份：local.toml 已有值优先，其次导入 git 全局配置，仍缺失时（交互终端）提问。
func TestInit_GitIdentity(t *testing.T) {
	src := func(f *fixture) InitOptions {
		f.write(t, "src/dots/.chezmoi.toml.tmpl", "")
		return InitOptions{Source: "~/src/dots"}
	}
	local := func(f *fixture) string { return readFile(t, filepath.Join(f.home, ".config", "dsync", "local.toml")) }

	t.Run("导入 git 全局配置", func(t *testing.T) {
		f := initFixture(t)
		f.rec.OnStdout("git config --global --get user.name", "Global Me\n").
			OnStdout("git config --global --get user.email", "g@example.com\n")
		e := f.Env()
		e.Prompt = func(q string) string { t.Errorf("已有身份时不应提问: %s", q); return "" }
		if err := e.Init(context.Background(), src(f)); err != nil {
			t.Fatal(err)
		}
		if l := local(f); !strings.Contains(l, "name = 'Global Me'") || !strings.Contains(l, "email = 'g@example.com'") {
			t.Errorf("local.toml:\n%s", l)
		}
	})

	t.Run("local.toml 优先，只补缺失项并提问", func(t *testing.T) {
		f := initFixture(t)
		f.write(t, ".config/dsync/local.toml", "[user]\nname = 'Local Me'\n")
		var asked []string
		e := f.Env()
		e.Prompt = func(q string) string { asked = append(asked, q); return "typed@example.com" }
		if err := e.Init(context.Background(), src(f)); err != nil {
			t.Fatal(err)
		}
		if len(asked) != 1 || !strings.Contains(asked[0], "user.email") {
			t.Errorf("应只询问 email: %v", asked)
		}
		if l := local(f); !strings.Contains(l, "name = 'Local Me'") || !strings.Contains(l, "email = 'typed@example.com'") {
			t.Errorf("local.toml:\n%s", l)
		}
	})

	t.Run("非交互且无身份：提示后继续", func(t *testing.T) {
		f := initFixture(t)
		if err := f.Env().Init(context.Background(), src(f)); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(local(f), "[user]") {
			t.Errorf("不应写入空身份:\n%s", local(f))
		}
		if !strings.Contains(f.out.String(), "[user] 填写 name / email") {
			t.Errorf("下一步应提示设置身份:\n%s", f.out.String())
		}
	})
}
