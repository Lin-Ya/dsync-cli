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

// fakeRelease 启动一个模拟公开 Release API 的服务器，发布 tag 并附带本机架构的 linux 二进制
// （fixture 固定 GOOS=linux；架构取实际值，macOS arm64 runner 上同样成立）。
// 与真实公开仓库一致：不带 Authorization 也允许访问。
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
		// 公开仓库：匿名或带任意 Token 都可读；令牌错误不在这里模拟（真实 GitHub 会返回 401）
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

	if err := e.SelfUpdate(ctx, "v2.0.0-alpha.1", SelfUpdateOptions{Target: target}); err != nil {
		t.Fatalf("公开仓库无 Token 也应能更新: %v", err)
	}
	if b, _ := os.ReadFile(target); string(b) != "new-binary" {
		t.Errorf("应替换为新版本: %q", b)
	}
	f.env["GITHUB_TOKEN"] = "tok"
	if err := e.SelfUpdate(ctx, "v2.0.0-alpha.1", SelfUpdateOptions{Target: target, Force: true}); err != nil {
		t.Fatalf("带 Token 也应能更新: %v", err)
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

func TestGithubToken_OptionalAndFallsBackToGh(t *testing.T) {
	f := newFixture(t)
	if tok := f.Env().githubToken(context.Background()); tok != "" {
		t.Errorf("没有 Token 时应返回空串（匿名访问）: %q", tok)
	}
	f.tools("gh")
	f.rec.OnStdout("gh auth token", "gho_abc\n")
	if tok := f.Env().githubToken(context.Background()); tok != "gho_abc" {
		t.Errorf("%q", tok)
	}
	f.env["GITHUB_TOKEN"] = "env"
	if tok := f.Env().githubToken(context.Background()); tok != "env" {
		t.Errorf("环境变量应优先: %q", tok)
	}
}
