// Package mise 封装对 mise 的子进程调用（M1：只读子集）。
package mise

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/Lin-Ya/dsync-cli/internal/exec"
)

// Client 调用 mise。
type Client struct {
	R exec.Runner
}

// Version 返回 `mise --version` 的首个字段（如 2026.9.0）。
func (c Client) Version(ctx context.Context) (string, error) {
	res, err := c.R.Query(ctx, exec.Cmd{Name: "mise", Args: []string{"--version"}})
	if err != nil {
		return "", err
	}
	f := strings.Fields(string(res.Stdout))
	if len(f) == 0 {
		return "", fmt.Errorf("mise --version 无输出")
	}
	return f[0], nil
}

// Tool 是 `mise ls --json` 中的一个工具版本。
type Tool struct {
	Name             string `json:"name"`
	Version          string `json:"version"`
	RequestedVersion string `json:"requested_version,omitempty"`
	Installed        bool   `json:"installed"`
	Active           bool   `json:"active"`
	Source           struct {
		Type string `json:"type"`
		Path string `json:"path"`
	} `json:"source"`
}

// ListGlobal 返回全局配置中的工具（`mise ls --global --json`）。
// 以 --global 避免当前目录的项目级 mise.toml 干扰判断。
func (c Client) ListGlobal(ctx context.Context) ([]Tool, error) {
	res, err := c.R.Query(ctx, exec.Cmd{Name: "mise", Args: []string{"ls", "--global", "--json"}})
	if err != nil {
		return nil, err
	}
	return ParseLs(res.Stdout)
}

// ParseLs 解析 `mise ls --json` 输出：{ "<tool>": [ {version, installed, …} ] }。
func ParseLs(b []byte) ([]Tool, error) {
	var raw map[string][]Tool
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("解析 mise ls --json 输出失败: %w", err)
	}
	var out []Tool
	for name, versions := range raw {
		for _, v := range versions {
			v.Name = name
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Version < out[j].Version
	})
	return out, nil
}

// Missing 过滤出未安装的工具。
func Missing(tools []Tool) []Tool {
	var out []Tool
	for _, t := range tools {
		if !t.Installed {
			out = append(out, t)
		}
	}
	return out
}

// Doctor 运行 `mise doctor`。
func (c Client) Doctor(ctx context.Context) (string, error) {
	res, err := c.R.Query(ctx, exec.Cmd{Name: "mise", Args: []string{"doctor"}})
	return string(res.Stdout), err
}

// ---------- 写操作 ----------

// Use 把工具 spec 写入指定配置文件（mise 自带 toml_edit，保留注释），并安装该工具。
// 通过 MISE_TRUSTED_CONFIG_PATHS 临时信任该文件，不污染全局 trust 列表。
func (c Client) Use(ctx context.Context, configPath string, specs ...string) error {
	_, err := c.R.Run(ctx, exec.Cmd{
		Name:        "mise",
		Args:        append([]string{"use", "--path", configPath}, specs...),
		Env:         []string{"MISE_TRUSTED_CONFIG_PATHS=" + configPath},
		Interactive: true,
	})
	return err
}

// InstallLocked 按 lockfile 安装全部工具（版本漂移即失败，不静默回退）。
func (c Client) InstallLocked(ctx context.Context) error {
	_, err := c.R.Run(ctx, exec.Cmd{Name: "mise", Args: []string{"install", "--locked"}, Interactive: true})
	return err
}

// Install 按配置安装全部工具（无 lockfile 时使用）。
func (c Client) Install(ctx context.Context) error {
	_, err := c.R.Run(ctx, exec.Cmd{Name: "mise", Args: []string{"install"}, Interactive: true})
	return err
}

// LockGlobal 为全局配置生成/更新 lockfile（全局工具必须 --global）。
func (c Client) LockGlobal(ctx context.Context) error {
	_, err := c.R.Run(ctx, exec.Cmd{Name: "mise", Args: []string{"lock", "--global"}, Interactive: true})
	return err
}
