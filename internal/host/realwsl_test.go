package host

import (
	"bytes"
	"context"
	"os"
	osexec "os/exec"
	"strings"
	"testing"

	"github.com/Lin-Ya/dsync-cli/internal/exec"
	"github.com/Lin-Ya/dsync-cli/internal/mirrors"
)

// 真实 WSL 端到端（默认跳过）：在一个临时发行版里用真实 wsl.exe 执行 wsl-init，
// 再检查发行版内 dsync status 完全收敛。只能在 Windows 上手动运行：
//
//	DSYNC_E2E_WSL_DISTRO   临时发行版名（会在其中安装软件，切勿指向日常使用的发行版）
//	DSYNC_E2E_SOURCE       Windows 侧 dotfiles 仓库目录（如 C:\...\m5src）
//	DSYNC_E2E_LINUX_BINARY Windows 侧 linux/amd64 dsync 路径
//	DSYNC_E2E_PROXY        可选，传给 --proxy（如 http://${WSL_HOST_IP}:7890）
//
// 准备步骤见 e2e/wsl-host.sh。
func TestRealWSL_WSLInit(t *testing.T) {
	distro := os.Getenv("DSYNC_E2E_WSL_DISTRO")
	if distro == "" {
		t.Skip("未设置 DSYNC_E2E_WSL_DISTRO")
	}
	x := &Context{
		R: exec.Real{}, Getenv: os.Getenv,
		Distro: distro, Geo: mirrors.Official, Source: os.Getenv("DSYNC_E2E_SOURCE"), Proxy: os.Getenv("DSYNC_E2E_PROXY"),
		LinuxBinary: func(context.Context) (string, error) { return os.Getenv("DSYNC_E2E_LINUX_BINARY"), nil },
		Log:         t.Logf,
	}
	// 跑两次：第二次必须同样成功（dsync init 幂等，仓库会被重新拷贝）
	for i := 1; i <= 2; i++ {
		if err := (wslInitStep{}).Apply(context.Background(), x); err != nil {
			t.Fatalf("第 %d 次 wsl-init: %v", i, err)
		}
	}
	out, err := osexec.Command("wsl.exe", "-d", distro, "--exec", "bash", "-lc", "~/.local/bin/dsync status --json").CombinedOutput()
	if err != nil {
		t.Fatalf("dsync status: %v\n%s", err, out)
	}
	t.Logf("status:\n%s", out)
	for _, k := range []string{"drift", "missing_tools", "provision_pending"} {
		if !bytes.Contains(out, []byte(`"`+k+`": []`)) {
			t.Errorf("status.%s 非空（未收敛）", k)
		}
	}
	if !strings.Contains(string(out), `"geo": "official"`) {
		t.Error("geo 应为 official")
	}
}
