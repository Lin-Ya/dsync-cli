package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Lin-Ya/dsync-cli/internal/release"
)

// fakeRelease 启动一个模拟私有 Release API 的服务器，发布 tag 并附带本机架构的 linux 二进制
// （fixture 固定 GOOS=linux；架构取实际值，macOS arm64 runner 上同样成立）。
func fakeRelease(t *testing.T, tag, binary string) *httptest.Server {
	t.Helper()
	return fakeReleaseAsset(t, tag, release.AssetName("linux", runtime.GOARCH), binary)
}

// fakeReleaseAsset 发布 tag，附带名为 name 的资产与对应的 checksums.txt。
func fakeReleaseAsset(t *testing.T, tag, name, binary string) *httptest.Server {
	t.Helper()
	sum := sha256.Sum256([]byte(binary))
	// 先建未启动的服务器再赋值、启动：处理请求的 goroutine 读取 srv 时已无并发写（-race 安全）
	var srv *httptest.Server
	srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.NotFound(w, r)
			return
		}
		switch r.URL.Path {
		case "/repos/" + release.Repo + "/releases":
			json.NewEncoder(w).Encode([]release.Release{{TagName: tag, Assets: []release.Asset{
				{Name: name, URL: srv.URL + "/bin"},
				{Name: release.ChecksumsFile, URL: srv.URL + "/sums"},
			}}})
		case "/bin":
			w.Write([]byte(binary))
		case "/sums":
			w.Write([]byte(hex.EncodeToString(sum[:]) + "  " + name + "\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	srv.Start()
	t.Cleanup(srv.Close)
	return srv
}

func TestSelfUpdate(t *testing.T) {
	f := newFixture(t)
	srv := fakeRelease(t, "v2.0.0-alpha.2", "new-binary")
	f.env["DSYNC_GITHUB_API"] = srv.URL
	target := filepath.Join(t.TempDir(), "dsync")
	os.WriteFile(target, []byte("old-binary"), 0o755)
	e := f.Env()
	e.HTTP = srv.Client()
	ctx := context.Background()

	if err := e.SelfUpdate(ctx, "v2.0.0-alpha.1", SelfUpdateOptions{Target: target}); err == nil || !strings.Contains(err.Error(), "GITHUB_TOKEN") {
		t.Fatalf("无 Token 应报错并提示: %v", err)
	}
	f.env["GITHUB_TOKEN"] = "tok"
	if err := e.SelfUpdate(ctx, "v2.0.0-alpha.1", SelfUpdateOptions{Target: target}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(target); string(b) != "new-binary" {
		t.Errorf("应替换为新版本: %q", b)
	}
	os.WriteFile(target, []byte("same"), 0o755)
	if err := e.SelfUpdate(ctx, "v2.0.0-alpha.2", SelfUpdateOptions{Target: target}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(target); string(b) != "same" || !strings.Contains(f.out.String(), "无需更新") {
		t.Error("已是最新时不应替换")
	}
	if err := e.SelfUpdate(ctx, "v2.0.0-alpha.2", SelfUpdateOptions{Target: target, Force: true}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(target); string(b) != "new-binary" {
		t.Error("--force 应重新安装")
	}

	// --dry-run：照常下载并校验，但不替换
	os.WriteFile(target, []byte("old-binary"), 0o755)
	e.DryRun = true
	if err := e.SelfUpdate(ctx, "v2.0.0-alpha.1", SelfUpdateOptions{Target: target}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(target); string(b) != "old-binary" || !strings.Contains(f.err.String(), "[dry-run] 用 v2.0.0-alpha.2（已校验）替换") {
		t.Errorf("dry-run 不应替换: %q\n%s", b, f.err.String())
	}
}

func TestGithubToken_FallsBackToGh(t *testing.T) {
	f := newFixture(t)
	f.tools("gh")
	f.rec.OnStdout("gh auth token", "gho_abc\n")
	if tok, err := f.Env().githubToken(context.Background()); err != nil || tok != "gho_abc" {
		t.Errorf("%q %v", tok, err)
	}
	f.env["GITHUB_TOKEN"] = "env"
	if tok, _ := f.Env().githubToken(context.Background()); tok != "env" {
		t.Errorf("环境变量应优先: %q", tok)
	}
}
