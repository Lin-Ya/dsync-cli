package workflow

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Lin-Ya/dsync-cli/internal/exec"
)

// configFixture：v2 仓库 + chezmoi apply 成功。返回 chezmoi 源目录。
func configFixture(t *testing.T) (*fixture, string) {
	f := newFixture(t)
	root := setupV2Repo(t, f)
	src := filepath.Join(root, "home")
	f.rec.OnStdout("chezmoi --source "+src+" apply", "")
	return f, src
}

func (f *fixture) local(t *testing.T) string {
	return readFile(t, filepath.Join(f.home, ".config", "dsync", "local.toml"))
}

// git 身份 / 标签注入模板：修改后立即重新生成 chezmoi 配置并 apply，status 随即不再提示待部署。
func TestConfigSet_TemplateDataRedeploys(t *testing.T) {
	f, src := configFixture(t)
	ctx := context.Background()
	err := f.Env().ConfigSet(ctx, [][2]string{{"user.name", "Me"}, {"user.email", " me@example.com "}, {"tags", "work, ,laptop"}})
	if err != nil {
		t.Fatalf("%v\n%s", err, f.out.String())
	}
	l := f.local(t)
	for _, want := range []string{"name = 'Me'", "email = 'me@example.com'", "tags = ['work', 'laptop']", "geo = 'official'"} {
		if !strings.Contains(l, want) {
			t.Errorf("local.toml 缺少 %q（已有项应保留）:\n%s", want, l)
		}
	}
	cfg := readFile(t, filepath.Join(f.home, ".config", "chezmoi", "chezmoi.toml"))
	if !strings.Contains(cfg, "name = 'Me'") {
		t.Errorf("chezmoi 配置应已重新生成:\n%s", cfg)
	}
	if len(f.lines("chezmoi --source "+src+" apply")) != 1 {
		t.Errorf("应 apply 一次: %v", f.rec.Lines())
	}
	if r, _ := f.Env().Status(ctx); r.ConfigStale {
		t.Error("config set 之后不应再提示待部署")
	}
}

// 代理只由 shell-init 读取：保存即可，不部署；全部清除后去掉整个 [proxy] 表。
func TestConfigSet_ProxyNoRedeployAndUnsetRemovesTable(t *testing.T) {
	f, _ := configFixture(t)
	ctx := context.Background()
	if err := f.Env().ConfigSet(ctx, [][2]string{{"proxy.http", "http://${WSL_HOST_IP}:7890"}, {"proxy.no_proxy", "localhost"}}); err != nil {
		t.Fatal(err)
	}
	if l := f.local(t); !strings.Contains(l, "[proxy]") || !strings.Contains(l, "http = 'http://${WSL_HOST_IP}:7890'") {
		t.Errorf("local.toml:\n%s", l)
	}
	if !strings.Contains(f.out.String(), "新开的终端中生效") {
		t.Errorf("应提示代理生效方式:\n%s", f.out.String())
	}
	if err := f.Env().ConfigSet(ctx, [][2]string{{"proxy.http", ""}, {"proxy.no_proxy", ""}}); err != nil {
		t.Fatal(err)
	}
	if l := f.local(t); strings.Contains(l, "proxy") {
		t.Errorf("代理全部清除后不应保留 [proxy]:\n%s", l)
	}
	for _, c := range f.rec.Calls {
		if c.Mutating {
			t.Errorf("只改代理不应执行写操作: %s", c.Cmd)
		}
	}
}

