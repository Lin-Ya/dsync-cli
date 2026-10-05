package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// ManifestFile 是 dotfiles 仓库根目录下的共享清单文件名。
const ManifestFile = "dsync.toml"

// Manifest 是 dsync.toml 的结构（多机共享，入库）。
type Manifest struct {
	Version   int            `toml:"version"`
	Deps      Deps           `toml:"deps"`
	Remote    Remote         `toml:"remote"`
	Packages  Packages       `toml:"packages"`
	Shell     Shell          `toml:"shell"`
	Provision Provision      `toml:"provision"`
	Secrets   Secrets        `toml:"secrets"`
	Data      map[string]any `toml:"data"`
}

// Deps 固定 dsync 负责安装的底座工具版本。
type Deps struct {
	Mise    string `toml:"mise"`
	Chezmoi string `toml:"chezmoi"`
}

// Remote 描述 dotfiles 仓库的远端。
type Remote struct {
	URL     string   `toml:"url"`
	Mirrors []string `toml:"mirrors"`
}

// Packages 是系统包清单（v1 中硬编码在 run_once 脚本里）。
type Packages struct {
	Apt  []string `toml:"apt"`
	Brew []string `toml:"brew"`
}

// Shell 是 shell 相关设置。
type Shell struct {
	Default        string   `toml:"default"`
	OhMyZshPlugins []string `toml:"ohmyzsh_plugins"`
}

// Provision 声明启用哪些系统级步骤。
type Provision struct {
	Enable []string `toml:"enable"`
}

// Secrets 是 age 加密设置。
type Secrets struct {
	AgeRecipients []string `toml:"age_recipients"`
}

// KnownProvisionSteps 是 dsync 实现的 provision 步骤（与 internal/provision 的注册表一致，由测试保证）。
var KnownProvisionSteps = []string{"apt", "wsl-ssh-agent", "homebrew", "ohmyzsh", "chsh", "docker-check"}

// DefaultManifest 是仓库中没有 dsync.toml（v1 布局）时使用的清单，
// 取值与 v1 run_once 脚本中硬编码的包清单 / 插件 / 步骤一致。
func DefaultManifest() Manifest {
	return Manifest{
		Version: 1,
		Packages: Packages{
			Apt:  []string{"build-essential", "curl", "wget", "git", "zsh", "unzip", "ca-certificates", "ripgrep", "socat"},
			Brew: []string{"ripgrep", "fd"},
		},
		Shell: Shell{
			Default:        "zsh",
			OhMyZshPlugins: []string{"zsh-users/zsh-autosuggestions", "zsh-users/zsh-syntax-highlighting"},
		},
		Provision: Provision{Enable: append([]string(nil), KnownProvisionSteps...)},
	}
}

// LoadManifest 读取并校验 <repo>/dsync.toml；不存在返回 exists=false。
func LoadManifest(repo string) (m Manifest, exists bool, err error) {
	path := filepath.Join(repo, ManifestFile)
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Manifest{}, false, nil
	}
	if err != nil {
		return Manifest{}, false, err
	}
	m, err = ParseManifest(b)
	if err != nil {
		return Manifest{}, true, fmt.Errorf("%s: %w", path, err)
	}
	return m, true, nil
}

// ParseManifest 解析并校验清单内容。
func ParseManifest(b []byte) (Manifest, error) {
	var m Manifest
	dec := toml.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return Manifest{}, describeTOMLError(err)
	}
	if err := m.Validate(); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

// Validate 校验清单语义，一次性汇总所有问题。
func (m Manifest) Validate() error {
	var errs []error
	if m.Version != 1 {
		errs = append(errs, fmt.Errorf("version = %d，当前仅支持 1", m.Version))
	}
	known := map[string]bool{}
	for _, s := range KnownProvisionSteps {
		known[s] = true
	}
	seen := map[string]bool{}
	for _, s := range m.Provision.Enable {
		if !known[s] {
			errs = append(errs, fmt.Errorf("provision.enable: 未知步骤 %q（可选: %s）", s, strings.Join(KnownProvisionSteps, ", ")))
		}
		if seen[s] {
			errs = append(errs, fmt.Errorf("provision.enable: 重复步骤 %q", s))
		}
		seen[s] = true
	}
	switch m.Shell.Default {
	case "", "zsh", "bash":
	default:
		errs = append(errs, fmt.Errorf("shell.default: 不支持 %q（可选: zsh | bash）", m.Shell.Default))
	}
	for _, p := range m.Shell.OhMyZshPlugins {
		if strings.Count(p, "/") != 1 {
			errs = append(errs, fmt.Errorf("shell.ohmyzsh_plugins: %q 应为 <owner>/<repo>", p))
		}
	}
	for _, r := range m.Secrets.AgeRecipients {
		if !strings.HasPrefix(r, "age1") {
			errs = append(errs, fmt.Errorf("secrets.age_recipients: %q 不是 age 公钥（应以 age1 开头）", r))
		}
	}
	if _, ok := m.Data["dsync"]; ok {
		errs = append(errs, errors.New("data.dsync 为 dsync 保留键，请改名"))
	}
	return errors.Join(errs...)
}
