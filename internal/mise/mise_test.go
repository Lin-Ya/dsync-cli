package mise

import (
	"context"
	"testing"

	"github.com/Lin-Ya/dsync-cli/internal/exec"
)

const lsJSON = `{
  "node": [{"version": "24.1.0", "requested_version": "24", "installed": true, "active": true,
            "source": {"type": "mise.toml", "path": "/h/.config/mise/config.toml"}}],
  "aqua:neovim/neovim": [{"version": "0.10.2", "installed": false, "active": false,
            "source": {"type": "mise.toml", "path": "/h/.config/mise/config.toml"}}]
}`

func TestParseLsAndMissing(t *testing.T) {
	tools, err := ParseLs([]byte(lsJSON))
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 2 || tools[0].Name != "aqua:neovim/neovim" || tools[1].RequestedVersion != "24" {
		t.Fatalf("%+v", tools)
	}
	if tools[1].Source.Path != "/h/.config/mise/config.toml" {
		t.Errorf("source.path = %q", tools[1].Source.Path)
	}
	m := Missing(tools)
	if len(m) != 1 || m[0].Name != "aqua:neovim/neovim" {
		t.Errorf("Missing = %+v", m)
	}
	if _, err := ParseLs([]byte("not json")); err == nil {
		t.Error("非法 JSON 应报错")
	}
	if tools, err := ParseLs([]byte("{}")); err != nil || len(tools) != 0 {
		t.Errorf("空清单: %v %v", tools, err)
	}
}

func TestClient(t *testing.T) {
	r := (&exec.Recording{}).
		OnStdout("mise --version", "2026.9.0 linux-x64 (2026-09-01)\n").
		OnStdout("mise ls --global --json", lsJSON)
	c := Client{R: r}
	if v, err := c.Version(context.Background()); err != nil || v != "2026.9.0" {
		t.Errorf("Version = %q, %v", v, err)
	}
	if tools, err := c.ListGlobal(context.Background()); err != nil || len(tools) != 2 {
		t.Errorf("ListGlobal = %v, %v", tools, err)
	}
}
