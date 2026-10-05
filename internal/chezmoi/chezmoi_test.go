package chezmoi

import (
	"context"
	"reflect"
	"testing"

	"github.com/Lin-Ya/dsync-cli/internal/exec"
)

func TestParseStatus(t *testing.T) {
	out := " M .zshenv\nMM .config/dsync/geo\n R .config/dsync/run_once.sh\nA  new file\n\n"
	want := []StatusEntry{
		{"", "M", ".zshenv"},
		{"M", "M", ".config/dsync/geo"},
		{"", "R", ".config/dsync/run_once.sh"},
		{"A", "", "new file"},
	}
	if got := ParseStatus(out); !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v", got)
	}
}

func TestClient_PassesSourceExplicitly(t *testing.T) {
	r := (&exec.Recording{}).
		OnStdout("chezmoi --source /repo source-path", "/repo\n").
		OnStdout("chezmoi --source /repo status --no-tty --exclude scripts", " M .npmrc\n")
	c := Client{R: r, Source: "/repo"}
	ctx := context.Background()
	if p, err := c.SourcePath(ctx); err != nil || p != "/repo" {
		t.Fatalf("SourcePath = %q, %v", p, err)
	}
	st, err := c.Status(ctx, "scripts")
	if err != nil || len(st) != 1 || st[0].Path != ".npmrc" {
		t.Fatalf("Status = %+v, %v", st, err)
	}
	for _, call := range r.Calls {
		if call.Mutating {
			t.Errorf("只读操作不应走 Run: %s", call.Cmd)
		}
	}
}

func TestClient_ErrorPropagates(t *testing.T) {
	r := (&exec.Recording{}).On("chezmoi source-path", exec.Result{ExitCode: 1, Stderr: []byte("no source")})
	if _, err := (Client{R: r}).SourcePath(context.Background()); err == nil {
		t.Error("非零退出码必须向上传播，不得吞掉")
	}
}
