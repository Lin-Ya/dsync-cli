// Package host 实现 Windows 宿主侧初始化（取代 v1 的 bootstrap.ps1）。
//
// 与 provision 相同的模型：每个步骤先 Check 再只对待办项 Apply，可重复运行。
// 所有 Windows 操作都通过子进程完成（wsl.exe / winget / powershell.exe / DockerCli.exe），
// 路径取自环境变量（LOCALAPPDATA / APPDATA / ProgramFiles / USERPROFILE），因此本包不需要
// build tag，在 Linux 上也能完整测试；CLI 只在 Windows 上开放该命令。
//
// 约定：不解析任何本地化文本（wsl.exe 的提示随系统语言变化），只依赖退出码、
// 发行版名、PowerShell 枚举值等稳定输出。
package host

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/Lin-Ya/dsync-cli/internal/exec"
	"github.com/Lin-Ya/dsync-cli/internal/mirrors"
	"github.com/Lin-Ya/dsync-cli/internal/provision"
)

// Context 是宿主步骤所需的全部输入。
type Context struct {
	R      exec.Runner
	Getenv func(string) string
	DryRun bool

	Distro     string // WSL 发行版名，如 Ubuntu-24.04
	InstallDir string // 自定义安装根目录（vhdx 落在 <InstallDir>\<Distro>\）；空 = 系统默认位置
	WSLUser    string // 迁移后创建 / 设为默认的用户
	Geo        mirrors.Geo

	Repo   string // 传给 WSL 内 dsync init 的仓库地址
	Source string // Windows 侧本地仓库目录：拷贝进 WSL 后 dsync init --source
	// Proxy 仅用于本次 WSL 内 dsync init（新发行版里还没有代理配置，下载 GitHub 资产需要）。
	// 可含字面量 ${WSL_HOST_IP}，运行时替换为 Windows 宿主（默认网关）IP。
	Proxy string
	// LinuxBinary 返回 Windows 文件系统上 linux 版 dsync 的路径（下载或用户指定）。
	LinuxBinary func(ctx context.Context) (string, error)
	Log         func(format string, a ...any)
	Timestamp   func() string // 迁移备份文件名中的时间戳（测试可固定）
}

// Item 是计划中的一项。Admin 表示 Apply 需要管理员权限。
type Item struct {
	ID string `json:"id"`
	provision.Result
	Admin bool `json:"needs_admin,omitempty"`
	step  Step
}

// Step 是一个宿主步骤。Check 返回结果与"执行是否需要管理员权限"。
type Step interface {
	ID() string
	Check(ctx context.Context, x *Context) (provision.Result, bool, error)
	Apply(ctx context.Context, x *Context) error
}

// Steps 按执行顺序排列：软件 → 发行版 → Docker 集成（需发行版已存在）→ ssh-agent → WSL 内 init。
var Steps = []Step{packagesStep{}, distroStep{}, dockerStep{}, sshAgentStep{}, wslInitStep{}}

// RerunError 表示需要用户先做某事（重启 Windows、首次登录发行版等）再重新运行。
// 它不是失败：Apply 遇到它即停止，并把原因交给调用方提示用户。
type RerunError struct{ Reason string }

func (e *RerunError) Error() string { return e.Reason }

// Plan 检测全部步骤。
func Plan(ctx context.Context, x *Context) ([]Item, error) {
	var items []Item
	for _, s := range Steps {
		r, admin, err := s.Check(ctx, x)
		if err != nil {
			return nil, fmt.Errorf("%s: 检测失败: %w", s.ID(), err)
		}
		items = append(items, Item{ID: s.ID(), Result: r, Admin: admin && r.State == provision.Pending, step: s})
	}
	return items, nil
}

// Outcome 是 Apply 的结果。
type Outcome struct {
	Warnings []string
	Rerun    string // 非空：已停止，需要用户处理后重跑
}

// Apply 依次执行待办项；每步执行前重新检测（前序步骤可能改变状态）。
func Apply(ctx context.Context, x *Context, items []Item) (Outcome, error) {
	var out Outcome
	for _, it := range items {
		if it.State != provision.Done && it.step != nil {
			r, _, err := it.step.Check(ctx, x)
			if err != nil {
				return out, fmt.Errorf("%s: 检测失败: %w", it.ID, err)
			}
			it.Result = r
		}
		switch it.State {
		case provision.Manual:
			out.Warnings = append(out.Warnings, it.ID+": "+it.Detail)
		case provision.Pending:
			x.Log("== %s: %s", it.ID, it.Detail)
			if err := it.step.Apply(ctx, x); err != nil {
				var rerun *RerunError
				if errors.As(err, &rerun) {
					out.Rerun = rerun.Reason
					return out, nil
				}
				return out, fmt.Errorf("%s: %w", it.ID, err)
			}
		}
	}
	return out, nil
}

