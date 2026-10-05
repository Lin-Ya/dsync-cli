package workflow

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Lin-Ya/dsync-cli/internal/config"
	"github.com/Lin-Ya/dsync-cli/internal/exec"
	"github.com/Lin-Ya/dsync-cli/internal/gitrepo"
	"github.com/Lin-Ya/dsync-cli/internal/mise"
)

// Level 是检查结果级别。
type Level string

const (
	OK   Level = "ok"
	Warn Level = "warn"
	Fail Level = "fail"
)

// Check 是一项诊断结果。
type Check struct {
	Name   string `json:"name"`
	Level  Level  `json:"level"`
	Detail string `json:"detail"`
	Hint   string `json:"hint,omitempty"`
}

// DoctorReport 是 `dsync doctor` 的结果。
type DoctorReport struct {
	Checks []Check `json:"checks"`
}

// Failed 表示存在 fail 级问题（命令以非零码退出）。
func (d DoctorReport) Failed() bool {
	for _, c := range d.Checks {
		if c.Level == Fail {
			return true
		}
	}
	return false
}

// Doctor 执行全部诊断（只读）。
func (e Env) Doctor(ctx context.Context) (DoctorReport, error) {
	c, err := e.Load()
	if err != nil {
		return DoctorReport{}, err
	}
	var d DoctorReport
	add := func(name string, lv Level, detail, hint string) {
		d.Checks = append(d.Checks, Check{Name: name, Level: lv, Detail: detail, Hint: hint})
	}

	// 1) 底座工具
	for _, t := range []struct{ name, hint string }{
		{"git", "安装 git（apt install git / xcode-select --install）"},
		{"mise", "dsync init <仓库地址>（自动下载固定版本并校验 sha256）"},
		{"chezmoi", "dsync init <仓库地址>（自动下载固定版本并校验 sha256）"},
	} {
		if p, err := e.Runner.LookPath(t.name); err != nil {
			add(t.name, Fail, "未找到", t.hint)
		} else {
			add(t.name, OK, e.versionOf(ctx, t.name, p), "")
		}
	}

	// 2) 本机配置
	switch {
	case c.LocalErr != nil:
		add("local.toml", Fail, c.LocalErr.Error(), "修正后重试")
	case c.LocalExists:
		add("local.toml", OK, c.Paths.LocalFile(), "")
	default:
		add("local.toml", OK, "未创建（v1 兼容模式：geo 读 ~/.config/dsync/geo，代理读 proxy.env）", "")
	}
	if geo, src, err := config.ResolveGeo(e.Getenv, c.Paths, c.Local); err != nil {
		add("geo", Fail, err.Error(), "dsync geo set cn|official")
	} else {
		add("geo", OK, fmt.Sprintf("%s（来自 %s）", geo, src), "")
	}

	// 3) dotfiles 仓库
	repo, rerr := e.LocateRepo(ctx, c)
	if rerr != nil {
		add("repo", Fail, rerr.Error(), "") // 下一步已包含在 ErrRepoNotFound 中
	} else {
		add("repo", OK, fmt.Sprintf("%s（%s 布局，经 %s）", repo.Root, repo.Layout, repo.Via), "")
		if repo.Layout == LayoutV1 {
			add("layout", Warn, "仍为 v1 混合布局（工具与配置同仓）", "仓库迁移为 v2 布局（根目录 dsync.toml + .chezmoiroot）并推送后，dsync sync 会自动切换")
		} else if _, _, err := config.LoadManifest(repo.Root); err != nil {
			add("dsync.toml", Fail, err.Error(), "")
		}
		if e.has("git") {
			if id, err := (gitrepo.Repo{R: e.Runner, Dir: repo.Root}).Ident(ctx); err != nil {
				add("git identity", Warn, "未配置，capture / tool add / save 无法提交", "在 local.toml 的 [user] 填写 name / email 后 dsync sync")
			} else {
				add("git identity", OK, id, "")
			}
			if gs, err := (gitrepo.Repo{R: e.Runner, Dir: repo.Root}).Status(ctx); err != nil {
				add("git", Warn, err.Error(), "")
			} else if gs.Upstream == "" {
				add("remote", Warn, "当前分支无上游，改动无法同步到其它机器", "git push -u origin "+gs.Branch)
			} else if gs.Ahead > 0 {
				add("remote", Warn, fmt.Sprintf("%d 个提交未推送，其它机器拿不到", gs.Ahead), "dsync save")
			} else {
				add("remote", OK, "已与 "+gs.Upstream+" 同步（以上次 fetch 为准）", "")
			}
		}
		if e.has("chezmoi") {
			if sp, err := e.chezmoi("").SourcePath(ctx); err != nil {
				add("chezmoi source", Warn, err.Error(), "")
			} else if !samePath(sp, repo.Source) {
				add("chezmoi source", Warn, fmt.Sprintf("chezmoi 源目录 %s ≠ 仓库源 %s", sp, repo.Source),
					"不带 --source 的 chezmoi 命令会作用于另一个目录")
			} else {
				add("chezmoi source", OK, sp, "")
			}
		}
	}

	// 4) 工具安装情况
	if e.has("mise") {
		if tools, err := e.mise().ListGlobal(ctx); err != nil {
			add("mise tools", Warn, err.Error(), "")
		} else if m := mise.Missing(tools); len(m) > 0 {
			add("mise tools", Warn, fmt.Sprintf("%d/%d 个工具未安装", len(m), len(tools)), "dsync sync")
		} else {
			add("mise tools", OK, fmt.Sprintf("%d 个工具全部已安装", len(tools)), "")
		}
	}

	// 5) SSH agent
	sock := e.Getenv("SSH_AUTH_SOCK")
	if st, err := os.Stat(sock); sock != "" && err == nil && st.Mode()&os.ModeSocket != 0 {
		add("ssh-agent", OK, sock, "")
	} else {
		hint := "macOS 通常由系统 agent 提供"
		if c.Platform.WSL {
			hint = "WSL 需 npiperelay + socat 桥接 Windows ssh-agent（eval \"$(dsync shell-init zsh)\" 自动处理，见 docs/ssh-agent.md）"
		}
		add("ssh-agent", Warn, "SSH_AUTH_SOCK 未设置或 socket 无效", hint)
	}

	// 6) docker（可选组件，只告警）
	if !e.has("docker") {
		add("docker", Warn, "未找到 docker", "见 docs/docker.md")
	} else if _, err := e.Runner.Query(ctx, exec.Cmd{Name: "docker", Args: []string{"info", "--format", "{{.ServerVersion}}"}}); err != nil {
		add("docker", Warn, "CLI 存在但守护进程不可达", "确认 Docker Desktop / OrbStack 已启动（WSL 需开启集成）")
	} else {
		add("docker", OK, "守护进程可用", "")
	}
	return d, nil
}

func (e Env) versionOf(ctx context.Context, name, path string) string {
	res, err := e.Runner.Query(ctx, exec.Cmd{Name: name, Args: []string{"--version"}})
	if err != nil {
		return path
	}
	v, _, _ := strings.Cut(strings.TrimSpace(string(res.Stdout)), "\n")
	return v
}

func samePath(a, b string) bool {
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	if err1 != nil || err2 != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return ra == rb
}

// WriteText 以人类可读格式输出诊断。
func (d DoctorReport) WriteText(w io.Writer) {
	tag := map[Level]string{OK: "[ OK ]", Warn: "[WARN]", Fail: "[FAIL]"}
	for _, c := range d.Checks {
		fmt.Fprintf(w, "%s %-15s %s\n", tag[c.Level], c.Name, c.Detail)
		if c.Hint != "" && c.Level != OK {
			fmt.Fprintf(w, "       %-15s → %s\n", "", c.Hint)
		}
	}
}
