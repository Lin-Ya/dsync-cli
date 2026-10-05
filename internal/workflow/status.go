package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/Lin-Ya/dsync-cli/internal/chezmoi"
	"github.com/Lin-Ya/dsync-cli/internal/config"
	"github.com/Lin-Ya/dsync-cli/internal/gitrepo"
	"github.com/Lin-Ya/dsync-cli/internal/mise"
	"github.com/Lin-Ya/dsync-cli/internal/platform"
	"github.com/Lin-Ya/dsync-cli/internal/provision"
)

// StatusReport 是 `dsync status` 的结果（--json 时原样输出，字段即契约）。
type StatusReport struct {
	Platform  platform.Platform     `json:"platform"`
	Geo       string                `json:"geo"`
	GeoFrom   string                `json:"geo_source"`
	Repo      *RepoInfo             `json:"repo,omitempty"`
	Git       *gitrepo.Status       `json:"git,omitempty"`
	Drift     []chezmoi.StatusEntry `json:"drift"`             // 目标与源不一致的文件（不含 run_ 脚本）
	Tools     int                   `json:"tools"`             // 全局配置中的工具数
	Missing   []string              `json:"missing_tools"`     // 未安装的工具
	Provision []string              `json:"provision_pending"` // 待执行的 provision 步骤
	Warnings  []string              `json:"warnings"`
}

// Clean 表示本机与仓库完全同步、无待办。
func (s StatusReport) Clean() bool {
	return len(s.Drift) == 0 && len(s.Missing) == 0 && len(s.Provision) == 0 && len(s.Warnings) == 0 &&
		s.Git != nil && s.Git.Ahead == 0 && s.Git.Behind == 0 && len(s.Git.Dirty) == 0
}

// Status 收集本机状态。各部分独立收集，某部分失败只记为警告，不中断整体。
func (e Env) Status(ctx context.Context) (StatusReport, error) {
	c, err := e.Load()
	if err != nil {
		return StatusReport{}, err
	}
	r := StatusReport{Platform: c.Platform, Drift: []chezmoi.StatusEntry{}, Missing: []string{}, Provision: []string{}, Warnings: []string{}}
	warn := func(format string, a ...any) { r.Warnings = append(r.Warnings, fmt.Sprintf(format, a...)) }

	if c.LocalErr != nil {
		warn("%v", c.LocalErr)
	}
	if geo, src, err := config.ResolveGeo(e.Getenv, c.Paths, c.Local); err != nil {
		warn("geo: %v", err)
	} else {
		r.Geo, r.GeoFrom = string(geo), string(src)
	}

	repo, err := e.LocateRepo(ctx, c)
	if err != nil {
		warn("%v", err)
	} else {
		r.Repo = &repo
		if gs, err := (gitrepo.Repo{R: e.Runner, Dir: repo.Root}).Status(ctx); err != nil {
			warn("git status: %v", err)
		} else {
			r.Git = &gs
		}
		if repo.Layout == LayoutV2 {
			if _, _, err := config.LoadManifest(repo.Root); err != nil {
				warn("%v", err)
			}
		}
	}

	if !e.has("chezmoi") {
		warn("未找到 chezmoi")
	} else {
		src := ""
		if r.Repo != nil {
			src = r.Repo.Source
		}
		if drift, err := e.chezmoi(src).Status(ctx, "scripts"); err != nil {
			warn("chezmoi status: %v", err)
		} else {
			r.Drift = drift
		}
	}

	if !e.has("mise") {
		warn("未找到 mise")
	} else if tools, err := e.mise().ListGlobal(ctx); err != nil {
		warn("mise ls: %v", err)
	} else {
		r.Tools = len(tools)
		for _, t := range mise.Missing(tools) {
			r.Missing = append(r.Missing, t.Name+"@"+t.Version)
		}
	}
	if _, items, err := e.provisionPlan(ctx, nil); err != nil {
		warn("provision: %v", err)
	} else {
		r.Provision = provision.PendingIDs(items)
	}
	return r, nil
}

// WriteJSON 以 JSON 输出报告。
func WriteJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// WriteText 以人类可读格式输出状态。
func (s StatusReport) WriteText(w io.Writer) {
	fmt.Fprintf(w, "平台    %s\n", s.Platform.Label())
	if s.Geo != "" {
		fmt.Fprintf(w, "镜像源  %s（来自 %s）\n", s.Geo, s.GeoFrom)
	}
	if s.Repo != nil {
		fmt.Fprintf(w, "仓库    %s [%s 布局，经 %s 定位]\n", s.Repo.Root, s.Repo.Layout, s.Repo.Via)
	}
	if g := s.Git; g != nil {
		up := g.Upstream
		if up == "" {
			up = "无上游"
		}
		fmt.Fprintf(w, "git     %s → %s", g.Branch, up)
		if g.Ahead > 0 {
			fmt.Fprintf(w, "  ↑%d 未推送", g.Ahead)
		}
		if g.Behind > 0 {
			fmt.Fprintf(w, "  ↓%d 待拉取", g.Behind)
		}
		if n := len(g.Dirty); n > 0 {
			fmt.Fprintf(w, "  %d 个未提交改动", n)
		}
		if g.Untracked > 0 {
			fmt.Fprintf(w, "  %d 个未跟踪文件", g.Untracked)
		}
		fmt.Fprintln(w)
	}
	if len(s.Drift) == 0 {
		fmt.Fprintln(w, "配置    已同步")
	} else {
		fmt.Fprintf(w, "配置    %d 个文件与仓库不一致:\n", len(s.Drift))
		for _, d := range s.Drift {
			fmt.Fprintf(w, "          %s%s %s\n", or(d.LastWrite, " "), or(d.Apply, " "), d.Path)
		}
		fmt.Fprintln(w, "          → 首列 = 本机改动，次列 = 部署将做的变更。保留本机改动: dsync capture <文件>；按仓库部署: dsync sync")
	}
	if len(s.Missing) == 0 {
		fmt.Fprintf(w, "工具    %d 个，全部已安装\n", s.Tools)
	} else {
		fmt.Fprintf(w, "工具    %d 个，缺失 %d 个: %s（dsync sync）\n", s.Tools, len(s.Missing), strings.Join(s.Missing, " "))
	}
	if len(s.Provision) > 0 {
		fmt.Fprintf(w, "系统    待执行: %s（dsync provision）\n", strings.Join(s.Provision, " "))
	}
	for _, x := range s.Warnings {
		fmt.Fprintf(w, "[WARN]  %s\n", x)
	}
}

func or(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
