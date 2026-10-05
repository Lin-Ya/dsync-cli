package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Lin-Ya/dsync-cli/internal/mirrors"
)

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func tmpPaths(t *testing.T) Paths {
	t.Helper()
	dir := t.TempDir()
	return Paths{Home: dir, ConfigDir: filepath.Join(dir, ".config", "dsync")}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestResolveGeo_Precedence(t *testing.T) {
	p := tmpPaths(t)

	g, src, err := ResolveGeo(envOf(nil), p, Local{})
	if err != nil || g != mirrors.CN || src != GeoFromDefault {
		t.Fatalf("无任何配置: %v %v %v", g, src, err)
	}

	write(t, p.V1GeoFile(), "official\n")
	g, src, _ = ResolveGeo(envOf(nil), p, Local{})
	if g != mirrors.Official || src != GeoFromV1File {
		t.Fatalf("v1 geo 文件: %v %v", g, src)
	}

	g, src, _ = ResolveGeo(envOf(nil), p, Local{Geo: "cn"})
	if g != mirrors.CN || src != GeoFromLocal {
		t.Fatalf("local.toml 应优先于 v1 文件: %v %v", g, src)
	}

	g, src, _ = ResolveGeo(envOf(map[string]string{"DSYNC_GEO": "official"}), p, Local{Geo: "cn"})
	if g != mirrors.Official || src != GeoFromEnv {
		t.Fatalf("显式环境变量应最优先: %v %v", g, src)
	}
}

// shell-init 自己导出的 DSYNC_GEO 不应覆盖 local.toml（否则 `dsync geo set` 后
// 同一会话里会读到陈旧值——v1 switch-geo 失效的同类问题）。
func TestResolveGeo_IgnoresShellInitExport(t *testing.T) {
	p := tmpPaths(t)
	env := envOf(map[string]string{"DSYNC_GEO": "cn", ShellInitMarker: "shell-init"})
	g, src, _ := ResolveGeo(env, p, Local{Geo: "official"})
	if g != mirrors.Official || src != GeoFromLocal {
		t.Fatalf("got %v %v", g, src)
	}
}

func TestResolveGeo_InvalidIsError(t *testing.T) {
	p := tmpPaths(t)
	if _, _, err := ResolveGeo(envOf(map[string]string{"DSYNC_GEO": "mars"}), p, Local{}); err == nil {
		t.Error("非法环境变量应报错而非静默回退")
	}
	write(t, p.V1GeoFile(), "bogus")
	if _, _, err := ResolveGeo(envOf(nil), p, Local{}); err == nil {
		t.Error("非法 v1 geo 文件应报错")
	}
}

func TestLoadLocal(t *testing.T) {
	p := tmpPaths(t)
	if _, exists, err := LoadLocal(p); exists || err != nil {
		t.Fatalf("不存在时: exists=%v err=%v", exists, err)
	}
	write(t, p.LocalFile(), `geo = "official"
tags = ["work"]
[user]
name = "LinYa"
[proxy]
http = "http://${WSL_HOST_IP}:7890"
`)
	l, exists, err := LoadLocal(p)
	if err != nil || !exists {
		t.Fatal(err)
	}
	if l.Geo != "official" || l.User.Name != "LinYa" || l.Proxy == nil || l.Proxy.HTTP != "http://${WSL_HOST_IP}:7890" || l.Tags[0] != "work" {
		t.Errorf("解析结果: %+v", l)
	}

	write(t, p.LocalFile(), "geo = \"cn\"\ntypo_key = 1\n")
	if _, _, err := LoadLocal(p); err == nil || !strings.Contains(err.Error(), "typo_key") {
		t.Errorf("未知字段应报错并指出字段名，得到: %v", err)
	}
	write(t, p.LocalFile(), "geo = \"mars\"\n")
	if _, _, err := LoadLocal(p); err == nil {
		t.Error("非法 geo 应报错")
	}
}

func TestDefaultPaths(t *testing.T) {
	p, err := DefaultPaths(envOf(map[string]string{"HOME": "/h"}))
	if err != nil || p.ConfigDir != "/h/.config/dsync" || p.LocalFile() != "/h/.config/dsync/local.toml" {
		t.Errorf("%+v %v", p, err)
	}
	p, _ = DefaultPaths(envOf(map[string]string{"HOME": "/h", "DSYNC_CONFIG_DIR": "/x"}))
	if p.ConfigDir != "/x" {
		t.Errorf("DSYNC_CONFIG_DIR 未生效: %+v", p)
	}
}
