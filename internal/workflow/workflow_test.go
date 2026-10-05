package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Lin-Ya/dsync-cli/internal/exec"
)

// fixture 构造一个隔离的伪 HOME 与 Recording Runner。
type fixture struct {
	home string
	env  map[string]string
	rec  *exec.Recording
	out  bytes.Buffer
	err  bytes.Buffer
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	// HOME 放在符号链接之后，在 Linux 上也复现 macOS 的 /var → /private/var 情形
	// （仓库定位会解析符号链接，测试必须与之一致）。
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(dir, "home")
	if err := os.Symlink(filepath.Join(dir, "real"), home); err != nil {
		t.Fatal(err)
	}
	f := &fixture{home: home, rec: &exec.Recording{Paths: map[string]string{}}}
	f.env = map[string]string{"HOME": f.home}
	return f
}

func (f *fixture) Env() Env {
	return Env{
		Getenv: func(k string) string { return f.env[k] },
		Runner: f.rec,
		RootFS: fstest.MapFS{"proc/version": {Data: []byte("Linux version 6.1 generic")}},
		GOOS:   "linux", // 固定平台：结果不随 CI runner（macOS / Linux）变化
		Out:    &f.out,
		Err:    &f.err,
	}
}

func (f *fixture) write(t *testing.T, rel, content string) string {
	t.Helper()
	p := filepath.Join(f.home, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func (f *fixture) tools(names ...string) {
	for _, n := range names {
		f.rec.Paths[n] = "/usr/bin/" + n
	}
}

// ---------- shell-init ----------

func TestShellInit_DegradesOnBadConfig(t *testing.T) {
	f := newFixture(t)
	f.write(t, ".config/dsync/local.toml", "geo = \"mars\"\n")
	if err := f.Env().ShellInit("zsh"); err != nil {
		t.Fatalf("配置错误不应让 shell 启动失败: %v", err)
	}
	if !strings.Contains(f.err.String(), "mars") {
		t.Errorf("应向 stderr 告警: %q", f.err.String())
	}
	if !strings.Contains(f.out.String(), "export DSYNC_GEO='cn'") {
		t.Errorf("应回退默认 geo:\n%s", f.out.String())
	}
}

func TestShellInit_ProxySources(t *testing.T) {
	f := newFixture(t)
	v1 := f.write(t, ".config/dsync/proxy.env", "export http_proxy=x\n")
	if err := f.Env().ShellInit("bash"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.out.String(), ". '"+v1+"'") {
		t.Errorf("无 local.toml [proxy] 时应 source v1 proxy.env:\n%s", f.out.String())
	}

	f.out.Reset()
	f.write(t, ".config/dsync/local.toml", "geo = \"official\"\n[proxy]\nhttps = \"http://127.0.0.1:7890\"\n")
	if err := f.Env().ShellInit("bash"); err != nil {
		t.Fatal(err)
	}
	out := f.out.String()
	if strings.Contains(out, "proxy.env") || !strings.Contains(out, `export https_proxy="http://127.0.0.1:7890"`) {
		t.Errorf("local.toml [proxy] 应优先于 v1 proxy.env:\n%s", out)
	}
	if !strings.Contains(out, "export DSYNC_GEO='official'") || !strings.Contains(out, "unset MISE_NODE_MIRROR_URL") {
		t.Errorf("geo=official 应清除镜像变量:\n%s", out)
	}
}

// ---------- 仓库定位 ----------

func TestLocateRepo_V2ViaLocalToml(t *testing.T) {
	f := newFixture(t)
	f.write(t, "dots/dsync.toml", "version = 1\n")
	f.write(t, "dots/.chezmoiroot", "home\n")
	f.write(t, ".config/dsync/local.toml", "[repo]\npath = \"~/dots\"\n")
	e := f.Env()
	c, _ := e.Load()
	info, err := e.LocateRepo(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	root := realpath(t, filepath.Join(f.home, "dots"))
	if info.Root != root || info.Source != filepath.Join(root, "home") || info.Layout != LayoutV2 || info.Via != "local.toml repo.path" {
		t.Errorf("%+v", info)
	}
}

func TestLocateRepo_V1Fallback(t *testing.T) {
	f := newFixture(t)
	f.write(t, "dsync/.chezmoi.toml.tmpl", "")
	f.tools("chezmoi")
	f.rec.On("chezmoi source-path", exec.Result{ExitCode: 1, Stderr: []byte("not found")})
	e := f.Env()
	c, _ := e.Load()
	info, err := e.LocateRepo(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if info.Layout != LayoutV1 || info.Via != "~/dsync" {
		t.Errorf("%+v", info)
	}
}

func TestLocateRepo_NotFound(t *testing.T) {
	f := newFixture(t)
	e := f.Env()
	c, _ := e.Load()
	if _, err := e.LocateRepo(context.Background(), c); err != ErrRepoNotFound {
		t.Errorf("err = %v", err)
	}
}

// ---------- status ----------

const lsOneMissing = `{"node":[{"version":"24.1.0","installed":true}],"fzf":[{"version":"0.57.0","installed":false}]}`

func setupV2Repo(t *testing.T, f *fixture) string {
	f.write(t, "dots/dsync.toml", "version = 1\n")
	f.write(t, "dots/.chezmoiroot", "home\n")
	f.write(t, ".config/dsync/local.toml", "geo = \"official\"\n[repo]\npath = \"~/dots\"\n")
	root := realpath(t, filepath.Join(f.home, "dots"))
	f.tools("git", "chezmoi", "mise")
	f.rec.OnStdout("git rev-parse --show-toplevel", root+"\n").
		OnStdout("git status --porcelain=v2 --branch", "# branch.head main\n# branch.upstream origin/main\n# branch.ab +1 -0\n").
		OnStdout("chezmoi --source "+filepath.Join(root, "home")+" status", " M .npmrc\n").
		OnStdout("chezmoi source-path", filepath.Join(root, "home")+"\n").
		OnStdout("mise ls --global --json", lsOneMissing)
	return root
}

func TestStatus(t *testing.T) {
	f := newFixture(t)
	root := setupV2Repo(t, f)
	r, err := f.Env().Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if r.Geo != "official" || r.GeoFrom != "local.toml" {
		t.Errorf("geo = %s/%s", r.Geo, r.GeoFrom)
	}
	if r.Repo == nil || r.Repo.Root != root || r.Git.Ahead != 1 {
		t.Errorf("repo/git: %+v %+v", r.Repo, r.Git)
	}
	if len(r.Drift) != 1 || r.Drift[0].Path != ".npmrc" || r.Tools != 2 || len(r.Missing) != 1 || r.Missing[0] != "fzf@0.57.0" {
		t.Errorf("drift/tools: %+v", r)
	}
	if len(r.Warnings) != 0 || r.Clean() {
		t.Errorf("warnings=%v clean=%v", r.Warnings, r.Clean())
	}
	for _, c := range f.rec.Calls {
		if c.Mutating {
			t.Errorf("status 必须只读，却执行了: %s", c.Cmd)
		}
	}

	// JSON 字段名是对外契约（e2e 依赖 missing_tools / drift 判断幂等）
	var buf bytes.Buffer
	if err := WriteJSON(&buf, r); err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal(buf.Bytes(), &m)
	for _, k := range []string{"platform", "geo", "geo_source", "repo", "git", "drift", "tools", "missing_tools", "provision_pending", "warnings"} {
		if _, ok := m[k]; !ok {
			t.Errorf("JSON 缺少字段 %q", k)
		}
	}
}

func TestStatus_ToolsMissingAreWarningsNotErrors(t *testing.T) {
	f := newFixture(t)
	r, err := f.Env().Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(r.Warnings, "\n")
	for _, w := range []string{"找不到 dotfiles 仓库", "未找到 chezmoi", "未找到 mise"} {
		if !strings.Contains(joined, w) {
			t.Errorf("缺少警告 %q: %v", w, r.Warnings)
		}
	}
}

// ---------- doctor ----------

func TestDoctor_Healthy(t *testing.T) {
	f := newFixture(t)
	setupV2Repo(t, f)
	f.rec.OnStdout("git --version", "git version 2.43.0\n").
		OnStdout("mise --version", "2026.9.0\n").
		OnStdout("chezmoi --version", "chezmoi version v2.72.1\n").
		OnStdout("git var GIT_AUTHOR_IDENT", "Me <me@example.com> 1791204907 +0800\n")
	d, err := f.Env().Doctor(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if c := find(d, "git identity"); c.Level != OK || c.Detail != "Me <me@example.com>" {
		t.Errorf("git identity: %+v", c)
	}
	if d.Failed() {
		t.Errorf("不应有 fail 项: %+v", d.Checks)
	}
	lv := levels(d)
	if lv["remote"] != Warn { // ahead=1
		t.Errorf("有未推送提交时 remote 应告警: %v", lv)
	}
	if lv["chezmoi source"] != OK || lv["mise tools"] != Warn || lv["git"] != OK {
		t.Errorf("%v", lv)
	}
}

func TestDoctor_FailsOnMissingBaseToolsAndBadConfig(t *testing.T) {
	f := newFixture(t)
	f.write(t, ".config/dsync/local.toml", "bogus = 1\n")
	d, err := f.Env().Doctor(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	lv := levels(d)
	for _, name := range []string{"git", "mise", "chezmoi", "local.toml", "repo"} {
		if lv[name] != Fail {
			t.Errorf("%s 应为 fail: %v", name, lv)
		}
	}
	if !d.Failed() {
		t.Error("Failed() 应为 true")
	}
}

func levels(d DoctorReport) map[string]Level {
	m := map[string]Level{}
	for _, c := range d.Checks {
		m[c.Name] = c.Level
	}
	return m
}

func realpath(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// 无漂移时 JSON 必须是 "drift": []，而不是 null（e2e / 脚本按空数组判断是否收敛）。
func TestStatus_EmptyListsAreArraysInJSON(t *testing.T) {
	f := newFixture(t)
	setupV2Repo(t, f)
	root := realpath(t, filepath.Join(f.home, "dots"))
	f.rec.OnStdout("chezmoi --source "+filepath.Join(root, "home")+" status", "")
	f.rec.OnStdout("mise ls --global --json", `{"node":[{"version":"24.1.0","installed":true}]}`)
	r, err := f.Env().Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	WriteJSON(&buf, r)
	for _, k := range []string{"drift", "missing_tools", "provision_pending", "warnings"} {
		if !strings.Contains(buf.String(), `"`+k+`": []`) {
			t.Errorf("%s 应输出为 []:\n%s", k, buf.String())
		}
	}
}

func find(d DoctorReport, name string) Check {
	for _, c := range d.Checks {
		if c.Name == name {
			return c
		}
	}
	return Check{}
}
