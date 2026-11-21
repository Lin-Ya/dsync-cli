# dsync v2 本地测试指引（新建 WSL 实例）

适用阶段：**M6 完成后**（工具在 `Lin-Ya/dsync-cli`，个人配置在 `Lin-Ya/dsync`，已迁移为 v2 布局）。

| 命令 | 作用 |
|---|---|
| `host setup`（Windows） | Windows 宿主初始化，并在 WSL 内执行 `init`（第 7 节） |
| `init` / `init --create` | 一条命令从零部署 / 用内置模板新建 dotfiles 仓库 |
| `provision` | 系统级安装步骤 |
| `sync` / `geo set` / `tool add` / `capture` / `save` | 日常同步 |
| `status` / `doctor` / `shell-init` / `verify` | 只读 |
| `self-update` | 从 Release 更新自身 |

> 默认流程只影响新建的测试实例 `dsync-test`。dotfiles 仓库的远端用 Windows 盘上的本地副本，**不会推送到 GitHub**。
> 第 8 节"真实 GitHub 流程"会真正访问 GitHub，按需进行。

---

## 0. 准备（在当前开发 WSL 中）

### 0.1 dotfiles 仓库副本

作为测试实例的 origin，`dsync save` / `--push` 只会推到这里：

```bash
WIN=/mnt/c/Users/<Windows用户名>/dsync-test      # 换成你的 Windows 用户名
mkdir -p "$WIN"
git clone --bare ~/dsync "$WIN/origin.git"        # ~/dsync 是 dotfiles 仓库（含 v2 布局与 tag v1-final）
git -C "$WIN/origin.git" symbolic-ref HEAD refs/heads/v2   # v2 尚未合并到 main 前，让克隆默认检出 v2
```

### 0.2 获取 dsync 二进制（二选一）

- **方式 A：从 Release 安装**（验证 `install.sh` 和 `self-update`）
  - 在 dsync-cli 发布一个预发布版本：`git -C ~/dsync-cli tag v2.0.0-alpha.1 && git -C ~/dsync-cli push origin v2.0.0-alpha.1`。
  - 推送 tag 会触发 Release 工作流，构建 5 个平台的二进制并附上 `checksums.txt`。
- **方式 B：本地编译后拷贝**（不需要发布，改完代码就能测）

  ```bash
  git clone git@github.com:Lin-Ya/dsync-cli.git ~/dsync-cli    # 首次
  cd ~/dsync-cli
  mise x go@1.25 -- env CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o "$WIN/dsync_linux_amd64" ./cmd/dsync
  mise x go@1.25 -- env CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o "$WIN/dsync.exe" ./cmd/dsync
  ```

## 1. 新建测试实例（Windows PowerShell）

```powershell
wsl --install Ubuntu-24.04 --name dsync-test --location D:\Linux\dsync-test
# 首次启动会要求创建用户名/密码（建议与日常不同，例如 tester）
```

- 你的 WSL 版本（2.7.13）支持 `--name` / `--location`，不影响现有的 `Ubuntu-24.04` 实例。
- 进入：`wsl -d dsync-test`；查看：`wsl -l -v`。

在测试实例中先设置代理（下载 mise / chezmoi / oh-my-zsh 需要访问 GitHub；Windows 侧代理需开启"允许局域网连接"）：

```bash
export WSL_HOST_IP="$(ip route show default | awk '{print $3}')"
export http_proxy="http://$WSL_HOST_IP:7890" https_proxy="http://$WSL_HOST_IP:7890"
export no_proxy="localhost,127.0.0.1,mirrors.tuna.tsinghua.edu.cn,archive.ubuntu.com,security.ubuntu.com"
WIN=/mnt/c/Users/<Windows用户名>/dsync-test
```

## 2. 安装 dsync

**方式 A：install.sh（公开仓库，匿名即可）**

