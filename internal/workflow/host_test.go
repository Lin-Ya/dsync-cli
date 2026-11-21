package workflow

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// hostEnvFixture：模拟 Windows 宿主（GOOS=windows），宿主已就绪，只剩 wsl-init。
func hostEnvFixture(t *testing.T) (*fixture, Env) {
	t.Helper()
	f := newFixture(t)
	root := t.TempDir()
	for k, v := range map[string]string{"LOCALAPPDATA": "Local", "APPDATA": "Roaming", "ProgramFiles": "PF", "USERPROFILE": "Kevin"} {
		f.env[k] = filepath.Join(root, v)
	}
	f.env["USERNAME"] = "Kevin"
	f.env["WSL_INSTALL_DIR"] = ""
	f.tools("winget", "git", "ssh-add")
	os.MkdirAll(filepath.Join(f.env["USERPROFILE"], ".ssh"), 0o700)
	os.WriteFile(filepath.Join(f.env["USERPROFILE"], ".ssh", "id_ed25519"), []byte("key"), 0o600) // ssh-add -l 由兜底响应返回成功
	os.MkdirAll(filepath.Join(f.env["LOCALAPPDATA"], "Packages", "Microsoft.WindowsTerminal_x"), 0o755)
	os.MkdirAll(filepath.Join(f.env["ProgramFiles"], "Docker", "Docker"), 0o755)
	os.WriteFile(filepath.Join(f.env["ProgramFiles"], "Docker", "Docker", "Docker Desktop.exe"), nil, 0o644)
	os.MkdirAll(filepath.Join(f.env["APPDATA"], "Docker"), 0o755)
	os.WriteFile(filepath.Join(f.env["APPDATA"], "Docker", "settings-store.json"),
		[]byte(`{"WslEngineEnabled": true, "IntegratedWslDistros": ["Ubuntu-24.04"]}`), 0o644)

	f.rec.OnStdout("", "")
	f.rec.OnStdout("wsl.exe --list --quiet", "Ubuntu-24.04\r\n")
	f.rec.OnStdout("powershell.exe -NoProfile -NonInteractive -Command '$s = Get-Service", "Running|Automatic")
	e := f.Env()
	e.GOOS = "windows"
	return f, e
}

func TestHostSetup_ReachesWSLInitWhenHostReady(t *testing.T) {
	f, e := hostEnvFixture(t)
	bin := f.write(t, "dsync_linux_amd64", "elf")
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, ".chezmoi.toml.tmpl"), nil, 0o644)
	err := e.HostSetup(context.Background(), HostOptions{Source: src, LinuxBinary: bin, InstallDirSet: true})
	// 宿主已就绪：前四步均为 [完成]，执行到 wsl-init。测试在 Linux 上运行，本地路径不是
	// Windows 盘符路径，ToWSLPath 会在这里拒绝（脚本内容由 host 包的测试覆盖）。
	if err == nil || !strings.Contains(err.Error(), "不是 Windows 盘符绝对路径") {
		t.Fatalf("err = %v\n%s", err, f.out.String())
	}
	for _, want := range []string{"[完成] packages", "[完成] wsl-distro", "[完成] docker-integration", "[完成] ssh-agent", "[待办] wsl-init"} {
		if !strings.Contains(f.out.String(), want) {
			t.Errorf("计划缺少 %q:\n%s", want, f.out.String())
		}
	}
	for _, l := range f.rec.Lines() {
		if strings.Contains(l, "Security.Principal") {
			t.Error("没有需要管理员的待办时不应检查管理员权限")
		}
	}
}

