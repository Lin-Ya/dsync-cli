package workflow

import (
	"context"
	"errors"
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

// githubToken：GITHUB_TOKEN 环境变量 > `gh auth token`。仓库私有，必须有 Token。
func (e Env) githubToken(ctx context.Context) (string, error) {
	if t := e.Getenv("GITHUB_TOKEN"); t != "" {
		return t, nil
	}
	if e.has("gh") {
		if res, err := e.Runner.Query(ctx, exec.Cmd{Name: "gh", Args: []string{"auth", "token"}}); err == nil {
			if t := strings.TrimSpace(string(res.Stdout)); t != "" {
				return t, nil
			}
		}
	}
	return "", errors.New("需要 GitHub Token 才能下载私有 Release：设置环境变量 GITHUB_TOKEN（需仓库 Contents 读权限），或先 gh auth login")
}

func (e Env) releaseClient(ctx context.Context) (release.Client, error) {
	token, err := e.githubToken(ctx)
	if err != nil {
		return release.Client{}, err
	}
	api := e.Getenv("DSYNC_GITHUB_API")
	if api == "" {
		api = release.DefaultAPI
	}
	return release.Client{HTTP: e.httpClient(), API: api, Token: token}, nil
}

// SelfUpdate 下载 Release 中的 dsync 并替换当前可执行文件（按 checksums.txt 校验）。
func (e Env) SelfUpdate(ctx context.Context, current string, o SelfUpdateOptions) error {
	c, err := e.releaseClient(ctx)
	if err != nil {
		return err
	}
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
