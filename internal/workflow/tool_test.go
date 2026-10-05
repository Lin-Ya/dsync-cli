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

// 移植 v1 tests/test_dsync_add.sh：mise / chezmoi 用 fake（记录调用并模拟写文件），
// git 用真命令断言仓库 diff / commit / push。全程零外网。

const baseConfig = `# mise 全局配置（测试基线，注释必须被保留）
[tools]
node = "22.11.0"
`

type toolFixture struct {
	*fixture
	repo string // dotfiles 仓库（v1 布局）
}

func newToolFixture(t *testing.T) *toolFixture {
	t.Helper()
	needBin(t, "git")
	f := newFixture(t)
	t.Setenv("HOME", f.home) // 委托给真实 git 的子进程继承进程环境
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	tf := &toolFixture{fixture: f, repo: filepath.Join(f.home, "dsync")}

	f.write(t, "dsync/.chezmoi.toml.tmpl", "")
	f.write(t, "dsync/dot_config/mise/config.toml.tmpl", baseConfig)
	f.write(t, "dsync/dot_config/mise/private_mise.lock", "lock-v0\n")
	f.write(t, ".config/mise/config.toml", baseConfig)
	f.write(t, ".config/mise/mise.lock", "lock-v0\n")
	f.write(t, ".config/dsync/local.toml", "[repo]\npath = '"+tf.repo+"'\n")
	tf.repo = realpath(t, tf.repo) // 仓库定位会解析符号链接（如 macOS /var → /private/var）
	tf.git(t, "init", "-q", "-b", "main")
	tf.git(t, "config", "user.email", "dsync@test.local")
	tf.git(t, "config", "user.name", "dsync test")
	tf.git(t, "add", "-A")
	tf.git(t, "commit", "-qm", "base")

	f.tools("git", "mise", "chezmoi")
	f.rec.Delegate = map[string]dexec.Runner{"git": dexec.Real{}}
	f.rec.OnStdout("chezmoi source-path", tf.repo+"\n")
	// fake mise use --path <file> <spec>...：每个 spec 追加一行（已存在则不重复，模拟幂等）
	f.rec.OnFunc("mise use --path", func(c dexec.Cmd) dexec.Result {
		path := c.Args[2]
		if !strings.Contains(strings.Join(c.Env, " "), "MISE_TRUSTED_CONFIG_PATHS="+path) {
			return dexec.Result{ExitCode: 1, Stderr: []byte("config not trusted")}
		}
		b, _ := os.ReadFile(path)
		for _, spec := range c.Args[3:] {
			name, ver, _ := strings.Cut(spec, "@")
			line := `"` + name + `" = "` + ver + `"` + "\n"
			if !strings.Contains(string(b), line) {
				b = append(b, line...)
			}
		}
		os.WriteFile(path, b, 0o644)
		return dexec.Result{}
	})
	// fake chezmoi apply <target>：把源模板（无变量）原样部署到目标
	f.rec.OnFunc("chezmoi --source "+tf.repo+" apply", func(c dexec.Cmd) dexec.Result {
		b, _ := os.ReadFile(filepath.Join(tf.repo, "dot_config", "mise", "config.toml.tmpl"))
		os.WriteFile(filepath.Join(f.home, ".config", "mise", "config.toml"), b, 0o644)
		return dexec.Result{}
	})
	f.rec.OnFunc("mise lock --global", func(dexec.Cmd) dexec.Result {
		os.WriteFile(filepath.Join(f.home, ".config", "mise", "mise.lock"), []byte("lock-v1\n"), 0o644)
		return dexec.Result{}
	})
	return tf
}

func (tf *toolFixture) git(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = tf.repo
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, b)
	}
	return strings.TrimSpace(string(b))
}

func (tf *toolFixture) src(t *testing.T, name string) string {
	return readFile(t, filepath.Join(tf.repo, "dot_config", "mise", name))
}

func (tf *toolFixture) called(prefix string) bool {
	for _, l := range tf.rec.Lines() {
		if strings.HasPrefix(l, prefix) {
			return true
		}
	}
	return false
}

