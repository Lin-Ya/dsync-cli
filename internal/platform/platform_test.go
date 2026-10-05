package platform

import (
	"io/fs"
	"testing"
	"testing/fstest"
)

const ubuntuRelease = `PRETTY_NAME="Ubuntu 24.04.1 LTS"
NAME="Ubuntu"
VERSION_ID="24.04"
ID=ubuntu
ID_LIKE=debian
`

func TestDetect(t *testing.T) {
	cases := []struct {
		name  string
		goos  string
		files fstest.MapFS
		want  Platform
	}{
		{
			name: "wsl2 ubuntu",
			goos: "linux",
			files: fstest.MapFS{
				"proc/version":   {Data: []byte("Linux version 6.6.36.3-microsoft-standard-WSL2 (root@x)")},
				"etc/os-release": {Data: []byte(ubuntuRelease)},
				"run/WSL":        {Mode: fs.ModeDir},
			},
			want: Platform{OS: "linux", Arch: "amd64", WSL: true, DistroID: "ubuntu", Version: "24.04"},
		},
		{
			// 容器与宿主共享内核：/proc/version 带 microsoft，但没有 WSL init 创建的 /run/WSL
			name: "WSL 宿主上的 docker 容器不是 WSL",
			goos: "linux",
			files: fstest.MapFS{
				"proc/version":   {Data: []byte("Linux version 6.6.36.3-microsoft-standard-WSL2 (root@x)")},
				"etc/os-release": {Data: []byte(ubuntuRelease)},
			},
			want: Platform{OS: "linux", Arch: "amd64", DistroID: "ubuntu", Version: "24.04"},
		},
		{
			name: "原生 linux / 容器",
			goos: "linux",
			files: fstest.MapFS{
				"proc/version":   {Data: []byte("Linux version 6.1.0-18-amd64 (debian-kernel@lists.debian.org)")},
				"etc/os-release": {Data: []byte("ID=debian\nVERSION_ID=\"12\"\n")},
			},
			want: Platform{OS: "linux", Arch: "amd64", DistroID: "debian", Version: "12"},
		},
		{
			name:  "缺失文件不报错",
			goos:  "linux",
			files: fstest.MapFS{},
			want:  Platform{OS: "linux", Arch: "amd64"},
		},
		{
			name:  "macOS 不读 linux 文件",
			goos:  "darwin",
			files: fstest.MapFS{"proc/version": {Data: []byte("microsoft")}},
			want:  Platform{OS: "darwin", Arch: "amd64"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := detect(c.files, c.goos, "amd64"); got != c.want {
				t.Errorf("got %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestLabel(t *testing.T) {
	if got := (Platform{OS: "linux", Arch: "amd64", WSL: true, DistroID: "ubuntu", Version: "24.04"}).Label(); got != "WSL2/ubuntu 24.04/amd64" {
		t.Errorf("Label = %q", got)
	}
	if got := (Platform{OS: "darwin", Arch: "arm64"}).Label(); got != "macOS/arm64" {
		t.Errorf("Label = %q", got)
	}
}
