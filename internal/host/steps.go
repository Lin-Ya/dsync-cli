package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Lin-Ya/dsync-cli/internal/exec"
	"github.com/Lin-Ya/dsync-cli/internal/provision"
)

var (
	done    = provision.Result{State: provision.Done}
	pending = func(d string) provision.Result { return provision.Result{State: provision.Pending, Detail: d} }
	manual  = func(d string) provision.Result { return provision.Result{State: provision.Manual, Detail: d} }
)

// ---------- packages：winget 补装 git / Windows Terminal / Docker Desktop ----------

type packagesStep struct{}

type pkg struct {
	id, name string
	present  func(x *Context) bool
}

var packages = []pkg{
	{"Git.Git", "Git", func(x *Context) bool { return x.has("git") }},
	{"Microsoft.WindowsTerminal", "Windows Terminal", func(x *Context) bool {
		m, _ := filepath.Glob(filepath.Join(x.env("LOCALAPPDATA"), "Packages", "Microsoft.WindowsTerminal*"))
		return len(m) > 0
	}},
	{"Docker.DockerDesktop", "Docker Desktop", dockerInstalled},
}

func dockerInstalled(x *Context) bool {
	return x.has("docker") || exists(filepath.Join(x.env("ProgramFiles"), "Docker", "Docker", "Docker Desktop.exe"))
}

func (packagesStep) ID() string { return "packages" }

func (packagesStep) missing(x *Context) []pkg {
	var out []pkg
	for _, p := range packages {
		if !p.present(x) {
			out = append(out, p)
		}
	}
	return out
}

func (s packagesStep) Check(_ context.Context, x *Context) (provision.Result, bool, error) {
	m := s.missing(x)
	if len(m) == 0 {
		return done, false, nil
	}
	var names []string
	for _, p := range m {
		names = append(names, p.name)
	}
	if !x.has("winget") {
		return manual("缺少 " + strings.Join(names, " / ") + "，且未找到 winget：请先从 Microsoft Store 安装「应用安装程序（App Installer）」"), false, nil
	}
	return pending("winget 安装 " + strings.Join(names, " / ")), true, nil
}

func (s packagesStep) Apply(ctx context.Context, x *Context) error {
	docker := false
	for _, p := range s.missing(x) {
		if err := x.run(ctx, exec.Cmd{Name: "winget", Args: []string{"install", "--id", p.id, "-e",
			"--accept-package-agreements", "--accept-source-agreements"}, Interactive: true}); err != nil {
			return fmt.Errorf("安装 %s 失败: %w", p.name, err)
		}
		docker = docker || p.id == "Docker.DockerDesktop"
	}
	if docker && !x.DryRun {
		return &RerunError{"Docker Desktop 已安装：请重启 Windows，并首次打开 Docker Desktop 完成初始化（登录 / 同意条款），然后重新运行 dsync host setup"}
	}
	return nil
}

// ---------- wsl-distro：安装发行版、迁移到自定义目录、设置默认用户 ----------

type distroStep struct{}

func (distroStep) ID() string { return "wsl-distro" }

func vhdxPath(x *Context) string { return filepath.Join(targetDir(x), "ext4.vhdx") }

func (distroStep) Check(ctx context.Context, x *Context) (provision.Result, bool, error) {
	if !registered(ctx, x) {
		d := "安装 " + x.Distro
		if x.InstallDir != "" {
			d += "，并迁移到 " + targetDir(x)
		}
		return pending(d), true, nil
	}
	if x.InstallDir != "" && !exists(vhdxPath(x)) {
		return pending(fmt.Sprintf("迁移 %s 到 %s（先导出备份）", x.Distro, targetDir(x))), false, nil
	}
	return done, false, nil
}

