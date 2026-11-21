package install

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// install.ps1 的测试：用模拟的 GitHub API 驱动真实的 powershell.exe（仅 Windows）。

const winAsset = "dsync_windows_amd64.exe"

// psServer 发布一个 Release，含 dsync_windows_amd64.exe 与 checksums.txt。
// genuine 用于计算校验值，served 为实际返回的内容（不同即模拟篡改）。
func psServer(t *testing.T, genuine, served []byte) *httptest.Server {
	t.Helper()
	sum := sha256.Sum256(genuine)
	var srv *httptest.Server
	srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 公开仓库：匿名可读；带了错误的 Token 仍然 404
		if auth := r.Header.Get("Authorization"); auth != "" && auth != "Bearer tok" {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		up := user{Login: "Lin-Ya", URL: srv.URL + "/users/Lin-Ya"}
		release := rel{URL: srv.URL + "/r/1", TagName: "v2.0.0-alpha.1", Name: "v2 alpha", Body: "说明",
			Assets: []asset{
				{URL: srv.URL + "/assets/1", ID: 1, Name: "checksums.txt", Uploader: up},
				{URL: srv.URL + "/assets/2", ID: 2, Name: winAsset, Uploader: up},
			}}
		switch r.URL.Path {
		case "/repos/Lin-Ya/dsync-cli/releases":
			json.NewEncoder(w).Encode([]rel{release})
		case "/repos/Lin-Ya/dsync-cli/releases/tags/v2.0.0-alpha.1":
			json.NewEncoder(w).Encode(release)
		case "/assets/1":
			w.Write([]byte(hex.EncodeToString(sum[:]) + "  " + winAsset + "\n"))
		case "/assets/2":
			if r.Header.Get("Accept") != "application/octet-stream" {
				http.Error(w, "need octet-stream", http.StatusBadRequest)
				return
			}
			w.Write(served)
		default:
			http.NotFound(w, r)
		}
	}))
	srv.Start()
	t.Cleanup(srv.Close)
	return srv
}

// runPS 执行 install.ps1。USERPROFILE / LOCALAPPDATA 指向临时目录；
// 用户 PATH 会被脚本持久写入注册表，因此测试结束后恢复原值。
func runPS(t *testing.T, srv *httptest.Server, env map[string]string, args ...string) (string, string, error) {
	t.Helper()
	dir := t.TempDir()
	cmdArgs := append([]string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", "install.ps1"}, args...)
	cmd := exec.Command("powershell.exe", cmdArgs...)
	cmd.Env = append(os.Environ(), "DSYNC_GITHUB_API="+srv.URL, "GITHUB_TOKEN=tok",
		"DSYNC_INSTALL_DIR="+filepath.Join(dir, "bin"), "LOCALAPPDATA="+dir)
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.CombinedOutput()
	return string(out), filepath.Join(dir, "bin", "dsync.exe"), err
}

func requirePS(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip("install.ps1 仅在 Windows 上测试")
	}
	if _, err := exec.LookPath("powershell.exe"); err != nil {
		t.Skip("缺少 powershell.exe")
	}
	userPath, err := exec.Command("powershell.exe", "-NoProfile", "-Command", "[Environment]::GetEnvironmentVariable('Path','User')").Output()
	if err != nil {
		t.Fatal(err)
	}
	orig := strings.TrimSpace(string(userPath))
	t.Cleanup(func() {
		exec.Command("powershell.exe", "-NoProfile", "-Command", "[Environment]::SetEnvironmentVariable('Path', $env:DSYNC_ORIG_PATH, 'User')").Run()
	})
	t.Setenv("DSYNC_ORIG_PATH", orig)
}

