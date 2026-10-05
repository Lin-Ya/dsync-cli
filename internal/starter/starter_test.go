package starter

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Lin-Ya/dsync-cli/internal/config"
)

func TestWrite(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "dots")
	if err := Write(dir); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{".chezmoiroot", "dsync.toml", ".github/workflows/verify.yml", "home/dot_zshenv", "home/dot_config/mise/config.toml"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("缺少 %s（点开头的文件需 go:embed all:）", f)
		}
	}
	// 清单必须能通过校验
	if _, exists, err := config.LoadManifest(dir); err != nil || !exists {
		t.Errorf("dsync.toml: %v %v", exists, err)
	}
	if err := Write(dir); err == nil {
		t.Error("目标非空时应拒绝覆盖")
	}
}