func (distroStep) Apply(ctx context.Context, x *Context) error {
	if !registered(ctx, x) {
		// --web-download 直接从微软 CDN 下载；较旧的 WSL 不支持时退回 Store 渠道
		err := x.run(ctx, exec.Cmd{Name: "wsl.exe", Args: []string{"--install", "-d", x.Distro, "--web-download"}, Interactive: true})
		if err != nil {
			x.Log("  --web-download 安装失败（WSL 版本较旧？），改用 Microsoft Store 渠道")
			if err := x.run(ctx, exec.Cmd{Name: "wsl.exe", Args: []string{"--install", "-d", x.Distro}, Interactive: true}); err != nil {
				return fmt.Errorf("安装 %s 失败（需以管理员身份运行）: %w", x.Distro, err)
			}
		}
		if x.DryRun {
			return nil
		}
		if !registered(ctx, x) {
			return &RerunError{"WSL 平台刚启用，需要重启 Windows；重启后重新运行 dsync host setup"}
		}
		if x.InstallDir == "" {
			return &RerunError{"首次进入 " + x.Distro + " 需创建 Linux 用户名 / 密码：在 Windows Terminal 中打开 " + x.Distro + " 完成后，重新运行 dsync host setup"}
		}
	}
	if x.InstallDir != "" && !exists(vhdxPath(x)) {
		return migrate(ctx, x)
	}
	return nil
}

// migrate 把发行版迁移到 targetDir：export（备份）→ unregister → import，再设置默认用户。
// 只有确认备份文件已生成才 unregister，失败时发行版保持原状。
func migrate(ctx context.Context, x *Context) error {
	dir := targetDir(x)
	backup := filepath.Join(dir, fmt.Sprintf("%s-backup-%s.tar", x.Distro, x.Timestamp()))
	if !x.DryRun {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	wsl := func(args ...string) error {
		return x.run(ctx, exec.Cmd{Name: "wsl.exe", Args: args, Interactive: true})
	}
	if err := wsl("--shutdown"); err != nil {
		return err
	}
	if err := wsl("--export", x.Distro, backup); err != nil {
		return fmt.Errorf("导出失败，未迁移（发行版保持原状）: %w", err)
	}
	if !x.DryRun && !exists(backup) {
		return fmt.Errorf("导出后未找到备份 %s，未迁移（发行版保持原状）", backup)
	}
	if err := wsl("--unregister", x.Distro); err != nil {
		return err
	}
	if err := wsl("--import", x.Distro, dir, backup); err != nil {
		return fmt.Errorf("导入失败（备份保留在 %s，可手动 wsl --import %s %s %s）: %w", backup, x.Distro, dir, backup, err)
	}
	x.Log("  已迁移到 %s（备份保留在 %s）", dir, backup)
	// import 后默认用户是 root：创建用户（免密 sudo，供后续 dsync init 使用）并写入 /etc/wsl.conf
	u := x.WSLUser
	script := fmt.Sprintf("id %[1]s >/dev/null 2>&1 || useradd -m -s /bin/bash %[1]s; "+
		"echo %[2]s > /etc/sudoers.d/dsync-%[3]s; chmod 440 /etc/sudoers.d/dsync-%[3]s; "+
		"printf '[user]\\ndefault=%%s\\n' %[1]s > /etc/wsl.conf", sq(u), sq(u+" ALL=(ALL) NOPASSWD:ALL"), u)
	// --exec 把参数原样交给进程；"--" 会把参数拼成一行交给默认 shell 再解析，破坏 '\'' 这类转义（实测）
	if err := wsl("-d", x.Distro, "-u", "root", "--exec", "sh", "-c", script); err != nil {
		return fmt.Errorf("设置默认用户 %s 失败，请进入发行版手动创建: %w", u, err)
	}
	return wsl("--shutdown")
}

// ---------- docker-integration：Docker Desktop 的 WSL 集成 ----------
//
// 修复 v1：bootstrap.ps1 修改的键 "Integration<发行版>" 并不存在（实测 settings-store.json），
// 所以 v1 的自动勾选从未生效。真实的键是 WslEngineEnabled 与 IntegratedWslDistros（数组）；
// 旧版 Docker Desktop 的 settings.json 使用小驼峰 wslEngineEnabled / integratedWslDistros。

type dockerStep struct{}

func (dockerStep) ID() string { return "docker-integration" }

type dockerFile struct {
	path            string
	engine, distros string // 键名
}

func dockerFiles(x *Context) []dockerFile {
	dir := filepath.Join(x.env("APPDATA"), "Docker")
	return []dockerFile{
		{filepath.Join(dir, "settings-store.json"), "WslEngineEnabled", "IntegratedWslDistros"},
		{filepath.Join(dir, "settings.json"), "wslEngineEnabled", "integratedWslDistros"},
	}
}

// edit 读取配置并返回需要的改动（changed=false 表示已就绪）；known=false 表示文件中没有任何已知键。
func (f dockerFile) edit(distro string) (out []byte, changed, known bool, err error) {
	b, err := os.ReadFile(f.path)
	if err != nil {
		return nil, false, false, err
	}
	var m map[string]any
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber() // 保持数字原样（避免大整数变成科学计数法）
	if err := dec.Decode(&m); err != nil {
		return nil, false, false, fmt.Errorf("%s: %w", f.path, err)
	}
	if v, ok := m[f.engine]; ok {
		known = true
		if v != true {
			m[f.engine] = true
			changed = true
		}
	}
	if v, ok := m[f.distros]; ok {
		list, isList := v.([]any)
		if !isList {
			return nil, false, false, fmt.Errorf("%s: %s 不是数组（schema 已变化）", f.path, f.distros)
		}
		known = true
		found := false
		for _, d := range list {
			if s, _ := d.(string); strings.EqualFold(s, distro) {
				found = true
			}
		}
		if !found {
			m[f.distros] = append(list, distro)
			changed = true
		}
	}
	if !changed {
		return nil, false, known, nil
	}
	out, err = json.MarshalIndent(m, "", "  ")
	return out, true, known, err
}

func (dockerStep) Check(_ context.Context, x *Context) (provision.Result, bool, error) {
	if !dockerInstalled(x) {
		return done, false, nil // 未安装（packages 步骤负责）：无需集成
	}
	var todo []string
	anyFile, anyKnown := false, false
	for _, f := range dockerFiles(x) {
		_, changed, known, err := f.edit(x.Distro)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return manual(fmt.Sprintf("无法解析 %v；请在 Docker Desktop → Settings → Resources → WSL Integration 勾选 %s", err, x.Distro)), false, nil
		}
		anyFile, anyKnown = true, anyKnown || known
		if changed {
			todo = append(todo, filepath.Base(f.path))
		}
	}
	switch {
	case !anyFile:
		return manual("未找到 Docker Desktop 配置：首次打开 Docker Desktop 完成初始化后重新运行"), false, nil
	case !anyKnown:
		return manual("Docker Desktop 配置中没有已知的 WSL 集成项（版本变化？）：请在 Settings → Resources → WSL Integration 勾选 " + x.Distro), false, nil
	case len(todo) > 0:
		return pending(fmt.Sprintf("启用 WSL 引擎与 %s 集成（写入 %s）", x.Distro, strings.Join(todo, "、"))), false, nil
	}
	return done, false, nil
}

