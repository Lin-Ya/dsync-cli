package workflow

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	dexec "github.com/Lin-Ya/dsync-cli/internal/exec"
)

// withRemote 给 toolFixture 仓库挂上一个 bare 远端并设为上游，返回远端路径。
func withRemote(t *testing.T, tf *toolFixture) string {
	t.Helper()
	bare := filepath.Join(t.TempDir(), "remote.git")
	if b, err := exec.Command("git", "init", "-q", "--bare", bare).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, b)
	}
	tf.git(t, "remote", "add", "origin", bare)
	tf.git(t, "push", "-q", "-u", "origin", "main")
	return bare
}

// ---------- capture ----------

func (tf *toolFixture) managed(t *testing.T, target, src string) {
	t.Helper()
	tf.rec.OnStdout("chezmoi --source "+tf.repo+" source-path "+target, src+"\n")
}

func TestCapture_PlainSourceUsesReAdd(t *testing.T) {
	tf := newToolFixture(t)
	target := tf.write(t, ".gitconfig", "[user]\n\tname = new\n")
	src := tf.write(t, "dsync/dot_gitconfig", "[user]\n\tname = old\n")
	tf.git(t, "add", "-A")
	tf.git(t, "commit", "-qm", "add gitconfig")
	tf.managed(t, target, src)
	tf.rec.OnFunc("chezmoi --source "+tf.repo+" re-add", func(c dexec.Cmd) dexec.Result {
		b, _ := os.ReadFile(c.Args[len(c.Args)-1])
		os.WriteFile(src, b, 0o644)
		return dexec.Result{}
	})
	if err := tf.Env().Capture(context.Background(), CaptureOptions{Targets: []string{"~/.gitconfig"}}); err != nil {
		t.Fatalf("%v\n%s", err, tf.out.String())
	}
	if !tf.called("chezmoi --source " + tf.repo + " re-add " + target) {
		t.Errorf("非模板源应走 re-add: %v", tf.rec.Lines())
	}
	if msg := tf.git(t, "log", "-1", "--format=%s"); msg != "chore: capture dot_gitconfig" {
		t.Errorf("提交信息 = %q", msg)
	}
}

