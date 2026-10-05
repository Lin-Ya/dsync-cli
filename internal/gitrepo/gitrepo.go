// Package gitrepo 封装对 dotfiles 仓库的 git 操作（M1：只读子集）。
package gitrepo

import (
	"bufio"
	"context"
	"strconv"
	"strings"

	"github.com/Lin-Ya/dsync-cli/internal/exec"
)

// Repo 是一个本地 git 仓库。
type Repo struct {
	R   exec.Runner
	Dir string
}

// Status 是仓库状态摘要（基于本地已知的远端引用，不发起网络请求）。
type Status struct {
	Branch    string   `json:"branch"`
	Upstream  string   `json:"upstream,omitempty"`
	Ahead     int      `json:"ahead"`  // 本地领先远端（未推送）的提交数
	Behind    int      `json:"behind"` // 本地落后远端的提交数（取决于上次 fetch）
	Dirty     []string `json:"dirty"`  // 未提交的改动（porcelain 路径）
	Untracked int      `json:"untracked"`
}

// Status 读取仓库状态。
func (r Repo) Status(ctx context.Context) (Status, error) {
	res, err := r.R.Query(ctx, exec.Cmd{Name: "git", Args: []string{"status", "--porcelain=v2", "--branch"}, Dir: r.Dir})
	if err != nil {
		return Status{}, err
	}
	return ParseStatus(string(res.Stdout)), nil
}

// ParseStatus 解析 `git status --porcelain=v2 --branch`。
func ParseStatus(out string) Status {
	s := Status{Dirty: []string{}}
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "# branch.head "):
			s.Branch = strings.TrimPrefix(line, "# branch.head ")
		case strings.HasPrefix(line, "# branch.upstream "):
			s.Upstream = strings.TrimPrefix(line, "# branch.upstream ")
		case strings.HasPrefix(line, "# branch.ab "):
			for _, f := range strings.Fields(strings.TrimPrefix(line, "# branch.ab ")) {
				n, _ := strconv.Atoi(f[1:])
				if f[0] == '+' {
					s.Ahead = n
				} else if f[0] == '-' {
					s.Behind = n
				}
			}
		case strings.HasPrefix(line, "? "):
			s.Untracked++
		case strings.HasPrefix(line, "1 "), strings.HasPrefix(line, "2 "), strings.HasPrefix(line, "u "):
			s.Dirty = append(s.Dirty, porcelainPath(line))
		}
	}
	return s
}

// porcelainPath 取 porcelain v2 条目的路径字段（重命名条目取新路径）。
func porcelainPath(line string) string {
	n := 8 // "1 XY sub mH mI mW hH hI path"
	switch line[0] {
	case '2':
		n = 9 // 额外的 Xscore 字段；路径后跟 \t 原路径
	case 'u':
		n = 10
	}
	f := strings.SplitN(line, " ", n+1)
	if len(f) <= n {
		return line
	}
	p, _, _ := strings.Cut(f[n], "\t")
	return p
}

// IsRepo 判断目录是否为 git 工作树。
func (r Repo) IsRepo(ctx context.Context) bool {
	res, err := r.R.Query(ctx, exec.Cmd{Name: "git", Args: []string{"rev-parse", "--is-inside-work-tree"}, Dir: r.Dir})
	return err == nil && strings.TrimSpace(string(res.Stdout)) == "true"
}

// Ident 返回提交身份 "Name <email>"。判定与 git commit 一致（环境变量、各级配置、自动推断邮箱），
// 身份不可用（如未设置 user.name）时返回错误。
func (r Repo) Ident(ctx context.Context) (string, error) {
	res, err := r.R.Query(ctx, exec.Cmd{Name: "git", Args: []string{"var", "GIT_AUTHOR_IDENT"}, Dir: r.Dir})
	if err != nil {
		return "", err
	}
	s := strings.TrimSpace(string(res.Stdout))
	if i := strings.LastIndex(s, ">"); i >= 0 {
		s = s[:i+1] // 去掉末尾的时间戳与时区
	}
	return s, nil
}

// ---------- 写操作 ----------

func (r Repo) run(ctx context.Context, args ...string) error {
	_, err := r.R.Run(ctx, exec.Cmd{Name: "git", Args: args, Dir: r.Dir})
	return err
}

// Pull 以 rebase 方式拉取上游；本地未提交改动自动暂存后恢复。
func (r Repo) Pull(ctx context.Context) error {
	return r.run(ctx, "pull", "--rebase", "--autostash")
}

// Add 暂存指定路径（相对仓库根）；不传路径时暂存全部改动。
func (r Repo) Add(ctx context.Context, paths ...string) error {
	if len(paths) == 0 {
		return r.run(ctx, "add", "-A")
	}
	return r.run(ctx, append([]string{"add", "-A", "--"}, paths...)...)
}

// Staged 返回已暂存的文件列表。
func (r Repo) Staged(ctx context.Context) ([]string, error) {
	res, err := r.R.Query(ctx, exec.Cmd{Name: "git", Args: []string{"diff", "--cached", "--name-only", "-z"}, Dir: r.Dir})
	if err != nil {
		return nil, err
	}
	var out []string
	for _, p := range strings.Split(string(res.Stdout), "\x00") {
		if p != "" {
			out = append(out, p)
		}
	}
	return out, nil
}

// Commit 提交已暂存的改动。
func (r Repo) Commit(ctx context.Context, msg string) error {
	return r.run(ctx, "commit", "-m", msg)
}

// Push 推送当前分支；remote 为空时推送到上游。
func (r Repo) Push(ctx context.Context, remote string) error {
	if remote == "" {
		return r.run(ctx, "push")
	}
	return r.run(ctx, "push", remote, "HEAD")
}
