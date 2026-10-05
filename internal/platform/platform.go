// Package platform 探测运行平台（OS / 架构 / WSL / Linux 发行版）。
// 文件读取经 fs.FS 注入，测试可伪造 /proc/version 与 /etc/os-release。
package platform

import (
	"bufio"
	"io/fs"
	"runtime"
	"strings"
)

// Platform 描述当前机器。
type Platform struct {
	OS       string `json:"os"`   // runtime.GOOS：linux / darwin / windows
	Arch     string `json:"arch"` // runtime.GOARCH
	WSL      bool   `json:"wsl"`
	DistroID string `json:"distro_id,omitempty"` // /etc/os-release 的 ID（ubuntu / debian …）
	Version  string `json:"distro_version,omitempty"`
}

// Detect 基于根文件系统 root（通常为 os.DirFS("/")）探测平台。
func Detect(root fs.FS) Platform {
	return detect(root, runtime.GOOS, runtime.GOARCH)
}

// DetectAs 与 Detect 相同，但由调用方指定 GOOS（测试用，使结果不依赖宿主系统）。
func DetectAs(root fs.FS, goos string) Platform {
	return detect(root, goos, runtime.GOARCH)
}

func detect(root fs.FS, goos, goarch string) Platform {
	p := Platform{OS: goos, Arch: goarch}
	if goos != "linux" {
		return p
	}
	// /proc/version 带 microsoft 只说明内核是 WSL2 的；WSL 宿主上的 docker 容器共享该内核。
	// /run/WSL 由 WSL init 在发行版内创建，容器里没有，以此区分。
	if b, err := fs.ReadFile(root, "proc/version"); err == nil && strings.Contains(strings.ToLower(string(b)), "microsoft") {
		if _, err := fs.Stat(root, "run/WSL"); err == nil {
			p.WSL = true
		}
	}
	if b, err := fs.ReadFile(root, "etc/os-release"); err == nil {
		kv := parseOSRelease(string(b))
		p.DistroID = kv["ID"]
		p.Version = kv["VERSION_ID"]
	}
	return p
}

func parseOSRelease(s string) map[string]string {
	out := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		out[k] = strings.Trim(v, `"'`)
	}
	return out
}

// Label 返回简短的人类可读描述。
func (p Platform) Label() string {
	switch {
	case p.OS == "darwin":
		return "macOS/" + p.Arch
	case p.WSL:
		return "WSL2/" + orUnknown(p.DistroID) + " " + p.Version + "/" + p.Arch
	case p.OS == "linux":
		return "Linux/" + orUnknown(p.DistroID) + " " + p.Version + "/" + p.Arch
	}
	return p.OS + "/" + p.Arch
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}
