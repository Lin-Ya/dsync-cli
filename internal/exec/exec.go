// Package exec 是 dsync 调用外部命令（mise / chezmoi / git / 系统工具）的唯一出口。
//
// 约定：
//   - Query 用于只读命令（status / ls / source-path …），dry-run 下照常执行；
//   - Run 用于有副作用的命令，dry-run 下只打印不执行；
//   - 非零退出码一律以 *ExitError 返回（附带 stderr），由调用方决定如何处理，
//     禁止在底层静默吞错（v1 的 `2>/dev/null || true` 问题）。
package exec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	osexec "os/exec"
	"strings"
)

// Cmd 描述一条外部命令。
type Cmd struct {
	Name string
	Args []string
	Dir  string   // 工作目录；空 = 继承
	Env  []string // 追加到当前环境的 KEY=VALUE
	// Interactive 时直连终端（stdin/stdout/stderr），用于 chezmoi apply 的覆盖确认、
	// mise install 的进度输出等；此时 Result 不含输出。
	Interactive bool
}

// String 返回便于展示的命令行（dry-run / 日志 / 测试断言使用）。
func (c Cmd) String() string {
	parts := make([]string, 0, len(c.Args)+1)
	parts = append(parts, c.Name)
	for _, a := range c.Args {
		parts = append(parts, quote(a))
	}
	s := strings.Join(parts, " ")
	if c.Dir != "" {
		s = fmt.Sprintf("(cd %s && %s)", quote(c.Dir), s)
	}
	return s
}

func quote(s string) string {
	if s == "" {
		return "''"
	}
	if strings.ContainsAny(s, " \t\n'\"$`\\|&;<>()*?[]{}~#!") {
		return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
	}
	return s
}

// Result 是命令执行结果。
type Result struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

// ExitError 表示命令以非零退出码结束。
type ExitError struct {
	Cmd      Cmd
	ExitCode int
	Stderr   string
}

func (e *ExitError) Error() string {
	msg := fmt.Sprintf("%s: 退出码 %d", e.Cmd.String(), e.ExitCode)
	if s := strings.TrimSpace(e.Stderr); s != "" {
		msg += ": " + s
	}
	return msg
}

// ErrNotFound 表示可执行文件不在 PATH 中。
var ErrNotFound = errors.New("命令未找到")

// Runner 是外部命令执行器。
type Runner interface {
	// Query 执行只读命令并捕获输出（dry-run 下也执行）。
	Query(ctx context.Context, c Cmd) (Result, error)
	// Run 执行有副作用的命令（dry-run 下只打印）。
	Run(ctx context.Context, c Cmd) (Result, error)
	// LookPath 查找可执行文件。
	LookPath(name string) (string, error)
}

// Real 直接执行命令。
type Real struct{}

func (Real) Query(ctx context.Context, c Cmd) (Result, error) { return runReal(ctx, c) }
func (Real) Run(ctx context.Context, c Cmd) (Result, error)   { return runReal(ctx, c) }

func (Real) LookPath(name string) (string, error) {
	p, err := osexec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, ErrNotFound)
	}
	return p, nil
}

func runReal(ctx context.Context, c Cmd) (Result, error) {
	cmd := osexec.CommandContext(ctx, c.Name, c.Args...)
	cmd.Dir = c.Dir
	if len(c.Env) > 0 {
		cmd.Env = append(cmd.Environ(), c.Env...)
	}
	var stdout, stderr bytes.Buffer
	if c.Interactive {
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	} else {
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
	}
	err := cmd.Run()
	res := Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	if err != nil {
		var ee *osexec.ExitError
		if errors.As(err, &ee) {
			res.ExitCode = ee.ExitCode()
			return res, &ExitError{Cmd: c, ExitCode: res.ExitCode, Stderr: stderr.String()}
		}
		if errors.Is(err, osexec.ErrNotFound) {
			return res, fmt.Errorf("%s: %w", c.Name, ErrNotFound)
		}
		return res, err
	}
	return res, nil
}

// DryRun 包装另一个 Runner：Query 透传，Run 只打印到 Out。
type DryRun struct {
	Inner Runner
	Out   io.Writer
}

func (d DryRun) Query(ctx context.Context, c Cmd) (Result, error) { return d.Inner.Query(ctx, c) }

func (d DryRun) Run(_ context.Context, c Cmd) (Result, error) {
	fmt.Fprintf(d.Out, "  [dry-run] %s\n", c.String())
	return Result{}, nil
}

func (d DryRun) LookPath(name string) (string, error) { return d.Inner.LookPath(name) }