func (dockerStep) Apply(ctx context.Context, x *Context) error {
	if cli := filepath.Join(x.env("ProgramFiles"), "Docker", "Docker", "DockerCli.exe"); exists(cli) {
		if err := x.run(ctx, exec.Cmd{Name: cli, Args: []string{"-SwitchLinuxEngine"}}); err != nil {
			x.Log("  切换 Linux 容器引擎失败（Docker Desktop 未运行？）: %v", err)
		}
	}
	for _, f := range dockerFiles(x) {
		out, changed, _, err := f.edit(x.Distro)
		if errors.Is(err, os.ErrNotExist) || (err == nil && !changed) {
			continue
		}
		if err != nil {
			return err
		}
		if x.DryRun {
			x.Log("  [dry-run] 写入 %s（备份为 .bak.dsync）", f.path)
			continue
		}
		if bak := f.path + ".bak.dsync"; !exists(bak) {
			orig, err := os.ReadFile(f.path)
			if err != nil {
				return err
			}
			if err := os.WriteFile(bak, orig, 0o644); err != nil {
				return err
			}
		}
		if err := os.WriteFile(f.path, append(out, '\n'), 0o644); err != nil {
			return err
		}
		x.Log("  已写入 %s", f.path)
	}
	x.Log("  若 Docker Desktop 正在运行，请在托盘菜单中 Restart 使设置生效")
	return nil
}

