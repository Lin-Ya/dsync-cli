// Package workflow 实现各命令的编排逻辑。只依赖接口（Runner / fs.FS / getenv），
// 不直接读取进程全局状态，便于测试。
package workflow

import (
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"

	"github.com/Lin-Ya/dsync-cli/internal/chezmoi"
	"github.com/Lin-Ya/dsync-cli/internal/config"
	"github.com/Lin-Ya/dsync-cli/internal/exec"
	"github.com/Lin-Ya/dsync-cli/internal/mise"
	"github.com/Lin-Ya/dsync-cli/internal/platform"
)

// Env 是一次命令执行的全部外部依赖。
type Env struct {
	Getenv func(string) string
	Runner exec.Runner
	RootFS fs.FS // 根文件系统（平台探测用），通常为 os.DirFS("/")
	Out    io.Writer
	Err    io.Writer
	// DryRun 时 dsync 自身的文件写入也只打印不执行（外部命令由 exec.DryRun 负责）。
	DryRun bool
	// GOOS 非空时覆盖 runtime.GOOS 做平台探测（测试用：结果不随 CI runner 的系统变化）。
	GOOS string
	// HTTP 用于下载（nil = deps.NewHTTPClient：带超时，沿用 http(s)_proxy 环境变量）。
	HTTP *http.Client
}

// Context 是解析后的本机上下文（多个命令共用）。
type Context struct {
	Paths       config.Paths
	Local       config.Local
	LocalExists bool
	LocalErr    error
	Platform    platform.Platform
}

// Load 解析本机上下文。local.toml 的错误记录在 LocalErr 中而不是直接失败，
// 由各命令决定严重程度（shell-init 需降级继续，doctor 需报告）。
func (e Env) Load() (Context, error) {
	p, err := config.DefaultPaths(e.Getenv)
	if err != nil {
		return Context{}, err
	}
	l, exists, lerr := config.LoadLocal(p)
	return Context{
		Paths:       p,
		Local:       l,
		LocalExists: exists,
		LocalErr:    lerr,
		Platform:    e.platform(),
	}, nil
}

func (e Env) platform() platform.Platform {
	if e.GOOS != "" {
		return platform.DetectAs(e.RootFS, e.GOOS)
	}
	return platform.Detect(e.RootFS)
}

func (e Env) chezmoi(source string) chezmoi.Client {
	return chezmoi.Client{R: e.Runner, Source: source}
}
func (e Env) mise() mise.Client { return mise.Client{R: e.Runner} }

// writeFile 写文件，必要时创建父目录（dry-run 下只打印，不建目录）。
func (e Env) writeFile(path string, data []byte, perm os.FileMode) error {
	if e.DryRun {
		fmt.Fprintf(e.Err, "  [dry-run] 写入 %s\n", path)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, perm)
}

// copyFile 复制文件内容（保留目标已有权限；目标不存在时用 perm）。
func (e Env) copyFile(src, dst string, perm os.FileMode) error {
	if e.DryRun { // 先于读取：src 可能由尚未执行的命令生成（如首次 mise lock）
		return e.writeFile(dst, nil, perm)
	}
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if st, err := os.Stat(dst); err == nil {
		perm = st.Mode().Perm()
	}
	return e.writeFile(dst, b, perm)
}

func (e Env) logf(format string, a ...any) {
	fmt.Fprintf(e.Out, format+"\n", a...)
}

// require 确认外部命令都存在。
func (e Env) require(names ...string) error {
	for _, n := range names {
		if !e.has(n) {
			return fmt.Errorf("未找到 %s（请先安装，或运行 dsync doctor 查看）", n)
		}
	}
	return nil
}

func (e Env) has(name string) bool {
	_, err := e.Runner.LookPath(name)
	return err == nil
}