// 场景 A：默认行为——源被编辑（注释保留）、部署、lock 写回 private_ 源、提交但不推送。
func TestToolAdd_A_DefaultCommitsWithoutPush(t *testing.T) {
	tf := newToolFixture(t)
	if err := tf.Env().ToolAdd(context.Background(), ToolAddOptions{Specs: []string{"npm:shellcheck@0.10.0"}}); err != nil {
		t.Fatalf("%v\n%s", err, tf.out.String())
	}
	src := tf.src(t, "config.toml.tmpl")
	if !strings.Contains(src, `"npm:shellcheck" = "0.10.0"`) || !strings.HasPrefix(src, "# mise 全局配置（测试基线") {
		t.Errorf("源模板应新增条目且保留注释:\n%s", src)
	}
	if got := readFile(t, filepath.Join(tf.home, ".config", "mise", "config.toml")); got != src {
		t.Errorf("部署产物应与源一致")
	}
	if tf.src(t, "private_mise.lock") != "lock-v1\n" {
		t.Error("lockfile 应写回已有的 private_mise.lock")
	}
	if fileExists(filepath.Join(tf.repo, "dot_config", "mise", "config.toml")) || fileExists(filepath.Join(tf.repo, "dot_config", "mise", "mise.lock")) {
		t.Error("不得另建非模板/非 private 源（同一目标两份源）")
	}
	if msg := tf.git(t, "log", "-1", "--format=%s"); msg != "chore(mise): add npm:shellcheck@0.10.0" {
		t.Errorf("提交信息 = %q", msg)
	}
	if tf.git(t, "status", "--porcelain") != "" {
		t.Error("提交后工作区应干净")
	}
	if tf.called("git push") {
		t.Error("默认不应推送")
	}
}

// 场景 B：--dry-run 不产生任何写操作。
func TestToolAdd_B_DryRunWritesNothing(t *testing.T) {
	tf := newToolFixture(t)
	head := tf.git(t, "rev-parse", "HEAD")
	e := tf.Env()
	e.DryRun = true
	e.Runner = dexec.DryRun{Inner: tf.rec, Out: &tf.err}
	if err := e.ToolAdd(context.Background(), ToolAddOptions{Specs: []string{"node@24"}, Push: true}); err != nil {
		t.Fatal(err)
	}
	if tf.src(t, "config.toml.tmpl") != baseConfig || tf.src(t, "private_mise.lock") != "lock-v0\n" {
		t.Error("dry-run 不得修改源文件")
	}
	if tf.git(t, "rev-parse", "HEAD") != head || tf.git(t, "status", "--porcelain") != "" {
		t.Error("dry-run 不得提交或留下改动")
	}
	for _, want := range []string{"[dry-run] mise use --path", "[dry-run] (cd " + tf.repo + " && git commit", "[dry-run] (cd " + tf.repo + " && git push"} {
		if !strings.Contains(tf.err.String(), want) {
			t.Errorf("dry-run 输出缺少 %q:\n%s", want, tf.err.String())
		}
	}
}

// 场景 C：--push 推送到远程。
func TestToolAdd_C_PushToRemote(t *testing.T) {
	tf := newToolFixture(t)
	bare := filepath.Join(t.TempDir(), "remote.git")
	if b, err := exec.Command("git", "init", "-q", "--bare", bare).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, b)
	}
	tf.git(t, "remote", "add", "origin", bare)
	tf.git(t, "push", "-q", "-u", "origin", "main")
	if err := tf.Env().ToolAdd(context.Background(), ToolAddOptions{Specs: []string{"fzf@0.57.0"}, Push: true}); err != nil {
		t.Fatalf("%v\n%s", err, tf.out.String())
	}
	if tf.git(t, "rev-parse", "HEAD") != tf.git(t, "rev-parse", "origin/main") {
		t.Error("推送后远端应与本地一致")
	}
	if !tf.called("git pull --rebase --autostash") {
		t.Error("有上游时应先拉取")
	}
}

// 场景 D：参数与环境错误。
func TestToolAdd_D_Errors(t *testing.T) {
	tf := newToolFixture(t)
	ctx := context.Background()
	if err := tf.Env().ToolAdd(ctx, ToolAddOptions{}); err == nil {
		t.Error("缺少 spec 应报错")
	}
	delete(tf.rec.Paths, "mise")
	if err := tf.Env().ToolAdd(ctx, ToolAddOptions{Specs: []string{"node@24"}}); err == nil || !strings.Contains(err.Error(), "mise") {
		t.Errorf("缺少 mise 应报错: %v", err)
	}
	tf.tools("mise")
	os.Remove(filepath.Join(tf.repo, "dot_config", "mise", "config.toml.tmpl"))
	if err := tf.Env().ToolAdd(ctx, ToolAddOptions{Specs: []string{"node@24"}}); err == nil || !strings.Contains(err.Error(), "找不到 mise 全局配置源文件") {
		t.Errorf("缺少源文件应报错: %v", err)
	}
}

// 场景 E：源模板含变量时拒绝（防止固化渲染结果）。
func TestToolAdd_E_RefusesTemplateWithVariables(t *testing.T) {
	tf := newToolFixture(t)
	tf.write(t, "dsync/dot_config/mise/config.toml.tmpl", baseConfig+"{{ if eq .geo \"cn\" }}x = 1{{ end }}\n")
	err := tf.Env().ToolAdd(context.Background(), ToolAddOptions{Specs: []string{"node@24"}})
	if err == nil || !strings.Contains(err.Error(), "含模板变量") {
		t.Fatalf("err = %v", err)
	}
	if tf.called("mise") {
		t.Error("拒绝时不得调用 mise")
	}
}

