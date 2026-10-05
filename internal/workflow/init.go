package workflow

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Lin-Ya/dsync-cli/internal/config"
	"github.com/Lin-Ya/dsync-cli/internal/deps"
	"github.com/Lin-Ya/dsync-cli/internal/exec"
	"github.com/Lin-Ya/dsync-cli/internal/mirrors"
	"github.com/Lin-Ya/dsync-cli/internal/starter"
)

// InitOptions 是 `dsync init` 的参数。
type InitOptions struct {
	Repo   string // dotfiles 仓库地址（git clone）
	Source string // 已有的本地仓库目录（与 Repo 二选一）
	Geo    string // 为空时：local.toml > 环境变量 DSYNC_GEO > cn
	Create bool   // 用内嵌的 starter 模板新建 dotfiles 仓库
	GitHub string // 与 Create 合用：gh repo create <owner/name> --private 并推送
}

// DefaultCloneDir 是 dsync init 克隆 dotfiles 仓库的默认位置（相对 HOME）。
const DefaultCloneDir = ".local/share/dsync/repo"

// Init 在新机器上从零部署（取代 v1 bootstrap.sh）。每一步先检测再执行，可重复运行。
func (e Env) Init(ctx context.Context, o InitOptions) error {
	n := 0
	for _, set := range []bool{o.Repo != "", o.Source != "", o.Create} {
		if set {
			n++
		}
	}
	if n > 1 {
		return errors.New("仓库地址、--source、--create 只能三选一")
	}
	if o.GitHub != "" && !o.Create {
		return errors.New("--github 需与 --create 一起使用")
	}
	c, err := e.Load()
	if err != nil {
		return err
	}
	if c.LocalErr != nil {
		return fmt.Errorf("%w（为避免覆盖，请先修正该文件）", c.LocalErr)
	}
	geo, err := e.initGeo(o.Geo, c.Local)
	if err != nil {
		return err
	}
	bin := filepath.Join(c.Paths.Home, ".local", "bin")
	e.ensurePath(bin)

	e.logf("[1/6] 底座工具（git / mise / chezmoi）")
	if err := e.ensureTools(ctx, bin, c.Platform.OS); err != nil {
		return err
	}

	e.logf("[2/6] dotfiles 仓库")
	repo, err := e.initRepo(ctx, c, o)
	if err != nil {
		return err
	}
	e.logf("  %s（%s 布局）", repo.Root, repo.Layout)

	c.Local.Geo = string(geo)
	c.Local.Repo.Path = repo.Root
	if err := config.SaveLocal(c.Paths, c.Local, e.writeFile); err != nil {
		return err
	}
	e.logf("[3/6] 本机配置 %s（geo = %s）", c.Paths.LocalFile(), geo)

	e.logf("[4/6] chezmoi 配置 + apply")
	if repo.Layout == LayoutV1 { // v2 的 chezmoi 配置自带 sourceDir，无需软链
		if err := e.linkDefaultSource(c.Paths.Home, repo.Source); err != nil {
			return err
		}
	}
	if err := e.configureChezmoi(ctx, c, repo, geo); err != nil {
		return err
	}
	if err := e.chezmoi(repo.Source).Apply(ctx); err != nil {
		return err
	}

	e.logf("[5/6] provision（系统级步骤）")
	if err := e.Provision(ctx, nil); err != nil {
		return err
	}

	e.logf("[6/6] mise install")
	if err := e.miseInstall(ctx, c.Paths.Home); err != nil {
		return err
	}

	e.logf(">>> 初始化完成。下一步：")
	e.logf("    exec zsh                      # 进入新 shell 使配置生效")
	e.logf("    dsync status                  # 查看同步状态")
	e.logf("    atuin login                   # （可选）同步 shell 历史")
	return nil
}