func TestCapture_TemplateWithoutVarsWrittenBack(t *testing.T) {
	tf := newToolFixture(t)
	target := filepath.Join(tf.home, ".config", "mise", "config.toml")
	src := filepath.Join(tf.repo, "dot_config", "mise", "config.toml.tmpl")
	tf.write(t, ".config/mise/config.toml", baseConfig+"edited = true\n")
	tf.managed(t, target, src)
	if err := tf.Env().Capture(context.Background(), CaptureOptions{Targets: []string{target}, NoCommit: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readFile(t, src), "edited = true") {
		t.Error("无变量模板应被直接写回")
	}
	if tf.called("chezmoi --source "+tf.repo+" re-add") || tf.called("git commit") {
		t.Errorf("模板不应 re-add；NoCommit 不应提交: %v", tf.rec.Lines())
	}
}

func TestCapture_Refusals(t *testing.T) {
	tf := newToolFixture(t)
	target := tf.write(t, ".npmrc", "registry=x\n")
	src := tf.write(t, "dsync/dot_npmrc.tmpl", "{{ if eq .geo \"cn\" }}registry=y{{ end }}\n")
	tf.managed(t, target, src)
	err := tf.Env().Capture(context.Background(), CaptureOptions{Targets: []string{target}})
	if err == nil || !strings.Contains(err.Error(), "chezmoi edit --apply") {
		t.Errorf("含变量模板应拒绝并提示 chezmoi edit: %v", err)
	}
	if !strings.Contains(readFile(t, src), "{{") {
		t.Error("拒绝时不得改动源")
	}

	tf.rec.On("chezmoi --source "+tf.repo+" source-path "+filepath.Join(tf.home, ".unmanaged"), dexec.Result{ExitCode: 1, Stderr: []byte("not managed")})
	err = tf.Env().Capture(context.Background(), CaptureOptions{Targets: []string{"~/.unmanaged"}})
	if err == nil || !strings.Contains(err.Error(), "chezmoi add") {
		t.Errorf("未管理文件应提示 chezmoi add: %v", err)
	}
}

// ---------- save ----------

func TestSave_CommitsAndPushesByDefault(t *testing.T) {
	tf := newToolFixture(t)
	withRemote(t, tf)
	tf.write(t, "dsync/dot_zshrc", "alias x=y\n")
	if err := tf.Env().Save(context.Background(), SaveOptions{Message: "feat: zshrc"}); err != nil {
		t.Fatalf("%v\n%s", err, tf.out.String())
	}
	if tf.git(t, "log", "-1", "--format=%s") != "feat: zshrc" || tf.git(t, "status", "--porcelain") != "" {
		t.Error("应提交全部改动")
	}
	if tf.git(t, "rev-parse", "HEAD") != tf.git(t, "rev-parse", "origin/main") {
		t.Error("默认应推送")
	}
}

func TestSave_NoPushAndNothingToCommit(t *testing.T) {
	tf := newToolFixture(t)
	withRemote(t, tf)
	tf.write(t, "dsync/dot_zshrc", "alias x=y\n")
	if err := tf.Env().Save(context.Background(), SaveOptions{NoPush: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tf.git(t, "log", "-1", "--format=%s"), "chore: dsync save (") {
		t.Error("应使用默认提交信息")
	}
	if tf.git(t, "rev-parse", "HEAD") == tf.git(t, "rev-parse", "origin/main") {
		t.Error("--no-push 不应推送")
	}
	// 无新改动时：不产生空提交，但仍把之前未推送的提交推上去
	head := tf.git(t, "rev-parse", "HEAD")
	if err := tf.Env().Save(context.Background(), SaveOptions{}); err != nil {
		t.Fatal(err)
	}
	if tf.git(t, "rev-parse", "HEAD") != head || tf.git(t, "rev-parse", "origin/main") != head {
		t.Error("应无新提交且推送已有提交")
	}
}

// ---------- sync ----------

func TestSync_OrderAndLockedFailure(t *testing.T) {
	tf := newToolFixture(t)
	withRemote(t, tf)
	tf.rec.OnStdout("mise install --locked", "")
	tf.tools("zsh")
	tf.rec.OnStdout("getent passwd", "u:x:1000:1000::/home/u:/usr/bin/zsh\n")
	if err := tf.Env().Sync(context.Background()); err != nil {
		t.Fatalf("%v\n%s", err, tf.out.String())
	}
	var order []string
	for _, l := range tf.rec.Lines() {
		for _, p := range []string{"git pull --rebase --autostash", "chezmoi --source " + tf.repo + " apply", "getent passwd", "mise install --locked"} {
			if strings.HasPrefix(l, p) {
				order = append(order, p)
			}
		}
	}
	// getent passwd 来自 provision 的 chsh 检测：证明 provision 在 apply 之后、mise install 之前
	if strings.Join(order, " | ") != "git pull --rebase --autostash | chezmoi --source "+tf.repo+" apply | getent passwd | mise install --locked" {
		t.Errorf("执行顺序 = %v", order)
	}

	tf.rec.On("mise install --locked", dexec.Result{ExitCode: 1, Stderr: []byte("lockfile mismatch")})
	err := tf.Env().Sync(context.Background())
	if err == nil || !strings.Contains(err.Error(), "mise lock --global") {
		t.Errorf("--locked 失败应报错并提示，不得静默退化为无锁安装: %v", err)
	}
	for _, l := range tf.rec.Lines() {
		if l == "mise install" {
			t.Error("不应调用无锁 mise install")
		}
	}
}

// 旧机器拉取到 v1→v2 迁移提交后，同一次 sync 必须按 v2 处理：生成 chezmoi 配置、以 home/ 为源 apply。
func TestSync_RedetectsLayoutAfterPull(t *testing.T) {
	tf := newToolFixture(t) // v1 布局仓库
	bare := withRemote(t, tf)
	tf.tools("zsh")
	tf.rec.OnStdout("getent passwd", "u:x:1000:1000::/home/u:/usr/bin/zsh\n")
	tf.rec.OnStdout("mise install --locked", "")
	home := filepath.Join(tf.repo, "home")
	tf.rec.OnStdout("chezmoi --source "+home+" apply", "")
	// 另一台机器把迁移提交推到远端：仓库根出现 dsync.toml、.chezmoiroot 与 home/
	other := filepath.Join(t.TempDir(), "other")
	run := func(dir string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, b)
		}
	}
	run(t.TempDir(), "clone", "-q", "-b", "main", bare, other)
	os.WriteFile(filepath.Join(other, "dsync.toml"), []byte("version = 1\n"), 0o644)
	os.WriteFile(filepath.Join(other, ".chezmoiroot"), []byte("home\n"), 0o644)
	os.MkdirAll(filepath.Join(other, "home"), 0o755)
	os.WriteFile(filepath.Join(other, "home", "dot_zshenv"), []byte("# v2\n"), 0o644)
	run(other, "add", "-A")
	run(other, "-c", "user.name=o", "-c", "user.email=o@o", "commit", "-qm", "migrate to v2")
	run(other, "push", "-q")
	if err := tf.Env().Sync(context.Background()); err != nil {
		t.Fatalf("%v\n%s", err, tf.out.String())
	}
	if !tf.called("chezmoi --source " + home + " apply") {
		t.Errorf("拉取后应以 home/ 为源 apply: %v", tf.rec.Lines())
	}
	cfg := filepath.Join(tf.home, ".config", "chezmoi", "chezmoi.toml")
	if !strings.Contains(readFile(t, cfg), "sourceDir = '"+home+"'") {
		t.Error("拉取后应按 v2 布局生成 chezmoi 配置")
	}
}

