package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Lin-Ya/dsync-cli/internal/exec"
	"github.com/Lin-Ya/dsync-cli/internal/workflow"
)

func run(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	home := t.TempDir()
	env := workflow.Env{
		Getenv: func(k string) string {
			if k == "HOME" {
				return home
			}
			return ""
		},
		Runner: &exec.Recording{},
		RootFS: fstest.MapFS{},
		GOOS:   "linux", // 固定平台：结果不随 CI runner 变化
		Out:    &out,
		Err:    &errb,
	}
	code = Main(context.Background(), env, args)
	return code, out.String(), errb.String()
}

func TestExitCodes(t *testing.T) {
	cases := []struct {
		args []string
		code int
		out  string
	}{
		{[]string{"version"}, 0, "dsync dev"},
		{[]string{"--version"}, 0, "dsync dev"},
		{[]string{"shell-init", "zsh"}, 0, "export DSYNC_GEO='cn'"},
		{[]string{"shell-init", "fish"}, 2, ""},
		{[]string{"shell-init"}, 2, ""},
		{[]string{"status"}, 0, "平台"},
		{[]string{"status", "--json"}, 0, `"missing_tools"`},
		{[]string{"doctor"}, 1, "[FAIL] git"}, // 伪环境里没有任何底座工具
		{[]string{"no-such-command"}, 2, ""},
		{[]string{"geo", "set", "mars"}, 2, ""},
		{[]string{"tool", "add"}, 2, ""},
		{[]string{"capture"}, 2, ""},
		{[]string{"sync"}, 2, ""}, // 伪环境缺少 git/chezmoi/mise
		{[]string{"--dry-run", "save"}, 2, ""},
		{[]string{"provision", "--check", "--json"}, 0, `"items"`},
		{[]string{"provision", "--json"}, 2, ""},
		{[]string{"--json", "status"}, 2, ""}, // --json 属于子命令，须写在其后
		{[]string{"sync", "--json"}, 2, ""},   // 不支持 --json 的命令明确报错
		{[]string{"--dry-run", "save", "--json"}, 2, ""},
		{[]string{"provision", "--check", "--only", "nix"}, 2, ""},
		{[]string{"init", "a", "b"}, 2, ""},
		{[]string{"init"}, 2, ""},                     // 伪环境没有 git
		{[]string{"self-update"}, 2, ""},              // 没有 Token
		{[]string{"host", "setup", "--check"}, 2, ""}, // 非 Windows 平台
		{[]string{"host", "setup", "--json"}, 2, ""},
	}
	for _, c := range cases {
		t.Run(strings.Join(c.args, " "), func(t *testing.T) {
			code, out, errOut := run(t, c.args...)
			if code != c.code {
				t.Errorf("退出码 = %d, want %d\nstdout: %s\nstderr: %s", code, c.code, out, errOut)
			}
			if c.out != "" && !strings.Contains(out, c.out) {
				t.Errorf("stdout 缺少 %q:\n%s", c.out, out)
			}
		})
	}
}

// 参数个数不对时附上用法；根帮助按场景分组且每个命令都有归属。
func TestUsageHints(t *testing.T) {
	if _, _, errOut := run(t, "geo", "set"); !strings.Contains(errOut, "用法: dsync geo set <cn|official>") {
		t.Errorf("缺少用法提示:\n%s", errOut)
	}
	_, out, _ := run(t, "--help")
	for _, want := range []string{"部署与更新:", "日常同步", "查看与诊断"} {
		if !strings.Contains(out, want) {
			t.Errorf("帮助缺少分组 %q:\n%s", want, out)
		}
	}
	for _, c := range New(workflow.Env{}).Commands() {
		if c.GroupID == "" {
			t.Errorf("命令 %s 未归入任何分组", c.Name())
		}
	}
}

// 不支持 --json 的命令：报未知 flag 并附用法，帮助中也不出现 --json。
func TestJSONOnlyWhereSupported(t *testing.T) {
	if _, _, errOut := run(t, "sync", "--json"); !strings.Contains(errOut, "unknown flag: --json") || !strings.Contains(errOut, "用法: dsync sync") {
		t.Errorf("应报未知 flag 并附用法:\n%s", errOut)
	}
	if _, out, _ := run(t, "sync", "--help"); strings.Contains(out, "--json") {
		t.Errorf("sync 帮助不应出现 --json:\n%s", out)
	}
	if _, out, _ := run(t, "provision", "--help"); !strings.Contains(out, "需与 --check 一起使用") {
		t.Errorf("provision 帮助应说明 --json 需配合 --check:\n%s", out)
	}
}
