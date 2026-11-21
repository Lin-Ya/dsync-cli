// Package release 通过 GitHub API 读取 dsync 的 Release（仓库已公开，Token 可选）。
package release

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/Lin-Ya/dsync-cli/internal/deps"
)

// Repo 是发布 dsync 的仓库。
const Repo = "Lin-Ya/dsync-cli"

// DefaultAPI 是 GitHub API 地址（测试可经 DSYNC_GITHUB_API 覆盖）。
const DefaultAPI = "https://api.github.com"

// ChecksumsFile 是每个 Release 附带的校验文件名（由 release 工作流生成）。
const ChecksumsFile = "checksums.txt"

// Client 访问 GitHub Release API。Token 为空时匿名请求（公开仓库可用，受未认证速率限制）。
type Client struct {
	HTTP  *http.Client
	API   string
	Token string
}

// Release 是一个 GitHub Release。
type Release struct {
	TagName string  `json:"tag_name"`
	Assets  []Asset `json:"assets"`
}

// Asset 是 Release 中的一个文件。url 为 API 地址，公开仓库可直接下载，带 Token 时同样可用。
type Asset struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// AssetName 返回某平台的 dsync 二进制资产名。
func AssetName(goos, goarch string) string {
	name := "dsync_" + goos + "_" + goarch
	if goos == "windows" {
		name += ".exe"
	}
	return name
}

func (c Client) request(ctx context.Context, url, accept string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	if c.Token != "" { // 公开仓库无需认证；只有设置了 Token 才发送
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	return req, nil
}

// Get 返回指定 tag 的 Release；tag 为空时返回最新一个（含预发布版本——
// /releases/latest 会排除预发布，而 v2 早期版本均为预发布）。
func (c Client) Get(ctx context.Context, tag string) (Release, error) {
	url := c.API + "/repos/" + Repo + "/releases?per_page=1"
	if tag != "" {
		url = c.API + "/repos/" + Repo + "/releases/tags/" + tag
	}
	req, err := c.request(ctx, url, "application/vnd.github+json")
	if err != nil {
		return Release{}, err
	}
	body, err := deps.Fetch(c.HTTP, req, "")
	if err != nil {
		return Release{}, fmt.Errorf("%w（网络受限、Token 无效，或触发匿名速率限制？设置 GITHUB_TOKEN 可提高限额）", err)
	}
	if tag != "" {
		var r Release
		return r, json.Unmarshal(body, &r)
	}
	var rs []Release
	if err := json.Unmarshal(body, &rs); err != nil {
		return Release{}, err
	}
	if len(rs) == 0 {
		return Release{}, errors.New(Repo + " 还没有任何 Release")
	}
	return rs[0], nil
}

// Download 下载资产并校验 sha256（wantSHA256 为空则不校验）。
func (c Client) Download(ctx context.Context, a Asset, wantSHA256 string) ([]byte, error) {
	req, err := c.request(ctx, a.URL, "application/octet-stream")
	if err != nil {
		return nil, err
	}
	return deps.Fetch(c.HTTP, req, wantSHA256)
}

// Find 按名称查找资产。
func (r Release) Find(name string) (Asset, error) {
	for _, a := range r.Assets {
		if a.Name == name {
			return a, nil
		}
	}
	return Asset{}, fmt.Errorf("Release %s 中没有 %s", r.TagName, name)
}

// ParseChecksums 解析 "<sha256>  <文件名>" 格式的校验文件。
func ParseChecksums(b []byte) map[string]string {
	out := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(string(b)))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 {
			out[strings.TrimPrefix(f[1], "*")] = f[0]
		}
	}
	return out
}

// Binary 下载某平台的 dsync 二进制：先取 checksums.txt，再按其中的 sha256 校验二进制。
func (c Client) Binary(ctx context.Context, r Release, goos, goarch string) ([]byte, error) {
	sumsAsset, err := r.Find(ChecksumsFile)
	if err != nil {
		return nil, err
	}
	sums, err := c.Download(ctx, sumsAsset, "")
	if err != nil {
		return nil, err
	}
	name := AssetName(goos, goarch)
	want, ok := ParseChecksums(sums)[name]
	if !ok {
		return nil, fmt.Errorf("%s 中没有 %s 的校验值", ChecksumsFile, name)
	}
	a, err := r.Find(name)
	if err != nil {
		return nil, err
	}
	return c.Download(ctx, a, want)
}