// ---------- ssh-agent：Windows OpenSSH Agent（供 WSL 经 npiperelay 复用） ----------

type sshAgentStep struct{}

func (sshAgentStep) ID() string { return "ssh-agent" }

// service 返回 "<Status>|<StartType>"（PowerShell 枚举名，不随系统语言变化）或 "missing"。
func (sshAgentStep) service(ctx context.Context, x *Context) (string, error) {
	return x.ps(ctx, `$s = Get-Service -Name ssh-agent -ErrorAction SilentlyContinue; if ($s) { '{0}|{1}' -f $s.Status, $s.StartType } else { 'missing' }`)
}

func defaultKey(x *Context) string { return filepath.Join(x.env("USERPROFILE"), ".ssh", "id_ed25519") }

// keyLoaded：ssh-add -l 退出码 0 = 已有密钥，1 = agent 中无密钥，其它 = 无法连接 agent。
func keyLoaded(ctx context.Context, x *Context) (loaded, reachable bool) {
	_, err := x.R.Query(ctx, exec.Cmd{Name: "ssh-add", Args: []string{"-l"}})
	var ee *exec.ExitError
	switch {
	case err == nil:
		return true, true
	case errors.As(err, &ee) && ee.ExitCode == 1:
		return false, true
	}
	return false, false
}

func (s sshAgentStep) Check(ctx context.Context, x *Context) (provision.Result, bool, error) {
	svc, err := s.service(ctx, x)
	if err != nil {
		return provision.Result{}, false, err
	}
	switch {
	case svc == "missing":
		return manual("未找到 ssh-agent 服务：以管理员身份执行 Add-WindowsCapability -Online -Name OpenSSH.Client~~~~0.0.1.0"), false, nil
	case svc != "Running|Automatic":
		return pending("ssh-agent 服务设为自动启动并启动（当前 " + svc + "）"), true, nil
	}
	if !exists(defaultKey(x)) {
		return manual("ssh-agent 已运行；未找到默认密钥 " + defaultKey(x) + "（用 ssh-keygen 生成，或手动 ssh-add 你的密钥）"), false, nil
	}
	if !x.has("ssh-add") {
		return manual("未找到 ssh-add（OpenSSH 客户端未安装？）"), false, nil
	}
	loaded, reachable := keyLoaded(ctx, x)
	switch {
	case loaded:
		return done, false, nil
	case !reachable:
		return manual("ssh-add 无法连接 ssh-agent"), false, nil
	}
	return pending("把 " + defaultKey(x) + " 加入 ssh-agent"), false, nil
}

func (s sshAgentStep) Apply(ctx context.Context, x *Context) error {
	svc, err := s.service(ctx, x)
	if err != nil {
		return err
	}
	if svc != "Running|Automatic" {
		if err := x.run(ctx, psCmd(`Set-Service -Name ssh-agent -StartupType Automatic -ErrorAction Stop; Start-Service ssh-agent -ErrorAction Stop`)); err != nil {
			return fmt.Errorf("启动 ssh-agent 失败（需管理员权限）: %w", err)
		}
	}
	if exists(defaultKey(x)) && x.has("ssh-add") {
		if loaded, reachable := keyLoaded(ctx, x); reachable && !loaded {
			// 直连终端：带口令的密钥需要输入口令
			if err := x.run(ctx, exec.Cmd{Name: "ssh-add", Args: []string{defaultKey(x)}, Interactive: true}); err != nil {
				x.Log("  ssh-add 失败，可稍后手动执行: ssh-add %s", defaultKey(x))
			}
		}
	}
	return nil
}

// ---------- wsl-init：在 WSL 内安装 dsync 并执行 dsync init ----------
//
// 新装的 WSL 里没有 SSH 密钥，所以与 v1 相同：给了 Windows 侧本地仓库（--source）时，
// 把整个仓库（含 .git）拷贝到 WSL 原生目录再 init，不在 WSL 内克隆私有仓库。
// dsync init 本身幂等，所以本步骤总是执行。

