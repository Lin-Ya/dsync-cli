// Package mirrors 是镜像源的单一事实源（取代 v1 的 lib/mirrors.sh）。
//
// 两种消费方式都从这里取值：
//   - 运行时环境变量：EnvFor(geo) → dsync shell-init 输出；
//   - 渲染型配置文件：TemplateData(geo) → 注入 chezmoi 模板数据 .dsync.mirrors。
package mirrors

import (
	"fmt"
	"sort"
)

// Geo 是镜像源开关。
type Geo string

const (
	CN       Geo = "cn"       // 国内镜像（默认）
	Official Geo = "official" // 官方源
)

// Default 是未配置时的 geo。
const Default = CN

// ParseGeo 校验并解析 geo 字符串。
func ParseGeo(s string) (Geo, error) {
	switch Geo(s) {
	case CN, Official:
		return Geo(s), nil
	}
	return "", fmt.Errorf("未知 geo %q（可选: cn | official）", s)
}

// cn 镜像表。official 一律为空串 = 使用工具默认（官方）源。
// 与 v1 lib/mirrors.sh 的 _mirror_cn 一一对应；新增组件时同步补测试。
var cn = map[string]string{
	"apt":         "mirrors.tuna.tsinghua.edu.cn",
	"brew_api":    "https://mirrors.tuna.tsinghua.edu.cn/homebrew-bottles/api",
	"brew_bottle": "https://mirrors.tuna.tsinghua.edu.cn/homebrew-bottles",
	"brew_brew":   "https://mirrors.tuna.tsinghua.edu.cn/git/homebrew/brew.git",
	"brew_core":   "https://mirrors.tuna.tsinghua.edu.cn/git/homebrew/homebrew-core.git",
	"npm":         "https://registry.npmmirror.com",
	"node":        "https://npmmirror.com/mirrors/node/",
	"pip":         "https://pypi.tuna.tsinghua.edu.cn/simple",
	"pyenv":       "https://gitee.com/mirrors/pyenv.git",
	"rustup_dist": "https://rsproxy.cn",
	"rustup_root": "https://rsproxy.cn/rustup",
	"cargo":       "https://rsproxy.cn",
	"docker":      "https://docker.m.daocloud.io",
}

// Components 返回全部组件名（排序后）。
func Components() []string {
	out := make([]string, 0, len(cn))
	for k := range cn {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// URL 返回组件在指定 geo 下的镜像地址；official 返回空串。
func URL(component string, geo Geo) (string, error) {
	u, ok := cn[component]
	if !ok {
		return "", fmt.Errorf("未知镜像组件 %q", component)
	}
	switch geo {
	case CN:
		return u, nil
	case Official:
		return "", nil
	}
	return "", fmt.Errorf("未知 geo %q", geo)
}

// EnvVar 是一个由镜像层管理的环境变量。
type EnvVar struct {
	Name      string
	Component string
}

// envVars：环境变量 → 组件。顺序即 shell-init 的输出顺序（与 v1 apply_mirror_env 一致）。
var envVars = []EnvVar{
	{"MISE_NODE_MIRROR_URL", "node"},
	{"PIP_INDEX_URL", "pip"},
	{"UV_DEFAULT_INDEX", "pip"},
	{"UV_INDEX_URL", "pip"},
	{"NPM_CONFIG_REGISTRY", "npm"},
	{"RUSTUP_DIST_SERVER", "rustup_dist"},
	{"RUSTUP_UPDATE_ROOT", "rustup_root"},
	{"MISE_PYENV_REPO", "pyenv"},
	{"HOMEBREW_API_DOMAIN", "brew_api"},
	{"HOMEBREW_BOTTLE_DOMAIN", "brew_bottle"},
	{"HOMEBREW_BREW_GIT_REMOTE", "brew_brew"},
	{"HOMEBREW_CORE_GIT_REMOTE", "brew_core"},
}

// EnvVars 返回镜像层管理的全部环境变量。
func EnvVars() []EnvVar {
	out := make([]EnvVar, len(envVars))
	copy(out, envVars)
	return out
}

// Setting 是一个环境变量在某 geo 下的取值；Value 为空表示应 unset。
type Setting struct {
	Name  string
	Value string
}

// EnvFor 返回 geo 对应的环境变量设置（cn = 导出镜像；official = 全部 unset）。
func EnvFor(geo Geo) ([]Setting, error) {
	out := make([]Setting, 0, len(envVars))
	for _, v := range envVars {
		u, err := URL(v.Component, geo)
		if err != nil {
			return nil, err
		}
		out = append(out, Setting{Name: v.Name, Value: u})
	}
	return out, nil
}

// TemplateData 返回注入 chezmoi 模板的镜像数据（组件 → URL，official 为空串）。
func TemplateData(geo Geo) (map[string]string, error) {
	out := make(map[string]string, len(cn))
	for _, c := range Components() {
		u, err := URL(c, geo)
		if err != nil {
			return nil, err
		}
		out[c] = u
	}
	return out, nil
}
