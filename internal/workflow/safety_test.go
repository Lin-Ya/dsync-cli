package workflow

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	dexec "github.com/Lin-Ya/dsync-cli/internal/exec"
)

// 本文件：防止破坏用户数据的保护分支，以及中途失败时必须停下的流程。

// local.toml 有误时，所有会重写它的命令都必须拒绝执行（否则会用零值覆盖用户配置）。
func TestRefuseToOverwriteBrokenLocalToml(t *testing.T) {
	const broken = "geo = 'cn'\nbogus = 1\n"
	cases := map[string]func(f *fixture) error{
		"geo set": func(f *fixture) error { return f.Env().GeoSet(context.Background(), "official") },
		"init": func(f *fixture) error {
			f.write(t, "dots/.chezmoi.toml.tmpl", "")
			return f.Env().Init(context.Background(), InitOptions{Source: "~/dots"})
		},
	}
	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
			f := initFixture(t)
			f.write(t, ".config/dsync/local.toml", broken)
			if err := run(f); err == nil || !strings.Contains(err.Error(), "请先修正该文件") {
				t.Errorf("应拒绝执行: %v", err)
			}
			if got := f.local(t); got != broken {
				t.Errorf("local.toml 被改写:\n%s", got)
			}
			for _, c := range f.rec.Calls {
				if c.Mutating {
					t.Errorf("不应执行写操作: %s", c.Cmd)
				}
			}
		})
	}
}

// v1 布局：重新 chezmoi init 会按模板重写整份配置，age recipient 必须先读出来再传回去；
// 读不出来时中止，宁可不切换也不能丢掉加密配置。
func TestGeoSet_V1PreservesAgeRecipientOrAborts(t *testing.T) {
	setup := func(t *testing.T) (*fixture, string) {
		f := newFixture(t)
		f.write(t, "dots/.chezmoi.toml.tmpl", "")
		f.write(t, ".config/dsync/local.toml", "[repo]\npath = '~/dots'\n")
		root := realpath(t, filepath.Join(f.home, "dots"))
		f.tools("git", "chezmoi")
		f.rec.OnStdout("", "")
		f.rec.On("git rev-parse --show-toplevel", dexec.Result{Stdout: []byte(root + "\n")})
		return f, root
	}

	t.Run("保留已有 recipient", func(t *testing.T) {
		f, root := setup(t)
		f.rec.OnStdout("chezmoi --source "+root+" dump-config", `{"age":{"recipient":"age1keep"}}`)
		if err := f.Env().GeoSet(context.Background(), "official"); err != nil {
			t.Fatal(err)
		}
		var env string
		for _, c := range f.rec.Calls {
			if strings.HasPrefix(dexec.Cmd{Name: c.Cmd.Name, Args: c.Cmd.Args}.String(), "chezmoi --source "+root+" init") {
				env = strings.Join(c.Cmd.Env, " ")
			}
		}
		if !strings.Contains(env, "CHEZMOI_AGE_RECIPIENT=age1keep") || !strings.Contains(env, "DSYNC_GEO=official") {
			t.Errorf("chezmoi init 环境 = %q", env)
		}
	})

	t.Run("读不出时中止", func(t *testing.T) {
		f, root := setup(t)
		f.rec.On("chezmoi --source "+root+" dump-config", dexec.Result{ExitCode: 1, Stderr: []byte("bad config")})
		err := f.Env().GeoSet(context.Background(), "official")
		if err == nil || !strings.Contains(err.Error(), "避免丢失 age recipient") {
			t.Fatalf("应中止: %v", err)
		}
		if len(f.lines("chezmoi --source "+root+" init")) != 0 || len(f.lines("chezmoi --source "+root+" apply")) != 0 {
			t.Errorf("中止后不应 init / apply: %v", f.rec.Lines())
		}
	})
}

// 默认克隆目录已有别的内容时，init <地址> 必须拒绝，不能克隆进去或覆盖。
func TestInit_RefusesForeignDirAtCloneDest(t *testing.T) {
	f := initFixture(t)
	marker := f.write(t, DefaultCloneDir+"/notes.txt", "mine\n")
	err := f.Env().Init(context.Background(), InitOptions{Repo: "https://example.com/dots.git"})
	if err == nil || !strings.Contains(err.Error(), "已存在但不是 dotfiles 仓库") {
		t.Fatalf("应拒绝: %v", err)
	}
	if readFile(t, marker) != "mine\n" || len(f.lines("git clone")) != 0 {
		t.Error("不应克隆或改动已有目录")
	}
}