1. 安装：

   ```bash
   curl -fsSL https://raw.githubusercontent.com/Lin-Ya/dsync-cli/main/install/install.sh | sh
   export PATH="$HOME/.local/bin:$PATH"
   ```

   - 期望输出：`dsync v2.0.0-alpha.1 已安装到 ~/.local/bin/dsync`。
   - 脚本会按 Release 中的 `checksums.txt` 校验 sha256，不符即拒绝安装。
   - 在 `sh` 后加 `-s -- <仓库地址>`，会在安装后直接执行 `dsync init <仓库地址>`。
   - 遇到 GitHub API 速率限制时再 `export GITHUB_TOKEN=<token>` 重跑（可选）。你的 dotfiles 仓库若为私有，克隆时仍需凭据。

**方式 B：拷贝本地编译的二进制**

```bash
mkdir -p ~/.local/bin && install -m 755 "$WIN/dsync_linux_amd64" ~/.local/bin/dsync
export PATH="$HOME/.local/bin:$PATH"
```

验证：`dsync version`

## 3. 测试 A：dsync init（主线）

```bash
dsync --dry-run init "$WIN/origin.git"    # 先看会做什么（不下载、不写文件）
dsync init "$WIN/origin.git"              # 真实执行（会提示输入 sudo 密码）
```

六个步骤依次是：

1. 下载并校验 mise / chezmoi，安装到 `~/.local/bin`
2. 克隆仓库到 `~/.local/share/dsync/repo`
3. 写入 `local.toml`
4. 生成 chezmoi 配置（v2 布局，取代 `.chezmoi.toml.tmpl`）并 apply
5. provision
6. mise install（按 lockfile）

**检查点：**

| 项目 | 命令 | 期望 |
|---|---|---|
| 底座工具 | `~/.local/bin/mise --version; ~/.local/bin/chezmoi --version` | 2026.9.0 / v2.72.1 |
| 本机配置 | `cat ~/.config/dsync/local.toml` | `geo = 'cn'`，`path` 指向克隆目录 |
| chezmoi 配置 | `cat ~/.config/chezmoi/chezmoi.toml` | 文件头"由 dsync 生成"，`sourceDir` 为克隆目录下的 `home`，含 `[data.dsync.mirrors]` |
| 配置渲染 | `grep registry ~/.npmrc` | npmmirror |
| shell 环境 | `exec zsh` 后 `echo $NPM_CONFIG_REGISTRY` | npmmirror（来自 `~/.zshenv` 中的 `dsync shell-init`） |
| provision | `dsync provision --check` | 除 docker-check 外全部 `[完成]` |
| 工具 | `node -v; fzf --version` | 可用 |
| 整体状态 | `dsync status` | 配置已同步，工具全部已安装，无待执行的系统步骤 |
| 幂等 | 再执行一次 `dsync init`（不带参数） | 各步骤跳过，不重新下载 |

> ⚠️ **已知环境问题**：清华镜像目前对你的网络下载软件包返回 403（索引能下载）。如果 apt 报 403，用官方源测试：`dsync init "$WIN/origin.git" --geo official`。

**可选：`init --create`**（用内置 starter 模板新建 dotfiles 仓库）。需要换一个全新实例，或先删掉 `~/.local/share/dsync/repo` 和 `~/.config/dsync/local.toml`：

```bash
dsync init --create                      # 只在本地建仓库；加 --github <你>/<新仓库名> 会用 gh 创建私有仓库并推送
dsync verify                             # starter 的 3 个模板在 4 种组合下均可渲染
```

## 4. 测试 B：日常命令

```bash
exec zsh
dsync status; dsync doctor                # doctor 各项基本 [ OK ]（docker 视情况为 [WARN]）
dsync verify                              # 11 个模板在 linux/darwin × cn/official 下均可渲染
```

**geo set**（v1 switch-geo 缺陷已修复）：

