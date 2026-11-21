package workflow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Lin-Ya/dsync-cli/internal/config"
	"github.com/Lin-Ya/dsync-cli/internal/gitrepo"
)

// ToolAddOptions 是 `dsync tool add` 的参数。
type ToolAddOptions struct {
	Specs []string
	Push  bool
}

// ToolAdd 新增/升级 mise 全局工具并同步回仓库（取代 v1 dsync add）。
func (e Env) ToolAdd(ctx context.Context, o ToolAddOptions) error {
	if len(o.Specs) == 0 {
		return errors.New("缺少工具 spec（例如 node@22.11.0 或 npm:typescript@5.6.2）")
	}
	return e.editTools(ctx, "use", "add", o.Specs, o.Push)
}

// ToolRemoveOptions 是 `dsync tool remove` 的参数。
type ToolRemoveOptions struct {
	Tools []string
	Push  bool
}

// ToolRemove 从 mise 全局工具清单中移除工具并同步回仓库。
// 已安装的版本不会卸载（mise prune 清理），其它机器 sync 后同样只是不再安装它。
func (e Env) ToolRemove(ctx context.Context, o ToolRemoveOptions) error {
	if len(o.Tools) == 0 {
		return errors.New("缺少工具名（例如 jq 或 npm:typescript）")
	}
	return e.editTools(ctx, "unuse", "remove", o.Tools, o.Push)
}

// editTools 是 tool add / remove 的共同流程：拉取 → mise use|unuse 编辑仓库源 → 部署 → lock → 提交。
//
// 与 v1 的区别：先改源、再部署。由 mise 直接编辑仓库中的源文件（保留注释），
// 然后 chezmoi apply 部署到 ~/.config/mise/config.toml，不再把部署产物 cp 回模板。
// 源文件是 .tmpl 时（mise 不认该扩展名），在同名临时副本上编辑后写回。
func (e Env) editTools(ctx context.Context, verb, action string, args []string, push bool) error {
	c, err := e.Load()
	if err != nil {
		return err
	}
	if err := e.require("mise", "chezmoi", "git"); err != nil {
		return err
	}
	repo, err := e.LocateRepo(ctx, c)
	if err != nil {
		return err
	}
	srcDir := filepath.Join(repo.Source, "dot_config", "mise")
	src, err := miseConfigSource(srcDir)
	if err != nil {
		return err
	}
	lockSrc, err := miseLockSource(srcDir)
	if err != nil {
		return err
	}
	git := gitrepo.Repo{R: e.Runner, Dir: repo.Root}
	if err := e.requireGitIdentity(ctx, git); err != nil { // 先于改动：避免做完全部步骤才在提交时失败
		return err
	}
	specs := strings.Join(args, " ")

	e.logf("[1/5] 同步仓库（%s）", repo.Root)
	if err := e.pullIfTracking(ctx, git); err != nil {
		return err
	}

	e.logf("[2/5] mise %s %s（编辑源文件 %s）", verb, specs, rel(repo.Root, src))
	before, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := e.miseEditSource(ctx, src, verb, args); err != nil {
		return err
	}
	if after, _ := os.ReadFile(src); !e.DryRun && verb == "unuse" && string(after) == string(before) {
		// mise unuse 对不存在的工具静默成功：此时继续只会产生空操作，直接报出
		return fmt.Errorf("%s 中没有 %s（dsync status 或 mise ls --global 查看已有工具）", rel(repo.Root, src), specs)
	}

	target := filepath.Join(c.Paths.Home, ".config", "mise", "config.toml")
	e.logf("[3/5] chezmoi apply %s", target)
	if err := e.chezmoi(repo.Source).Apply(ctx, target); err != nil {
		return err
	}

	e.logf("[4/5] mise lock --global 并写回 %s", rel(repo.Root, lockSrc))
	geo, _, err := config.ResolveGeo(e.Getenv, c.Paths, c.Local)
	if err != nil {
		return err
	}
	m, err := e.miseFor(geo)
	if err != nil {
		return err
	}
	if err := m.LockGlobal(ctx); err != nil {
		return err
	}
	if err := e.copyFile(filepath.Join(c.Paths.Home, ".config", "mise", "mise.lock"), lockSrc, 0o600); err != nil {
		return fmt.Errorf("写回 lockfile 失败: %w", err)
	}

	e.logf("[5/5] 提交")
	return e.commitAndPush(ctx, git, []string{rel(repo.Root, srcDir)}, "chore(mise): "+action+" "+specs, push)
}

// miseConfigSource 返回 mise 全局配置的源文件；拒绝重复条目与含模板变量的模板。
func miseConfigSource(dir string) (string, error) {
	plain := filepath.Join(dir, "config.toml")
	tmpl := plain + ".tmpl"
	hasPlain, hasTmpl := fileExists(plain), fileExists(tmpl)
	switch {
	case hasPlain && hasTmpl:
		return "", fmt.Errorf("源目录存在重复条目 %s 与 %s（同一目标两份源），请删除其一", plain, tmpl)
	case hasPlain:
		return plain, nil
	case hasTmpl:
		b, err := os.ReadFile(tmpl)
		if err != nil {
			return "", err
		}
		if strings.Contains(string(b), "{{") {
			return "", fmt.Errorf("%s 含模板变量，无法自动编辑（会固化渲染结果）；请手工编辑后 dsync save", tmpl)
		}
		return tmpl, nil
	}
	return "", fmt.Errorf("找不到 mise 全局配置源文件（%s 或 .tmpl）", plain)
}