// 仓库没有远端（init --create 之后）：save 按 dsync.toml [remote].url 添加 origin，推送并建立跟踪。
func TestSave_AddsOriginFromManifestAndSetsUpstream(t *testing.T) {
	tf := newToolFixture(t)
	bare := filepath.Join(t.TempDir(), "remote.git")
	if b, err := exec.Command("git", "init", "-q", "--bare", bare).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, b)
	}
	tf.write(t, "dsync/dsync.toml", "version = 1\n[remote]\nurl = '"+bare+"'\n")

	e := tf.Env()
	e.DryRun = true
	e.Runner = dexec.DryRun{Inner: tf.rec, Out: &tf.err}
	if err := e.Save(context.Background(), SaveOptions{}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(tf.git(t, "remote"), "origin") || !strings.Contains(tf.err.String(), "git push -u origin HEAD") {
		t.Errorf("dry-run 只应打印，不添加远端:\n%s", tf.err.String())
	}

	if err := tf.Env().Save(context.Background(), SaveOptions{}); err != nil {
		t.Fatalf("%v\n%s", err, tf.out.String())
	}
	if got := tf.git(t, "remote", "get-url", "origin"); got != bare {
		t.Errorf("origin = %q", got)
	}
	if tf.git(t, "rev-parse", "--abbrev-ref", "@{upstream}") != "origin/main" {
		t.Error("首次推送应建立上游跟踪")
	}
	out, _ := exec.Command("git", "-C", bare, "log", "-1", "--format=%s", "main").Output()
	if !strings.HasPrefix(strings.TrimSpace(string(out)), "chore: dsync save") {
		t.Errorf("远端应收到提交: %q", out)
	}
}

// 没有 git 身份：capture 先于写回即失败；save 在无改动时仍可推送已有提交（不需要身份）。
func TestNoGitIdentity_CaptureFailsEarlySaveStillPushes(t *testing.T) {
	tf := newToolFixture(t)
	bare := withRemote(t, tf)
	tf.git(t, "commit", "-q", "--allow-empty", "-m", "unpushed")
	tf.noGitIdentity(t)

	target := filepath.Join(tf.home, ".config", "mise", "config.toml")
	tf.write(t, ".config/mise/config.toml", baseConfig+"edited = true\n")
	tf.managed(t, target, filepath.Join(tf.repo, "dot_config", "mise", "config.toml.tmpl"))
	if err := tf.Env().Capture(context.Background(), CaptureOptions{Targets: []string{target}}); err != errNoGitIdentity {
		t.Fatalf("capture 应报 git 身份未配置: %v", err)
	}
	if tf.src(t, "config.toml.tmpl") != baseConfig {
		t.Error("身份缺失时不应写回源文件")
	}

	if err := tf.Env().Save(context.Background(), SaveOptions{}); err != nil {
		t.Fatalf("无改动时 save 不需要身份: %v", err)
	}
	out, _ := exec.Command("git", "-C", bare, "log", "-1", "--format=%s", "main").Output()
	if strings.TrimSpace(string(out)) != "unpushed" {
		t.Errorf("应推送已有提交: %q", out)
	}

	tf.write(t, "dsync/new.txt", "x\n")
	if err := tf.Env().Save(context.Background(), SaveOptions{}); err != errNoGitIdentity {
		t.Errorf("有改动时 save 应报 git 身份未配置: %v", err)
	}
}
