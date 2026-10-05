// Package config 管理 dsync 的两类配置：
//
//   - local.toml（~/.config/dsync/local.toml，不入库）：本机状态——geo、代理、机器标签、
//     git 身份、dotfiles 仓库位置；
//   - dsync.toml（dotfiles 仓库根，入库）：多机共享清单，见 manifest.go。
//
// v1 兼容：local.toml 不存在时，geo 回退读取 v1 的 ~/.config/dsync/geo，
// 代理回退 source v1 的 ~/.config/dsync/proxy.env。
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/Lin-Ya/dsync-cli/internal/mirrors"
)

// Paths 汇总 dsync 用到的本机路径。
type Paths struct {
	Home      string
	ConfigDir string // ~/.config/dsync（可用 DSYNC_CONFIG_DIR 覆盖，便于测试）
}

// LocalFile 返回 local.toml 路径。
func (p Paths) LocalFile() string { return filepath.Join(p.ConfigDir, "local.toml") }

// V1GeoFile 返回 v1 的 geo 状态文件路径。
func (p Paths) V1GeoFile() string { return filepath.Join(p.ConfigDir, "geo") }

// V1ProxyFile 返回 v1 的代理脚本路径。
func (p Paths) V1ProxyFile() string { return filepath.Join(p.ConfigDir, "proxy.env") }

// DefaultPaths 基于环境变量计算路径。
func DefaultPaths(getenv func(string) string) (Paths, error) {
	home := getenv("HOME")
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return Paths{}, fmt.Errorf("无法确定 HOME: %w", err)
		}
		home = h
	}
	dir := getenv("DSYNC_CONFIG_DIR")
	if dir == "" {
		dir = filepath.Join(home, ".config", "dsync")
	}
	return Paths{Home: home, ConfigDir: dir}, nil
}

// Local 是 local.toml 的结构。
type Local struct {
	Geo   string   `toml:"geo,omitempty"`
	Tags  []string `toml:"tags,omitempty"`
	Repo  Repo     `toml:"repo,omitempty"`
	User  User     `toml:"user,omitempty"`
	Proxy *Proxy   `toml:"proxy,omitempty"`
}

// Repo 记录 dotfiles 仓库在本机的位置。
type Repo struct {
	Path string `toml:"path,omitempty"`
}

// User 是本机 git 身份（注入模板，取代硬编码在 create_dot_gitconfig.local 中的值）。
type User struct {
	Name  string `toml:"name,omitempty"`
	Email string `toml:"email,omitempty"`
}

// Proxy 是本机代理配置。值中可使用字面量 ${WSL_HOST_IP}，
// shell-init 会在 WSL 下替换为 Windows 宿主（默认网关）IP。
type Proxy struct {
	HTTP    string `toml:"http,omitempty"`
	HTTPS   string `toml:"https,omitempty"`
	All     string `toml:"all,omitempty"`
	NoProxy string `toml:"no_proxy,omitempty"`
}

// LoadLocal 读取 local.toml；文件不存在返回零值（exists=false）。
func LoadLocal(p Paths) (l Local, exists bool, err error) {
	b, err := os.ReadFile(p.LocalFile())
	if errors.Is(err, fs.ErrNotExist) {
		return Local{}, false, nil
	}
	if err != nil {
		return Local{}, false, err
	}
	dec := toml.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&l); err != nil {
		return Local{}, true, fmt.Errorf("%s: %w", p.LocalFile(), describeTOMLError(err))
	}
	if l.Geo != "" {
		if _, err := mirrors.ParseGeo(l.Geo); err != nil {
			return Local{}, true, fmt.Errorf("%s: %w", p.LocalFile(), err)
		}
	}
	return l, true, nil
}

func describeTOMLError(err error) error {
	var sm *toml.StrictMissingError
	if errors.As(err, &sm) {
		return fmt.Errorf("未知配置项:\n%s", sm.String())
	}
	var de *toml.DecodeError
	if errors.As(err, &de) {
		row, col := de.Position()
		return fmt.Errorf("第 %d 行第 %d 列: %s", row, col, de.Error())
	}
	return err
}

// GeoSource 说明 geo 取值来源（status / doctor 展示用）。
type GeoSource string

const (
	GeoFromEnv     GeoSource = "env:DSYNC_GEO"
	GeoFromLocal   GeoSource = "local.toml"
	GeoFromV1File  GeoSource = "v1:~/.config/dsync/geo"
	GeoFromDefault GeoSource = "default"
)

// ShellInitMarker 由 shell-init 导出：标记当前 DSYNC_GEO 是 dsync 自己写入的
// 会话值而非用户显式覆盖，解析 geo 时应忽略，以免 `dsync geo set` 后
// 同一会话里读到陈旧值。
const ShellInitMarker = "DSYNC_GEO_SOURCE"

// ResolveGeo 按优先级解析 geo：
// 显式 DSYNC_GEO 环境变量 > local.toml > v1 geo 文件 > 默认 cn。
// 非法值不会静默回退，而是返回错误。
func ResolveGeo(getenv func(string) string, p Paths, l Local) (mirrors.Geo, GeoSource, error) {
	if v := getenv("DSYNC_GEO"); v != "" && getenv(ShellInitMarker) != "shell-init" {
		g, err := mirrors.ParseGeo(v)
		if err != nil {
			return "", GeoFromEnv, fmt.Errorf("环境变量 DSYNC_GEO: %w", err)
		}
		return g, GeoFromEnv, nil
	}
	if l.Geo != "" {
		g, err := mirrors.ParseGeo(l.Geo)
		return g, GeoFromLocal, err
	}
	if b, err := os.ReadFile(p.V1GeoFile()); err == nil {
		v := strings.TrimSpace(string(b))
		g, err := mirrors.ParseGeo(v)
		if err != nil {
			return "", GeoFromV1File, fmt.Errorf("%s: %w", p.V1GeoFile(), err)
		}
		return g, GeoFromV1File, nil
	}
	return mirrors.Default, GeoFromDefault, nil
}

// SaveLocal 写入 local.toml（经 write 回调，以便 dry-run 只打印）。该文件由 dsync 维护，手写注释不会保留。
func SaveLocal(p Paths, l Local, write func(path string, data []byte, perm os.FileMode) error) error {
	b, err := toml.Marshal(l)
	if err != nil {
		return err
	}
	content := append([]byte("# dsync 本机配置（不入库）。由 dsync 维护，可手改但注释不会保留。\n"), b...)
	return write(p.LocalFile(), content, 0o600) // write 负责创建父目录（dry-run 下不建）
}
