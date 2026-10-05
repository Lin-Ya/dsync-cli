package workflow

import (
	"context"
	"fmt"

	"github.com/Lin-Ya/dsync-cli/internal/chezmoi"
	"github.com/Lin-Ya/dsync-cli/internal/config"
	"github.com/Lin-Ya/dsync-cli/internal/mirrors"
)

// GeoSet 切换镜像源。
//
// 修复 v1 switch-geo 的缺陷：v1 只改 ~/.config/dsync/geo 后 chezmoi apply，但模板读的是
// chezmoi 配置里的 data.geo（只在 chezmoi init 时生成），导致 .npmrc 等不切换、
// geo 文件还被改回。这里先把 geo 写入 local.toml，再以 DSYNC_GEO 重新 chezmoi init
// 生成配置，最后 apply——渲染型配置与 geo 文件都由 chezmoi 按新数据重写。
func (e Env) GeoSet(ctx context.Context, value string) error {
	geo, err := mirrors.ParseGeo(value)
	if err != nil {
		return err
	}
	c, err := e.Load()
	if err != nil {
		return err
	}
	if c.LocalErr != nil {
		return fmt.Errorf("%w（为避免覆盖，请先修正该文件）", c.LocalErr)
	}
	if err := e.require("chezmoi"); err != nil {
		return err
	}
	repo, err := e.LocateRepo(ctx, c)
	if err != nil {
		return err
	}
	cz := e.chezmoi(repo.Source)

	c.Local.Geo = string(geo)
	if err := config.SaveLocal(c.Paths, c.Local, e.writeFile); err != nil {
		return err
	}
	e.logf("[1/2] 本机配置 %s: geo = %q", c.Paths.LocalFile(), geo)

	if err := e.configureChezmoi(ctx, c, repo, geo); err != nil {
		return err
	}
	e.logf("[2/2] chezmoi apply（按 geo=%s 重新渲染配置）", geo)
	if err := cz.Apply(ctx); err != nil {
		return err
	}
	e.logf(">>> 已切换到 %s。重新打开终端（或 exec zsh）使环境变量生效。", geo)
	return nil
}

// reinitChezmoi 以指定 geo 重新生成 chezmoi 配置（chezmoi init）。
// init 会按模板重写整份配置；age recipient 来自环境变量，需显式保留现有值。
func (e Env) reinitChezmoi(ctx context.Context, cz chezmoi.Client, geo mirrors.Geo) error {
	recipient := e.Getenv("CHEZMOI_AGE_RECIPIENT")
	// 未安装 chezmoi（新机器上 init --dry-run）时不存在旧配置，无需读取
	if recipient == "" && e.has("chezmoi") {
		var err error
		if recipient, err = cz.AgeRecipient(ctx); err != nil {
			return fmt.Errorf("读取现有 chezmoi 配置失败（中止，避免丢失 age recipient）: %w", err)
		}
	}
	if err := cz.Init(ctx, "DSYNC_GEO="+string(geo), "CHEZMOI_AGE_RECIPIENT="+recipient); err != nil {
		return fmt.Errorf("生成 chezmoi 配置失败: %w", err)
	}
	return nil
}