// 场景 F：源目录已有重复条目时拒绝（防脏数据扩散）。
func TestToolAdd_F_RefusesDuplicateSources(t *testing.T) {
	tf := newToolFixture(t)
	tf.write(t, "dsync/dot_config/mise/config.toml", baseConfig)
	if err := tf.Env().ToolAdd(context.Background(), ToolAddOptions{Specs: []string{"node@24"}}); err == nil || !strings.Contains(err.Error(), "重复条目") {
		t.Errorf("重复配置源应拒绝: %v", err)
	}
	os.Remove(filepath.Join(tf.repo, "dot_config", "mise", "config.toml"))
	tf.write(t, "dsync/dot_config/mise/mise.lock", "x")
	if err := tf.Env().ToolAdd(context.Background(), ToolAddOptions{Specs: []string{"node@24"}}); err == nil || !strings.Contains(err.Error(), "重复 lockfile") {
		t.Errorf("重复 lockfile 源应拒绝: %v", err)
	}
	if tf.called("mise") {
		t.Error("拒绝时不得调用 mise")
	}
}

// 新增场景 G：拉取失败时中止（v1 会继续在可能冲突的状态上提交）。
func TestToolAdd_G_AbortsWhenPullFails(t *testing.T) {
	tf := newToolFixture(t)
	tf.git(t, "remote", "add", "origin", filepath.Join(t.TempDir(), "missing.git"))
	tf.git(t, "config", "branch.main.remote", "origin")
	tf.git(t, "config", "branch.main.merge", "refs/heads/main")
	tf.git(t, "update-ref", "refs/remotes/origin/main", "HEAD")
	err := tf.Env().ToolAdd(context.Background(), ToolAddOptions{Specs: []string{"node@24"}})
	if err == nil || !strings.Contains(err.Error(), "拉取失败") {
		t.Fatalf("err = %v", err)
	}
	if tf.called("mise") {
		t.Error("拉取失败后不得继续")
	}
}

