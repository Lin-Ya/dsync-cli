package exec

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestReal_ExitErrorCarriesStderr(t *testing.T) {
	_, err := Real{}.Query(context.Background(), Cmd{Name: "sh", Args: []string{"-c", "echo boom >&2; exit 3"}})
	var ee *ExitError
	if !errors.As(err, &ee) || ee.ExitCode != 3 || !strings.Contains(ee.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
}

func TestReal_NotFound(t *testing.T) {
	_, err := Real{}.Run(context.Background(), Cmd{Name: "definitely-not-a-command-xyz"})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v", err)
	}
	if _, err := (Real{}).LookPath("definitely-not-a-command-xyz"); !errors.Is(err, ErrNotFound) {
		t.Errorf("LookPath err = %v", err)
	}
}

func TestReal_DirAndEnv(t *testing.T) {
	dir := t.TempDir()
	res, err := Real{}.Query(context.Background(), Cmd{Name: "sh", Args: []string{"-c", "pwd; echo $X"}, Dir: dir, Env: []string{"X=42"}})
	if err != nil || !strings.Contains(string(res.Stdout), "42") {
		t.Fatalf("%q %v", res.Stdout, err)
	}
}

func TestReal_Unset(t *testing.T) {
	t.Setenv("DSYNC_T_GONE", "stale")
	t.Setenv("DSYNC_T_KEEP", "kept")
	res, err := Real{}.Query(context.Background(), Cmd{Name: "sh", Args: []string{"-c", `echo "${DSYNC_T_GONE-unset}|$DSYNC_T_KEEP"`}, Unset: []string{"DSYNC_T_GONE"}})
	if err != nil || strings.TrimSpace(string(res.Stdout)) != "unset|kept" {
		t.Fatalf("%q %v", res.Stdout, err)
	}
}

func TestDryRun_OnlyQueriesExecute(t *testing.T) {
	var out bytes.Buffer
	rec := (&Recording{}).OnStdout("git status", "clean")
	d := DryRun{Inner: rec, Out: &out}
	if res, err := d.Query(context.Background(), Cmd{Name: "git", Args: []string{"status"}}); err != nil || string(res.Stdout) != "clean" {
		t.Fatalf("Query 应透传: %q %v", res.Stdout, err)
	}
	if _, err := d.Run(context.Background(), Cmd{Name: "git", Args: []string{"commit", "-m", "a b"}, Dir: "/r"}); err != nil {
		t.Fatal(err)
	}
	if len(rec.Calls) != 1 {
		t.Errorf("dry-run 下 Run 不得到达内层: %v", rec.Lines())
	}
	if got := out.String(); got != "  [dry-run] (cd /r && git commit -m 'a b')\n" {
		t.Errorf("dry-run 输出 = %q", got)
	}
}

func TestCmdString_Quoting(t *testing.T) {
	c := Cmd{Name: "echo", Args: []string{"plain", "", "it's", "$HOME", "a b"}}
	if got := c.String(); got != `echo plain '' 'it'\''s' '$HOME' 'a b'` {
		t.Errorf("got %s", got)
	}
}

func TestRecording(t *testing.T) {
	r := (&Recording{}).
		OnStdout("mise", "generic").
		OnStdout("mise ls", "specific").
		On("mise install", Result{ExitCode: 1, Stderr: []byte("net")})
	ctx := context.Background()
	if res, _ := r.Query(ctx, Cmd{Name: "mise", Args: []string{"ls", "--json"}}); string(res.Stdout) != "specific" {
		t.Error("后注册的应优先匹配")
	}
	if _, err := r.Run(ctx, Cmd{Name: "mise", Args: []string{"install"}}); err == nil {
		t.Error("预设的非零退出码应返回错误")
	}
	var ee *ExitError
	if _, err := r.Query(ctx, Cmd{Name: "git"}); !errors.As(err, &ee) || ee.ExitCode != 127 {
		t.Errorf("未预设命令应返回 127: %v", err)
	}
	if got := r.Lines(); len(got) != 3 || got[1] != "mise install" || !r.Calls[1].Mutating || r.Calls[0].Mutating {
		t.Errorf("调用记录: %v %+v", got, r.Calls)
	}
}
