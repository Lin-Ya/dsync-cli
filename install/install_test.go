// Package install 只包含 install.sh 的测试：用模拟的 GitHub API 驱动真实脚本。
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
	"sync/atomic"
	"testing"
)

// 资产与 release 对象的字段顺序与 GitHub API 一致（脚本依赖"资产 url 在 name 之前"）。
type user struct {
	Login string `json:"login"`
	URL   string `json:"url"`
}

type asset struct {
	URL                string `json:"url"`
	ID                 int    `json:"id"`
	Name               string `json:"name"`
	Uploader           user   `json:"uploader"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type rel struct {
	URL     string  `json:"url"`
	TagName string  `json:"tag_name"`
	Name    string  `json:"name"` // release 标题：干扰项，不应被当成资产名
	Assets  []asset `json:"assets"`
	Body    string  `json:"body"`
}

// fakeDsync 是被安装的"二进制"：一个回显参数的脚本，用于验证 install.sh 最后 exec 了 init。
const fakeDsync = "#!/bin/sh\necho \"fake-dsync $*\"\n"

func platformAsset() string {
	osName := map[string]string{"linux": "linux", "darwin": "darwin"}[runtime.GOOS]
	return "dsync_" + osName + "_" + runtime.GOARCH
}

type server struct {
	*httptest.Server
	binary    string // 实际提供的二进制内容（可被篡改）
	pretty    bool
	tokenOK   string
	anonymous int32 // 不带 Authorization 的请求数，证明无 Token 时真的没发令牌
}

// newServer 先配置好全部字段再启动，处理请求的 goroutine 只读不写（-race 安全）。
func newServer(t *testing.T, pretty bool, binary string) *server {
	t.Helper()
	s := &server{binary: binary, pretty: pretty, tokenOK: "tok"}
	s.Server = httptest.NewUnstartedServer(http.HandlerFunc(s.handle))
	s.Start()
	t.Cleanup(s.Close)
	return s
}

func (s *server) handle(w http.ResponseWriter, r *http.Request) {
	// 公开仓库：匿名可读；带了错误的 Token 仍然 404（与真实 GitHub 的不透明 404 一致）
	if auth := r.Header.Get("Authorization"); auth == "" {
		atomic.AddInt32(&s.anonymous, 1)
	} else if auth != "Bearer "+s.tokenOK {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
		return
	}
	sum := sha256.Sum256([]byte(fakeDsync)) // 校验值总是基于正版
	switch r.URL.Path {
	case "/repos/Lin-Ya/dsync-cli/releases", "/repos/Lin-Ya/dsync-cli/releases/tags/v2.0.0-alpha.1":
		up := user{Login: "Lin-Ya", URL: s.URL + "/users/Lin-Ya"}
		release := rel{
			URL: s.URL + "/repos/Lin-Ya/dsync-cli/releases/1", TagName: "v2.0.0-alpha.1", Name: "v2 alpha, 第一版",
			Assets: []asset{
				{URL: s.URL + "/repos/Lin-Ya/dsync-cli/releases/assets/11", ID: 11, Name: "checksums.txt", Uploader: up},
				{URL: s.URL + "/repos/Lin-Ya/dsync-cli/releases/assets/12", ID: 12, Name: "dsync_windows_amd64.exe", Uploader: up},
				{URL: s.URL + "/repos/Lin-Ya/dsync-cli/releases/assets/13", ID: 13, Name: platformAsset(), Uploader: up},
			},
			Body: `说明, 含逗号 和 "name": "dsync_linux_amd64" 这样的文字`,
		}
		var v any = []rel{release}
		if strings.Contains(r.URL.Path, "/tags/") {
			v = release
		}
		var b []byte
		if s.pretty {
			b, _ = json.MarshalIndent(v, "", "  ")
		} else {
			b, _ = json.Marshal(v)
		}
		w.Write(b)
	case "/repos/Lin-Ya/dsync-cli/releases/assets/11":
		w.Write([]byte(hex.EncodeToString(sum[:]) + "  " + platformAsset() + "\n"))
	case "/repos/Lin-Ya/dsync-cli/releases/assets/13":
		if r.Header.Get("Accept") != "application/octet-stream" {
			http.Error(w, "need octet-stream", http.StatusBadRequest)
			return
		}
		w.Write([]byte(s.binary))
	default:
		http.NotFound(w, r)
	}
}

// run 以 sh 执行 install.sh，返回合并输出与退出错误。
func run(t *testing.T, srv *server, env map[string]string, args ...string) (string, string, error) {
	t.Helper()
	home := t.TempDir()
	cmd := exec.Command("sh", append([]string{"install.sh"}, args...)...)
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "HOME=" + home, "DSYNC_GITHUB_API=" + srv.URL, "GITHUB_TOKEN=tok"}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.CombinedOutput()
	return string(out), filepath.Join(home, ".local", "bin", "dsync"), err
}

func requireTools(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("install.sh 仅支持 Linux / macOS")
	}
	for _, c := range []string{"sh", "curl"} {
		if _, err := exec.LookPath(c); err != nil {
			t.Skipf("缺少 %s", c)
		}
	}
}

func TestInstall_PrettyAndMinifiedJSON(t *testing.T) {
	requireTools(t)
	for _, pretty := range []bool{true, false} {
		srv := newServer(t, pretty, fakeDsync)
		out, bin, err := run(t, srv, nil, "https://github.com/me/dotfiles.git", "--geo", "official")
		if err != nil {
			t.Fatalf("pretty=%v: %v\n%s", pretty, err, out)
		}
		if b, _ := os.ReadFile(bin); string(b) != fakeDsync {
			t.Errorf("pretty=%v: 安装内容不符", pretty)
		}
		if st, _ := os.Stat(bin); st == nil || st.Mode().Perm()&0o111 == 0 {
			t.Errorf("pretty=%v: 应可执行", pretty)
		}
		if !strings.Contains(out, "dsync v2.0.0-alpha.1 已安装") || !strings.Contains(out, "fake-dsync init https://github.com/me/dotfiles.git --geo official") {
			t.Errorf("pretty=%v: 应安装后执行 init 并原样传参:\n%s", pretty, out)
		}
	}
}

func TestInstall_NoArgsOnlyInstallsAndPinnedVersion(t *testing.T) {
	requireTools(t)
	srv := newServer(t, true, fakeDsync)
	out, bin, err := run(t, srv, map[string]string{"DSYNC_VERSION": "v2.0.0-alpha.1"})
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if strings.Contains(out, "fake-dsync") || !strings.Contains(out, "下一步") {
		t.Errorf("无参数时不应执行 init:\n%s", out)
	}
	if _, err := os.Stat(bin); err != nil {
		t.Error("应已安装")
	}
}

func TestInstall_Failures(t *testing.T) {
	requireTools(t)
	srv := newServer(t, true, "tampered")
	out, bin, err := run(t, srv, nil, "x")
	if err == nil || !strings.Contains(out, "校验失败") {
		t.Errorf("篡改的二进制应被拒绝:\n%s", out)
	}
	if _, statErr := os.Stat(bin); statErr == nil {
		t.Error("校验失败时不得安装")
	}

	srv = newServer(t, true, fakeDsync)
	out, bin, err = run(t, srv, map[string]string{"GITHUB_TOKEN": ""})
	if err != nil || !strings.Contains(out, "已安装") {
		t.Errorf("公开仓库无 Token 也应能安装:\n%s", out)
	}
	if atomic.LoadInt32(&srv.anonymous) == 0 {
		t.Error("无 Token 时不应发送 Authorization 头")
	}
	if _, statErr := os.Stat(bin); statErr != nil {
		t.Error("无 Token 时也应已安装")
	}
	out, _, err = run(t, srv, map[string]string{"GITHUB_TOKEN": "wrong"})
	if err == nil || !strings.Contains(out, "Token 无效") {
		t.Errorf("Token 错误:\n%s", out)
	}
}