// initGeo：--geo > 已有 local.toml > 环境变量 DSYNC_GEO > 默认 cn。
// local.toml 优先于环境变量：v1 的 .zshenv 会在每个 shell 导出 DSYNC_GEO，geo set 之后
// 同一终端里它仍是旧值，不能让它覆盖已记录的选择。新机器上没有 local.toml，环境变量照常生效。
func (e Env) initGeo(flag string, l config.Local) (mirrors.Geo, error) {
	for _, v := range []string{flag, l.Geo, e.Getenv("DSYNC_GEO")} {
		if v != "" {
			return mirrors.ParseGeo(v)
		}
	}
	return mirrors.Default, nil
}

// ensurePath 让本进程后续调用能找到装进 ~/.local/bin 的工具。
func (e Env) ensurePath(dir string) {
	path := os.Getenv("PATH")
	for _, p := range filepath.SplitList(path) {
		if p == dir {
			return
		}
	}
	os.Setenv("PATH", dir+string(os.PathListSeparator)+path)
}

// ensureTools：git 需要系统自带（新机器上通常已有）；mise / chezmoi 缺失时下载固定版本。
func (e Env) ensureTools(ctx context.Context, bin, goos string) error {
	if !e.has("git") {
		hint := "sudo apt-get install -y git"
		if goos == "darwin" {
			hint = "xcode-select --install"
		}
		return fmt.Errorf("未找到 git，请先安装: %s", hint)
	}
	for _, t := range []deps.Tool{deps.Mise, deps.Chezmoi} {
		if e.has(t.Name) {
			e.logf("  %s 已安装，跳过", t.Name)
			continue
		}
		a, err := t.AssetFor(goos, runtime.GOARCH)
		if err != nil {
			return err
		}
		dst := filepath.Join(bin, t.Name)
		if e.DryRun {
			fmt.Fprintf(e.Err, "  [dry-run] 下载 %s 并校验 sha256 后安装到 %s\n", a.URL, dst)
			continue
		}
		e.logf("  安装 %s %s → %s", t.Name, t.Version, dst)
		if err := deps.Install(ctx, e.httpClient(), a, dst); err != nil {
			return err
		}
	}
	return nil
}

func (e Env) httpClient() *http.Client {
	if e.HTTP != nil {
		return e.HTTP
	}
	return deps.NewHTTPClient()
}

// initRepo 确定 dotfiles 仓库：--source 目录 > 克隆仓库地址 > 已配置/可定位的仓库（重复运行时）。
func (e Env) initRepo(ctx context.Context, c Context, o InitOptions) (RepoInfo, error) {
	switch {
	case o.Create:
		return e.createRepo(ctx, c, o.GitHub)

	case o.Source != "":
		dir, err := filepath.Abs(expandHome(o.Source, c.Paths.Home))
		if err != nil {
			return RepoInfo{}, err
		}
		info, ok := e.inspect(ctx, dir)
		if !ok {
			return RepoInfo{}, fmt.Errorf("%s 不是 dotfiles 仓库（缺少 dsync.toml 或 .chezmoi.toml.tmpl）", dir)
		}
		info.Via = "--source"
		return info, nil

	case o.Repo != "":
		dest := filepath.Join(c.Paths.Home, DefaultCloneDir)
		if _, err := os.Stat(dest); err == nil {
			info, ok := e.inspect(ctx, dest)
			if !ok {
				return RepoInfo{}, fmt.Errorf("%s 已存在但不是 dotfiles 仓库，请移走后重试", dest)
			}
			e.logf("  %s 已存在，跳过克隆", dest)
			info.Via = "clone"
			return info, nil
		}
		if !e.DryRun {
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				return RepoInfo{}, err
			}
		}
		// 直连终端：私有仓库需要输入凭据（用户名 + Token）时可交互
		if _, err := e.Runner.Run(ctx, exec.Cmd{Name: "git", Args: []string{"clone", o.Repo, dest}, Interactive: true}); err != nil {
			return RepoInfo{}, fmt.Errorf("克隆失败（私有仓库需要凭据：HTTPS 地址输入用户名 + Token，或改用已配置密钥的 SSH 地址）: %w", err)
		}
		if e.DryRun {
			return RepoInfo{Root: dest, Source: dest, Layout: LayoutV1, Via: "clone"}, nil
		}
		info, ok := e.inspect(ctx, dest)
		if !ok {
			return RepoInfo{}, fmt.Errorf("%s 不是 dotfiles 仓库（缺少 dsync.toml 或 .chezmoi.toml.tmpl）", o.Repo)
		}
		info.Via = "clone"
		return info, nil
	}
	info, err := e.LocateRepo(ctx, c)
	if err != nil {
		return RepoInfo{}, errors.New("请指定 dotfiles 仓库：dsync init <仓库地址> 或 dsync init --source <目录>")
	}
	return info, nil
}

