# dsync 使用指南

面向日常使用者：怎么装、怎么在新机器上部署、每天怎么用、出问题怎么查。
开发与测试见 [README](../README.md) 和 [testing-guide.md](testing-guide.md)。

## 1. 先建立三个概念

dsync 自己不保存任何配置，它只编排三样东西：

| 是什么 | 在哪里 | 是否多机共享 | 怎么改 |
|---|---|---|---|
| **dotfiles 仓库**：配置文件源（chezmoi）+ 工具清单（mise）+ `dsync.toml` | 默认 `~/.local/share/dsync/repo`（`dsync status` 的"仓库"行显示实际位置） | 是（git） | `dsync tool add` / `dsync capture` / 直接编辑后 `dsync save` |
| **本机配置** `local.toml`：geo、代理、git 身份、机器标签、仓库位置 | `~/.config/dsync/local.toml` | 否（不入库） | `dsync config set`（geo 用 `dsync geo set`），见[第 6 节](#6-本机配置-localtoml) |
| **部署结果**：`~/.zshrc`、`~/.npmrc`、已安装的工具… | `$HOME` 下 | 否 | 不要直接当作"真相"——改完要写回仓库，否则下次 `sync` 会提示覆盖 |

数据只有一个方向是自动的：**仓库 → 本机**（`dsync sync`）。反方向（本机 → 仓库）必须显式执行 `capture` / `tool add` / `tool remove` / `save`。

## 2. 安装

仓库私有，下载需要 Token（fine-grained token，对 `Lin-Ya/dsync-cli` 有 Contents 只读权限；克隆私有 dotfiles 仓库还需要对该仓库的权限）。

**Linux / macOS / WSL**

```bash
export GITHUB_TOKEN=<token>
curl -fsSL -H "Authorization: Bearer $GITHUB_TOKEN" -H "Accept: application/vnd.github.raw" \
  "https://api.github.com/repos/Lin-Ya/dsync-cli/contents/install/install.sh" | sh
export PATH="$HOME/.local/bin:$PATH"
dsync --version
```

- 安装到 `~/.local/bin/dsync`，按 Release 的 `checksums.txt` 校验 sha256。
- 在 `sh` 后加 `-s -- <仓库地址>`，安装后直接执行 `dsync init <仓库地址>`。
- `DSYNC_VERSION=<tag>` 指定版本，`DSYNC_INSTALL_DIR=<目录>` 指定安装位置。

**Windows**：见[第 9 节](#9-windows-宿主)。

## 3. 首次部署

访问 GitHub 需要代理时，先在当前终端导出 `http_proxy` / `https_proxy`（dsync 的下载和 git 都会用到），部署完成后再把代理写进 `local.toml` 固化。

三种入口，选一种：

```bash
dsync init https://github.com/you/dotfiles.git   # 已有 dotfiles 仓库：克隆到 ~/.local/share/dsync/repo
dsync init --source ~/dotfiles                   # 仓库已在本地：原地使用，不克隆
dsync init --create [--github you/dotfiles]      # 还没有仓库：用内置 starter 模板新建（--github 需要 gh）
```

`init` 依次执行六步，**每步先检测再执行，失败后修好原因直接重跑即可**（不带参数时沿用已记录的仓库）：

1. 下载固定版本的 mise / chezmoi 并校验（已安装则跳过；git 需系统自带）
2. 获取 dotfiles 仓库
3. 写入 `local.toml`（geo、仓库位置、git 身份——见下文）
4. 生成 chezmoi 配置并 `chezmoi apply`
5. `provision`：系统级步骤（会用到 sudo）
6. `mise install`（有 lockfile 时严格按锁定版本）

想先看会做什么：`dsync --dry-run init <仓库地址>`。国内镜像不可用时加 `--geo official`。

**git 身份**：`capture` / `tool add` / `save` 需要提交 dotfiles 仓库。第 3 步按以下顺序补全 `local.toml` 的 `[user]`：已有值 → 本机 `git config --global` 中的身份（在 `~/.gitconfig` 被受管理的版本覆盖之前导入）→ 在终端中提问（直接回车跳过；经 `curl | sh` 执行等非交互场景不提问）。仍未设置时，`init` 结束时会提示，之后补上即可（立即生效，无需 `dsync sync`）：

```bash
dsync config set user.name 'Your Name' user.email you@example.com
```

也可以在 `init` **之前**先执行这条命令：没有仓库时它只保存，`init` 会直接使用。

**部署后**

```bash
exec zsh          # 进入新 shell，让 ~/.zshenv 中的 dsync shell-init 生效
dsync status      # 确认：配置已同步、工具全部已安装、无待执行的系统步骤
```

需要长期使用代理：`dsync config set proxy.http 'http://${WSL_HOST_IP}:7890' proxy.https 'http://${WSL_HOST_IP}:7890'`（见[第 6 节](#6-本机配置-localtoml)）。

用 `--create` 且没带 `--github` 时，仓库只在本地。要让其它机器能同步，在 GitHub 新建一个空的私有仓库，把地址写进仓库根目录 `dsync.toml` 的 `[remote]`：

```toml
[remote]
url = "git@github.com:you/dotfiles.git"
```

然后 `dsync save`：仓库没有 `origin` 时按这个地址添加，首次推送并建立跟踪。

## 4. 日常使用

| 我想… | 命令 |
|---|---|
| 把其它机器的改动同步到本机 | `dsync sync` |
| 看本机和仓库差在哪 | `dsync status` |
| 新增 / 升级一个工具 | `dsync tool add node@22.11.0` |
| 移除一个工具 | `dsync tool remove jq` |
| 把本机改过的配置文件写回仓库 | `dsync capture ~/.zshrc` |
| 让一个新文件受管理 | `chezmoi add ~/.config/foo/config.toml`，然后 `dsync save` |
| 提交并推送仓库里的全部改动 | `dsync save` |
| 查看 / 切换镜像源 | `dsync geo` / `dsync geo set cn`、`dsync geo set official` |
| 改 git 身份、代理、机器标签 | `dsync config set <键> <值>`（查看：`dsync config show`） |
| 补跑系统级安装步骤 | `dsync provision` |
| 排查环境问题 | `dsync doctor` |

所有会写东西的命令都支持全局 `--dry-run`：只读查询照常执行，写操作只打印。

### 4.1 同步：`dsync sync`

`git pull --rebase --autostash` → 重新生成 chezmoi 配置 → `chezmoi apply` → `provision` → `mise install --locked`。

- 本机改过某个受管文件时，chezmoi 会询问是否覆盖。想保留本机改动，先 `dsync capture <文件>`。
- `mise install --locked` 失败说明 lockfile 与工具清单不一致（不会静默退化为无锁安装）：在任一台机器上 `mise lock --global`，再 `dsync capture ~/.config/mise/mise.lock && dsync save`。
- 仓库还没有 lockfile 时按配置普通安装；第一次 `dsync tool add` 会生成它。

### 4.2 工具：`dsync tool add` / `dsync tool remove`

```bash
dsync tool add node@22.11.0
dsync tool add npm:typescript@5.6.2 jq@1.7.1 --push
dsync tool remove jq             # 别名 dsync tool rm
```

先拉取仓库 → 由 mise 编辑仓库里的工具清单（注释保留；add 同时安装）→ 部署到 `~/.config/mise/config.toml` → 更新 lockfile → 提交（信息为 `chore(mise): add …` / `chore(mise): remove …`）。默认**只提交不推送**，加 `--push` 或之后 `dsync save`。

- `remove` 只写工具名，不带版本。清单里没有该工具时直接报错，不会产生空提交。
- `remove` 不卸载已安装的版本（项目里可能还在用）；需要清理时执行 `mise prune`。其它机器 `dsync sync` 后同样只是不再安装它。

### 4.3 改配置文件

按"改的是什么"分三种情况：

| 情况 | 做法 |
|---|---|
| 直接改了 `$HOME` 下的普通文件（如 `~/.zshrc`） | `dsync capture ~/.zshrc`（写回源并提交；`--push` 一并推送，`--no-commit` 只写回） |
| 文件的源是**含变量的模板**（如随 geo 变化的 `~/.npmrc`），`capture` 会拒绝 | `chezmoi edit --apply ~/.npmrc` 编辑模板本身，然后 `dsync save` |
| 想直接改仓库里的源文件 | 编辑 `<仓库>/home/...`，`chezmoi apply` 部署到本机，然后 `dsync save` |

`capture` 拒绝含变量的模板是有意的：把渲染结果写回去会把 `{{ ... }}` 固化成当前机器的值。

### 4.4 提交与推送：`dsync save`

`git add -A` → 提交（默认信息 `chore: dsync save (<主机名>)`，`-m` 自定义）→ 推送；`dsync.toml` 的 `[remote].mirrors` 中列出的镜像仓库也会一并推送。`--no-push` 只提交。

推送规则（`capture --push`、`tool add/remove --push` 相同）：仓库没有 `origin` 而 `dsync.toml` 设置了 `[remote].url` 时先按它添加 `origin`；当前分支没有上游时推送到 `origin` 并建立跟踪。没有需要提交的改动时也会把之前未推送的提交推上去。

`capture` 和 `tool add` 默认不推送，所以一个常见节奏是：改几处 → 最后 `dsync save` 一次推上去 → 其它机器 `dsync sync`。

### 4.5 镜像源：`dsync geo` / `dsync geo set cn|official`

`dsync geo` 显示当前镜像源及其来源。`dsync geo set` 写入 `local.toml` → 重新生成 chezmoi 配置 → `chezmoi apply`，于是 `.npmrc`、cargo 配置等渲染型文件一起切换。环境变量（`NPM_CONFIG_REGISTRY`、`PIP_INDEX_URL` 等）由 `shell-init` 在开 shell 时导出，**切换后需要 `exec zsh` 或新开终端**。

不要手改 `local.toml` 里的 `geo`：那样只有环境变量会变，渲染型文件要等下次 `sync`。

注意：geo=cn 时 `provision` 会把 apt 源换成清华镜像（原文件备份为 `*.bak.dsync`）；切回 official **不会**自动还原 apt 源。

### 4.6 系统级步骤：`dsync provision`

```bash
dsync provision --check              # 只看计划：[完成] / [待办] / [手动]
dsync provision                      # 执行全部待办
dsync provision --only ohmyzsh,chsh  # 只处理指定步骤
```

| 步骤 | 适用 | 做什么 |
|---|---|---|
| `apt` | Ubuntu / Debian | 按 geo 切换 apt 源，安装 `dsync.toml [packages].apt` |
| `wsl-ssh-agent` | WSL | 下载 npiperelay，桥接 Windows ssh-agent（见 [ssh-agent.md](ssh-agent.md)） |
| `homebrew` | macOS | 安装 Homebrew 与 `[packages].brew`（需先 `xcode-select --install`） |
| `ohmyzsh` | Linux / macOS | 安装 oh-my-zsh 与 `[shell].ohmyzsh_plugins` |
| `chsh` | Linux / macOS | 把登录 shell 设为 `[shell].default` |
| `docker-check` | Linux / macOS | 检查 docker；macOS 上经 brew 安装 OrbStack（见 [docker.md](docker.md)） |

完成与否以实际检测为准，可随时重跑。`wsl-ssh-agent` / `ohmyzsh` / `chsh` / `docker-check` 失败只告警、不中断；`[手动]` 项会说明需要你自己做什么。

## 5. 查看状态与排错

**`dsync status`**——日常入口，"配置 / 工具 / 系统"三行不正常时会直接给出下一步命令：

```
平台    WSL2/ubuntu 24.04/amd64
镜像源  cn（来自 local.toml）
仓库    /home/you/.local/share/dsync/repo [v2 布局，经 local.toml repo.path 定位]
git     main → origin/main  ↑1 未推送  2 个未提交改动
配置    1 个文件与仓库不一致:
          MM .zshrc
          → 首列 = 本机改动，次列 = 部署将做的变更。保留本机改动: dsync capture <文件>；按仓库部署: dsync sync
工具    12 个，缺失 1 个: jq@1.7.1（dsync sync）
系统    待执行: ohmyzsh（dsync provision）
```

- 手工改了 `local.toml` / `dsync.toml`（或拉取到别人改的 `dsync.toml`）但还没部署时，"配置"行显示 `local.toml / dsync.toml / geo 有改动尚未部署 → dsync sync`（JSON 中为 `"config_stale": true`）。
- "配置"行的两列代码来自 `chezmoi status`：`A` 新增、`M` 修改、`D` 删除。首列非空 = 你在本机改过它；只有次列 = 仓库有更新尚未部署。
- git 行：`↑ 未推送` / `未提交改动` → `dsync save`；`↓ 待拉取` → `dsync sync`（以上次 fetch 为准）。

**`dsync doctor`**——逐项检查底座工具、`local.toml`、仓库、git 身份、上游与 `[remote].url`、工具安装、ssh-agent、docker，`[WARN]` / `[FAIL]` 项下方的 `→` 是处理办法。有 `[FAIL]` 时退出码为 1。

**机器可读输出**：`status`、`doctor`、`verify`、`provision --check`、`host setup --check` 支持 `--json`，须写在子命令之后（`dsync status --json`）；其它命令传 `--json` 会报错。反馈问题时请附上这几条的输出和 `dsync version`。

**退出码**：`0` 成功；`1` doctor / verify 发现问题；`2` 用法或执行错误。

## 6. 本机配置 `local.toml`

`~/.config/dsync/local.toml`，不入库，每台机器一份。推荐用命令修改，而不是手工编辑：

```bash
dsync config show                                              # 文件位置与各项当前值
dsync config set user.name 'Your Name' user.email you@example.com
dsync config set tags work,laptop                              # 逗号分隔
dsync config set proxy.http 'http://${WSL_HOST_IP}:7890' proxy.https 'http://${WSL_HOST_IP}:7890'
dsync config unset proxy.http proxy.https                      # proxy.* 全部清除后即不再设置代理
```

可用的键：`user.name` `user.email` `tags` `proxy.http` `proxy.https` `proxy.all` `proxy.no_proxy` `repo.path`（`repo.path` 会校验目标是 dotfiles 仓库）。`geo` 用 `dsync geo set`。一次可以改多项，任一项有误时不写入任何改动。

`init` / `geo set` / `config set` 都会重写该文件，手写注释不会保留；出现未知配置项会报错。文件格式：

```toml
geo = 'cn'                # cn | official；请用 dsync geo set 修改
tags = ['work']           # 机器标签，模板中 .dsync.tags

[repo]
path = '/home/you/.local/share/dsync/repo'   # dotfiles 仓库位置，init 写入

[user]                    # git 身份，模板中 .dsync.user.name / .dsync.user.email
name = 'Your Name'
email = 'you@example.com'

[proxy]                   # 由 shell-init 导出为 http_proxy / HTTP_PROXY 等（大小写各一份）
http = 'http://${WSL_HOST_IP}:7890'
https = 'http://${WSL_HOST_IP}:7890'
all = 'socks5://${WSL_HOST_IP}:7890'
no_proxy = 'localhost,127.0.0.1'
```

- `${WSL_HOST_IP}` 是字面量占位符：WSL 下展开为 Windows 宿主 IP（代理软件需允许局域网连接），非 WSL 下为 `127.0.0.1`，所以同一段配置可以跨平台复用。
- 没有 `[proxy]` 且存在 v1 的 `~/.config/dsync/proxy.env` 时，会回退为 source 该文件。

**改完之后怎么生效**

| 改了什么 | 用 `dsync config set` | 手工编辑文件后 |
|---|---|---|
| `[user]`、`tags`（注入模板） | 立即重新生成 chezmoi 配置并 apply | `dsync sync`；在此之前 `dsync status` 会提示有改动尚未部署 |
| `[proxy]` | 新开终端（`shell-init` 每次开 shell 读取） | 同左 |
| `geo` | 不支持，用 `dsync geo set` | 不要手改：只有环境变量会变 |

**geo 的优先级**：显式设置的环境变量 `DSYNC_GEO` > `local.toml` > v1 的 `~/.config/dsync/geo` > 默认 `cn`。（`shell-init` 自己导出的 `DSYNC_GEO` 不算显式设置；`dsync init` 例外：`--geo` > 已有 `local.toml` > 环境变量。）

## 7. 共享清单 `dsync.toml`

在 dotfiles 仓库根目录，入库，所有机器共用。出现未知配置项会报错（`dsync verify` 可提前发现）。

```toml
version = 1                      # 必填，当前只支持 1

[remote]
url = "git@github.com:you/dotfiles.git"            # 仓库没有 origin 时推送前按它添加；doctor 检查 origin 是否一致
mirrors = ["https://gitee.com/you/dotfiles.git"]   # dsync save 时一并推送

[packages]
apt = ["build-essential", "curl", "git", "zsh", "unzip", "ripgrep", "socat"]
brew = ["ripgrep", "fd"]

[shell]
default = "zsh"                  # zsh | bash；留空则不改登录 shell
ohmyzsh_plugins = ["zsh-users/zsh-autosuggestions"]   # <owner>/<repo>

[provision]
enable = ["apt", "wsl-ssh-agent", "homebrew", "ohmyzsh", "chsh", "docker-check"]

[secrets]
age_recipients = ["age1..."]     # 设置后 dsync 为 chezmoi 启用 age 加密

[data]                           # 自定义模板数据，模板中以 {{ .editor }} 引用；键名 dsync 保留
editor = "nvim"
```

- `[provision].enable` 是白名单：**省略它就不会执行任何系统步骤**。不适用于当前平台的步骤自动跳过，所以可以把全部步骤都列上。
- `[secrets]`：dsync 只负责把 `encryption = "age"`、公钥和私钥位置（`~/.config/chezmoi/keys.txt`）写进 chezmoi 配置。私钥不入库，每台机器自行放置（生成：`chezmoi age-keygen -o ~/.config/chezmoi/keys.txt`）；加密文件用 `chezmoi add --encrypt <文件>`。
- `[remote].url`：`doctor` 会对比 `origin`（同一仓库的 SSH / HTTPS 地址视为一致），不一致时告警。
- `[deps]` 已废弃：mise / chezmoi 的版本随 dsync 固定（带 sha256 校验），这个表从未生效。为兼容旧仓库仍可解析，`dsync verify` 会告警，请删除。

## 8. 写模板

仓库 `home/` 是 chezmoi 源目录：`dot_` 前缀映射为 `~/.`，`.tmpl` 后缀是模板，其余 chezmoi 规则（`private_`、`executable_`、`create_` 等）不变。chezmoi 的配置文件由 dsync 生成，仓库里不需要 `.chezmoi.toml.tmpl`。

dsync 注入的模板数据：

| 键 | 值 |
|---|---|
| `.dsync.geo` | `cn` \| `official` |
| `.dsync.mirrors.<组件>` | 镜像地址；official 下为空串。组件：`apt` `brew_api` `brew_bottle` `brew_brew` `brew_core` `cargo` `docker` `node` `npm` `pip` `pyenv` `rustup_dist` `rustup_root` |
| `.dsync.wsl` | 是否 WSL |
| `.dsync.tags` | `local.toml` 的 `tags` |
| `.dsync.user.name` / `.email` | `local.toml` 的 `[user]` |
| `.<键>` | `dsync.toml` 的 `[data]` |

镜像地址只在 dsync 里维护一份，模板按"有镜像用镜像，否则回退官方"来写：

```
registry={{ .dsync.mirrors.npm | default "https://registry.npmjs.org" }}

{{- if .dsync.wsl }}
# 仅 WSL
{{- end }}
{{- if has "work" .dsync.tags }}
# 仅带 work 标签的机器
{{- end }}
```

改完模板先校验再推送：

```bash
dsync verify            # 校验 dsync.toml，并按 linux/darwin × cn/official 渲染全部模板
dsync verify --source . # 指定仓库目录（CI 中使用）
```

引用了不存在的数据键会直接报错，可发现拼写错误；`[WARN]` 项（如已废弃的 `[deps]`）不影响退出码。starter 模板自带的 GitHub Actions 工作流会在每次 push 时执行它（需在 dotfiles 仓库的 Secrets 中添加 `DSYNC_CLI_TOKEN`）。

## 9. Windows 宿主

在 Windows 上用 `dsync.exe host setup` 一次性完成：winget 补装 Git / Windows Terminal / Docker Desktop → 安装（或迁移）WSL 发行版 → Docker Desktop 的 WSL 集成 → 启用 ssh-agent → 在 WSL 内安装 dsync 并执行 `dsync init`。

```powershell
# 1) 安装 dsync.exe（PowerShell；不要用 irm | iex）
$env:GITHUB_TOKEN = '<token>'
$h = @{ Authorization = "Bearer $env:GITHUB_TOKEN"; Accept = 'application/vnd.github.raw' }
Invoke-RestMethod -Headers $h -OutFile "$env:TEMP\install-dsync.ps1" `
  'https://api.github.com/repos/Lin-Ya/dsync-cli/contents/install/install.ps1'
powershell -ExecutionPolicy Bypass -File "$env:TEMP\install-dsync.ps1"

# 2) 新开一个终端，先看计划（不需要管理员）
dsync host setup --check

# 3) 管理员 PowerShell 中执行
dsync host setup https://github.com/you/dotfiles.git --proxy 'http://${WSL_HOST_IP}:7890'
```

- 遇到需要重启 Windows、首次打开 Docker Desktop、首次进入发行版创建用户等情况，命令会暂停并说明原因；处理完**重新运行同一条命令**即可。
- `--source <Windows 目录>`：把本地仓库整个拷进 WSL 再 init——新机器上 WSL 里还没有 git 凭据时用它。
- `--proxy`：只用于这一次在 WSL 内执行的 `dsync init`（`${WSL_HOST_IP}` 自动替换）。之后要长期使用代理，在 WSL 内的 `local.toml` 写 `[proxy]`。
- `--wsl-install-dir D:\Linux`：发行版安装位置；`--distro`、`--wsl-user`、`--geo` 见 `dsync host setup --help`。
- 修改 Docker Desktop 设置前会备份为 `.bak.dsync`。

之后的日常操作都在 WSL 里进行。

## 10. 更新 dsync 与 shell 补全

```bash
dsync self-update                    # 更新到最新 Release（含预发布），按 checksums.txt 校验
dsync self-update --version v2.0.0   # 指定版本；--force 重新安装同一版本
```

需要 `GITHUB_TOKEN` 或已登录的 `gh`。

补全（zsh 为例，写入 `~/.zshrc.local`）：`source <(dsync completion zsh)`。

## 11. 常见问题

| 现象 | 原因与处理 |
|---|---|
| `找不到 dotfiles 仓库` | 这台机器还没部署：`dsync init <仓库地址>`（或 `--source` / `--create`） |
| `git 身份未配置，无法提交` | `dsync config set user.name '…' user.email …` 后重试（命令在改动任何文件之前就会检查） |
| `推送失败`，git 提示 `'origin' does not appear to be a git repository` | 仓库没有远端：在 `dsync.toml` 的 `[remote]` 写 `url` 后 `dsync save`（或 `git -C <仓库> remote add origin <地址>`） |
| `doctor` 报 `remote url` 不一致 | 按提示 `git remote set-url origin …`，或修改 `dsync.toml` 的 `[remote].url` |
| `tool remove` 报"中没有 …" | 工具名写错，或该工具不在全局清单里：`dsync status` / `mise ls --global` 查看 |
| `capture` 报"含模板变量，无法自动写回" | `chezmoi edit --apply <文件>` 手工改模板，然后 `dsync save` |
| `capture` 报"未被 dsync 管理" | 新文件先 `chezmoi add <文件>`，然后 `dsync save` |
| `geo set` 后环境变量还是旧的 | `exec zsh` 或新开终端 |
| apt 走镜像返回 403 | `dsync geo set official`；apt 源已被换成镜像的，用源文件同目录下的 `*.bak.dsync` 备份还原 |
| oh-my-zsh / npiperelay 下载失败（只有告警） | 配好代理后 `dsync provision --only ohmyzsh,wsl-ssh-agent` |
| `mise install --locked` 失败 | 见 [4.1](#41-同步dsync-sync) |
| `sync` 时 chezmoi 询问是否覆盖 | 本机改过该文件：要保留就中止并 `dsync capture <文件>`，否则选择覆盖 |
| WSL 里 `ssh-add -l` 没有密钥 | 见 [ssh-agent.md](ssh-agent.md) |
| WSL 里找不到 `docker` | 见 [docker.md](docker.md) |
| 改了 `local.toml` 后命令报"未知配置项" | 键名拼错或放错了表；按[第 6 节](#6-本机配置-localtoml)的示例修正，或改用 `dsync config set` |

## 12. 环境变量

| 变量 | 作用 |
|---|---|
| `GITHUB_TOKEN` | 下载私有 Release（安装脚本、`self-update`、`host setup`）；没有时回退 `gh auth token` |
| `http_proxy` / `https_proxy` | dsync 自身的下载（mise / chezmoi / Release）沿用 |
| `DSYNC_GEO` | 临时覆盖 geo（优先级见第 6 节） |
| `DSYNC_CONFIG_DIR` | 覆盖 `~/.config/dsync` |
| `CHEZMOI_SOURCE_DIR` | 定位 dotfiles 仓库的候选之一（`local.toml` 的 `repo.path` 优先） |
| `DSYNC_VERSION` / `DSYNC_INSTALL_DIR` | 仅安装脚本：目标版本 / 安装目录 |
| `WSL_INSTALL_DIR` | 仅 `host setup`：WSL 发行版安装根目录的默认值 |