// miseLockSource 返回 lockfile 的源路径（沿用已有的 private_ / 普通命名，均无则新建 private_）。
func miseLockSource(dir string) (string, error) {
	priv := filepath.Join(dir, "private_mise.lock")
	plain := filepath.Join(dir, "mise.lock")
	switch {
	case fileExists(priv) && fileExists(plain):
		return "", fmt.Errorf("源目录存在重复 lockfile 条目 %s 与 %s，请删除其一", priv, plain)
	case fileExists(plain):
		return plain, nil
	}
	return priv, nil
}

// miseEditSource 让 mise 编辑源文件（verb = use | unuse）；.tmpl 源在临时副本 config.toml 上编辑后写回。
func (e Env) miseEditSource(ctx context.Context, src, verb string, args []string) error {
	run := e.mise().Use
	if verb == "unuse" {
		run = e.mise().Unuse
	}
	if !strings.HasSuffix(src, ".tmpl") {
		return run(ctx, src, args...)
	}
	tmp, err := os.MkdirTemp("", "dsync-mise-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	work := filepath.Join(tmp, "config.toml")
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.WriteFile(work, b, 0o644); err != nil {
		return err
	}
	if err := run(ctx, work, args...); err != nil {
		return err
	}
	edited, err := os.ReadFile(work)
	if err != nil {
		return err
	}
	return e.writeFile(src, edited, 0o644)
}

// pullIfTracking 有上游时 rebase 拉取（失败即中止，避免在冲突状态上继续提交）；无上游则跳过。
func (e Env) pullIfTracking(ctx context.Context, git gitrepo.Repo) error {
	st, err := git.Status(ctx)
	if err != nil {
		return err
	}
	if st.Upstream == "" {
		e.logf("  （当前分支无上游，跳过拉取）")
		return nil
	}
	if err := git.Pull(ctx); err != nil {
		return fmt.Errorf("拉取失败，请先解决后重试: %w", err)
	}
	return nil
}

// commitAndPush 暂存 paths（空 = 全部）并提交；无改动时跳过。push 为 true 时推送到上游。
func (e Env) commitAndPush(ctx context.Context, git gitrepo.Repo, paths []string, msg string, push bool) error {
	if err := git.Add(ctx, paths...); err != nil {
		return err
	}
	staged, err := git.Staged(ctx)
	if err != nil {
		return err
	}
	if len(staged) == 0 && !e.DryRun {
		e.logf("  （没有需要提交的改动）")
	} else {
		if err := e.requireGitIdentity(ctx, git); err != nil {
			return err
		}
		if err := git.Commit(ctx, msg); err != nil {
			return fmt.Errorf("提交失败: %w", err)
		}
		if !e.DryRun {
			e.logf("  已提交: %s", msg)
		}
	}
	if !push {
		e.logf(">>> 未推送。推送: dsync save（或 git -C %s push）", git.Dir)
		return nil
	}
	if err := e.ensureOrigin(ctx, git); err != nil {
		return err
	}
	if err := pushCurrent(ctx, git); err != nil {
		return fmt.Errorf("推送失败（未配置远端 / 认证 / 网络），处理后执行 dsync save 重试: %w", err)
	}
	e.logf(">>> 已推送。其它机器执行 dsync sync 即可同步")
	return nil
}

// ensureOrigin：仓库没有 origin、而 dsync.toml 指定了 [remote].url 时按它添加
// （init --create 只建本地仓库，写好 url 后 dsync save 即可首次推送）。
func (e Env) ensureOrigin(ctx context.Context, git gitrepo.Repo) error {
	m, _, err := config.LoadManifest(git.Dir) // v1 布局没有 dsync.toml：零值，不处理
	if err != nil || m.Remote.URL == "" {
		return err
	}
	if _, err := git.RemoteURL(ctx, "origin"); err == nil {
		return nil
	}
	if err := git.AddRemote(ctx, "origin", m.Remote.URL); err != nil {
		return err
	}
	e.logf("  已按 dsync.toml [remote].url 添加远端 origin: %s", m.Remote.URL)
	return nil
}

// pushCurrent 推送当前分支：有上游时推送到上游，否则推送到 origin 并建立跟踪。
func pushCurrent(ctx context.Context, git gitrepo.Repo) error {
	st, err := git.Status(ctx)
	if err != nil {
		return err
	}
	if st.Upstream != "" {
		return git.Push(ctx, "")
	}
	return git.PushSetUpstream(ctx, "origin")
}

// errNoGitIdentity 指向 local.toml：starter 模板的 ~/.gitconfig 从其 [user] 渲染身份。
var errNoGitIdentity = errors.New("git 身份未配置，无法提交：在 ~/.config/dsync/local.toml 的 [user] 填写 name / email 后执行 dsync sync" +
	"（部署到 ~/.gitconfig），或 git config --global user.name / user.email")

// requireGitIdentity 确认仓库中可以提交（身份可用）。
func (e Env) requireGitIdentity(ctx context.Context, git gitrepo.Repo) error {
	if _, err := git.Ident(ctx); err != nil {
		return errNoGitIdentity
	}
	return nil
}

// rel 返回 p 相对 root 的路径。两边先解析符号链接：chezmoi source-path 可能返回
// 未解析的 ~/.local/share/chezmoi/…，而仓库根是解析后的真实路径。
func rel(root, p string) string {
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	if r, err := filepath.Rel(root, p); err == nil {
		return r
	}
	return p
}