// 新增场景 H：重复执行同一 spec 无改动时不产生空提交。
func TestToolAdd_H_IdempotentNoEmptyCommit(t *testing.T) {
	tf := newToolFixture(t)
	opts := ToolAddOptions{Specs: []string{"fzf@0.57.0"}}
	if err := tf.Env().ToolAdd(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	head := tf.git(t, "rev-parse", "HEAD")
	if err := tf.Env().ToolAdd(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if tf.git(t, "rev-parse", "HEAD") != head {
		t.Error("无改动时不应产生新提交")
	}
	if !strings.Contains(tf.out.String(), "没有需要提交的改动") {
		t.Errorf("应提示无改动:\n%s", tf.out.String())
	}
}

// 非模板源（v2 布局目标形态）：mise 直接编辑源文件，不经临时副本。
func TestToolAdd_PlainSourceEditedInPlace(t *testing.T) {
	tf := newToolFixture(t)
	tmpl := filepath.Join(tf.repo, "dot_config", "mise", "config.toml.tmpl")
	plain := strings.TrimSuffix(tmpl, ".tmpl")
	tf.git(t, "mv", tmpl, plain)
	tf.git(t, "commit", "-qm", "rename")
	tf.rec.OnFunc("chezmoi --source "+tf.repo+" apply", func(dexec.Cmd) dexec.Result { return dexec.Result{} })
	if err := tf.Env().ToolAdd(context.Background(), ToolAddOptions{Specs: []string{"fzf@0.57.0"}}); err != nil {
		t.Fatal(err)
	}
	if !tf.called("mise use --path " + plain) {
		t.Errorf("应直接编辑源文件: %v", tf.rec.Lines())
	}
	if !strings.Contains(tf.src(t, "config.toml"), `"fzf" = "0.57.0"`) {
		t.Error("源文件未更新")
	}
}

// 场景 I：新仓库还没有 lockfile 时，--dry-run 不应因读不到尚未生成的 mise.lock 而失败。
func TestToolAdd_I_DryRunWithoutLockfile(t *testing.T) {
	tf := newToolFixture(t)
	os.Remove(filepath.Join(tf.home, ".config", "mise", "mise.lock"))
	os.Remove(filepath.Join(tf.repo, "dot_config", "mise", "private_mise.lock"))
	e := tf.Env()
	e.DryRun = true
	e.Runner = dexec.DryRun{Inner: tf.rec, Out: &tf.err}
	if err := e.ToolAdd(context.Background(), ToolAddOptions{Specs: []string{"node@24"}}); err != nil {
		t.Fatal(err)
	}
	if fileExists(filepath.Join(tf.repo, "dot_config", "mise", "private_mise.lock")) {
		t.Error("dry-run 不得写入 lockfile")
	}
}

// noGitIdentity 去掉 fixture 仓库的 git 身份（环境变量也清掉，避免继承开发机设置），
// 并禁止 git 自动推断：macOS 上 git 会从账户全名与主机名推断出可用身份（CI runner 即如此）。
func (tf *toolFixture) noGitIdentity(t *testing.T) {
	t.Helper()
	for _, k := range []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL", "EMAIL"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	tf.git(t, "config", "--unset", "user.name")
	tf.git(t, "config", "--unset", "user.email")
	tf.git(t, "config", "user.useConfigOnly", "true")
}

// 场景 J：没有 git 身份时在改动任何文件之前失败，而不是做完全部步骤后提交失败。
func TestToolAdd_J_NoIdentityFailsBeforeChanges(t *testing.T) {
	tf := newToolFixture(t)
	tf.noGitIdentity(t)
	err := tf.Env().ToolAdd(context.Background(), ToolAddOptions{Specs: []string{"node@24"}})
	if err != errNoGitIdentity {
		t.Fatalf("应报 git 身份未配置: %v", err)
	}
	if tf.called("mise use") || tf.src(t, "config.toml.tmpl") != baseConfig {
		t.Errorf("不应改动源文件: %v", tf.rec.Lines())
	}
}

// fakeUnuse：mise unuse --no-prune --path <file> <tool>...，删除对应行（不存在时不改文件，与真实 mise 一致）。
func (tf *toolFixture) fakeUnuse() {
	tf.rec.OnFunc("mise unuse --no-prune --path", func(c dexec.Cmd) dexec.Result {
		path := c.Args[3]
		b, _ := os.ReadFile(path)
		var keep []string
		for _, line := range strings.SplitAfter(string(b), "\n") {
			drop := false
			for _, name := range c.Args[4:] {
				drop = drop || strings.HasPrefix(line, name+" =") || strings.HasPrefix(line, `"`+name+`" =`)
			}
			if !drop {
				keep = append(keep, line)
			}
		}
		os.WriteFile(path, []byte(strings.Join(keep, "")), 0o644)
		return dexec.Result{}
	})
}

// 场景 K：tool remove 与 add 同一流程——编辑源（注释保留）、部署、lock 写回、提交。
func TestToolRemove_EditsSourceLocksAndCommits(t *testing.T) {
	tf := newToolFixture(t)
	tf.fakeUnuse()
	if err := tf.Env().ToolRemove(context.Background(), ToolRemoveOptions{Tools: []string{"node"}}); err != nil {
		t.Fatalf("%v\n%s", err, tf.out.String())
	}
	src := tf.src(t, "config.toml.tmpl")
	if strings.Contains(src, "node") || !strings.Contains(src, "# mise 全局配置") {
		t.Errorf("应删除 node 并保留注释:\n%s", src)
	}
	if tf.src(t, "private_mise.lock") != "lock-v1\n" {
		t.Error("lockfile 应重新生成并写回")
	}
	if msg := tf.git(t, "log", "-1", "--format=%s"); msg != "chore(mise): remove node" {
		t.Errorf("提交信息 = %q", msg)
	}
}

// 场景 L：移除清单中没有的工具——mise 静默成功，dsync 应报错且不提交；dry-run 不改任何东西。
func TestToolRemove_UnknownToolAndDryRun(t *testing.T) {
	tf := newToolFixture(t)
	tf.fakeUnuse()
	head := tf.git(t, "rev-parse", "HEAD")
	err := tf.Env().ToolRemove(context.Background(), ToolRemoveOptions{Tools: []string{"jq"}})
	if err == nil || !strings.Contains(err.Error(), "中没有 jq") {
		t.Errorf("应报工具不存在: %v", err)
	}
	if tf.git(t, "rev-parse", "HEAD") != head || tf.called("mise lock") {
		t.Error("工具不存在时不应继续部署 / 提交")
	}

	e := tf.Env()
	e.DryRun = true
	e.Runner = dexec.DryRun{Inner: tf.rec, Out: &tf.err}
	if err := e.ToolRemove(context.Background(), ToolRemoveOptions{Tools: []string{"node"}}); err != nil {
		t.Fatal(err)
	}
	if tf.src(t, "config.toml.tmpl") != baseConfig || tf.git(t, "rev-parse", "HEAD") != head {
		t.Error("dry-run 不得修改源文件或提交")
	}
	if !strings.Contains(tf.err.String(), "[dry-run] mise unuse --no-prune --path") {
		t.Errorf("dry-run 应打印 mise unuse:\n%s", tf.err.String())
	}
}
