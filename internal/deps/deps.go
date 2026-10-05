// Package deps 下载并安装 dsync 依赖的底座工具（mise / chezmoi）以及 dsync 自身。
//
// 版本与 sha256 固定在 versions.go（由 scripts/update-deps.sh 从上游官方 checksum 生成），
// 下载后逐字节校验，不符即拒绝安装。网络代理沿用环境变量 http(s)_proxy（net/http 默认行为）。
package deps

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Tool 是一个固定版本的工具。
type Tool struct {
	Name    string
	Version string
	Assets  map[string]Asset // 键为 "<GOOS>/<GOARCH>"
}

// Asset 是某平台的下载资产。
type Asset struct {
	URL    string
	SHA256 string
	Member string // 非空表示 URL 是 tar.gz，需取出其中该文件
}

// AssetFor 返回指定平台的资产。
func (t Tool) AssetFor(goos, goarch string) (Asset, error) {
	a, ok := t.Assets[goos+"/"+goarch]
	if !ok {
		return Asset{}, fmt.Errorf("%s %s 不提供 %s/%s 版本，请手动安装", t.Name, t.Version, goos, goarch)
	}
	return a, nil
}

// StallTimeout：下载过程中超过该时长没有收到任何数据即中止（不限制总时长，慢网络下的大文件仍可下完）。
var StallTimeout = 60 * time.Second

// NewHTTPClient 返回带连接 / 握手 / 响应头超时的客户端，代理沿用 http(s)_proxy 环境变量。
// （http.DefaultClient 没有任何超时，代理卡住时会无限等待。）
func NewHTTPClient() *http.Client {
	return &http.Client{Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   30 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		ForceAttemptHTTP2:     true,
	}}
}

var errStalled = errors.New("下载停滞")

// download 执行请求并把响应体写入 w；超过 StallTimeout 没有新数据即中止。
func download(client *http.Client, req *http.Request, w io.Writer) error {
	ctx, cancel := context.WithCancelCause(req.Context())
	defer cancel(nil)
	timer := time.AfterFunc(StallTimeout, func() { cancel(errStalled) })
	defer timer.Stop()

	resp, err := client.Do(req.WithContext(ctx))
	if err != nil {
		if errors.Is(context.Cause(ctx), errStalled) {
			return fmt.Errorf("下载 %s 失败：%v 没有响应（网络受限时请配置 http(s)_proxy）", req.URL, StallTimeout)
		}
		return fmt.Errorf("下载 %s 失败（网络受限时请配置 http(s)_proxy）: %w", req.URL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载 %s 失败: HTTP %d", req.URL, resp.StatusCode)
	}
	buf := make([]byte, 64<<10)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			timer.Reset(StallTimeout)
			if _, err := w.Write(buf[:n]); err != nil {
				return err
			}
		}
		if errors.Is(rerr, io.EOF) {
			return nil
		}
		if rerr != nil {
			if errors.Is(context.Cause(ctx), errStalled) {
				return fmt.Errorf("下载 %s 停滞：%v 内没有收到数据，已中止（网络受限时请配置 http(s)_proxy）", req.URL, StallTimeout)
			}
			return fmt.Errorf("下载 %s 中断: %w", req.URL, rerr)
		}
	}
}

func verify(url string, h hash.Hash, want string) error {
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, want) {
		return fmt.Errorf("%s 校验失败：sha256 为 %s，期望 %s（文件可能被篡改或下载不完整），已拒绝安装", url, got, want)
	}
	return nil
}

// Install 下载资产（边写临时文件边计算 sha256，不整体读入内存），校验通过后以可执行文件原子写入 dst。
func Install(ctx context.Context, client *http.Client, a Asset, dst string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
	if err != nil {
		return err
	}
	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(dst)+".download-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	h := sha256.New()
	if err := download(client, req, io.MultiWriter(tmp, h)); err != nil {
		return err
	}
	if err := verify(a.URL, h, a.SHA256); err != nil {
		return err
	}
	if a.Member == "" {
		if err := tmp.Chmod(0o755); err != nil {
			return err
		}
		if err := tmp.Close(); err != nil {
			return err
		}
		return os.Rename(tmp.Name(), dst)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return err
	}
	data, err := extractTarGz(tmp, a.Member)
	if err != nil {
		return fmt.Errorf("%s: %w", a.URL, err)
	}
	return WriteExecutable(dst, data)
}

// Fetch 下载小文件（API 响应、校验文件、dsync 自身）到内存并校验 sha256（wantSHA256 为空则不校验）。
func Fetch(client *http.Client, req *http.Request, wantSHA256 string) ([]byte, error) {
	var buf bytes.Buffer
	h := sha256.New()
	if err := download(client, req, io.MultiWriter(&buf, h)); err != nil {
		return nil, err
	}
	if wantSHA256 != "" {
		if err := verify(req.URL.String(), h, wantSHA256); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

func extractTarGz(r io.Reader, member string) ([]byte, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("压缩包中没有 %s", member)
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag == tar.TypeReg && h.Name == member {
			return io.ReadAll(tr)
		}
	}
}

// WriteExecutable 先写同目录临时文件再 rename，保证 dst 要么是旧文件要么是完整的新文件
// （替换正在运行的 dsync 自身时也安全）。
func WriteExecutable(dst string, data []byte) error {
	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(dst)+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // rename 成功后为空操作
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dst)
}