type wslInitStep struct{}

func (wslInitStep) ID() string { return "wsl-init" }

func (wslInitStep) Check(_ context.Context, x *Context) (provision.Result, bool, error) {
	return pending("在 " + x.Distro + " 内安装 dsync 并执行 dsync init " + strings.Join(initArgs(x), " ")), false, nil
}

func initArgs(x *Context) []string {
	var args []string
	switch {
	case x.Source != "":
		args = append(args, "--source", "~/"+winBase(x.Source))
	case x.Repo != "":
		args = append(args, sq(x.Repo))
	}
	return append(args, "--geo", string(x.Geo))
}

// Script 返回在 WSL 内执行的脚本（导出供测试断言）。
func Script(x *Context, linuxBinary string) (string, error) {
	bin, err := ToWSLPath(linuxBinary)
	if err != nil {
		return "", err
	}
	lines := []string{"set -e"}
	if x.Proxy != "" {
		p := dqWithHost(x.Proxy)
		lines = append(lines,
			`export WSL_HOST_IP="$(ip route show default 2>/dev/null | awk '{print $3; exit}')"`,
			fmt.Sprintf("export http_proxy=%[1]s https_proxy=%[1]s HTTP_PROXY=%[1]s HTTPS_PROXY=%[1]s", p),
			`export no_proxy="localhost,127.0.0.1,archive.ubuntu.com,security.ubuntu.com,mirrors.tuna.tsinghua.edu.cn"`)
	}
	lines = append(lines, "mkdir -p ~/.local/bin", "install -m 755 "+sq(bin)+" ~/.local/bin/dsync")
	if x.Source != "" {
		src, err := ToWSLPath(x.Source)
		if err != nil {
			return "", err
		}
		dest := "~/" + winBase(x.Source)
		// 跨 /mnt 的 cp -a 会带上 DrvFs 映射的只读 / 属主权限，普通用户在 .git/objects 里写入会被拒：
		// sudo 拷贝后 chown 交还当前用户（v1 troubleshooting.md 记录的问题）
		lines = append(lines,
			"sudo rm -rf "+dest, "sudo mkdir -p "+dest,
			"sudo cp -a "+sq(src+"/.")+" "+dest+"/",
			`sudo chown -R "$(id -un):$(id -gn)" `+dest)
	}
	lines = append(lines, "~/.local/bin/dsync init "+strings.Join(initArgs(x), " "))
	return strings.Join(lines, "\n"), nil
}

// dqWithHost 生成双引号字符串：除 ${WSL_HOST_IP} 外的 \ " $ ` 都按字面量转义（与 shellinit 一致）。
func dqWithHost(v string) string {
	const token = "${WSL_HOST_IP}"
	parts := strings.Split(v, token)
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "$", `\$`, "`", "\\`")
	for i, p := range parts {
		parts[i] = r.Replace(p)
	}
	return `"` + strings.Join(parts, token) + `"`
}

// winBase 返回 Windows 路径的最后一段（同时识别 \ 与 /，在任何平台上结果一致）。
func winBase(p string) string {
	p = strings.TrimRight(p, `\/`)
	if i := strings.LastIndexAny(p, `\/`); i >= 0 {
		return p[i+1:]
	}
	return p
}

func (wslInitStep) Apply(ctx context.Context, x *Context) error {
	bin, err := x.LinuxBinary(ctx)
	if err != nil {
		return err
	}
	script, err := Script(x, bin)
	if err != nil {
		return err
	}
	// --exec：脚本原样传给 bash（"--" 会经默认 shell 二次解析，破坏引号转义）
	if err := x.run(ctx, exec.Cmd{Name: "wsl.exe", Args: []string{"-d", x.Distro, "--exec", "bash", "-lc", script}, Interactive: true}); err != nil {
		return fmt.Errorf("WSL 内 dsync init 未完成（进入 %s 后可直接重跑 dsync init）: %w", x.Distro, err)
	}
	return nil
}
