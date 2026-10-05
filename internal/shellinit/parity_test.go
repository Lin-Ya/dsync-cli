package shellinit

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/Lin-Ya/dsync-cli/internal/mirrors"
	"github.com/Lin-Ya/dsync-cli/internal/platform"
)

// M1 退出标准：在 v1 已部署的机器上，`eval "$(dsync shell-init zsh)"` 得到的环境
// 与 source v1 ~/.zshenv 一致。以 M0 冻结的 v1 渲染快照（cli/testdata/v1-render）
// 作为伪 HOME，两种方式各跑一遍后比对环境变量。
//
// 允许的差异（白名单）：
//   - PATH：v2 中 PATH 归还给用户 .zshenv 维护，不由 shell-init 输出；
//   - DSYNC_GEO_SOURCE：v2 新增的会话标记。
var parityIgnore = map[string]bool{
	"PATH": true, "PWD": true, "OLDPWD": true, "SHLVL": true, "_": true,
	"DSYNC_GEO_SOURCE": true,
}

func TestParityWithV1(t *testing.T) {
	sh := "zsh"
	if _, err := exec.LookPath(sh); err != nil {
		sh = "bash"
	}
	plat := platform.Detect(os.DirFS("/"))
	for _, geo := range []mirrors.Geo{mirrors.CN, mirrors.Official} {
		t.Run(string(geo), func(t *testing.T) {
			fixture := filepath.Join("..", "..", "testdata", "v1-render", string(geo))
			if _, err := os.Stat(fixture); err != nil {
				t.Skipf("缺少 v1 快照（运行 scripts/gen-v1-fixture.sh 生成）: %v", err)
			}
			home := t.TempDir()
			if err := os.CopyFS(home, os.DirFS(fixture)); err != nil {
				t.Fatal(err)
			}

			v1 := runIn(t, sh, home, `. "$HOME/.zshenv"`)

			script, err := Generate(Options{
				Shell: "zsh", Geo: geo, Platform: plat,
				V1ProxyFile: filepath.Join(home, ".config", "dsync", "proxy.env"),
			})
			if err != nil {
				t.Fatal(err)
			}
			v2 := runIn(t, sh, home, `eval "$DSYNC_SCRIPT"; unset DSYNC_SCRIPT`, "DSYNC_SCRIPT="+script)

			if diff := envDiff(v1, v2); diff != "" {
				t.Errorf("v1 与 v2 环境不一致（- v1 / + v2）:\n%s", diff)
			}
			if v2["DSYNC_GEO"] != string(geo) {
				t.Errorf("DSYNC_GEO = %q", v2["DSYNC_GEO"])
			}
		})
	}
}

func runIn(t *testing.T, sh, home, script string, extraEnv ...string) map[string]string {
	t.Helper()
	cmd := exec.Command(sh, "-c", script+"\nenv -0")
	cmd.Dir = home
	cmd.Env = append([]string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "HOME=" + home}, extraEnv...)
	b, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s 执行失败: %v", sh, err)
	}
	env := parseEnv0(b)
	delete(env, "DSYNC_SCRIPT")
	return env
}

func envDiff(a, b map[string]string) string {
	keys := map[string]bool{}
	for k := range a {
		keys[k] = true
	}
	for k := range b {
		keys[k] = true
	}
	var sorted []string
	for k := range keys {
		if !parityIgnore[k] {
			sorted = append(sorted, k)
		}
	}
	sort.Strings(sorted)
	var sb strings.Builder
	for _, k := range sorted {
		av, aok := a[k]
		bv, bok := b[k]
		if aok == bok && av == bv {
			continue
		}
		if aok {
			sb.WriteString("- " + k + "=" + av + "\n")
		}
		if bok {
			sb.WriteString("+ " + k + "=" + bv + "\n")
		}
	}
	return sb.String()
}
