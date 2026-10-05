package release

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeGitHub 模拟私有仓库的 Release API：所有请求都必须带正确 Token。
type fakeGitHub struct {
	mu       sync.Mutex // publish 在服务器运行期间修改数据
	srv      *httptest.Server
	token    string
	releases []Release
	files    map[string][]byte // 资产名 → 内容
}

func newFakeGitHub(t *testing.T, token string) *fakeGitHub {
	t.Helper()
	g := &fakeGitHub{token: token, files: map[string][]byte{}}
	g.srv = httptest.NewUnstartedServer(http.HandlerFunc(g.handle))
	g.srv.Start()
	t.Cleanup(g.srv.Close)
	return g
}

func (g *fakeGitHub) handle(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer "+g.token {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound) // 私有仓库无权限时 GitHub 返回 404
		return
	}
	switch {
	case r.URL.Path == "/repos/"+Repo+"/releases":
		json.NewEncoder(w).Encode(g.releases)
	case strings.HasPrefix(r.URL.Path, "/repos/"+Repo+"/releases/tags/"):
		tag := strings.TrimPrefix(r.URL.Path, "/repos/"+Repo+"/releases/tags/")
		for _, rel := range g.releases {
			if rel.TagName == tag {
				json.NewEncoder(w).Encode(rel)
				return
			}
		}
		http.NotFound(w, r)
	case strings.HasPrefix(r.URL.Path, "/assets/"):
		if r.Header.Get("Accept") != "application/octet-stream" {
			http.Error(w, "need octet-stream", http.StatusBadRequest)
			return
		}
		b, ok := g.files[strings.TrimPrefix(r.URL.Path, "/assets/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(b)
	default:
		http.NotFound(w, r)
	}
}

// publish 发布一个 Release，附带各文件与 checksums.txt。
func (g *fakeGitHub) publish(tag string, files map[string]string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	rel := Release{TagName: tag}
	var sums strings.Builder
	for name, content := range files {
		g.files[tag+"/"+name] = []byte(content)
		rel.Assets = append(rel.Assets, Asset{Name: name, URL: g.srv.URL + "/assets/" + tag + "/" + name})
		s := sha256.Sum256([]byte(content))
		sums.WriteString(hex.EncodeToString(s[:]) + "  " + name + "\n")
	}
	g.files[tag+"/"+ChecksumsFile] = []byte(sums.String())
	rel.Assets = append(rel.Assets, Asset{Name: ChecksumsFile, URL: g.srv.URL + "/assets/" + tag + "/" + ChecksumsFile})
	g.releases = append([]Release{rel}, g.releases...) // 新的在前，与 GitHub 一致
}

func TestBinary_LatestIncludesPrerelease(t *testing.T) {
	g := newFakeGitHub(t, "tok")
	g.publish("v2.0.0-alpha.1", map[string]string{"dsync_linux_amd64": "old"})
	g.publish("v2.0.0-alpha.2", map[string]string{"dsync_linux_amd64": "new", "dsync_windows_amd64.exe": "win"})
	c := Client{HTTP: g.srv.Client(), API: g.srv.URL, Token: "tok"}
	ctx := context.Background()

	r, err := c.Get(ctx, "")
	if err != nil || r.TagName != "v2.0.0-alpha.2" {
		t.Fatalf("latest = %+v %v", r, err)
	}
	b, err := c.Binary(ctx, r, "linux", "amd64")
	if err != nil || string(b) != "new" {
		t.Errorf("Binary = %q %v", b, err)
	}
	if b, _ := c.Binary(ctx, r, "windows", "amd64"); string(b) != "win" {
		t.Errorf("windows 资产名应带 .exe: %q", b)
	}
	r, err = c.Get(ctx, "v2.0.0-alpha.1")
	if err != nil || r.TagName != "v2.0.0-alpha.1" {
		t.Errorf("指定 tag: %+v %v", r, err)
	}
}

func TestBinary_RejectsTamperedAsset(t *testing.T) {
	g := newFakeGitHub(t, "tok")
	g.publish("v2.0.0", map[string]string{"dsync_linux_amd64": "genuine"})
	g.mu.Lock()
	g.files["v2.0.0/dsync_linux_amd64"] = []byte("tampered")
	g.mu.Unlock()
	c := Client{HTTP: g.srv.Client(), API: g.srv.URL, Token: "tok"}
	r, _ := c.Get(context.Background(), "")
	if _, err := c.Binary(context.Background(), r, "linux", "amd64"); err == nil || !strings.Contains(err.Error(), "校验失败") {
		t.Errorf("err = %v", err)
	}
}

func TestErrors(t *testing.T) {
	g := newFakeGitHub(t, "tok")
	ctx := context.Background()
	bad := Client{HTTP: g.srv.Client(), API: g.srv.URL, Token: "wrong"}
	if _, err := bad.Get(ctx, ""); err == nil || !strings.Contains(err.Error(), "Token") {
		t.Errorf("无权限应提示 Token: %v", err)
	}
	c := Client{HTTP: g.srv.Client(), API: g.srv.URL, Token: "tok"}
	if _, err := c.Get(ctx, ""); err == nil || !strings.Contains(err.Error(), "还没有任何 Release") {
		t.Errorf("无 Release: %v", err)
	}
	g.publish("v2.0.0", map[string]string{"dsync_linux_amd64": "x"})
	r, _ := c.Get(ctx, "")
	if _, err := c.Binary(ctx, r, "darwin", "arm64"); err == nil {
		t.Error("缺少平台资产应报错")
	}
}

func TestParseChecksums(t *testing.T) {
	m := ParseChecksums([]byte("abc  dsync_linux_amd64\ndef *dsync_windows_amd64.exe\n\nbad line here\n"))
	if m["dsync_linux_amd64"] != "abc" || m["dsync_windows_amd64.exe"] != "def" || len(m) != 2 {
		t.Errorf("%v", m)
	}
}