```bash
dsync geo set official && grep registry ~/.npmrc      # 期望 registry.npmjs.org
exec zsh                                              # 然后执行：echo $NPM_CONFIG_REGISTRY，期望为空（official 下清除镜像变量）
dsync geo set cn && grep registry ~/.npmrc            # 期望恢复 npmmirror
```

**tool add / capture / save / sync：**

```bash
R=~/.local/share/dsync/repo
dsync tool add jq@1.7.1                   # 编辑 home/dot_config/mise/config.toml → 部署 → 安装 → lock → 提交
jq --version                              # jq-1.7.1
git -C $R log -1 --stat                   # 改动 home/dot_config/mise/config.toml 与 private_mise.lock
head -8 $R/home/dot_config/mise/config.toml   # 注释仍在

git config --global alias.hello '!echo hello'   # 修改一个受管理的文件
dsync capture ~/.gitconfig                # 写回 home/dot_gitconfig.tmpl 并提交
dsync capture ~/.npmrc                    # 期望拒绝（模板含变量），提示 chezmoi edit

dsync save                                # 推送到 origin.git（本地副本）
dsync sync                                # pull → 生成配置 → apply → provision（全部完成）→ mise install --locked
```

**self-update**（只在方式 A 安装、且已发布更新版本时有意义）：

```bash
dsync --dry-run self-update               # 显示将替换为哪个版本
dsync self-update                         # 已是最新时提示"无需更新"
```

## 5. 测试 C：旧机器从 v1 迁移

模拟"一台用 v1 部署的机器，拉到迁移提交后换用 v2"。需要另建一个全新实例（例如 `dsync-mig`）：

```bash
git clone "$WIN/origin.git" ~/dsync && cd ~/dsync
git checkout -q -b main-sim v1-final && git branch -q -u origin/v2   # 停在 v1，上游指向已迁移的 v2
bash ./bootstrap.sh --skip-docker         # v1 部署（与真实旧机器一致：~/.local/share/chezmoi → ~/dsync）
exec zsh

# 按 dotfiles 仓库 README 的"旧机器从 v1 迁移"操作：
#   1) 安装 v2 dsync（第 2 节；会替换 ~/.local/bin/dsync 这个指向 v1 脚本的软链）
dsync sync                                # 期望日志出现"仓库布局 v1 → v2"，随后按 v2 部署
exec zsh
```

**检查点：**

| 项目 | 期望 |
|---|---|
| v1 遗留文件 | `~/.config/dsync/bin`、`~/.config/dsync/lib` 已被删除（`.chezmoiremove`） |
| `~/.zshenv` | 改为 `eval "$(dsync shell-init zsh)"`；`echo $NPM_CONFIG_REGISTRY` 仍为 npmmirror |
| 渲染文件 | `.npmrc` / `.cargo/config.toml` / `.docker/daemon.json` 与迁移前内容一致 |
| 状态 | `dsync status`：配置已同步、工具全部已安装、无待执行的系统步骤 |

## 6. Windows：获取 dsync.exe

