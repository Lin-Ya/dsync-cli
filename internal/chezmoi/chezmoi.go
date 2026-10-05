// Package chezmoi 封装对 chezmoi 的子进程调用（M1：只读子集）。
package chezmoi

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Lin-Ya/dsync-cli/internal/exec"
)

// Client 调用 chezmoi。Source 非空时显式传 --source，不依赖 ~/.local/share/chezmoi 软链。
type Client struct {
	R      exec.Runner
	Source string
}

func (c Client) cmd(args ...string) exec.Cmd {
	if c.Source != "" {
		args = append([]string{"--source", c.Source}, args...)
	}
	return exec.Cmd{Name: "chezmoi", Args: args}
}

// Version 返回 `chezmoi --version` 的首行。
func (c Client) Version(ctx context.Context) (string, error) {
	res, err := c.R.Query(ctx, exec.Cmd{Name: "chezmoi", Args: []string{"--version"}})
	if err != nil {
		return "", err
	}
	return firstLine(string(res.Stdout)), nil
}

// SourcePath 返回 chezmoi 当前使用的源目录。
func (c Client) SourcePath(ctx context.Context) (string, error) {
	res, err := c.R.Query(ctx, c.cmd("source-path"))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(res.Stdout)), nil
}

// StatusEntry 是 `chezmoi status` 的一行。
// 第一列：上次写入后目标被改动（漂移）；第二列：apply 将执行的动作。
type StatusEntry struct {
	LastWrite string `json:"last_write"`
	Apply     string `json:"apply"`
	Path      string `json:"path"`
}

// Status 返回目标与源的差异。exclude 例如 "scripts"（避免把 run_ 脚本算作漂移）。
func (c Client) Status(ctx context.Context, exclude string) ([]StatusEntry, error) {
	args := []string{"status", "--no-tty"}
	if exclude != "" {
		args = append(args, "--exclude", exclude)
	}
	res, err := c.R.Query(ctx, c.cmd(args...))
	if err != nil {
		return nil, err
	}
	return ParseStatus(string(res.Stdout)), nil
}

// ParseStatus 解析 `chezmoi status` 输出（"XY path"）。
func ParseStatus(out string) []StatusEntry {
	entries := []StatusEntry{} // 非 nil：JSON 中输出 [] 而非 null
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		if len(line) < 4 || line[2] != ' ' {
			continue
		}
		entries = append(entries, StatusEntry{
			LastWrite: strings.TrimSpace(line[0:1]),
			Apply:     strings.TrimSpace(line[1:2]),
			Path:      line[3:],
		})
	}
	return entries
}

// Doctor 运行 `chezmoi doctor`，返回输出；有 failed/error 项时 err 非空。
func (c Client) Doctor(ctx context.Context) (string, error) {
	res, err := c.R.Query(ctx, exec.Cmd{Name: "chezmoi", Args: []string{"doctor", "--no-network"}})
	return string(res.Stdout), err
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return s
}

// ---------- 写操作 ----------

// Init 按源目录中的 .chezmoi.toml.tmpl 重新生成 chezmoi 配置（不 clone、不 apply）。
// env 用于向配置模板传值（如 DSYNC_GEO）。
func (c Client) Init(ctx context.Context, env ...string) error {
	cmd := c.cmd("init", "--no-tty")
	cmd.Env = env
	_, err := c.R.Run(ctx, cmd)
	return err
}

// Apply 把源同步到目标。直连终端：目标被本地改动时 chezmoi 需要用户确认是否覆盖。
func (c Client) Apply(ctx context.Context, targets ...string) error {
	cmd := c.cmd(append([]string{"apply"}, targets...)...)
	cmd.Interactive = true
	_, err := c.R.Run(ctx, cmd)
	return err
}

// ReAdd 把目标的本地改动写回源（仅对非模板源有效，模板会被 chezmoi 静默跳过）。
func (c Client) ReAdd(ctx context.Context, targets ...string) error {
	_, err := c.R.Run(ctx, c.cmd(append([]string{"re-add"}, targets...)...))
	return err
}

// SourcePathOf 返回目标对应的源文件路径；目标未被管理时返回错误。
func (c Client) SourcePathOf(ctx context.Context, target string) (string, error) {
	res, err := c.R.Query(ctx, c.cmd("source-path", target))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(res.Stdout)), nil
}

// AgeRecipient 读取当前 chezmoi 配置中的 age.recipient（重新 init 前保留它）。
func (c Client) AgeRecipient(ctx context.Context) (string, error) {
	res, err := c.R.Query(ctx, c.cmd("dump-config", "--format", "json"))
	if err != nil {
		return "", err
	}
	var cfg struct {
		Age struct {
			Recipient string `json:"recipient"`
		} `json:"age"`
	}
	if err := json.Unmarshal(res.Stdout, &cfg); err != nil {
		return "", fmt.Errorf("解析 chezmoi dump-config 输出失败: %w", err)
	}
	return cfg.Age.Recipient, nil
}
