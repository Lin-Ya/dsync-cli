package workflow

import (
	"fmt"

	"github.com/Lin-Ya/dsync-cli/internal/config"
	"github.com/Lin-Ya/dsync-cli/internal/mirrors"
	"github.com/Lin-Ya/dsync-cli/internal/shellinit"
)

// ShellInit 输出 shell 初始化脚本。
//
// 它在每次开 shell 时执行，因此配置错误不应让 shell 启动失败：
// local.toml / geo 有误时向 stderr 告警并回退默认 geo，仍输出可用脚本。
// 只有调用方式本身错误（不支持的 shell）才返回错误。
func (e Env) ShellInit(shell string) error {
	c, err := e.Load()
	if err != nil {
		return err
	}
	if c.LocalErr != nil {
		fmt.Fprintf(e.Err, "dsync: %v（shell-init 回退默认配置）\n", c.LocalErr)
	}
	geo, _, gerr := config.ResolveGeo(e.Getenv, c.Paths, c.Local)
	if gerr != nil {
		fmt.Fprintf(e.Err, "dsync: %v（回退 geo=%s）\n", gerr, mirrors.Default)
		geo = mirrors.Default
	}
	opts := shellinit.Options{Shell: shell, Geo: geo, Platform: c.Platform, Proxy: c.Local.Proxy}
	if opts.Proxy == nil && fileExists(c.Paths.V1ProxyFile()) {
		opts.V1ProxyFile = c.Paths.V1ProxyFile()
	}
	out, err := shellinit.Generate(opts)
	if err != nil {
		return err
	}
	_, err = fmt.Fprint(e.Out, out)
	return err
}