func TestHostSetup_Guards(t *testing.T) {
	f, e := hostEnvFixture(t)
	ctx := context.Background()

	linux := f.Env() // GOOS=linux
	if err := linux.HostSetup(ctx, HostOptions{}); err == nil || !strings.Contains(err.Error(), "dsync init") {
		t.Errorf("非 Windows 应拒绝并提示 dsync init: %v", err)
	}
	if err := e.HostSetup(ctx, HostOptions{Repo: "x", Source: "y"}); err == nil || !strings.Contains(err.Error(), "二选一") {
		t.Errorf("二选一: %v", err)
	}
	if err := e.HostSetup(ctx, HostOptions{Source: t.TempDir()}); err == nil || !strings.Contains(err.Error(), "不是 dotfiles 仓库") {
		t.Errorf("非仓库目录: %v", err)
	}
	if err := e.HostSetup(ctx, HostOptions{Geo: "mars"}); err == nil {
		t.Error("非法 geo 应报错")
	}

	// 需要管理员的待办（ssh-agent 未启动）且当前不是管理员 → 拒绝执行
	f.rec.OnStdout("powershell.exe -NoProfile -NonInteractive -Command '$s = Get-Service", "Stopped|Manual")
	f.rec.OnStdout("powershell.exe -NoProfile -NonInteractive -Command '([Security.Principal", "False")
	err := e.HostSetup(ctx, HostOptions{Repo: "https://x/y.git", InstallDirSet: true})
	if err == nil || !strings.Contains(err.Error(), "ssh-agent 需要管理员权限") {
		t.Errorf("非管理员应拒绝: %v", err)
	}
	for _, c := range f.rec.Calls {
		if c.Mutating {
			t.Errorf("拒绝时不得执行任何写操作: %s", c.Cmd)
		}
	}
}

func TestHostSetup_PausesForRerun(t *testing.T) {
	f, e := hostEnvFixture(t)
	f.rec.OnStdout("wsl.exe --list --quiet", "") // 没有发行版
	f.rec.OnStdout("powershell.exe -NoProfile -NonInteractive -Command '([Security.Principal", "True")
	if err := e.HostSetup(context.Background(), HostOptions{Repo: "https://x/y.git", InstallDirSet: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.out.String(), ">>> 已暂停：WSL 平台刚启用，需要重启 Windows") {
		t.Errorf("安装后未注册应暂停并提示重启:\n%s", f.out.String())
	}
}

func TestHostCheck_DefaultsAndCwdSource(t *testing.T) {
	_, e := hostEnvFixture(t)
	repo := t.TempDir()
	os.WriteFile(filepath.Join(repo, "dsync.toml"), []byte("version = 1\n"), 0o644)
	wd, _ := os.Getwd()
	t.Cleanup(func() { os.Chdir(wd) })
	os.Chdir(repo)

	x, err := e.hostContext(HostOptions{})
	if err != nil {
		t.Fatal(err)
	}
	real, _ := filepath.EvalSymlinks(repo)
	got, _ := filepath.EvalSymlinks(x.Source)
	if got != real || x.Distro != "Ubuntu-24.04" || x.WSLUser != "kevin" || x.Geo != "cn" || x.InstallDir != "" {
		t.Errorf("默认值: source=%s distro=%s user=%s geo=%s dir=%q", x.Source, x.Distro, x.WSLUser, x.Geo, x.InstallDir)
	}
	r, err := e.HostCheck(context.Background(), HostOptions{})
	if err != nil || len(r.Items) != 5 {
		t.Errorf("HostCheck: %+v %v", r, err)
	}
}

func TestDefaultInstallDir(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	yes := func(string) bool { return true }
	no := func(string) bool { return false }
	if got := defaultInstallDir(env(map[string]string{"WSL_INSTALL_DIR": `E:\WSL`}), yes); got != `E:\WSL` {
		t.Errorf("环境变量优先: %q", got)
	}
	if got := defaultInstallDir(env(nil), yes); got != `D:\Linux` {
		t.Errorf("D 盘存在: %q", got)
	}
	if got := defaultInstallDir(env(nil), no); got != "" {
		t.Errorf("D 盘不存在时用系统默认位置: %q", got)
	}
}

func TestLinuxBinary_DownloadsSameVersionFromRelease(t *testing.T) {
	f, e := hostEnvFixture(t)
	srv := fakeReleaseAsset(t, "v2.0.0-alpha.3", "dsync_linux_amd64", "linux-elf")
	f.env["DSYNC_GITHUB_API"] = srv.URL
	f.env["GITHUB_TOKEN"] = "tok"
	e.HTTP = srv.Client()
	t.Setenv("TMPDIR", t.TempDir())
	p, err := e.linuxBinary(context.Background(), HostOptions{Version: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "linux-elf" || !strings.Contains(p, "dsync-v2.0.0-alpha.3") {
		t.Errorf("%s %q", p, b)
	}

	f.env["GITHUB_TOKEN"] = ""
	if _, err := e.linuxBinary(context.Background(), HostOptions{}); err != nil {
		t.Errorf("公开仓库无 Token 也应能下载: %v", err)
	}
	if _, err := e.linuxBinary(context.Background(), HostOptions{LinuxBinary: "/no/such/file"}); err == nil {
		t.Error("指定的文件不存在应报错")
	}
}
