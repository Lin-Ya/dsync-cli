// Package provision 实现系统级安装步骤（取代 v1 的 run_once_* 脚本）。
//
// 每个步骤先 Check 当前状态，再仅对"待办"项 Apply。完成与否以实际检测为准，
// 而不是 chezmoi 的 run_once 标记，因此可随时重跑、天然幂等，也不再需要
// v1 的 reinstall-run-once.sh。Apply 只通过 exec.Runner 执行命令，--dry-run 下只打印。
package provision

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Lin-Ya/dsync-cli/internal/config"
	"github.com/Lin-Ya/dsync-cli/internal/exec"
	"github.com/Lin-Ya/dsync-cli/internal/mirrors"
	"github.com/Lin-Ya/dsync-cli/internal/platform"
)

// State 是步骤的检测结果。
type State string

const (
	Done    State = "done"    // 已就绪
	Pending State = "pending" // 需要执行 Apply
	Manual  State = "manual"  // 需要用户手动处理（Apply 无法自动完成）
)

// Result 是一次 Check 的结果。
type Result struct {
	State  State  `json:"state"`
	Detail string `json:"detail,omitempty"` // 待办时说明将做什么；手动时说明如何处理
}

// Step 是一个系统级安装步骤。
type Step interface {
	ID() string
	Applicable(p platform.Platform) bool
	Check(ctx context.Context, x *Context) (Result, error)
	Apply(ctx context.Context, x *Context) error
	// Optional 为 true 时 Apply 失败只记为警告（如依赖 GitHub 下载的增强项），不中断后续步骤。
	Optional() bool
}

// Steps 是全部步骤，按执行顺序排列（apt 先装好 curl/git/unzip/zsh 供后续步骤使用）。
var Steps = []Step{aptStep{}, wslSSHAgentStep{}, homebrewStep{}, ohmyzshStep{}, chshStep{}, dockerStep{}}

// Context 是步骤执行所需的全部输入。
type Context struct {
	R        exec.Runner
	Platform platform.Platform
	Geo      mirrors.Geo
	Manifest config.Manifest
	Home     string
	Root     string // 根文件系统路径（测试可指向临时目录）
	UID      int
	User     string
}

// sudoCmd 在非 root 且有 sudo 时为命令加 sudo 前缀（容器 / root 下不需要）。
func (x *Context) sudoCmd(name string, args ...string) exec.Cmd {
	if x.UID != 0 {
		if _, err := x.R.LookPath("sudo"); err == nil {
			return exec.Cmd{Name: "sudo", Args: append([]string{name}, args...), Interactive: true}
		}
	}
	return exec.Cmd{Name: name, Args: args, Interactive: true}
}

// mirrorEnv 返回当前 geo 的镜像环境变量（chezmoi run_once 时 v1 需 source mirrors.sh 才能拿到）。
func (x *Context) mirrorEnv() []string {
	settings, _ := mirrors.EnvFor(x.Geo)
	var env []string
	for _, s := range settings {
		if s.Value != "" {
			env = append(env, s.Name+"="+s.Value)
		}
	}
	return env
}

func (x *Context) run(ctx context.Context, c exec.Cmd) error {
	_, err := x.R.Run(ctx, c)
	return err
}

func (x *Context) path(rel string) string { return filepath.Join(x.Root, rel) }

func (x *Context) has(name string) bool {
	_, err := x.R.LookPath(name)
	return err == nil
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// Item 是计划中的一项。
type Item struct {
	ID string `json:"id"`
	Result
	step Step
}

// Plan 检测已启用且适用于本平台的步骤。only 非空时只包含其中列出的步骤。
func Plan(ctx context.Context, x *Context, only []string) ([]Item, error) {
	enabled := setOf(x.Manifest.Provision.Enable)
	want := setOf(only)
	for id := range want {
		if !enabled[id] {
			return nil, fmt.Errorf("步骤 %q 未启用或不存在（已启用: %v）", id, x.Manifest.Provision.Enable)
		}
	}
	var items []Item
	for _, s := range Steps {
		if !enabled[s.ID()] || (len(want) > 0 && !want[s.ID()]) || !s.Applicable(x.Platform) {
			continue
		}
		r, err := s.Check(ctx, x)
		if err != nil {
			return nil, fmt.Errorf("%s: 检测失败: %w", s.ID(), err)
		}
		items = append(items, Item{ID: s.ID(), Result: r, step: s})
	}
	return items, nil
}

// Apply 依次执行计划中的待办项。可选步骤失败与手动项记为警告返回；必选步骤失败即中止。
func Apply(ctx context.Context, x *Context, items []Item, log func(string, ...any)) (warnings []string, err error) {
	for _, it := range items {
		// 执行前重新检测：前序步骤可能已改变状态（如 apt 装好 zsh 后 chsh 才可执行）
		if it.State != Done && it.step != nil {
			r, err := it.step.Check(ctx, x)
			if err != nil {
				return warnings, fmt.Errorf("%s: 检测失败: %w", it.ID, err)
			}
			it.Result = r
		}
		switch it.State {
		case Manual:
			warnings = append(warnings, fmt.Sprintf("%s: %s", it.ID, it.Detail))
		case Pending:
			log("== %s: %s", it.ID, it.Detail)
			if err := it.step.Apply(ctx, x); err != nil {
				if it.step.Optional() {
					warnings = append(warnings, fmt.Sprintf("%s: 失败（可选步骤，已跳过）: %v", it.ID, err))
					continue
				}
				return warnings, fmt.Errorf("%s: %w", it.ID, err)
			}
		}
	}
	return warnings, nil
}

// PendingIDs 返回计划中待办项的 ID。
func PendingIDs(items []Item) []string {
	ids := []string{}
	for _, it := range items {
		if it.State == Pending {
			ids = append(ids, it.ID)
		}
	}
	return ids
}

func setOf(xs []string) map[string]bool {
	m := map[string]bool{}
	for _, x := range xs {
		m[x] = true
	}
	return m
}
