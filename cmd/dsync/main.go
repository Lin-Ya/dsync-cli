// dsync — 跨设备开发环境同步工具（编排 mise + chezmoi + git）。
package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/Lin-Ya/dsync-cli/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := cli.Main(ctx, cli.DefaultEnv(os.Stdout, os.Stderr), os.Args[1:])
	stop()
	os.Exit(code)
}