func TestConfigSet_Errors(t *testing.T) {
	f, _ := configFixture(t)
	ctx := context.Background()
	before := f.local(t)
	cases := []struct {
		pair [2]string
		want string
	}{
		{[2]string{"geo", "cn"}, "dsync geo set"},
		{[2]string{"user.nmae", "x"}, "可选: user.name"},
		{[2]string{"repo.path", "~/nowhere"}, "不是 dotfiles 仓库"},
	}
	for _, c := range cases {
		err := f.Env().ConfigSet(ctx, [][2]string{{"user.name", "Partial"}, c.pair})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: 应报 %q: %v", c.pair, c.want, err)
		}
	}
	if f.local(t) != before {
		t.Error("任一项出错时不应写入任何改动")
	}
	f.write(t, ".config/dsync/local.toml", "bogus = 1\n")
	if err := f.Env().ConfigSet(ctx, [][2]string{{"user.name", "x"}}); err == nil || !strings.Contains(err.Error(), "请先修正") {
		t.Errorf("local.toml 有误时应拒绝覆盖: %v", err)
	}
}

func TestConfigSet_RepoPathResolvedToRoot(t *testing.T) {
	f, _ := configFixture(t)
	f.write(t, "other/dsync.toml", "version = 1\n")
	root := realpath(t, filepath.Join(f.home, "other"))
	f.rec.On("git rev-parse --show-toplevel", exec.Result{Stdout: []byte(root + "\n")})
	if err := f.Env().ConfigSet(context.Background(), [][2]string{{"repo.path", "~/other"}}); err != nil {
		t.Fatal(err)
	}
	if l := f.local(t); !strings.Contains(l, "path = '"+root+"'") {
		t.Errorf("repo.path 应解析为仓库根:\n%s", l)
	}
}

// 还没有仓库（新机器先配身份再 init）：只保存，不调用 chezmoi。
func TestConfigSet_WithoutRepoOnlySaves(t *testing.T) {
	f := newFixture(t)
	if err := f.Env().ConfigSet(context.Background(), [][2]string{{"user.name", "Me"}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.local(t), "name = 'Me'") || !strings.Contains(f.out.String(), "无需部署") {
		t.Errorf("local.toml:\n%s\n输出:\n%s", f.local(t), f.out.String())
	}
	if len(f.rec.Calls) != 0 {
		t.Errorf("不应调用外部命令: %v", f.rec.Lines())
	}
}

func TestConfigSet_DryRunWritesNothing(t *testing.T) {
	f, _ := configFixture(t)
	before := f.local(t)
	e := f.Env()
	e.DryRun = true
	e.Runner = exec.DryRun{Inner: f.rec, Out: &f.err}
	if err := e.ConfigSet(context.Background(), [][2]string{{"user.name", "Me"}}); err != nil {
		t.Fatal(err)
	}
	if f.local(t) != before || fileExists(filepath.Join(f.home, ".config", "chezmoi", "chezmoi.toml")) {
		t.Error("dry-run 不得写文件")
	}
	if !strings.Contains(f.err.String(), "[dry-run] 写入") || !strings.Contains(f.err.String(), "apply") {
		t.Errorf("dry-run 应打印将执行的操作:\n%s", f.err.String())
	}
}

func TestConfigShow(t *testing.T) {
	f := newFixture(t)
	if err := f.Env().ConfigShow(); err != nil {
		t.Fatal(err)
	}
	out := f.out.String()
	for _, want := range []string{"（尚未创建）", "cn（来自 default", "user.name       （未设置）", "proxy.no_proxy"} {
		if !strings.Contains(out, want) {
			t.Errorf("输出缺少 %q:\n%s", want, out)
		}
	}
	f, _ = configFixture(t)
	f.write(t, ".config/dsync/local.toml", "geo = 'official'\ntags = ['a', 'b']\n[proxy]\nhttp = 'http://p:1'\n")
	f.Env().ConfigShow()
	if out := f.out.String(); !strings.Contains(out, "tags            a,b") || !strings.Contains(out, "proxy.http      http://p:1") {
		t.Errorf("输出:\n%s", out)
	}
}

func TestGeoShow(t *testing.T) {
	f, _ := configFixture(t)
	if err := f.Env().GeoShow(); err != nil || strings.TrimSpace(f.out.String()) != "official（来自 local.toml）" {
		t.Errorf("%q %v", f.out.String(), err)
	}
}
