package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/Lin-Ya/dsync-cli/internal/exec"
	"github.com/Lin-Ya/dsync-cli/internal/mirrors"
	"github.com/Lin-Ya/dsync-cli/internal/provision"
)

// utf16le 模拟 wsl.exe 的输出编码。
func utf16le(s string, bom bool) []byte {
	var b []byte
	if bom {
		b = append(b, 0xFF, 0xFE)
	}
	for _, u := range utf16.Encode([]rune(s)) {
		b = append(b, byte(u), byte(u>>8))
	}
	return b
}

// fakeWSL 是有状态的 wsl.exe：记录已注册的发行版，install / export / import 会改变状态并产生文件。
type fakeWSL struct {
	distros       []string
	installWorks  bool // false：模拟 WSL 平台刚启用、需要重启（安装命令成功但发行版未注册）
	webDownloadOK bool
	exportWorks   bool
}

type hostFixture struct {
	x    *Context
	rec  *exec.Recording
	env  map[string]string
	wsl  *fakeWSL
	logs []string
}

func newHostFixture(t *testing.T) *hostFixture {
	t.Helper()
	root := t.TempDir()
	env := map[string]string{
		"LOCALAPPDATA": filepath.Join(root, "Local"),
		"APPDATA":      filepath.Join(root, "Roaming"),
		"ProgramFiles": filepath.Join(root, "Program Files"),
		"USERPROFILE":  filepath.Join(root, "Users", "Kevin"),
	}
	f := &hostFixture{
		rec: &exec.Recording{Paths: map[string]string{}},
		env: env,
		wsl: &fakeWSL{installWorks: true, webDownloadOK: true, exportWorks: true},
	}
	f.x = &Context{
		R: f.rec, Getenv: func(k string) string { return env[k] },
		Distro: "Ubuntu-24.04", InstallDir: filepath.Join(root, "Linux"), WSLUser: "kevin", Geo: mirrors.CN,
		LinuxBinary: func(context.Context) (string, error) {
			return `C:\Users\Kevin\AppData\Local\Temp\dsync_linux_amd64`, nil
		},
		Log:       func(format string, a ...any) { f.logs = append(f.logs, format) },
		Timestamp: func() string { return "202610051200" },
	}
	f.rec.OnStdout("", "") // 兜底：未单独预设的命令成功
	f.rec.OnFunc("wsl.exe", f.handleWSL)
	return f
}

func (f *hostFixture) handleWSL(c exec.Cmd) exec.Result {
	a := c.Args
	switch {
	case len(a) >= 2 && a[0] == "--list" && a[1] == "--quiet":
		if len(f.wsl.distros) == 0 {
			return exec.Result{ExitCode: 0xFFFFFFFF & 1, Stdout: utf16le("适用于 Linux 的 Windows 子系统没有已安装的分发。\r\n", false)}
		}
		return exec.Result{Stdout: utf16le(strings.Join(f.wsl.distros, "\r\n")+"\r\n", false)}
	case a[0] == "--install":
		web := len(a) == 4 && a[3] == "--web-download"
		if web && !f.wsl.webDownloadOK {
			return exec.Result{ExitCode: 1}
		}
		if f.wsl.installWorks {
			f.wsl.distros = append(f.wsl.distros, a[2])
		}
	case a[0] == "--export":
		if f.wsl.exportWorks {
			os.WriteFile(a[2], []byte("tar"), 0o644)
		}
	case a[0] == "--unregister":
		f.wsl.distros = nil
	case a[0] == "--import":
		os.MkdirAll(a[2], 0o755)
		os.WriteFile(filepath.Join(a[2], "ext4.vhdx"), nil, 0o644)
		f.wsl.distros = append(f.wsl.distros, a[1])
	}
	return exec.Result{}
}

func (f *hostFixture) mutating() []string {
	var out []string
	for _, c := range f.rec.Calls {
		if c.Mutating {
			out = append(out, exec.Cmd{Name: c.Cmd.Name, Args: c.Cmd.Args}.String())
		}
	}
	return out
}

func check(t *testing.T, s Step, x *Context) (provision.Result, bool) {
	t.Helper()
	r, admin, err := s.Check(context.Background(), x)
	if err != nil {
		t.Fatal(err)
	}
	return r, admin
}

