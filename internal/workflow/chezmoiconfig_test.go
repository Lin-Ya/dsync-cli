package workflow

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"github.com/Lin-Ya/dsync-cli/internal/config"
	dexec "github.com/Lin-Ya/dsync-cli/internal/exec"
	"github.com/Lin-Ya/dsync-cli/internal/mirrors"
	"github.com/Lin-Ya/dsync-cli/internal/platform"
	"github.com/Lin-Ya/dsync-cli/internal/starter"
)

func TestChezmoiData(t *testing.T) {
	m := config.Manifest{Data: map[string]any{"editor": "nvim"}}
	l := config.Local{Tags: []string{"work"}, User: config.User{Name: "LinYa", Email: "a@b.c"}}
	d, err := ChezmoiData(mirrors.CN, platform.Platform{WSL: true}, l, m)
	if err != nil {
		t.Fatal(err)
	}
	ds := d["dsync"].(map[string]any)
	if d["editor"] != "nvim" || ds["geo"] != "cn" || ds["wsl"] != true ||
		ds["mirrors"].(map[string]any)["npm"] != "https://registry.npmmirror.com" ||
		ds["user"].(map[string]any)["email"] != "a@b.c" || ds["tags"].([]string)[0] != "work" {
		t.Errorf("%+v", d)
	}
	d, _ = ChezmoiData(mirrors.Official, platform.Platform{}, config.Local{}, config.Manifest{})
	ds = d["dsync"].(map[string]any)
	if ds["mirrors"].(map[string]any)["npm"] != "" || len(ds["mirrors"].(map[string]any)) != len(mirrors.Components()) {
		t.Errorf("official 下镜像键应齐全且为空串: %+v", ds["mirrors"])
	}
	if ds["tags"] == nil {
		t.Error("tags 应为空数组而非 nil（模板中 range / has 不报错）")
	}
}

func TestChezmoiConfig(t *testing.T) {
	data := map[string]any{"dsync": map[string]any{"geo": "cn"}}
	b, err := chezmoiConfig("/r/home", data, config.Manifest{})
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := toml.Unmarshal(b, &cfg); err != nil {
		t.Fatalf("生成的配置不是合法 TOML: %v\n%s", err, b)
	}
	if cfg["sourceDir"] != "/r/home" || !strings.HasPrefix(string(b), "# 由 dsync 生成") {
		t.Errorf("%s", b)
	}
	if _, ok := cfg["encryption"]; ok {
		t.Error("没有 age 收件人时不应启用加密")
	}
	b, _ = chezmoiConfig("/r/home", data, config.Manifest{Secrets: config.Secrets{AgeRecipients: []string{"age1abc"}}})
	toml.Unmarshal(b, &cfg)
	if cfg["encryption"] != "age" || cfg["age"].(map[string]any)["recipients"].([]any)[0] != "age1abc" {
		t.Errorf("%s", b)
	}
}

// ---------- 契约：真实 chezmoi + starter 模板（v2 布局） ----------

func v2Repo(t *testing.T, home string) string {
	t.Helper()
	repo := filepath.Join(home, "dots")
	if err := starter.Write(repo); err != nil {
		t.Fatal(err)
	}
	return repo
}

func TestContract_V2GeoSetRendersFromInjectedData(t *testing.T) {
	needBin(t, "chezmoi")
	home := isolatedHome(t)
	repo := v2Repo(t, home)
	os.MkdirAll(filepath.Join(home, ".config", "dsync"), 0o755)
	os.WriteFile(filepath.Join(home, ".config", "dsync", "local.toml"),
		[]byte("[repo]\npath = '"+repo+"'\n[user]\nname = 'LinYa'\nemail = 'a@b.c'\n"), 0o600)
	var out, errb bytes.Buffer
	e := Env{Getenv: os.Getenv, Runner: dexec.Real{}, RootFS: os.DirFS("/"), Out: &out, Err: &errb}

	for _, c := range []struct {
		geo, npm string
		cargo    bool
	}{
		{"official", "registry=https://registry.npmjs.org", false},
		{"cn", "registry=https://registry.npmmirror.com", true},
	} {
		if err := e.GeoSet(context.Background(), c.geo); err != nil {
			t.Fatalf("GeoSet(%s): %v\n%s%s", c.geo, err, out.String(), errb.String())
		}
		if !strings.Contains(readFile(t, filepath.Join(home, ".npmrc")), c.npm) {
			t.Errorf("%s: .npmrc = %q", c.geo, readFile(t, filepath.Join(home, ".npmrc")))
		}
		if got := strings.Contains(readFile(t, filepath.Join(home, ".cargo", "config.toml")), "sparse+https://rsproxy.cn/index/"); got != c.cargo {
			t.Errorf("%s: cargo 镜像 = %v", c.geo, got)
		}
	}
	gitconfig := readFile(t, filepath.Join(home, ".gitconfig"))
	if !strings.Contains(gitconfig, "name = LinYa") || !strings.Contains(gitconfig, "email = a@b.c") || !strings.Contains(gitconfig, "editor = nvim") {
		t.Errorf("git 身份应来自 local.toml，editor 来自 dsync.toml [data]:\n%s", gitconfig)
	}
	if _, err := os.Stat(filepath.Join(home, "README.md")); err == nil {
		t.Error(".chezmoiroot 之外的仓库文件不应被部署")
	}
	// 生成的配置自带 sourceDir：不带 --source 的 chezmoi 也能找到源目录
	b, err := exec.Command("chezmoi", "source-path").Output()
	if err != nil || strings.TrimSpace(string(b)) != filepath.Join(repo, "home") {
		t.Errorf("chezmoi source-path = %q, %v", b, err)
	}
}

func TestContract_Verify(t *testing.T) {
	needBin(t, "chezmoi")
	home := isolatedHome(t)
	repo := v2Repo(t, home)
	var out, errb bytes.Buffer
	e := Env{Getenv: os.Getenv, Runner: dexec.Real{}, RootFS: os.DirFS("/"), Out: &out, Err: &errb}
	r, err := e.Verify(context.Background(), VerifyOptions{Source: repo})
	if err != nil || len(r.Errors) != 0 || len(r.Templates) != 3 || len(r.Matrix) != 4 {
		t.Fatalf("starter 应通过: %+v %v", r, err)
	}
	os.WriteFile(filepath.Join(repo, "home", "dot_bad.tmpl"), []byte("{{ if eq .chezmoi.os \"darwin\" }}{{ .nope }}{{ end }}\n"), 0o644)
	r, _ = e.Verify(context.Background(), VerifyOptions{Source: repo})
	if len(r.Errors) != 2 || !strings.Contains(r.Errors[0], "[darwin/") || !strings.Contains(r.Errors[0], `"nope"`) {
		t.Errorf("只在 darwin 分支出错的模板应在 2 个 darwin 组合中报出: %v", r.Errors)
	}
	os.WriteFile(filepath.Join(repo, "dsync.toml"), []byte("version = 2\n"), 0o644)
	if r, _ := e.Verify(context.Background(), VerifyOptions{Source: repo}); len(r.Errors) != 1 || !strings.Contains(r.Errors[0], "version") {
		t.Errorf("非法清单应报错: %v", r.Errors)
	}
}
