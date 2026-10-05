package deps

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func tarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg})
		tw.Write([]byte(content))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func serve(t *testing.T, files map[string][]byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(b)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestInstall_BinaryAndTarGz(t *testing.T) {
	bin := []byte("#!/bin/sh\necho mise\n")
	archive := tarGz(t, map[string]string{"README.md": "x", "chezmoi": "chezmoi-binary"})
	srv := serve(t, map[string][]byte{"/mise": bin, "/chezmoi.tar.gz": archive})
	dir := t.TempDir()
	ctx := context.Background()

	if err := Install(ctx, srv.Client(), Asset{URL: srv.URL + "/mise", SHA256: sha(bin)}, filepath.Join(dir, "mise")); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(filepath.Join(dir, "mise"))
	if err != nil || st.Mode().Perm() != 0o755 {
		t.Fatalf("应写入可执行文件: %v %v", st, err)
	}

	a := Asset{URL: srv.URL + "/chezmoi.tar.gz", SHA256: sha(archive), Member: "chezmoi"}
	if err := Install(ctx, srv.Client(), a, filepath.Join(dir, "chezmoi")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "chezmoi")); string(b) != "chezmoi-binary" {
		t.Errorf("应从压缩包取出 chezmoi: %q", b)
	}
}

func TestInstall_RejectsChecksumMismatchAndKeepsOldFile(t *testing.T) {
	srv := serve(t, map[string][]byte{"/mise": []byte("tampered")})
	dst := filepath.Join(t.TempDir(), "mise")
	os.WriteFile(dst, []byte("old"), 0o755)
	err := Install(context.Background(), srv.Client(), Asset{URL: srv.URL + "/mise", SHA256: sha([]byte("genuine"))}, dst)
	if err == nil || !strings.Contains(err.Error(), "校验失败") {
		t.Fatalf("err = %v", err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "old" {
		t.Error("校验失败时不得改动已有文件")
	}
	entries, _ := os.ReadDir(filepath.Dir(dst))
	if len(entries) != 1 {
		t.Errorf("不应残留临时文件: %v", entries)
	}
}

func TestInstall_Errors(t *testing.T) {
	archive := tarGz(t, map[string]string{"other": "x"})
	srv := serve(t, map[string][]byte{"/a.tar.gz": archive})
	dst := filepath.Join(t.TempDir(), "x")
	ctx := context.Background()
	if err := Install(ctx, srv.Client(), Asset{URL: srv.URL + "/missing"}, dst); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Errorf("404: %v", err)
	}
	if err := Install(ctx, srv.Client(), Asset{URL: srv.URL + "/a.tar.gz", SHA256: sha(archive), Member: "chezmoi"}, dst); err == nil || !strings.Contains(err.Error(), "没有 chezmoi") {
		t.Errorf("缺少成员: %v", err)
	}
	if _, err := Mise.AssetFor("windows", "amd64"); err == nil {
		t.Error("未提供的平台应报错")
	}
}

// 生成的版本表必须完整且格式正确（防止手改出错）。
func TestVersionsTable(t *testing.T) {
	hexRe := regexp.MustCompile(`^[0-9a-f]{64}$`)
	for _, tool := range []Tool{Mise, Chezmoi} {
		for _, p := range []string{"linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64"} {
			a, err := tool.AssetFor(strings.Split(p, "/")[0], strings.Split(p, "/")[1])
			if err != nil {
				t.Fatal(err)
			}
			if !hexRe.MatchString(a.SHA256) || !strings.HasPrefix(a.URL, "https://github.com/") || !strings.Contains(a.URL, tool.Version) {
				t.Errorf("%s %s: %+v", tool.Name, p, a)
			}
		}
	}
}

// 连接已建立但数据中途停止：应在 StallTimeout 后报错，而不是无限等待。
func TestInstall_FailsOnStalledDownload(t *testing.T) {
	old := StallTimeout
	StallTimeout = 200 * time.Millisecond
	t.Cleanup(func() { StallTimeout = old })
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000")
		w.Write([]byte("partial"))
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(release); srv.Close() })
	dst := filepath.Join(t.TempDir(), "mise")
	start := time.Now()
	err := Install(context.Background(), NewHTTPClient(), Asset{URL: srv.URL + "/mise", SHA256: sha([]byte("x"))}, dst)
	if err == nil || !strings.Contains(err.Error(), "停滞") {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("应在停滞超时后尽快失败，实际耗时 %v", time.Since(start))
	}
	if entries, _ := os.ReadDir(filepath.Dir(dst)); len(entries) != 0 {
		t.Errorf("失败时不应留下任何文件: %v", entries)
	}
}

// 大文件以流式写入（不经内存整体缓冲），校验通过后才出现在目标位置。
func TestInstall_StreamsLargeFile(t *testing.T) {
	big := bytes.Repeat([]byte("0123456789abcdef"), 1<<20) // 16 MiB
	srv := serve(t, map[string][]byte{"/big": big})
	dst := filepath.Join(t.TempDir(), "big")
	if err := Install(context.Background(), NewHTTPClient(), Asset{URL: srv.URL + "/big", SHA256: sha(big)}, dst); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(dst); err != nil || st.Size() != int64(len(big)) {
		t.Errorf("%v %v", st, err)
	}
}
