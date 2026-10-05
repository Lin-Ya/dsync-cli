package workflow

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"

	"github.com/Lin-Ya/dsync-cli/internal/config"
	"github.com/Lin-Ya/dsync-cli/internal/mirrors"
	"github.com/Lin-Ya/dsync-cli/internal/platform"
)

// chezmoiConfigHeader 写在生成的 chezmoi 配置开头。
const chezmoiConfigHeader = "# 由 dsync 生成（init / geo set / sync 时重写），请勿手改。\n" +
	"# 自定义模板数据请写在 dotfiles 仓库 dsync.toml 的 [data] 中。\n"

// ChezmoiData 返回注入 chezmoi 模板的数据（v2 布局）：
//
//	.<key>                 dsync.toml [data] 中的自定义数据
//	.dsync.geo             cn | official
//	.dsync.mirrors.<组件>  镜像地址，official 下为空串（模板据此回退官方源）
//	.dsync.wsl             是否 WSL
//	.dsync.tags            local.toml 中的机器标签
//	.dsync.user.name/email local.toml 中的 git 身份
//
// 镜像 URL 只来自 internal/mirrors，模板不再硬编码（v1 中 .npmrc / cargo / daemon.json 各写一份）。
func ChezmoiData(geo mirrors.Geo, p platform.Platform, l config.Local, m config.Manifest) (map[string]any, error) {
	urls, err := mirrors.TemplateData(geo)
	if err != nil {
		return nil, err
	}
	mirrorData := make(map[string]any, len(urls))
	for k, v := range urls {
		mirrorData[k] = v
	}
	tags := l.Tags
	if tags == nil {
		tags = []string{}
	}
	data := map[string]any{}
	for k, v := range m.Data {
		data[k] = v
	}
	data["dsync"] = map[string]any{
		"geo":     string(geo),
		"mirrors": mirrorData,
		"wsl":     p.WSL,
		"tags":    tags,
		"user":    map[string]any{"name": l.User.Name, "email": l.User.Email},
	}
	return data, nil
}

// chezmoiConfig 生成完整的 chezmoi 配置内容。
func chezmoiConfig(source string, data map[string]any, m config.Manifest) ([]byte, error) {
	cfg := map[string]any{"sourceDir": source, "data": data}
	if len(m.Secrets.AgeRecipients) > 0 {
		cfg["encryption"] = "age"
		cfg["age"] = map[string]any{
			"identity":   "~/.config/chezmoi/keys.txt",
			"recipients": m.Secrets.AgeRecipients,
		}
	}
	b, err := toml.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	return append([]byte(chezmoiConfigHeader), b...), nil
}

// chezmoiConfigPath 是 chezmoi 默认读取的配置文件位置。
func (e Env) chezmoiConfigPath(home string) string {
	dir := e.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "chezmoi", "chezmoi.toml")
}

// configureChezmoi 让 chezmoi 配置反映当前 geo / 本机配置 / 仓库清单：
//   - v2 布局：由 dsync 直接生成配置（取代仓库中的 .chezmoi.toml.tmpl）；
//   - v1 布局：仍按仓库 .chezmoi.toml.tmpl 执行 chezmoi init（DSYNC_GEO 经环境变量传入）。
func (e Env) configureChezmoi(ctx context.Context, c Context, repo RepoInfo, geo mirrors.Geo) error {
	if repo.Layout != LayoutV2 {
		return e.reinitChezmoi(ctx, e.chezmoi(repo.Source), geo)
	}
	m, _, err := config.LoadManifest(repo.Root)
	if err != nil {
		return err
	}
	data, err := ChezmoiData(geo, c.Platform, c.Local, m)
	if err != nil {
		return err
	}
	b, err := chezmoiConfig(repo.Source, data, m)
	if err != nil {
		return err
	}
	if err := e.writeFile(e.chezmoiConfigPath(c.Paths.Home), b, 0o600); err != nil {
		return fmt.Errorf("写入 chezmoi 配置失败: %w", err)
	}
	return nil
}