- **方式 A：install.ps1**（需已发布 Release）：

  ```powershell
  Invoke-RestMethod -OutFile "$env:TEMP\install-dsync.ps1" `
    'https://raw.githubusercontent.com/Lin-Ya/dsync-cli/main/install/install.ps1'
  powershell -ExecutionPolicy Bypass -File "$env:TEMP\install-dsync.ps1"
  ```

  - 安装到 `%LOCALAPPDATA%\dsync\bin\dsync.exe` 并加入用户 PATH，新开的终端生效。
  - 在命令末尾加参数，会安装后直接执行 `dsync host setup <参数>`。
  - 不要用 `irm | iex`：脚本带 UTF-8 BOM（PowerShell 5.1 读中文必需），经 scriptblock 执行会失败。
  - 在 PowerShell 7 中执行上面的命令也可以。

- **方式 B**：用 0.2 节编译出的 `$WIN/dsync.exe`；没有 Release 时，用 `--linux-binary` 指定 0.2 节编译出的 linux 版。

## 7. 测试 D：Windows 宿主（dsync host setup）

在 **Windows PowerShell** 中操作，取代 v1 的 `bootstrap.ps1`。只读检查不需要管理员权限，真正执行需要管理员 PowerShell。

### 7.1 只读检查（任意 PowerShell）

```powershell
cd C:\Users\<Windows用户名>\dsync-test
.\dsync.exe host setup --check         # 你的日常环境上：前四项应为 [完成]，wsl-init 为 [待办]
.\dsync.exe host setup --check --json
.\dsync.exe --dry-run host setup --source D:\Develop\dsync   # 打印将执行的全部命令，不做任何改动
```

### 7.2 在测试实例上真实执行（管理员 PowerShell）

借用第 1 节建好的 `dsync-test` 实例（它已注册，`wsl-distro` 步骤会判定为完成），让 wsl-init 在其中执行：

```powershell
.\dsync.exe host setup "/mnt/c/Users/<Windows用户名>/dsync-test/origin.git" `
  --distro dsync-test --linux-binary .\dsync_linux_amd64 --proxy 'http://${WSL_HOST_IP}:7890'
```

- **仓库参数**：原样传给 WSL 内的 `dsync init`，这里用本地副本的 WSL 路径。
  用 `--source <Windows 目录>` 则会把整个本地仓库拷贝进 WSL 后再 init；新机器上 WSL 里还没有凭据时用这种方式。
- **`--proxy`**：只用于这一次在 WSL 内执行的 `dsync init`。新实例里还没有任何代理配置，下载 mise / chezmoi 需要它；`${WSL_HOST_IP}` 会自动替换为 Windows 宿主 IP。
- **docker-integration 步骤**：会把 `dsync-test` 加入 Docker Desktop 的 WSL 集成列表，即修改 `%APPDATA%\Docker\settings-store.json`。首次修改前会备份为 `.bak.dsync`，不想改可以事后用备份还原。

**检查点：**

| 项目 | 期望 |
|---|---|
| 计划输出 | packages / wsl-distro / ssh-agent 为 `[完成]` |
| WSL 内 | 自动执行第 3 节 `dsync init` 的六个步骤 |
| 完成后 | `wsl -d dsync-test` 进入实例，`dsync status` 配置已同步、工具全部已安装 |
| 幂等 | 再执行一次同样的命令，各步骤跳过（wsl-init 每次都会执行，init 本身幂等） |
| 非管理员 | 有需要管理员的待办时，不做任何改动，提示以管理员身份运行 |

**全新机器的完整流程**（例如重装系统后），在管理员 PowerShell 中执行：

```powershell
dsync host setup --source D:\Develop\dsync --proxy 'http://${WSL_HOST_IP}:7890'
```

遇到需要重启 Windows、首次进入发行版创建用户等情况，命令会暂停并说明原因，处理完重新运行同一条命令即可。

## 8. 真实 GitHub 流程（可选，会访问 GitHub）

```bash
# dotfiles 仓库若为私有：克隆需要凭据（用户名 + 个人访问令牌；若要 dsync save 推送，令牌还需写权限）
git config --global credential.helper store      # 首次输入后保存（明文存于 ~/.git-credentials，测试实例可接受）
dsync init https://github.com/Lin-Ya/dsync.git   # dotfiles 仓库（v2 合并到 main 之前需先 checkout v2）
```

⚠️ 这种方式下，`dsync save` / `tool add --push` 会**真正推送到 GitHub**，测试提交请勿推送，或测试后自行回退。

## 9. 反馈问题时请附上

```bash
dsync version
dsync status --json
dsync doctor --json
dsync provision --check --json
dsync verify --json
dsync host setup --check --json        # Windows 宿主相关问题（PowerShell 中执行）
```

以及出问题那条命令的完整输出。

## 10. 清理

```powershell
wsl --unregister dsync-test     # 删除测试实例及其全部数据（dsync-mig 同理）
```