// NeedsAdmin 返回计划中需要管理员权限的待办项。
func NeedsAdmin(items []Item) []string {
	var ids []string
	for _, it := range items {
		if it.Admin {
			ids = append(ids, it.ID)
		}
	}
	return ids
}

// IsAdmin 判断当前进程是否以管理员身份运行。
func IsAdmin(ctx context.Context, x *Context) (bool, error) {
	out, err := x.ps(ctx, `([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)`)
	return strings.EqualFold(out, "True"), err
}

// ---------- 辅助 ----------

// ps 执行只读的 PowerShell 片段，返回去除首尾空白的输出。
func (x *Context) ps(ctx context.Context, script string) (string, error) {
	res, err := x.R.Query(ctx, psCmd(script))
	return strings.TrimSpace(strings.ReplaceAll(string(res.Stdout), "\r", "")), err
}

func psCmd(script string) exec.Cmd {
	return exec.Cmd{Name: "powershell.exe", Args: []string{"-NoProfile", "-NonInteractive", "-Command", script}}
}

func (x *Context) run(ctx context.Context, c exec.Cmd) error {
	_, err := x.R.Run(ctx, c)
	return err
}

func (x *Context) has(name string) bool {
	_, err := x.R.LookPath(name)
	return err == nil
}

func (x *Context) env(name string) string { return x.Getenv(name) }

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// DecodeWSL 解码 wsl.exe 的输出：它写 UTF-16LE（设 WSL_UTF8=1 也不一定生效，
// 取决于调用方式），也可能是 UTF-8。有 BOM、含 NUL 字节或不是合法 UTF-8 时按 UTF-16LE 解码
// （纯中文的 UTF-16 文本不含 NUL，只看 NUL 会误判）。
func DecodeWSL(b []byte) string {
	isUTF16 := bytes.HasPrefix(b, []byte{0xFF, 0xFE}) || bytes.IndexByte(b, 0) >= 0 || !utf8.Valid(b)
	if !isUTF16 {
		return strings.ReplaceAll(string(b), "\r", "")
	}
	if len(b)%2 == 1 {
		b = b[:len(b)-1]
	}
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		u = append(u, uint16(b[i])|uint16(b[i+1])<<8)
	}
	if len(u) > 0 && u[0] == 0xFEFF {
		u = u[1:]
	}
	return strings.ReplaceAll(string(utf16.Decode(u)), "\r", "")
}

// Distros 返回已注册的 WSL 发行版名。wsl.exe 不可用或没有发行版时返回空列表
// （两种情况的提示都是本地化文本，无需区分：后续都走安装流程）。
func Distros(ctx context.Context, x *Context) []string {
	res, err := x.R.Query(ctx, exec.Cmd{Name: "wsl.exe", Args: []string{"--list", "--quiet"}})
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(DecodeWSL(res.Stdout), "\n") {
		if l := strings.TrimSpace(line); l != "" {
			out = append(out, l)
		}
	}
	return out
}

func registered(ctx context.Context, x *Context) bool {
	for _, d := range Distros(ctx, x) {
		if strings.EqualFold(d, x.Distro) {
			return true
		}
	}
	return false
}

// ToWSLPath 把 Windows 绝对路径转为 WSL 内的挂载路径：D:\Develop\dsync → /mnt/d/Develop/dsync。
func ToWSLPath(p string) (string, error) {
	if len(p) < 3 || p[1] != ':' || (p[2] != '\\' && p[2] != '/') {
		return "", fmt.Errorf("不是 Windows 盘符绝对路径: %q", p)
	}
	rest := strings.TrimLeft(strings.ReplaceAll(p[2:], `\`, "/"), "/")
	return "/mnt/" + strings.ToLower(p[:1]) + "/" + rest, nil
}

// SanitizeUser 生成合法的 Linux 用户名（移植 bootstrap.ps1 的 Get-SanitizedUserName）。
func SanitizeUser(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r < 128 && (r == '_' || r == '.' || r == '-' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			b.WriteRune(r)
		}
	}
	n := strings.ToLower(b.String())
	if n == "" {
		return "dev"
	}
	if len(n) > 31 {
		n = n[:31]
	}
	if n[0] >= '0' && n[0] <= '9' {
		n = "u" + n
	}
	return n
}

// sq 生成 POSIX 单引号字面量。
func sq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func targetDir(x *Context) string {
	if x.InstallDir == "" {
		return ""
	}
	return filepath.Join(x.InstallDir, x.Distro)
}