// 已有 origin 时不被 [remote].url 覆盖；[remote].mirrors 逐个推送，失败时报出是哪一个。
func TestSave_KeepsOriginAndPushesMirrors(t *testing.T) {
	tf := newToolFixture(t)
	origin := withRemote(t, tf)
	bare := func() string {
		p := filepath.Join(t.TempDir(), "mirror.git")
		if b, err := exec.Command("git", "init", "-q", "--bare", p).CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, b)
		}
		return p
	}
	m1, m2 := bare(), bare()
	tf.write(t, "dsync/dsync.toml", "version = 1\n[remote]\nurl = 'git@github.com:me/elsewhere.git'\nmirrors = ['"+m1+"', '"+m2+"']\n")

	if err := tf.Env().Save(context.Background(), SaveOptions{}); err != nil {
		t.Fatalf("%v\n%s", err, tf.out.String())
	}
	if got := tf.git(t, "remote", "get-url", "origin"); got != origin {
		t.Errorf("已有 origin 被改为 %q", got)
	}
	head := tf.git(t, "rev-parse", "HEAD")
	for _, r := range []string{origin, m1, m2} {
		out, _ := exec.Command("git", "-C", r, "rev-parse", "main").Output()
		if strings.TrimSpace(string(out)) != head {
			t.Errorf("%s 未收到推送", r)
		}
	}
	if strings.Count(tf.out.String(), "已推送到镜像") != 2 {
		t.Errorf("应逐个报告镜像推送:\n%s", tf.out.String())
	}

	missing := filepath.Join(t.TempDir(), "gone.git")
	tf.write(t, "dsync/dsync.toml", "version = 1\n[remote]\nmirrors = ['"+missing+"']\n")
	if err := tf.Env().Save(context.Background(), SaveOptions{}); err == nil || !strings.Contains(err.Error(), "推送到镜像 "+missing+" 失败") {
		t.Errorf("镜像推送失败应报出地址: %v", err)
	}
}

// sync 任一步失败即停止：拉取失败不部署；apply 失败不执行 provision 与 mise install。
func TestSync_StopsAtFailedStep(t *testing.T) {
	later := func(tf *toolFixture) []string {
		var out []string
		for _, l := range tf.rec.Lines() {
			if strings.HasPrefix(l, "getent passwd") || strings.HasPrefix(l, "mise install") {
				out = append(out, l)
			}
		}
		return out
	}

	t.Run("拉取失败", func(t *testing.T) {
		tf := newToolFixture(t)
		bare := withRemote(t, tf)
		os.RemoveAll(bare)
		err := tf.Env().Sync(context.Background())
		if err == nil || !strings.Contains(err.Error(), "拉取失败") {
			t.Fatalf("应报拉取失败: %v", err)
		}
		if tf.called("chezmoi --source "+tf.repo+" apply") || len(later(tf)) != 0 {
			t.Errorf("拉取失败后不应继续: %v", tf.rec.Lines())
		}
	})

	t.Run("apply 失败", func(t *testing.T) {
		tf := newToolFixture(t)
		tf.rec.On("chezmoi --source "+tf.repo+" apply", dexec.Result{ExitCode: 1})
		if err := tf.Env().Sync(context.Background()); err == nil {
			t.Fatal("apply 失败应报错")
		}
		if got := later(tf); len(got) != 0 {
			t.Errorf("apply 失败后不应执行 provision / mise install: %v", got)
		}
	})
}

func TestVerify_RejectsNonV2Repos(t *testing.T) {
	f := newFixture(t)
	f.write(t, "v1/.chezmoi.toml.tmpl", "")
	if err := os.MkdirAll(filepath.Join(f.home, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	for dir, want := range map[string]string{"v1": "v1 布局", "empty": "不是 dotfiles 仓库"} {
		if _, err := f.Env().Verify(context.Background(), VerifyOptions{Source: filepath.Join(f.home, dir)}); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: 应报 %q: %v", dir, want, err)
		}
	}
}

// dotfiles 仓库的 CI 读的是这段文本输出。
func TestVerifyReport_WriteText(t *testing.T) {
	var b bytes.Buffer
	VerifyReport{Errors: []string{"[linux/cn] a.tmpl: x", "[darwin/cn] a.tmpl: x"}, Warnings: []string{"w"}}.WriteText(&b)
	want := "[WARN] w\n[FAIL] [linux/cn] a.tmpl: x\n[FAIL] [darwin/cn] a.tmpl: x\n共 2 处错误\n"
	if b.String() != want {
		t.Errorf("输出:\n%s\n期望:\n%s", b.String(), want)
	}
}
