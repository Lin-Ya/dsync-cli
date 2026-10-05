package workflow

import (
	"context"
	"fmt"
	"strings"

	"github.com/Lin-Ya/dsync-cli/internal/config"
)

// localKey 是可经 `dsync config` 读写的 local.toml 配置项（show 的输出与 set 的校验共用这张表）。
type localKey struct {
	name string
	get  func(config.Local) string
	set  func(*config.Local, string)
	// redeploy：值注入 chezmoi 模板数据，修改后需重新生成 chezmoi 配置并 apply。
	redeploy bool
}

var localKeys = []localKey{
	{"user.name", func(l config.Local) string { return l.User.Name }, func(l *config.Local, v string) { l.User.Name = v }, true},
	{"user.email", func(l config.Local) string { return l.User.Email }, func(l *config.Local, v string) { l.User.Email = v }, true},
	{"tags", func(l config.Local) string { return strings.Join(l.Tags, ",") }, func(l *config.Local, v string) { l.Tags = splitList(v) }, true},
	proxyKey("proxy.http", func(p *config.Proxy) *string { return &p.HTTP }),
	proxyKey("proxy.https", func(p *config.Proxy) *string { return &p.HTTPS }),
	proxyKey("proxy.all", func(p *config.Proxy) *string { return &p.All }),
	proxyKey("proxy.no_proxy", func(p *config.Proxy) *string { return &p.NoProxy }),
	{"repo.path", func(l config.Local) string { return l.Repo.Path }, func(l *config.Local, v string) { l.Repo.Path = v }, false},
}

// proxyKey 构造 [proxy] 下的配置项；全部字段清空后去掉整个表（回到"未配置代理"）。
func proxyKey(name string, field func(*config.Proxy) *string) localKey {
	return localKey{
		name: name,
		get: func(l config.Local) string {
			if l.Proxy == nil {
				return ""
			}
			return *field(l.Proxy)
		},
		set: func(l *config.Local, v string) {
			if l.Proxy == nil {
				l.Proxy = &config.Proxy{}
			}
			*field(l.Proxy) = v
			if *l.Proxy == (config.Proxy{}) {
				l.Proxy = nil
			}
		},
	}
}

// splitList 解析逗号分隔的列表（忽略空项与首尾空白）。
func splitList(v string) []string {
	var out []string
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// LocalKeyNames 返回可经 dsync config 读写的配置项名（帮助与补全使用）。
func LocalKeyNames() []string {
	names := make([]string, len(localKeys))
	for i, k := range localKeys {
		names[i] = k.name
	}
	return names
}

func findLocalKey(name string) (localKey, error) {
	for _, k := range localKeys {
		if k.name == name {
			return k, nil
		}
	}
	if name == "geo" {
		return localKey{}, fmt.Errorf("geo 请用 dsync geo set cn|official 修改（需要重新渲染镜像相关配置）")
	}
	return localKey{}, fmt.Errorf("未知配置项 %q（可选: %s）", name, strings.Join(LocalKeyNames(), " | "))
}

// ConfigShow 输出 local.toml 的位置与各配置项的当前值。
func (e Env) ConfigShow() error {
	c, err := e.Load()
	if err != nil {
		return err
	}
	if c.LocalErr != nil {
		return c.LocalErr
	}
	file := c.Paths.LocalFile()
	if !c.LocalExists {
		file += "（尚未创建）"
	}
	e.logf("%-15s %s", "local.toml", file)
	if geo, src, err := config.ResolveGeo(e.Getenv, c.Paths, c.Local); err != nil {
		e.logf("%-15s %v", "geo", err)
	} else {
		e.logf("%-15s %s（来自 %s；修改: dsync geo set）", "geo", geo, src)
	}
	for _, k := range localKeys {
		e.logf("%-15s %s", k.name, or(k.get(c.Local), "（未设置）"))
	}
	return nil
}

// ConfigSet 修改 local.toml 的配置项（值为空 = 清除），取代手工编辑。
// 注入模板的项（git 身份、标签）修改后立即重新生成 chezmoi 配置并 apply，
// 不必再记得执行 dsync sync；代理由 shell-init 在开 shell 时读取，只需保存。
func (e Env) ConfigSet(ctx context.Context, pairs [][2]string) error {
	c, err := e.Load()
	if err != nil {
		return err
	}
	if c.LocalErr != nil {
		return fmt.Errorf("%w（为避免覆盖，请先修正该文件）", c.LocalErr)
	}
	redeploy, proxy := false, false
	for _, kv := range pairs {
		k, err := findLocalKey(kv[0])
		if err != nil {
			return err
		}
		v := strings.TrimSpace(kv[1])
		if k.name == "repo.path" && v != "" {
			info, ok := e.inspect(ctx, expandHome(v, c.Paths.Home))
			if !ok {
				return fmt.Errorf("%s 不是 dotfiles 仓库（缺少 dsync.toml 或 .chezmoi.toml.tmpl）", v)
			}
			v = info.Root
		}
		k.set(&c.Local, v)
		e.logf("  %s = %s", k.name, or(k.get(c.Local), "（已清除）"))
		redeploy = redeploy || k.redeploy
		proxy = proxy || strings.HasPrefix(k.name, "proxy.")
	}
	if err := config.SaveLocal(c.Paths, c.Local, e.writeFile); err != nil {
		return err
	}
	if proxy {
		e.logf("  代理在新开的终端中生效（或 exec zsh）")
	}
	if !redeploy {
		e.logf(">>> 已保存到 %s", c.Paths.LocalFile())
		return nil
	}
	repo, err := e.LocateRepo(ctx, c)
	if err != nil || repo.Layout != LayoutV2 {
		// 还没部署，或 v1 布局（模板数据不来自 local.toml）：只保存，之后的 init / sync 会用到
		e.logf(">>> 已保存到 %s（当前没有 v2 布局的 dotfiles 仓库，无需部署）", c.Paths.LocalFile())
		return nil
	}
	if err := e.require("chezmoi"); err != nil {
		return err
	}
	geo, _, err := config.ResolveGeo(e.Getenv, c.Paths, c.Local)
	if err != nil {
		return err
	}
	if err := e.configureChezmoi(ctx, c, repo, geo); err != nil {
		return err
	}
	if err := e.chezmoi(repo.Source).Apply(ctx); err != nil {
		return err
	}
	e.logf(">>> 已保存并部署（chezmoi apply）")
	return nil
}
