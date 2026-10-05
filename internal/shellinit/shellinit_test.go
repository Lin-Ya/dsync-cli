package shellinit

import (
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Lin-Ya/dsync-cli/internal/config"
	"github.com/Lin-Ya/dsync-cli/internal/mirrors"
	"github.com/Lin-Ya/dsync-cli/internal/platform"
)

var update = flag.Bool("update", false, "重新生成 golden 文件")

var (
	linux = platform.Platform{OS: "linux", Arch: "amd64", DistroID: "ubuntu", Version: "24.04"}
	wsl   = platform.Platform{OS: "linux", Arch: "amd64", WSL: true, DistroID: "ubuntu", Version: "24.04"}
	mac   = platform.Platform{OS: "darwin", Arch: "arm64"}
)

var goldenCases = map[string]Options{
	"linux-cn":             {Shell: "zsh", Geo: mirrors.CN, Platform: linux},
	"linux-official":       {Shell: "bash", Geo: mirrors.Official, Platform: linux},
	"wsl-cn-v1proxy":       {Shell: "zsh", Geo: mirrors.CN, Platform: wsl, V1ProxyFile: "/home/u/.config/dsync/proxy.env"},
	"wsl-cn-localproxy":    {Shell: "zsh", Geo: mirrors.CN, Platform: wsl, Proxy: &config.Proxy{HTTP: "http://${WSL_HOST_IP}:7890", HTTPS: "http://${WSL_HOST_IP}:7890", NoProxy: "localhost,127.0.0.1"}},
	"macos-official-proxy": {Shell: "zsh", Geo: mirrors.Official, Platform: mac, Proxy: &config.Proxy{HTTPS: "http://127.0.0.1:7890"}},
}

func TestGenerate_Golden(t *testing.T) {
	for name, o := range goldenCases {
		t.Run(name, func(t *testing.T) {
			got, err := Generate(o)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join("testdata", name+".golden")
			if *update {
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v（首次运行请加 -update）", err)
			}
			if got != string(want) {
				t.Errorf("输出与 %s 不一致（确认改动符合预期后用 -update 更新）:\n%s", path, got)
			}
		})
	}
}

// 生成的脚本在真实 shell 中必须语法正确。
func TestGenerate_Syntax(t *testing.T) {
	for _, sh := range Shells {
		bin, err := exec.LookPath(sh)
		if err != nil {
			t.Logf("跳过 %s：未安装", sh)
			continue
		}
		for name, o := range goldenCases {
			o.Shell = sh
			out, err := Generate(o)
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(bin, "-n")
			cmd.Stdin = strings.NewReader(out)
			if b, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("%s/%s 语法错误: %v\n%s", sh, name, err, b)
			}
		}
	}
}

// eval 之后环境变量符合预期（含 official 清除镜像、代理值中的特殊字符不被展开）。
func TestGenerate_Eval(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("无 bash")
	}
	out, _ := Generate(Options{Shell: "bash", Geo: mirrors.CN, Platform: linux,
		Proxy: &config.Proxy{HTTP: "http://${WSL_HOST_IP}:7890", NoProxy: "a,$(rm -rf /),`x`"}})
	env := evalEnv(t, out, map[string]string{})
	want := map[string]string{
		"DSYNC_GEO":            "cn",
		"DSYNC_GEO_SOURCE":     "shell-init",
		"MISE_NODE_MIRROR_URL": "https://npmmirror.com/mirrors/node/",
		"NPM_CONFIG_REGISTRY":  "https://registry.npmmirror.com",
		"http_proxy":           "http://127.0.0.1:7890", // 非 WSL：占位符回退回环
		"HTTP_PROXY":           "http://127.0.0.1:7890",
		"no_proxy":             "a,$(rm -rf /),`x`", // 字面量，不得被执行
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("%s = %q, want %q", k, env[k], v)
		}
	}

	off, _ := Generate(Options{Shell: "bash", Geo: mirrors.Official, Platform: linux})
	env = evalEnv(t, off, map[string]string{"PIP_INDEX_URL": "stale", "MISE_NODE_MIRROR_URL": "stale"})
	for _, k := range []string{"PIP_INDEX_URL", "MISE_NODE_MIRROR_URL"} {
		if _, ok := env[k]; ok {
			t.Errorf("official 下 %s 应被 unset", k)
		}
	}
}

func TestGenerate_UnsupportedShell(t *testing.T) {
	if _, err := Generate(Options{Shell: "fish", Geo: mirrors.CN}); err == nil {
		t.Error("应拒绝不支持的 shell")
	}
}

// evalEnv 在干净环境的 bash 中 eval 脚本后返回环境变量。
func evalEnv(t *testing.T, script string, pre map[string]string) map[string]string {
	t.Helper()
	cmd := exec.Command("bash", "-c", `eval "$DSYNC_SCRIPT" && unset DSYNC_SCRIPT && env -0`)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + t.TempDir(), "DSYNC_SCRIPT=" + script}
	for k, v := range pre {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	b, err := cmd.Output()
	if err != nil {
		t.Fatalf("eval 失败: %v", err)
	}
	return parseEnv0(b)
}

func parseEnv0(b []byte) map[string]string {
	out := map[string]string{}
	for _, kv := range strings.Split(string(b), "\x00") {
		if k, v, ok := strings.Cut(kv, "="); ok {
			out[k] = v
		}
	}
	return out
}
