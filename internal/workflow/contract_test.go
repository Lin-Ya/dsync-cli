package workflow

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	dexec "github.com/Lin-Ya/dsync-cli/internal/exec"
)

// 契约测试：调用真实 chezmoi（未安装时跳过）。
//
// 隔离要求：exec.Real 继承进程环境，因此必须用 t.Setenv 把 HOME 等指向临时目录，
// 并从源副本中剔除 run_ 脚本（否则会在测试机上执行 apt/brew 安装）。

// isolatedHome 设置隔离的进程环境并返回伪 HOME。
func isolatedHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, k := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME",
		"DSYNC_GEO", "DSYNC_GEO_SOURCE", "DSYNC_CONFIG_DIR", "CHEZMOI_SOURCE_DIR", "CHEZMOI_AGE_RECIPIENT"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	return home
}

// v1Source 复制 v1 布局的最小 fixture（e2e/fixtures/dotfiles-mini，不含 run_ 脚本）作为测试用 chezmoi 源。
func v1Source(t *testing.T) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "src")
	if err := os.CopyFS(dst, os.DirFS(filepath.Join("..", "..", "e2e", "fixtures", "dotfiles-mini"))); err != nil {
		t.Fatal(err)
	}
	return dst
}

func needBin(t *testing.T, names ...string) {
	t.Helper()
	for _, n := range names {
		if _, err := exec.LookPath(n); err != nil {
			t.Skipf("未安装 %s，跳过契约测试", n)
		}
	}
}

func mustRun(t *testing.T, env []string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), env...)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, b)
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// v1 switch-geo 缺陷的回归测试：切换后 .npmrc / cargo / geo 文件必须真正切换，且可往返。
func TestContract_GeoSetSwitchesRenderedConfig(t *testing.T) {
	needBin(t, "chezmoi")
	home := isolatedHome(t)
	src := v1Source(t)

	// 模拟 v1 bootstrap 后的状态：geo=cn 初始化并部署
	mustRun(t, []string{"DSYNC_GEO=cn"}, "chezmoi", "init", "--source", src, "--no-tty")
	mustRun(t, nil, "chezmoi", "apply", "--source", src, "--no-tty")
	if !strings.Contains(readFile(t, filepath.Join(home, ".npmrc")), "registry.npmmirror.com") {
		t.Fatal("初始部署应为 cn")
	}
	os.WriteFile(filepath.Join(home, ".config", "dsync", "local.toml"), []byte("[repo]\npath = '"+src+"'\n"), 0o600)

	var out, errb bytes.Buffer
	e := Env{Getenv: os.Getenv, Runner: dexec.Real{}, RootFS: os.DirFS("/"), Out: &out, Err: &errb}

	check := func(geo, npm string, cargoMirror bool) {
		t.Helper()
		if err := e.GeoSet(context.Background(), geo); err != nil {
			t.Fatalf("GeoSet(%s): %v\n%s%s", geo, err, out.String(), errb.String())
		}
		if got := strings.TrimSpace(readFile(t, filepath.Join(home, ".config", "dsync", "geo"))); got != geo {
			t.Errorf("geo 文件 = %q, want %q", got, geo)
		}
		if !strings.Contains(readFile(t, filepath.Join(home, ".npmrc")), npm) {
			t.Errorf(".npmrc 未切换到 %s", npm)
		}
		if got := strings.Contains(readFile(t, filepath.Join(home, ".cargo", "config.toml")), "[source.rsproxy"); got != cargoMirror {
			t.Errorf("cargo rsproxy = %v, want %v", got, cargoMirror)
		}
		if !strings.Contains(readFile(t, filepath.Join(home, ".config", "dsync", "local.toml")), "geo = '"+geo+"'") {
			t.Errorf("local.toml 未记录 geo:\n%s", readFile(t, filepath.Join(home, ".config", "dsync", "local.toml")))
		}
	}
	check("official", "registry.npmjs.org", false)
	check("cn", "registry.npmmirror.com", true)
}
