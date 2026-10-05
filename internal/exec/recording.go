package exec

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// Recording 是测试用 Runner：按命令行前缀返回预设结果，并记录全部调用。
// 未预设的命令返回 ExitError(127)，迫使测试显式声明依赖的每一条外部命令。
type Recording struct {
	mu        sync.Mutex
	responses []response
	Paths     map[string]string // LookPath 结果；缺省视为未找到
	Calls     []Call
	// Delegate 中列出的命令名在记录后转交给对应 Runner 真实执行
	// （例如 {"git": Real{}}：mise/chezmoi 用 fake，git 用真命令断言仓库状态）。
	Delegate map[string]Runner
}

// Call 是一次被记录的调用。
type Call struct {
	Mutating bool
	Cmd      Cmd
}

type response struct {
	prefix string
	res    Result
	fn     func(Cmd) Result // 非 nil 时调用它生成结果（可模拟写文件等副作用）
}

// On 注册一个响应：命令行（Cmd.String() 不含 Dir）以 prefix 开头时返回 res。
// 后注册的优先匹配。res.ExitCode 非零时返回 *ExitError。
func (r *Recording) On(prefix string, res Result) *Recording {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.responses = append(r.responses, response{prefix: prefix, res: res})
	return r
}

// OnFunc 注册一个带副作用的响应（例如 fake mise 改写配置文件）。
func (r *Recording) OnFunc(prefix string, fn func(Cmd) Result) *Recording {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.responses = append(r.responses, response{prefix: prefix, fn: fn})
	return r
}

// OnStdout 是 On 的便捷形式。
func (r *Recording) OnStdout(prefix, stdout string) *Recording {
	return r.On(prefix, Result{Stdout: []byte(stdout)})
}

func (r *Recording) Query(ctx context.Context, c Cmd) (Result, error) {
	if d, ok := r.delegate(c, false); ok {
		return d.Query(ctx, c)
	}
	return r.do(c, false)
}

func (r *Recording) Run(ctx context.Context, c Cmd) (Result, error) {
	if d, ok := r.delegate(c, true); ok {
		return d.Run(ctx, c)
	}
	return r.do(c, true)
}

func (r *Recording) delegate(c Cmd, mutating bool) (Runner, bool) {
	d, ok := r.Delegate[c.Name]
	if ok {
		r.mu.Lock()
		r.Calls = append(r.Calls, Call{Mutating: mutating, Cmd: c})
		r.mu.Unlock()
	}
	return d, ok
}

func (r *Recording) LookPath(name string) (string, error) {
	if p, ok := r.Paths[name]; ok {
		return p, nil
	}
	return "", fmt.Errorf("%s: %w", name, ErrNotFound)
}

func (r *Recording) do(c Cmd, mutating bool) (Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Calls = append(r.Calls, Call{Mutating: mutating, Cmd: c})
	line := Cmd{Name: c.Name, Args: c.Args}.String()
	for i := len(r.responses) - 1; i >= 0; i-- {
		resp := r.responses[i]
		if strings.HasPrefix(line, resp.prefix) {
			res := resp.res
			if resp.fn != nil {
				res = resp.fn(c)
			}
			if res.ExitCode != 0 {
				return res, &ExitError{Cmd: c, ExitCode: res.ExitCode, Stderr: string(res.Stderr)}
			}
			return res, nil
		}
	}
	return Result{ExitCode: 127}, &ExitError{Cmd: c, ExitCode: 127, Stderr: "recording: 未预设的命令: " + line}
}

// Lines 返回所有调用的命令行（便于断言调用序列）。
func (r *Recording) Lines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.Calls))
	for i, c := range r.Calls {
		out[i] = Cmd{Name: c.Cmd.Name, Args: c.Cmd.Args}.String()
	}
	return out
}
