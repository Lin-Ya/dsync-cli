package workflow

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Lin-Ya/dsync-cli/internal/config"
	"github.com/Lin-Ya/dsync-cli/internal/deps"
	"github.com/Lin-Ya/dsync-cli/internal/host"
	"github.com/Lin-Ya/dsync-cli/internal/mirrors"
	"github.com/Lin-Ya/dsync-cli/internal/release"
)

// HostOptions 是 `dsync host setup` 的参数。
type HostOptions struct {
	Repo          string // 传给 WSL 内 dsync init 的仓库地址
	Source        string // Windows 侧本地仓库目录（拷贝进 WSL）
	Distro        string
	InstallDir    string
	InstallDirSet bool // 显式给了 --wsl-install-dir（含空值 = 使用系统默认位置）
	WSLUser       string
	Geo           string
	LinuxBinary   string // linux 版 dsync 的本地路径；为空则从 Release 下载
	Proxy         string // 仅用于 WSL 内本次 dsync init 的代理，可含 ${WSL_HOST_IP}
	Version       string // 当前 dsync 版本：下载同版本的 linux 二进制（dev 构建取最新）
}

// HostReport 是 `dsync host setup --check --json` 的结果。
type HostReport struct {
	Items []host.Item `json:"items"`
}

// DefaultDistro 是默认的 WSL 发行版。
const DefaultDistro = "Ubuntu-24.04"

