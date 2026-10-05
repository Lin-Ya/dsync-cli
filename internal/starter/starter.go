// Package starter 内嵌一个最小的 v2 dotfiles 仓库（dsync init --create 使用，也是 v2 布局的测试 fixture）。
package starter

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

//go:embed all:files
var files embed.FS

// FS 返回模板仓库的文件系统（根目录即仓库根）。
func FS() fs.FS {
	sub, err := fs.Sub(files, "files")
	if err != nil {
		panic(err) // 嵌入路径在编译期确定，不会失败
	}
	return sub
}

// Write 把模板仓库写入 dir（dir 必须不存在或为空目录，避免覆盖已有内容）。
func Write(dir string) error {
	if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
		return fmt.Errorf("%s 已存在且不为空", dir)
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	src := FS()
	return fs.WalkDir(src, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		dst := filepath.Join(dir, filepath.FromSlash(p))
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		b, err := fs.ReadFile(src, p)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o644)
	})
}
