package config

import (
	"strings"
	"testing"
)

const validManifest = `
version = 1
[deps]
mise = "2026.9.0"
chezmoi = "2.72.1"
[remote]
url = "https://github.com/Lin-Ya/dotfiles.git"
mirrors = ["https://gitee.com/lin-ya/dotfiles.git"]
[packages]
apt = ["build-essential", "curl", "zsh", "socat"]
brew = ["ripgrep", "fd"]
[shell]
default = "zsh"
ohmyzsh_plugins = ["zsh-users/zsh-autosuggestions"]
[provision]
enable = ["apt", "homebrew", "ohmyzsh", "wsl-ssh-agent", "docker-check"]
[secrets]
age_recipients = ["age1qyqszqgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpqyqs3290gq"]
[data]
editor = "nvim"
`

func TestParseManifest_Valid(t *testing.T) {
	m, err := ParseManifest([]byte(validManifest))
	if err != nil {
		t.Fatal(err)
	}
	if m.Remote.URL == "" || len(m.Packages.Apt) != 4 || m.Data["editor"] != "nvim" || len(m.Provision.Enable) != 5 {
		t.Errorf("解析结果: %+v", m)
	}
}

func TestParseManifest_Errors(t *testing.T) {
	cases := map[string]struct {
		in   string
		want []string // 错误信息需同时包含的片段（验证一次性汇总）
	}{
		"版本缺失": {"", []string{"version = 0"}},
		"未知字段": {"version = 1\n[packages]\npacman = []\n", []string{"pacman"}},
		"语义错误汇总": {
			`version = 1
[provision]
enable = ["apt", "apt", "nix"]
[shell]
default = "fish"
ohmyzsh_plugins = ["bad"]
[secrets]
age_recipients = ["ssh-ed25519 AAAA"]
[data]
dsync = 1
`,
			[]string{`重复步骤 "apt"`, `未知步骤 "nix"`, `"fish"`, `"bad"`, "age 公钥", "保留键"},
		},
		"TOML 语法错误": {"version = \n", []string{"第 1 行"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseManifest([]byte(c.in))
			if err == nil {
				t.Fatal("应报错")
			}
			for _, w := range c.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("错误信息缺少 %q:\n%v", w, err)
				}
			}
		})
	}
}

func TestDefaultManifest_ValidAndMatchesV1(t *testing.T) {
	m := DefaultManifest()
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	// v1 run_once_10 安装的 apt 包
	if strings.Join(m.Packages.Apt, " ") != "build-essential curl wget git zsh unzip ca-certificates ripgrep socat" {
		t.Errorf("apt = %v", m.Packages.Apt)
	}
	m.Provision.Enable[0] = "mutated"
	if DefaultManifest().Provision.Enable[0] == "mutated" {
		t.Error("DefaultManifest 不得共享底层切片")
	}
}