// defaultInstallDir：环境变量 WSL_INSTALL_DIR > D:\Linux（D 盘存在时，与 v1 默认一致）> 系统默认位置。
func defaultInstallDir(getenv func(string) string, exists func(string) bool) string {
	if d := getenv("WSL_INSTALL_DIR"); d != "" {
		return d
	}
	if exists(`D:\`) {
		return `D:\Linux`
	}
	return ""
}

func (e Env) hostContext(o HostOptions) (*host.Context, error) {
	if e.platform().OS != "windows" {
		return nil, errors.New("host setup 只用于 Windows 宿主；macOS / Linux 上请直接运行 dsync init")
	}
	x := &host.Context{
		R: e.Runner, Getenv: e.Getenv, DryRun: e.DryRun,
		Distro: o.Distro, InstallDir: o.InstallDir, WSLUser: o.WSLUser,
		Repo: o.Repo, Source: o.Source, Proxy: o.Proxy,
		Log:       e.logf,
		Timestamp: func() string { return time.Now().Format("200601021504") },
	}
	if x.Distro == "" {
		x.Distro = DefaultDistro
	}
	if !o.InstallDirSet {
		x.InstallDir = defaultInstallDir(e.Getenv, fileExistsAny)
	}
	if x.WSLUser == "" {
		x.WSLUser = host.SanitizeUser(e.Getenv("USERNAME"))
	}
	geo := o.Geo
	if geo == "" {
		geo = e.Getenv("DSYNC_GEO")
	}
	if geo == "" {
		geo = string(mirrors.Default)
	}
	g, err := mirrors.ParseGeo(geo)
	if err != nil {
		return nil, err
	}
	x.Geo = g

	if x.Repo != "" && x.Source != "" {
		return nil, errors.New("仓库地址与 --source 只能二选一")
	}
	if x.Repo == "" && x.Source == "" {
		// 与 v1 一致：在本地仓库目录内运行时默认使用它
		if wd, err := os.Getwd(); err == nil && isDotfilesRepo(wd) {
			x.Source = wd
		}
	}
	if x.Source != "" {
		abs, err := filepath.Abs(x.Source)
		if err != nil {
			return nil, err
		}
		if !isDotfilesRepo(abs) {
			return nil, fmt.Errorf("%s 不是 dotfiles 仓库（缺少 dsync.toml 或 .chezmoi.toml.tmpl）", abs)
		}
		x.Source = abs
	}
	x.LinuxBinary = func(ctx context.Context) (string, error) { return e.linuxBinary(ctx, o) }
	return x, nil
}

func isDotfilesRepo(dir string) bool {
	return fileExists(filepath.Join(dir, config.ManifestFile)) || fileExists(filepath.Join(dir, ".chezmoi.toml.tmpl"))
}

func fileExistsAny(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// linuxBinary 返回 Windows 文件系统上 linux/amd64 版 dsync 的路径：
// 用户指定的文件，或从私有 Release 下载（需要 Token）到临时目录。
// Windows 端只发布 amd64，WSL 与宿主同架构。
func (e Env) linuxBinary(ctx context.Context, o HostOptions) (string, error) {
	if o.LinuxBinary != "" {
		abs, err := filepath.Abs(o.LinuxBinary)
		if err != nil {
			return "", err
		}
		if !fileExists(abs) {
			return "", fmt.Errorf("--linux-binary 指定的文件不存在: %s", abs)
		}
		return abs, nil
	}
	name := release.AssetName("linux", "amd64")
	if e.DryRun {
		p := filepath.Join(os.TempDir(), "dsync", name)
		fmt.Fprintf(e.Err, "  [dry-run] 从 Release 下载 %s（校验 sha256）到 %s\n", name, p)
		return p, nil
	}
	c, err := e.releaseClient(ctx)
	if err != nil {
		return "", fmt.Errorf("%w\n  或用 --linux-binary 指定本地文件", err)
	}
	tag := o.Version
	if tag == "dev" {
		tag = "" // 本地构建：取最新 Release
	}
	r, err := c.Get(ctx, tag)
	if err != nil {
		return "", err
	}
	data, err := c.Binary(ctx, r, "linux", "amd64")
	if err != nil {
		return "", err
	}
	p := filepath.Join(os.TempDir(), "dsync-"+r.TagName, name)
	if err := deps.WriteExecutable(p, data); err != nil {
		return "", err
	}
	e.logf("  已下载 %s %s → %s", name, r.TagName, p)
	return p, nil
}

// HostCheck 只检测宿主状态，不执行。
func (e Env) HostCheck(ctx context.Context, o HostOptions) (HostReport, error) {
	x, err := e.hostContext(o)
	if err != nil {
		return HostReport{}, err
	}
	items, err := host.Plan(ctx, x)
	return HostReport{Items: items}, err
}

// HostSetup 初始化 Windows 宿主并在 WSL 内执行 dsync init（取代 v1 bootstrap.ps1）。
func (e Env) HostSetup(ctx context.Context, o HostOptions) error {
	x, err := e.hostContext(o)
	if err != nil {
		return err
	}
	where := "系统默认位置"
	if x.InstallDir != "" {
		where = x.InstallDir
	}
	e.logf("发行版 %s（安装到 %s，用户 %s）| 镜像源 %s", x.Distro, where, x.WSLUser, x.Geo)
	items, err := host.Plan(ctx, x)
	if err != nil {
		return err
	}
	WriteHostPlan(e.Out, items)

	if need := host.NeedsAdmin(items); len(need) > 0 {
		admin, err := host.IsAdmin(ctx, x)
		switch {
		case err != nil:
			e.logf("  （无法判断是否为管理员: %v）", err)
		case !admin && e.DryRun:
			e.logf("  （注意：%s 需要管理员权限，当前不是管理员）", strings.Join(need, " / "))
		case !admin:
			return fmt.Errorf("%s 需要管理员权限：请右键「以管理员身份运行」PowerShell / Windows Terminal 后重试", strings.Join(need, " / "))
		}
	}

	out, err := host.Apply(ctx, x, items)
	for _, w := range out.Warnings {
		fmt.Fprintf(e.Out, "[WARN] %s\n", w)
	}
	if err != nil {
		return err
	}
	if out.Rerun != "" {
		e.logf(">>> 已暂停：%s", out.Rerun)
		return nil
	}
	if e.DryRun {
		e.logf(">>> dry-run 结束，未做任何改动。")
		return nil
	}
	e.logf(">>> Windows 侧初始化完成。打开 Windows Terminal 中的 %s 标签开始使用。", x.Distro)
	return nil
}

// WriteHostPlan 以人类可读格式输出宿主计划。
func WriteHostPlan(w io.Writer, items []host.Item) {
	for _, it := range items {
		admin := ""
		if it.Admin {
			admin = "（需管理员）"
		}
		fmt.Fprintf(w, "  %s %-18s %s%s\n", stateLabel[it.State], it.ID, it.Detail, admin)
	}
}