// linkDefaultSource 把 ~/.local/share/chezmoi 链接到仓库源目录（与 v1 bootstrap 一致），
// 让不带 --source 的 chezmoi 命令也能找到源。已存在则不动。
func (e Env) linkDefaultSource(home, source string) error {
	link := filepath.Join(home, ".local", "share", "chezmoi")
	if _, err := os.Lstat(link); err == nil {
		if !samePath(link, source) {
			e.logf("  （%s 已存在且不指向本仓库，保持不动；dsync 命令始终显式传 --source）", link)
		}
		return nil
	}
	if e.DryRun {
		fmt.Fprintf(e.Err, "  [dry-run] ln -s %s %s\n", source, link)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		return err
	}
	return os.Symlink(strings.TrimSuffix(source, "/"), link)
}

// createRepo 用内嵌的 starter 模板在默认位置新建 dotfiles 仓库（v2 布局）并提交；
// 给了 github 时用 gh 创建同名私有仓库并推送。
func (e Env) createRepo(ctx context.Context, c Context, github string) (RepoInfo, error) {
	dest := filepath.Join(c.Paths.Home, DefaultCloneDir)
	if github != "" && !e.has("gh") {
		return RepoInfo{}, errors.New("--github 需要 GitHub CLI（gh）：先安装并 gh auth login，或去掉 --github 只在本地创建")
	}
	if e.DryRun {
		fmt.Fprintf(e.Err, "  [dry-run] 写入 starter 模板到 %s 并 git init / commit\n", dest)
		if github != "" {
			fmt.Fprintf(e.Err, "  [dry-run] gh repo create %s --private --source %s --push\n", github, dest)
		}
		return RepoInfo{Root: dest, Source: filepath.Join(dest, "home"), Layout: LayoutV2, Via: "--create"}, nil
	}
	if err := starter.Write(dest); err != nil {
		return RepoInfo{}, fmt.Errorf("%w（已有仓库请用 dsync init 不带参数，或移走后重试）", err)
	}
	git := func(args ...string) error {
		_, err := e.Runner.Run(ctx, exec.Cmd{Name: "git", Args: args, Dir: dest})
		return err
	}
	// 新机器上通常还没有 git 身份：首次提交使用占位身份
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"add", "-A"},
		{"-c", "user.name=dsync", "-c", "user.email=dsync@localhost", "commit", "-q", "-m", "init: dsync starter"},
	} {
		if err := git(args...); err != nil {
			return RepoInfo{}, err
		}
	}
	e.logf("  已用 starter 模板创建 %s", dest)
	if github != "" {
		if _, err := e.Runner.Run(ctx, exec.Cmd{Name: "gh", Args: []string{"repo", "create", github, "--private", "--source", dest, "--push"}, Interactive: true}); err != nil {
			return RepoInfo{}, fmt.Errorf("创建 GitHub 仓库失败（本地仓库已建好，可稍后手动关联远端）: %w", err)
		}
	} else {
		e.logf("  （仅本地仓库：在 GitHub 新建空的私有仓库后执行 git -C %s remote add origin <地址> && dsync save）", dest)
	}
	info, ok := e.inspect(ctx, dest)
	if !ok {
		return RepoInfo{}, fmt.Errorf("%s 不是 dotfiles 仓库", dest)
	}
	info.Via = "--create"
	return info, nil
}
