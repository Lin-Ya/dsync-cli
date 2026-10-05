package workflow

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/Lin-Ya/dsync-cli/internal/config"
	"github.com/Lin-Ya/dsync-cli/internal/exec"
)

// Layout 是 dotfiles 仓库的布局版本。
type Layout string

const (
	LayoutV1 Layout = "v1" // 工具与配置混合，仓库根即 chezmoi 源目录
	LayoutV2 Layout = "v2" // 仓库根含 dsync.toml，chezmoi 源目录由 .chezmoiroot 指定
)

// RepoInfo 描述定位到的 dotfiles 仓库。
type RepoInfo struct {
	Root   string `json:"root"`   // git 仓库根
	Source string `json:"source"` // chezmoi 源目录
	Via    string `json:"via"`    // 定位依据
	Layout Layout `json:"layout"`
}

// ErrRepoNotFound 表示找不到 dotfiles 仓库。
var ErrRepoNotFound = errors.New("找不到 dotfiles 仓库（已尝试: local.toml repo.path、$CHEZMOI_SOURCE_DIR、chezmoi source-path、~/dsync、~/dotfiles）\n" +
	"  首次使用请先运行: dsync init <仓库地址>（已有本地仓库: dsync init --source <目录>；还没有仓库: dsync init --create）")

// LocateRepo 按优先级定位 dotfiles 仓库：
// local.toml repo.path > $CHEZMOI_SOURCE_DIR > `chezmoi source-path` > v1 常见位置。
func (e Env) LocateRepo(ctx context.Context, c Context) (RepoInfo, error) {
	type cand struct{ dir, via string }
	var cands []cand
	if c.Local.Repo.Path != "" {
		cands = append(cands, cand{expandHome(c.Local.Repo.Path, c.Paths.Home), "local.toml repo.path"})
	}
	if d := e.Getenv("CHEZMOI_SOURCE_DIR"); d != "" {
		cands = append(cands, cand{d, "$CHEZMOI_SOURCE_DIR"})
	}
	if e.has("chezmoi") {
		if d, err := e.chezmoi("").SourcePath(ctx); err == nil && d != "" {
			cands = append(cands, cand{d, "chezmoi source-path"})
		}
	}
	for _, d := range []string{"dsync", "dotfiles"} {
		cands = append(cands, cand{filepath.Join(c.Paths.Home, d), "~/" + d})
	}

	for _, cd := range cands {
		if info, ok := e.inspect(ctx, cd.dir); ok {
			info.Via = cd.via
			return info, nil
		}
	}
	return RepoInfo{}, ErrRepoNotFound
}

// inspect 判断目录是否为 dotfiles 仓库（或其中的 chezmoi 源目录），并识别布局。
func (e Env) inspect(ctx context.Context, dir string) (RepoInfo, bool) {
	if dir == "" {
		return RepoInfo{}, false
	}
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return RepoInfo{}, false
	}
	root := dir
	if res, err := e.Runner.Query(ctx, exec.Cmd{Name: "git", Args: []string{"rev-parse", "--show-toplevel"}, Dir: dir}); err == nil {
		if top := strings.TrimSpace(string(res.Stdout)); top != "" {
			root = top
		}
	}
	if fileExists(filepath.Join(root, config.ManifestFile)) {
		src := root
		if b, err := os.ReadFile(filepath.Join(root, ".chezmoiroot")); err == nil {
			src = filepath.Join(root, strings.TrimSpace(string(b)))
		}
		return RepoInfo{Root: root, Source: src, Layout: LayoutV2}, true
	}
	if fileExists(filepath.Join(root, ".chezmoi.toml.tmpl")) {
		return RepoInfo{Root: root, Source: root, Layout: LayoutV1}, true
	}
	return RepoInfo{}, false
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func expandHome(p, home string) string {
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:])
	}
	return p
}
