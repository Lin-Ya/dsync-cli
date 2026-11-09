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
	"github.com/Lin-Ya/dsync-cli/internal/mirrors"
)

// Sync 拉取仓库 → chezmoi apply → provision → mise install --locked（取代 v1 dsync update）。
// 与 v1 不同：--locked 失败不再静默退化为无锁安装，而是报错提示重新生成 lockfile。
func (e Env) Sync(ctx context.Context) error {
	c, err := e.Load()
	if err != nil {
		return err
	}
	if err := e.require("git", "chezmoi", "mise"); err != nil {
		return err
	}
	repo, err := e.LocateRepo(ctx, c)
	if err != nil {
		return err
	}
	if repo.Layout == LayoutV2 {
		if _, _, err := config.LoadManifest(repo.Root); err != nil {
			return err
		}
	}

	e.logf("[1/4] 拉取仓库（%s）", repo.Root)
	if err := e.pullIfTracking(ctx, gitrepo.Repo{R: e.Runner, Dir: repo.Root}); err != nil {
		return err
	}
	// 拉取可能带来布局变化（旧机器拉到 v1 → v2 迁移提交）：重新识别后按新布局部署
	if info, ok := e.inspect(ctx, repo.Root); ok && info.Layout != repo.Layout {
		e.logf("  仓库布局 %s → %s", repo.Layout, info.Layout)
		info.Via = repo.Via
		repo = info
		if _, _, err := config.LoadManifest(repo.Root); err != nil {
			return err
		}
	}
	geo, _, err := config.ResolveGeo(e.Getenv, c.Paths, c.Local)
	if err != nil {
		return err
	}
	e.logf("[2/4] chezmoi apply")
	if repo.Layout == LayoutV2 { // 拉取后 dsync.toml [data] 可能变化：重新生成 chezmoi 配置
		if err := e.configureChezmoi(ctx, c, repo, geo); err != nil {
			return err
		}
	}
	if err := e.chezmoi(repo.Source).Apply(ctx); err != nil {
		return err
	}
	e.logf("[3/4] provision（系统级步骤）")
	if err := e.Provision(ctx, nil); err != nil {
		return err
	}
	e.logf("[4/4] mise install")
	if err := e.miseInstall(ctx, c.Paths.Home, geo); err != nil {
		return err
	}
	e.logf(">>> 同步完成")
	return nil
}

// miseInstall 安装全局配置中的工具：有 lockfile 时严格按锁定版本（失败即报错，不退化）；
// 没有 lockfile（新仓库）时普通安装并提示生成 lockfile。
// 按当前 geo 设置镜像环境（见 miseFor）。
func (e Env) miseInstall(ctx context.Context, home string, geo mirrors.Geo) error {
	m, err := e.miseFor(geo)
	if err != nil {
		return err
	}
	if fileExists(filepath.Join(home, ".config", "mise", "mise.lock")) {
		if err := m.InstallLocked(ctx); err != nil {
			return fmt.Errorf("%w\n  lockfile 与配置不一致？在任一机器上执行 mise lock --global 后 dsync capture ~/.config/mise/mise.lock", err)
		}
		return nil
	}
	e.logf("  （未找到 lockfile，按配置安装；建议执行 mise lock --global 后 dsync capture ~/.config/mise/mise.lock 固化版本）")
	return m.Install(ctx)
}

// CaptureOptions 是 `dsync capture` 的参数。
type CaptureOptions struct {
	Targets  []string
	NoCommit bool
	Push     bool
}

// Capture 把目标文件的本机改动写回仓库源文件，并提交。
//
// chezmoi 的两个坑（见 v1 sync.sh 注释）：re-add 对模板静默无效；add 会另建非模板源。
// 因此：非模板源 → chezmoi re-add；无变量的模板 → 直接写回原模板；含变量的模板 → 拒绝。
func (e Env) Capture(ctx context.Context, o CaptureOptions) error {
	if len(o.Targets) == 0 {
		return errors.New("缺少目标文件（例如 dsync capture ~/.gitconfig）")
	}
	c, err := e.Load()
	if err != nil {
		return err
	}
	if err := e.require("chezmoi", "git"); err != nil {
		return err
	}
	repo, err := e.LocateRepo(ctx, c)
	if err != nil {
		return err
	}
	cz := e.chezmoi(repo.Source)
	git := gitrepo.Repo{R: e.Runner, Dir: repo.Root}
	if !o.NoCommit {
		if err := e.requireGitIdentity(ctx, git); err != nil {
			return err
		}
	}

	var changed []string
	for _, t := range o.Targets {
		target, err := filepath.Abs(expandHome(t, c.Paths.Home))
		if err != nil {
			return err
		}
		src, err := cz.SourcePathOf(ctx, target)
		if err != nil {
			return fmt.Errorf("%s 未被 dsync 管理（新增文件请用: chezmoi add %s）: %w", target, target, err)
		}
		if strings.HasSuffix(src, ".tmpl") {
			b, err := os.ReadFile(src)
			if err != nil {
				return err
			}
			if strings.Contains(string(b), "{{") {
				return fmt.Errorf("%s 的源 %s 含模板变量，无法自动写回；请手工合并: chezmoi edit --apply %s", target, rel(repo.Root, src), target)
			}
			if err := e.copyFile(target, src, 0o644); err != nil {
				return err
			}
		} else if err := cz.ReAdd(ctx, target); err != nil {
			return err
		}
		e.logf("  %s → %s", target, rel(repo.Root, src))
		changed = append(changed, rel(repo.Root, src))
	}
	if o.NoCommit {
		return nil
	}
	return e.commitAndPush(ctx, git, changed, "chore: capture "+strings.Join(changed, " "), o.Push)
}

// SaveOptions 是 `dsync save` 的参数。
type SaveOptions struct {
	Message string
	NoPush  bool
}

// Save 提交仓库中的全部改动并推送（默认推送：只提交不推送 = 其它机器拿不到）。
// v2 布局下额外推送到 dsync.toml [remote].mirrors 列出的镜像仓库。
func (e Env) Save(ctx context.Context, o SaveOptions) error {
	c, err := e.Load()
	if err != nil {
		return err
	}
	if err := e.require("git"); err != nil {
		return err
	}
	repo, err := e.LocateRepo(ctx, c)
	if err != nil {
		return err
	}
	git := gitrepo.Repo{R: e.Runner, Dir: repo.Root}
	msg := o.Message
	if msg == "" {
		host, _ := os.Hostname()
		msg = "chore: dsync save (" + host + ")"
	}
	if err := e.commitAndPush(ctx, git, nil, msg, !o.NoPush); err != nil {
		return err
	}
	if o.NoPush || repo.Layout != LayoutV2 {
		return nil
	}
	m, _, err := config.LoadManifest(repo.Root)
	if err != nil {
		return err
	}
	for _, url := range m.Remote.Mirrors {
		if err := git.Push(ctx, url); err != nil {
			return fmt.Errorf("推送到镜像 %s 失败: %w", url, err)
		}
		e.logf("  已推送到镜像 %s", url)
	}
	return nil
}