// fakeExe 返回可执行的假 dsync.exe：DSYNC_TEST_FAKE_EXE 指定，或现场 go build。
func fakeExe(t *testing.T) []byte {
	t.Helper()
	p := os.Getenv("DSYNC_TEST_FAKE_EXE")
	if p == "" {
		if _, err := exec.LookPath("go"); err != nil {
			t.Skip("没有 go，无法构建假 dsync.exe（可设 DSYNC_TEST_FAKE_EXE）")
		}
		p = filepath.Join(t.TempDir(), "fake.exe")
		if out, err := exec.Command("go", "build", "-o", p, "./testdata/fakedsync").CombinedOutput(); err != nil {
			t.Fatalf("构建假 dsync.exe 失败: %v\n%s", err, out)
		}
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestInstallPS1_InstallsVerifiesAndRunsHostSetup(t *testing.T) {
	requirePS(t)
	exe := fakeExe(t)
	srv := psServer(t, exe, exe)
	out, bin, err := runPS(t, srv, nil, "--source", `D:\Develop\dsync`, "--geo", "official")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if b, _ := os.ReadFile(bin); string(b) != string(exe) {
		t.Error("安装内容不符")
	}
	if !strings.Contains(out, "v2.0.0-alpha.1") || !strings.Contains(out, `fake-dsync host setup --source D:\Develop\dsync --geo official`) {
		t.Errorf("应安装后执行 host setup 并原样传参:\n%s", out)
	}
}

func TestInstallPS1_NoArgsAndPinnedVersion(t *testing.T) {
	requirePS(t)
	srv := psServer(t, []byte("binary"), []byte("binary"))
	out, bin, err := runPS(t, srv, map[string]string{"DSYNC_VERSION": "v2.0.0-alpha.1"})
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if _, err := os.Stat(bin); err != nil || strings.Contains(out, "fake-dsync") {
		t.Errorf("无参数时只安装:\n%s", out)
	}
}

// 断言只用 ASCII 信息（退出码 / sha256 / HTTP 状态码 / 文件是否存在）：PowerShell 写管道时
// 使用控制台代码页（中文系统为 GBK，英文 CI runner 上中文会变成 ?），中文提示无法可靠比对。
func TestInstallPS1_Failures(t *testing.T) {
	requirePS(t)
	srv := psServer(t, []byte("genuine"), []byte("tampered"))
	out, bin, err := runPS(t, srv, nil, "--source", "x")
	tampered := sha256.Sum256([]byte("tampered"))
	if err == nil || !strings.Contains(out, hex.EncodeToString(tampered[:])) {
		t.Errorf("篡改的二进制应被拒绝并给出实际 sha256:\n%s", out)
	}
	if _, statErr := os.Stat(bin); statErr == nil {
		t.Error("校验失败时不得安装")
	}
	if strings.Contains(out, "fake-dsync") {
		t.Error("校验失败时不得执行 host setup")
	}
	out, bin, err = runPS(t, srv, map[string]string{"GITHUB_TOKEN": "wrong"})
	if err == nil || !strings.Contains(out, "404") {
		t.Errorf("Token 错误应失败并给出 HTTP 状态:\n%s", out)
	}
	if _, statErr := os.Stat(bin); statErr == nil {
		t.Error("Token 错误时不得安装")
	}
}

// 从 PowerShell 7（pwsh）里启动 powershell.exe 时，子进程继承 pwsh 的 PSModulePath，
// 5.1 会去加载 7 的 Microsoft.PowerShell.Utility 而失败（Get-FileHash 等脚本定义的命令随之不可用）。
// Windows Terminal 默认终端常是 pwsh 7，用户按指引执行 powershell -File 正是这种情况。
// 这里在 PSModulePath 最前面放一个只兼容 PowerShell 7 的同名模块来稳定复现。
func TestInstallPS1_WorksWhenLaunchedFromPwsh7(t *testing.T) {
	requirePS(t)
	mods := t.TempDir()
	util := filepath.Join(mods, "Microsoft.PowerShell.Utility")
	os.MkdirAll(util, 0o755)
	// 与 PowerShell 7 自带的清单结构相同：声明兼容 3.0，cmdlet 来自一个 5.1 无法加载的程序集
	os.WriteFile(filepath.Join(util, "Microsoft.PowerShell.Utility.psd1"), []byte(
		"@{ GUID = '1da87e53-152b-403e-98dc-74d7b4d63d59'; ModuleVersion = '7.0.0.0'; PowerShellVersion = '3.0'\r\n"+
			"   CompatiblePSEditions = @('Core'); NestedModules = 'Microsoft.PowerShell.Commands.Utility.dll'\r\n"+
			"   CmdletsToExport = @('Get-FileHash', 'Invoke-RestMethod', 'Invoke-WebRequest', 'Select-Object', 'Write-Host') }\r\n"), 0o644)
	os.WriteFile(filepath.Join(util, "Microsoft.PowerShell.Commands.Utility.dll"), []byte("not a .NET Framework assembly"), 0o644)
	srv := psServer(t, []byte("binary"), []byte("binary"))
	out, bin, err := runPS(t, srv, map[string]string{"PSModulePath": mods + ";" + os.Getenv("PSModulePath")})
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if b, _ := os.ReadFile(bin); string(b) != "binary" {
		t.Error("应已安装")
	}
}
