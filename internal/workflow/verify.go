package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Lin-Ya/dsync-cli/internal/config"
	"github.com/Lin-Ya/dsync-cli/internal/exec"
	"github.com/Lin-Ya/dsync-cli/internal/mirrors"
	"github.com/Lin-Ya/dsync-cli/internal/platform"
)

// VerifyOptions 是 `dsync verify` 的参数。
type VerifyOptions struct {
	Source string // dotfiles 仓库根目录；为空则定位本机仓库
}

// VerifyReport 是校验结果。
type VerifyReport struct {
	Templates []string `json:"templates"` // 渲染的模板（相对 chezmoi 源目录）
	Matrix    []string `json:"matrix"`    // 渲染组合，如 linux/cn
	Errors    []string `json:"errors"`
}

// 渲染矩阵：两种系统 × 两种镜像源。模板中的 git 身份用示例值，以覆盖"已设置身份"的分支。
var verifyOS = []string{"linux", "darwin"}
var verifyGeos = []mirrors.Geo{mirrors.CN, mirrors.Official}
var verifyUser = config.User{Name: "dsync verify", Email: "verify@example.com"}

// Verify 校验 v2 dotfiles 仓库：dsync.toml 合法，且全部模板在每个组合下都能渲染
// （引用不存在的数据键即报错，可发现模板拼写错误）。dotfiles 仓库的 CI 运行它。
func (e Env) Verify(ctx context.Context, o VerifyOptions) (VerifyReport, error) {
	r := VerifyReport{Templates: []string{}, Matrix: []string{}, Errors: []string{}}
	repo, err := e.verifyRepo(ctx, o)
	if err != nil {
		return r, err
	}
	m, _, err := config.LoadManifest(repo.Root)
	if err != nil {
		r.Errors = append(r.Errors, err.Error())
		return r, nil
	}
	if err := e.require("chezmoi"); err != nil {
		return r, err
	}
	if r.Templates, err = templates(repo.Source); err != nil {
		return r, err
	}

	tmp, err := os.MkdirTemp("", "dsync-verify-")
	if err != nil {
		return r, err
	}
	defer os.RemoveAll(tmp)
	for _, geo := range verifyGeos {
		data, err := ChezmoiData(geo, platform.Platform{}, config.Local{User: verifyUser}, m)
		if err != nil {
			return r, err
		}
		cfg, err := chezmoiConfig(repo.Source, data, m)
		if err != nil {
			return r, err
		}
		cfgPath := filepath.Join(tmp, "chezmoi-"+string(geo)+".toml")
		if err := os.WriteFile(cfgPath, cfg, 0o600); err != nil {
			return r, err
		}
		for _, goos := range verifyOS {
			cell := goos + "/" + string(geo)
			r.Matrix = append(r.Matrix, cell)
			override, _ := json.Marshal(map[string]any{"chezmoi": map[string]any{"os": goos}})
			for _, t := range r.Templates {
				_, err := e.Runner.Query(ctx, exec.Cmd{Name: "chezmoi", Args: []string{
					"execute-template", "--config", cfgPath, "--source", repo.Source,
					"--override-data", string(override), "--file", filepath.Join(repo.Source, t)}})
				if err != nil {
					r.Errors = append(r.Errors, fmt.Sprintf("[%s] %s: %s", cell, t, renderError(err)))
				}
			}
		}
	}
	return r, nil
}

func (e Env) verifyRepo(ctx context.Context, o VerifyOptions) (RepoInfo, error) {
	var repo RepoInfo
	if o.Source != "" {
		dir, err := filepath.Abs(o.Source)
		if err != nil {
			return repo, err
		}
		info, ok := e.inspect(ctx, dir)
		if !ok {
			return repo, fmt.Errorf("%s 不是 dotfiles 仓库", dir)
		}
		repo = info
	} else {
		c, err := e.Load()
		if err != nil {
			return repo, err
		}
		if repo, err = e.LocateRepo(ctx, c); err != nil {
			return repo, err
		}
	}
	if repo.Layout != LayoutV2 {
		return repo, fmt.Errorf("%s 是 v1 布局：verify 只支持 v2 布局（仓库根有 dsync.toml）", repo.Root)
	}
	return repo, nil
}

// templates 返回源目录中需要渲染的文件：*.tmpl，以及 chezmoi 总是按模板处理的
// .chezmoiignore / .chezmoiremove。.chezmoitemplates 中是供 include 的片段，不单独渲染。
func templates(src string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if name == ".chezmoitemplates" || name == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(name, ".tmpl") || name == ".chezmoiignore" || name == ".chezmoiremove" {
			rel, _ := filepath.Rel(src, p)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

// renderError 取 chezmoi 报错中有用的一行。
func renderError(err error) string {
	var ee *exec.ExitError
	if errors.As(err, &ee) && strings.TrimSpace(ee.Stderr) != "" {
		return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(ee.Stderr), "chezmoi: "))
	}
	return err.Error()
}

// WriteText 以人类可读格式输出校验结果。
func (r VerifyReport) WriteText(w io.Writer) {
	if len(r.Errors) == 0 {
		fmt.Fprintf(w, "[ OK ] dsync.toml 合法；%d 个模板在 %s 下均可渲染\n", len(r.Templates), strings.Join(r.Matrix, "、"))
		return
	}
	for _, e := range r.Errors {
		fmt.Fprintf(w, "[FAIL] %s\n", e)
	}
	fmt.Fprintf(w, "共 %d 处错误\n", len(r.Errors))
}
