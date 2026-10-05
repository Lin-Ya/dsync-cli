package workflow

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/user"

	"github.com/Lin-Ya/dsync-cli/internal/config"
	"github.com/Lin-Ya/dsync-cli/internal/provision"
)

// ProvisionReport 是 `dsync provision` 的结果（--json 输出）。
type ProvisionReport struct {
	Items    []provision.Item `json:"items"`
	Warnings []string         `json:"warnings"`
}

// provisionPlan 构造执行上下文并检测各步骤状态。
// 找不到 dotfiles 仓库或仓库无 dsync.toml（v1 布局）时使用与 v1 一致的默认清单。
func (e Env) provisionPlan(ctx context.Context, only []string) (*provision.Context, []provision.Item, error) {
	c, err := e.Load()
	if err != nil {
		return nil, nil, err
	}
	geo, _, err := config.ResolveGeo(e.Getenv, c.Paths, c.Local)
	if err != nil {
		return nil, nil, err
	}
	m := config.DefaultManifest()
	if repo, err := e.LocateRepo(ctx, c); err == nil {
		mm, exists, err := config.LoadManifest(repo.Root)
		if err != nil {
			return nil, nil, err
		}
		if exists {
			m = mm
		}
	}
	u, err := user.Current()
	if err != nil {
		return nil, nil, err
	}
	x := &provision.Context{
		R: e.Runner, Platform: c.Platform, Geo: geo, Manifest: m,
		Home: c.Paths.Home, Root: "/", UID: os.Getuid(), User: u.Username,
	}
	items, err := provision.Plan(ctx, x, only)
	return x, items, err
}

// ProvisionCheck 只检测、不执行。
func (e Env) ProvisionCheck(ctx context.Context, only []string) (ProvisionReport, error) {
	_, items, err := e.provisionPlan(ctx, only)
	return ProvisionReport{Items: items, Warnings: []string{}}, err
}

// Provision 检测并执行待办步骤（取代 v1 run_once_* 脚本）。
func (e Env) Provision(ctx context.Context, only []string) error {
	x, items, err := e.provisionPlan(ctx, only)
	if err != nil {
		return err
	}
	WritePlan(e.Out, items)
	warnings, err := provision.Apply(ctx, x, items, e.logf)
	for _, w := range warnings {
		fmt.Fprintf(e.Out, "[WARN] %s\n", w)
	}
	return err
}

var stateLabel = map[provision.State]string{provision.Done: "[完成]", provision.Pending: "[待办]", provision.Manual: "[手动]"}

// WritePlan 以人类可读格式输出计划。
func WritePlan(w io.Writer, items []provision.Item) {
	for _, it := range items {
		fmt.Fprintf(w, "  %s %-14s %s\n", stateLabel[it.State], it.ID, it.Detail)
	}
}