func write(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ---------- 纯函数 ----------

func TestDecodeWSL(t *testing.T) {
	cases := map[string][]byte{
		"UTF-16LE":     utf16le("docker-desktop\r\nUbuntu-24.04\r\n", false),
		"UTF-16LE+BOM": utf16le("docker-desktop\r\nUbuntu-24.04\r\n", true),
		"UTF-8":        []byte("docker-desktop\r\nUbuntu-24.04\r\n"),
	}
	for name, b := range cases {
		if got := DecodeWSL(b); got != "docker-desktop\nUbuntu-24.04\n" {
			t.Errorf("%s: %q", name, got)
		}
	}
	if got := DecodeWSL(utf16le("中文提示", false)); got != "中文提示" {
		t.Errorf("不含 NUL 的纯中文 UTF-16 也应正确解码: %q", got)
	}
	if got := DecodeWSL(append(utf16le("中文", false), 'x')); got != "中文" {
		t.Errorf("奇数长度应丢弃末字节: %q", got)
	}
	if got := DecodeWSL([]byte("Ubuntu-24.04 中文\n")); got != "Ubuntu-24.04 中文\n" {
		t.Errorf("合法 UTF-8 应原样返回: %q", got)
	}
}

func TestToWSLPath(t *testing.T) {
	ok := map[string]string{
		`D:\Develop\dsync`:     "/mnt/d/Develop/dsync",
		`C:\Users\Kevin\a b\x`: "/mnt/c/Users/Kevin/a b/x",
		`c:/Users/Kevin/`:      "/mnt/c/Users/Kevin/",
		`E:\`:                  "/mnt/e/",
	}
	for in, want := range ok {
		if got, err := ToWSLPath(in); err != nil || got != want {
			t.Errorf("ToWSLPath(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{`relative\path`, `\\server\share`, "/home/u", "C:"} {
		if _, err := ToWSLPath(bad); err == nil {
			t.Errorf("ToWSLPath(%q) 应报错", bad)
		}
	}
}

func TestSanitizeUserAndWinBase(t *testing.T) {
	cases := map[string]string{"Kevin": "kevin", "林 Ya": "ya", "": "dev", "中文": "dev", "1abc": "u1abc", "A.b_c-D": "a.b_c-d"}
	for in, want := range cases {
		if got := SanitizeUser(in); got != want {
			t.Errorf("SanitizeUser(%q) = %q, want %q", in, got, want)
		}
	}
	if got := SanitizeUser(strings.Repeat("a", 40)); len(got) != 31 {
		t.Errorf("应截断到 31: %d", len(got))
	}
	for in, want := range map[string]string{`D:\Develop\dsync`: "dsync", `D:\Develop\dsync\`: "dsync", "C:/a/b": "b", "x": "x"} {
		if got := winBase(in); got != want {
			t.Errorf("winBase(%q) = %q", in, got)
		}
	}
}

// ---------- packages ----------

func TestPackages(t *testing.T) {
	f := newHostFixture(t)
	s := packagesStep{}
	if r, _ := check(t, s, f.x); r.State != provision.Manual || !strings.Contains(r.Detail, "App Installer") {
		t.Errorf("无 winget 应为 manual: %+v", r)
	}
	f.rec.Paths["winget"] = `C:\winget.exe`
	r, admin := check(t, s, f.x)
	if r.State != provision.Pending || r.Detail != "winget 安装 Git / Windows Terminal / Docker Desktop" || !admin {
		t.Fatalf("%+v admin=%v", r, admin)
	}
	err := s.Apply(context.Background(), f.x)
	var rerun *RerunError
	if !errors.As(err, &rerun) || !strings.Contains(rerun.Reason, "重启 Windows") {
		t.Errorf("装了 Docker Desktop 应要求重启后重跑: %v", err)
	}
	if got := f.mutating(); len(got) != 3 || got[2] != "winget install --id Docker.DockerDesktop -e --accept-package-agreements --accept-source-agreements" {
		t.Errorf("%v", got)
	}

	// 全部已装：git 在 PATH、Terminal 包目录存在、Docker Desktop.exe 存在
	f.rec.Paths["git"] = `C:\git.exe`
	write(t, filepath.Join(f.env["LOCALAPPDATA"], "Packages", "Microsoft.WindowsTerminal_8wekyb3d8bbwe", "x"), "")
	write(t, filepath.Join(f.env["ProgramFiles"], "Docker", "Docker", "Docker Desktop.exe"), "")
	if r, _ := check(t, s, f.x); r.State != provision.Done {
		t.Errorf("%+v", r)
	}
}

// ---------- wsl-distro ----------

func TestDistro_InstallThenMigrate(t *testing.T) {
	f := newHostFixture(t)
	s := distroStep{}
	r, admin := check(t, s, f.x)
	if r.State != provision.Pending || !admin || !strings.Contains(r.Detail, "并迁移到") {
		t.Fatalf("%+v admin=%v", r, admin)
	}
	if err := s.Apply(context.Background(), f.x); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(f.x.InstallDir, "Ubuntu-24.04")
	backup := filepath.Join(dir, "Ubuntu-24.04-backup-202610051200.tar")
	got := f.mutating()
	wsl := func(args ...string) string { return exec.Cmd{Name: "wsl.exe", Args: args}.String() } // 与实际一致的引号规则（Windows 路径含 \）
	want := []string{
		wsl("--install", "-d", "Ubuntu-24.04", "--web-download"),
		wsl("--shutdown"),
		wsl("--export", "Ubuntu-24.04", backup),
		wsl("--unregister", "Ubuntu-24.04"),
		wsl("--import", "Ubuntu-24.04", dir, backup),
	}
	if len(got) != 7 || !reflect.DeepEqual(got[:5], want) || got[6] != "wsl.exe --shutdown" {
		t.Fatalf("命令序列:\n%s", strings.Join(got, "\n"))
	}
	if !strings.HasPrefix(got[5], "wsl.exe -d Ubuntu-24.04 -u root --exec sh -c ") ||
		!strings.Contains(got[5], "useradd -m -s /bin/bash") || !strings.Contains(got[5], "NOPASSWD") || !strings.Contains(got[5], "default=") {
		t.Errorf("默认用户脚本: %s", got[5])
	}
	if r, _ := check(t, s, f.x); r.State != provision.Done {
		t.Errorf("迁移后应为 done: %+v", r)
	}
}

func TestDistro_RerunCases(t *testing.T) {
	var rerun *RerunError

	f := newHostFixture(t)
	f.wsl.installWorks = false
	if err := (distroStep{}).Apply(context.Background(), f.x); !errors.As(err, &rerun) || !strings.Contains(rerun.Reason, "重启 Windows") {
		t.Errorf("安装后仍未注册应要求重启: %v", err)
	}

	f = newHostFixture(t)
	f.x.InstallDir = ""
	if err := (distroStep{}).Apply(context.Background(), f.x); !errors.As(err, &rerun) || !strings.Contains(rerun.Reason, "用户名") {
		t.Errorf("无自定义目录时应提示首次登录创建用户: %v", err)
	}
	if r, _ := check(t, distroStep{}, f.x); r.State != provision.Done {
		t.Errorf("已注册且无自定义目录应为 done: %+v", r)
	}
}

func TestDistro_FallbackAndExportFailure(t *testing.T) {
	f := newHostFixture(t)
	f.wsl.webDownloadOK = false
	f.x.InstallDir = ""
	(distroStep{}).Apply(context.Background(), f.x)
	if got := f.mutating(); len(got) != 2 || got[1] != "wsl.exe --install -d Ubuntu-24.04" {
		t.Errorf("--web-download 失败应退回 Store 渠道: %v", got)
	}

	f = newHostFixture(t)
	f.wsl.distros = []string{"Ubuntu-24.04"}
	f.wsl.exportWorks = false
	r, admin := check(t, distroStep{}, f.x)
	if r.State != provision.Pending || admin || !strings.Contains(r.Detail, "迁移") {
		t.Errorf("已注册但不在目标目录: %+v admin=%v", r, admin)
	}
	err := (distroStep{}).Apply(context.Background(), f.x)
	if err == nil || !strings.Contains(err.Error(), "未迁移") {
		t.Errorf("导出未产生备份应中止: %v", err)
	}
	for _, c := range f.mutating() {
		if strings.Contains(c, "--unregister") {
			t.Fatal("没有备份时绝不能 unregister")
		}
	}
}

func TestDistro_ExactNameMatch(t *testing.T) {
	f := newHostFixture(t)
	f.wsl.distros = []string{"Ubuntu-24.04-test", "docker-desktop"}
	f.x.InstallDir = ""
	if r, _ := check(t, distroStep{}, f.x); r.State != provision.Pending {
		t.Errorf("Ubuntu-24.04-test 不应被当作 Ubuntu-24.04: %+v", r)
	}
}

// ---------- docker-integration ----------

// 字段取自真实的 settings-store.json（Docker Desktop 4.4x）。
const dockerSettings = `{
  "AutoStart": true,
  "EnableIntegrationWithDefaultWslDistro": false,
  "IntegratedWslDistros": [],
  "WslEngineEnabled": true,
  "SettingsVersion": 40,
  "LastContainerdSnapshotterEnable": 1730000000123
}`

func dockerFixture(t *testing.T, settings string) (*hostFixture, string) {
	f := newHostFixture(t)
	write(t, filepath.Join(f.env["ProgramFiles"], "Docker", "Docker", "Docker Desktop.exe"), "")
	p := filepath.Join(f.env["APPDATA"], "Docker", "settings-store.json")
	if settings != "" {
		write(t, p, settings)
	}
	return f, p
}

func TestDocker_EnablesIntegrationPreservingOtherKeys(t *testing.T) {
	f, p := dockerFixture(t, dockerSettings)
	s := dockerStep{}
	if r, _ := check(t, s, f.x); r.State != provision.Pending || !strings.Contains(r.Detail, "settings-store.json") {
		t.Fatalf("%+v", r)
	}
	if err := s.Apply(context.Background(), f.x); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, p)
	for _, want := range []string{`"IntegratedWslDistros": [` + "\n" + `    "Ubuntu-24.04"`, `"AutoStart": true`, `"LastContainerdSnapshotterEnable": 1730000000123`, `"SettingsVersion": 40`} {
		if !strings.Contains(got, want) {
			t.Errorf("缺少 %q:\n%s", want, got)
		}
	}
	if readFile(t, p+".bak.dsync") != dockerSettings {
		t.Error("应保留原文件备份")
	}
	if r, _ := check(t, s, f.x); r.State != provision.Done {
		t.Errorf("写入后应为 done: %+v", r)
	}
	// 再次写入不覆盖已有备份
	write(t, p, strings.Replace(dockerSettings, `"WslEngineEnabled": true`, `"WslEngineEnabled": false`, 1))
	s.Apply(context.Background(), f.x)
	if readFile(t, p+".bak.dsync") != dockerSettings {
		t.Error("备份只应在首次写入时创建")
	}
}

func TestDocker_LegacyAndManualCases(t *testing.T) {
	f, _ := dockerFixture(t, "")
	write(t, filepath.Join(f.env["APPDATA"], "Docker", "settings.json"), `{"wslEngineEnabled": false, "integratedWslDistros": ["Ubuntu-24.04"]}`)
	if r, _ := check(t, dockerStep{}, f.x); r.State != provision.Pending || !strings.Contains(r.Detail, "settings.json") {
		t.Errorf("旧版 settings.json（小驼峰）: %+v", r)
	}

	cases := map[string]string{
		"":                              "首次打开 Docker Desktop",
		`{"SomethingElse": 1}`:          "没有已知的 WSL 集成项",
		`{"IntegratedWslDistros": "x"}`: "无法解析",
		`not json`:                      "无法解析",
	}
	for settings, want := range cases {
		f, _ := dockerFixture(t, settings)
		if r, _ := check(t, dockerStep{}, f.x); r.State != provision.Manual || !strings.Contains(r.Detail, want) {
			t.Errorf("settings=%q: %+v", settings, r)
		}
	}

	f = newHostFixture(t) // 未安装 Docker Desktop
	if r, _ := check(t, dockerStep{}, f.x); r.State != provision.Done {
		t.Errorf("未安装 Docker 时无需集成: %+v", r)
	}
}

// ---------- ssh-agent ----------

const svcQuery = "powershell.exe -NoProfile -NonInteractive -Command '$s = Get-Service"

func TestSSHAgent(t *testing.T) {
	f := newHostFixture(t)
	s := sshAgentStep{}
	f.rec.OnStdout(svcQuery, "missing\r\n")
	if r, _ := check(t, s, f.x); r.State != provision.Manual || !strings.Contains(r.Detail, "Add-WindowsCapability") {
		t.Errorf("%+v", r)
	}
	f.rec.OnStdout(svcQuery, "Stopped|Manual\r\n")
	r, admin := check(t, s, f.x)
	if r.State != provision.Pending || !admin || !strings.Contains(r.Detail, "Stopped|Manual") {
		t.Errorf("%+v admin=%v", r, admin)
	}
	if err := s.Apply(context.Background(), f.x); err != nil {
		t.Fatal(err)
	}
	if got := f.mutating(); len(got) != 1 || !strings.Contains(got[0], "Set-Service -Name ssh-agent -StartupType Automatic") {
		t.Errorf("%v", got)
	}

	f.rec.OnStdout(svcQuery, "Running|Automatic\r\n")
	if r, _ := check(t, s, f.x); r.State != provision.Manual || !strings.Contains(r.Detail, "未找到默认密钥") {
		t.Errorf("无默认密钥: %+v", r)
	}
	write(t, defaultKey(f.x), "key")
	f.rec.Paths["ssh-add"] = `C:\Windows\System32\OpenSSH\ssh-add.exe`
	f.rec.On("ssh-add -l", exec.Result{ExitCode: 1})
	r, admin = check(t, s, f.x)
	if r.State != provision.Pending || admin {
		t.Errorf("agent 无密钥应 pending 且无需管理员: %+v %v", r, admin)
	}
	f.rec.Calls = nil
	s.Apply(context.Background(), f.x)
	if got := f.mutating(); len(got) != 1 || got[0] != (exec.Cmd{Name: "ssh-add", Args: []string{defaultKey(f.x)}}).String() {
		t.Errorf("%v", got)
	}
	f.rec.On("ssh-add -l", exec.Result{ExitCode: 2})
	if r, _ := check(t, s, f.x); r.State != provision.Manual {
		t.Errorf("无法连接 agent: %+v", r)
	}
	f.rec.OnStdout("ssh-add -l", "256 SHA256:x kevin (ED25519)\n")
	if r, _ := check(t, s, f.x); r.State != provision.Done {
		t.Errorf("%+v", r)
	}
}

// ---------- wsl-init ----------

func TestWSLInit_ScriptAndApply(t *testing.T) {
	f := newHostFixture(t)
	f.x.Source = `D:\Develop\dsync`
	script, err := Script(f.x, `C:\Users\Kevin\AppData\Local\Temp\dsync_linux_amd64`)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"set -e",
		"mkdir -p ~/.local/bin",
		"install -m 755 '/mnt/c/Users/Kevin/AppData/Local/Temp/dsync_linux_amd64' ~/.local/bin/dsync",
		"sudo rm -rf ~/dsync",
		"sudo mkdir -p ~/dsync",
		"sudo cp -a '/mnt/d/Develop/dsync/.' ~/dsync/",
		`sudo chown -R "$(id -un):$(id -gn)" ~/dsync`,
		"~/.local/bin/dsync init --source ~/dsync --geo cn",
	}, "\n")
	if script != want {
		t.Errorf("脚本:\n%s\nwant:\n%s", script, want)
	}
	if err := (wslInitStep{}).Apply(context.Background(), f.x); err != nil {
		t.Fatal(err)
	}
	if got := f.mutating(); len(got) != 1 || !strings.HasPrefix(got[0], "wsl.exe -d Ubuntu-24.04 --exec bash -lc ") {
		t.Errorf("%v", got)
	}

	f.x.Source, f.x.Repo, f.x.Geo = "", "https://github.com/me/dot's.git", mirrors.Official
	script, _ = Script(f.x, `C:\t\dsync`)
	if !strings.HasSuffix(script, `~/.local/bin/dsync init 'https://github.com/me/dot'\''s.git' --geo official`) || strings.Contains(script, "sudo") {
		t.Errorf("仓库地址应安全转义且不拷贝: %s", script)
	}

	f.x.LinuxBinary = func(context.Context) (string, error) { return "", errors.New("需要 GitHub Token") }
	if err := (wslInitStep{}).Apply(context.Background(), f.x); err == nil || !strings.Contains(err.Error(), "Token") {
		t.Errorf("获取二进制失败应报错: %v", err)
	}
}

// ---------- Plan / Apply ----------

func TestPlanApply_StopsOnRerunAndReportsAdmin(t *testing.T) {
	f := newHostFixture(t)
	f.rec.Paths["winget"] = `C:\winget.exe`
	f.rec.OnStdout(svcQuery, "Stopped|Disabled")
	items, err := Plan(context.Background(), f.x)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, it := range items {
		ids = append(ids, it.ID)
	}
	if strings.Join(ids, " ") != "packages wsl-distro docker-integration ssh-agent wsl-init" {
		t.Errorf("顺序: %v", ids)
	}
	if got := NeedsAdmin(items); strings.Join(got, " ") != "packages wsl-distro ssh-agent" {
		t.Errorf("NeedsAdmin = %v", got)
	}
	out, err := Apply(context.Background(), f.x, items)
	if err != nil || !strings.Contains(out.Rerun, "Docker Desktop") {
		t.Fatalf("应在装完 Docker Desktop 后停止: %+v %v", out, err)
	}
	for _, c := range f.mutating() {
		if strings.HasPrefix(c, "wsl.exe") {
			t.Errorf("停止后不应继续执行后续步骤: %s", c)
		}
	}
}

func TestIsAdmin(t *testing.T) {
	f := newHostFixture(t)
	f.rec.OnStdout("powershell.exe -NoProfile -NonInteractive -Command '([Security.Principal", "True\r\n")
	if ok, err := IsAdmin(context.Background(), f.x); !ok || err != nil {
		t.Errorf("%v %v", ok, err)
	}
	f.rec.OnStdout("powershell.exe -NoProfile -NonInteractive -Command '([Security.Principal", "False\r\n")
	if ok, _ := IsAdmin(context.Background(), f.x); ok {
		t.Error("应为 false")
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestWSLInit_ProxyExportedForInitOnly(t *testing.T) {
	f := newHostFixture(t)
	f.x.Repo = "https://github.com/me/dotfiles.git"
	f.x.Proxy = "http://${WSL_HOST_IP}:7890"
	script, err := Script(f.x, `C:\t\dsync`)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(script, "\n")
	want := []string{
		`export WSL_HOST_IP="$(ip route show default 2>/dev/null | awk '{print $3; exit}')"`,
		`export http_proxy="http://${WSL_HOST_IP}:7890" https_proxy="http://${WSL_HOST_IP}:7890" HTTP_PROXY="http://${WSL_HOST_IP}:7890" HTTPS_PROXY="http://${WSL_HOST_IP}:7890"`,
		`export no_proxy="localhost,127.0.0.1,archive.ubuntu.com,security.ubuntu.com,mirrors.tuna.tsinghua.edu.cn"`,
	}
	if !reflect.DeepEqual(lines[1:4], want) {
		t.Errorf("代理导出应紧跟 set -e:\n%s", script)
	}
	// 除占位符外的 $ ` " \ 都按字面量转义，不得被 shell 展开
	f.x.Proxy = "http://u:p$a`b\"c@127.0.0.1:7890"
	script, _ = Script(f.x, `C:\t\dsync`)
	if !strings.Contains(script, "export http_proxy=\"http://u:p\\$a\\`b\\\"c@127.0.0.1:7890\"") {
		t.Errorf("特殊字符应转义:\n%s", script)
	}
	f.x.Proxy = ""
	if script, _ = Script(f.x, `C:\t\dsync`); strings.Contains(script, "proxy") {
		t.Errorf("未指定代理时不应导出:\n%s", script)
	}
}
