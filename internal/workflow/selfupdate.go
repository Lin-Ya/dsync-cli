package workflow

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Lin-Ya/dsync-cli/internal/deps"
	"github.com/Lin-Ya/dsync-cli/internal/exec"
	"github.com/Lin-Ya/dsync-cli/internal/release"
)

// SelfUpdateOptions 是 `dsync self-update` 的参数。
type SelfUpdateOptions struct {
	Version string // 目标 tag；为空 = 最新（含预发布）
	Force   bool   // 已是该版本时仍重新安装
	Target  string // 被替换的文件；为空 = 当前可执行文件（测试用）
}

// githubToken 返回可选的 GitHub Token：GITHUB_TOKEN 环境变量 > `gh auth token`，都没有时返回空串。
// 仓库已公开，匿名访问 Release API 就够用（未认证每小时 60 次），Token 只在遇到速率限制、
// 或企业代理要求认证时才需要。
func (e Env) githubToken(ctx context.Context) string {
	if t := e.Getenv("GITHUB_TOKEN"); t != "" {
		return t
	}
	if e.has("gh") {
		if res, err := e.Runner.Query(ctx, exec.Cmd{Name: "gh", Args: []string{"auth", "token"}}); err == nil {
			return strings.TrimSpace(string(res.Stdout))
		}
	}
	return ""
}

func (e Env) releaseClient(ctx context.Context) release.Client {
	api := e.Getenv("DSYNC_GITHUB_API")
	if api == "" {
		api = release.DefaultAPI
	}
	return release.Client{HTTP: e.httpClient(), API: api, Token: e.githubToken(ctx)}
}

// SelfUpdate 下载 Release 中的 dsync 并替换当前可执行文件（按 checksums.txt 校验）。
func (e Env) SelfUpdate(ctx context.Context, current string, o SelfUpdateOptions) error {
	c := e.releaseClient(ctx)
	r, err := c.Get(ctx, o.Version)
	if err != nil {
		return err
	}
	if r.TagName == current && !o.Force {
		e.logf("已是 %s，无需更新（--force 可重新安装）", current)
		return nil
	}
	target := o.Target
	if target == "" {
		if target, err = os.Executable(); err != nil {
			return err
		}
		if real, err := filepath.EvalSymlinks(target); err == nil {
			target = real
		}
	}
	plat := e.platform()
	data, err := c.Binary(ctx, r, plat.OS, runtime.GOARCH)
	if err != nil {
		return err
	}
	if e.DryRun {
		fmt.Fprintf(e.Err, "  [dry-run] 用 %s（已校验）替换 %s\n", r.TagName, target)
		return nil
	}
	if err := deps.WriteExecutable(target, data); err != nil {
		return fmt.Errorf("替换 %s 失败: %w", target, err)
	}
	e.logf(">>> dsync %s → %s（%s）", current, r.TagName, target)
	return nil
}
