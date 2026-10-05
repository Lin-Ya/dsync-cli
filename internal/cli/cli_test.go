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
