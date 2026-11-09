package mirrors

import "testing"

// 移植自 v1 tests/test_mirrors.sh，保证行为等价。
func TestURL_CN(t *testing.T) {
	cases := map[string]string{
		"node":        "https://npmmirror.com/mirrors/node/",
		"npm":         "https://registry.npmmirror.com",
		"pip":         "https://pypi.tuna.tsinghua.edu.cn/simple",
		"rustup_dist": "https://rsproxy.cn",
		"rustup_root": "https://rsproxy.cn/rustup",
		"apt":         "mirrors.tuna.tsinghua.edu.cn",
		"brew_api":    "https://mirrors.tuna.tsinghua.edu.cn/homebrew-bottles/api",
		"docker":      "https://docker.m.daocloud.io",
	}
	for comp, want := range cases {
		got, err := URL(comp, CN)
		if err != nil || got != want {
			t.Errorf("URL(%s, cn) = %q, %v; want %q", comp, got, err, want)
		}
	}
}

func TestURL_OfficialIsEmptyForAll(t *testing.T) {
	for _, c := range Components() {
		got, err := URL(c, Official)
		if err != nil || got != "" {
			t.Errorf("URL(%s, official) = %q, %v; want empty", c, got, err)
		}
	}
}

func TestComponents_CompleteCN(t *testing.T) {
	// v1 MIRROR_COMPONENTS 的完整清单
	want := []string{"apt", "brew_api", "brew_bottle", "brew_brew", "brew_core", "npm", "node", "pip", "pyenv", "rustup_dist", "rustup_root", "cargo", "docker"}
	if got := len(Components()); got != len(want) {
		t.Fatalf("组件数 = %d, want %d", got, len(want))
	}
	for _, c := range want {
		if u, err := URL(c, CN); err != nil || u == "" {
			t.Errorf("%s 缺少 cn 配置", c)
		}
	}
}

func TestURL_Errors(t *testing.T) {
	if _, err := URL("node", "bogus"); err == nil {
		t.Error("非法 geo 应返回错误")
	}
	if _, err := URL("nope", CN); err == nil {
		t.Error("未知组件应返回错误")
	}
	if _, err := ParseGeo("CN"); err == nil {
		t.Error("ParseGeo 应区分大小写")
	}
}

func TestEnvFor(t *testing.T) {
	cnEnv, err := EnvFor(CN)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]string{}
	for _, s := range cnEnv {
		m[s.Name] = s.Value
	}
	if m["MISE_URL_REPLACEMENTS"] != `{"https://nodejs.org/dist/":"https://npmmirror.com/mirrors/node/"}` || m["RUSTUP_DIST_SERVER"] != "https://rsproxy.cn" {
		t.Errorf("cn env 错误: %v", m)
	}
	// node 镜像不得经 MISE_NODE_MIRROR_URL（会写进 mise.lock）：两种 geo 下都应清除
	if v, ok := m["MISE_NODE_MIRROR_URL"]; !ok || v != "" {
		t.Errorf("cn 下 MISE_NODE_MIRROR_URL 应被清除: %q %v", v, ok)
	}
	off, _ := EnvFor(Official)
	for _, s := range off {
		if s.Value != "" {
			t.Errorf("official 下 %s 应为空（unset），得到 %q", s.Name, s.Value)
		}
	}
	if len(off) != 13 {
		t.Errorf("环境变量数 = %d, want 13（v1 apply_mirror_env 的 12 个，node 改用 url_replacements，另清除旧变量）", len(off))
	}
	// 每个变量引用的组件都必须存在
	for _, v := range EnvVars() {
		if _, err := URL(v.Component, CN); err != nil {
			t.Errorf("%s 引用未知组件 %s", v.Name, v.Component)
		}
	}
}

func TestTemplateData(t *testing.T) {
	d, _ := TemplateData(CN)
	if d["npm"] != "https://registry.npmmirror.com" || len(d) != len(Components()) {
		t.Errorf("TemplateData(cn) = %v", d)
	}
	d, _ = TemplateData(Official)
	if d["npm"] != "" {
		t.Errorf("TemplateData(official).npm = %q", d["npm"])
	}
}
