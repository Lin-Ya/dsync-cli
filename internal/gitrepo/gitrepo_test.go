package gitrepo

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	dexec "github.com/Lin-Ya/dsync-cli/internal/exec"
)

func TestParseStatus(t *testing.T) {
	out := `# branch.oid 89bce79
# branch.head v2
# branch.upstream origin/v2
# branch.ab +2 -3
1 .M N... 100644 100644 100644 cc02 cc02 .chezmoiignore
2 R. N... 100644 100644 100644 aaaa bbbb R100 new name.txt	old.txt
u UU N... 100644 100644 100644 100644 a b c conflict.lock
? cli/
? x
`
	got := ParseStatus(out)
	want := Status{Branch: "v2", Upstream: "origin/v2", Ahead: 2, Behind: 3,
		Dirty: []string{".chezmoiignore", "new name.txt", "conflict.lock"}, Untracked: 2}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

func TestParseStatus_NoUpstream(t *testing.T) {
	got := ParseStatus("# branch.oid (initial)\n# branch.head main\n")
	if got.Branch != "main" || got.Upstream != "" || got.Ahead != 0 || got.Dirty == nil {
		t.Errorf("%+v", got)
	}
}

// 用真实 git 验证解析与真实输出格式一致（契约）。
func TestStatus_RealGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("无 git")
	}
	dir := t.TempDir()
	git := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, b)
		}
	}
	git("init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(dir, "a"), []byte("1"), 0o644)
	git("add", "a")
	git("commit", "-q", "-m", "init")
	os.WriteFile(filepath.Join(dir, "a"), []byte("2"), 0o644)
	os.WriteFile(filepath.Join(dir, "b"), []byte("new"), 0o644)

	r := Repo{R: dexec.Real{}, Dir: dir}
	if !r.IsRepo(context.Background()) {
		t.Fatal("应识别为 git 仓库")
	}
	s, err := r.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.Branch != "main" || !reflect.DeepEqual(s.Dirty, []string{"a"}) || s.Untracked != 1 {
		t.Errorf("%+v", s)
	}
	if (Repo{R: dexec.Real{}, Dir: t.TempDir()}).IsRepo(context.Background()) {
		t.Error("空目录不应识别为仓库")
	}
}
