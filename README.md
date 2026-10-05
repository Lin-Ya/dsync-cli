# dsync

跨设备开发环境同步工具：编排 mise（工具版本）+ chezmoi（配置文件）+ git（多机同步），单个静态二进制。
个人配置放在独立的 dotfiles 仓库（如 `Lin-Ya/dsync`），本仓库只有工具代码。
v1（bash 脚本实现）冻结在 dotfiles 仓库的 tag `v1-final`。

## 进度

| 阶段 | 状态 | 内容 |
|---|---|---|
| M0 冻结 | ✅ | tag `v1-final`；v1 渲染快照（`testdata/v1-render`，供 shell-init 与 v1 的等价测试）；已复现 v1 `switch-geo` 失效 bug |
| M1 骨架 | ✅ | `shell-init` / `status` / `doctor` / `version`（全部只读，可在 v1 机器上直接试用） |
| M2 编排 | ✅ | `sync` / `geo set` / `tool add` / `capture` / `save`（v1 `test_dsync_add.sh` 场景 A–F 全部移植并新增 G/H） |
| M3 Provision | ✅ | `provision`：v1 run_once_10/15/20/30/40 改写为"检测 → 执行"的幂等步骤；接入 `sync` / `status`；容器 E2E |
| M4 引导与发布 | ✅ | `init`（一条命令从零部署）、mise/chezmoi 固定版本 + sha256 校验下载、`self-update`、`install.sh`、Release 工作流 |
| M5 Windows 宿主 | ✅ | `host setup`（取代 bootstrap.ps1）、`install.ps1`；真实 WSL 端到端验证 |
| M6 拆仓 | ✅ | v2 布局（dsync 生成 chezmoi 配置并注入模板数据）、`verify`、starter 模板 + `init --create`；dotfiles 仓库迁移（L5 逐文件等价）；工具迁入本仓库 |

**使用指南**：见 [docs/usage-guide.md](docs/usage-guide.md)（安装、首次部署、日常流程、`local.toml` / `dsync.toml` 参考、常见问题）。

**本地手动测试**：见 [docs/testing-guide.md](docs/testing-guide.md)（新建 WSL 实例逐步验证）。

## 命令

```bash
# 首次部署 / 更新自身
dsync init [<仓库地址> | --source <目录>] [--geo cn|official]   # 装 mise/chezmoi → 克隆 → apply → provision → mise install；可重复运行
dsync init --create [--github <owner/name>]                     # 用内置 starter 模板新建 dotfiles 仓库（v2 布局）
dsync self-update [--version <tag>] [--force]                   # 从私有 Release 更新（需 GITHUB_TOKEN 或 gh auth）

# Windows 宿主（管理员 PowerShell；取代 v1 bootstrap.ps1）
dsync host setup [<仓库地址> | --source <Windows 目录>] [--wsl-install-dir D:\Linux] [--proxy 'http://${WSL_HOST_IP}:7890'] [--check]
#   winget 补装 Git / Windows Terminal / Docker Desktop → 安装 / 迁移 WSL 发行版 → Docker Desktop WSL 集成
#   → ssh-agent → 在 WSL 内安装 dsync 并 dsync init；需重启 / 首次登录时暂停说明，处理后重跑

# 写操作（均支持 --dry-run：只读查询照常执行，写操作只打印）
dsync sync                       # git pull --rebase --autostash → chezmoi apply → provision → mise install --locked
dsync provision [--check] [--only apt,chsh]   # 系统级步骤：apt / wsl-ssh-agent / homebrew / ohmyzsh / chsh / docker-check
dsync geo set cn|official        # 写 local.toml → 重新生成 chezmoi 配置 → apply（修复 v1 switch-geo）；dsync geo 查看当前值
dsync tool add <spec>... [--push]   # mise 直接编辑仓库源（保留注释）→ apply → lock → 写回 lockfile → 提交
dsync tool remove <tool>... [--push] # 同上，mise unuse（不卸载已安装版本）
dsync config set <键> <值>...    # 改 local.toml（git 身份 / 标签 / 代理…）；注入模板的项立即重新部署；config unset / show
dsync capture <file>... [--no-commit] [--push]   # 本机改动写回源：非模板 re-add；无变量模板直接写回；含变量模板拒绝
dsync save [-m msg] [--no-push]  # 提交全部改动并推送（默认推送；无 origin 时按 dsync.toml [remote].url 添加）

# 只读
dsync shell-init zsh     # 输出镜像 env / 代理 / WSL ssh-agent 桥接；用法: eval "$(dsync shell-init zsh)"
dsync status [--json]    # 平台、geo 来源、仓库 ahead/behind/dirty、chezmoi 漂移与待部署配置、mise 缺失工具
dsync doctor [--json]    # 只读诊断；存在 fail 项时退出码 1
dsync verify [--source <仓库>] [--json]   # 校验 dsync.toml，按 linux/darwin × cn/official 渲染全部模板（dotfiles 仓库 CI 使用）
# --json 只用于以上三条与 provision --check / host setup --check，须写在子命令之后
dsync version             # 或 dsync --version
```

退出码：`0` 成功；`1` doctor / verify 发现问题；`2` 用法或执行错误。

## dotfiles 仓库布局（v2）

```
dsync.toml         多机共享清单：[packages] [shell] [provision] [secrets] [remote] [data]
.chezmoiroot       内容为 home
home/              chezmoi 源目录（dot_ 前缀、.tmpl 模板、create_ / executable_ 等 chezmoi 规则不变）
```

- chezmoi 配置由 dsync 生成（`~/.config/chezmoi/chezmoi.toml`，含 `sourceDir`），`init` / `geo set` / `sync` 时重写；仓库中不需要 `.chezmoi.toml.tmpl`。
- 模板数据：`.dsync.geo`、`.dsync.mirrors.<组件>`（official 下为空串）、`.dsync.wsl`、`.dsync.tags`、`.dsync.user.name/email`（来自 local.toml），以及 `dsync.toml` 的 `[data]`。
- 新建：`dsync init --create`；模板内容见 `internal/starter/files`。

**v1 兼容**：没有 `~/.config/dsync/local.toml` 时，geo 读取 v1 的 `~/.config/dsync/geo`，代理会 source `~/.config/dsync/proxy.env`，仓库按 `chezmoi source-path`、`~/dsync`、`~/dotfiles` 依次定位；v1 布局仓库仍可 `init` / `sync`，拉取到 v2 布局后 `sync` 自动切换。

## 结构

```
cmd/dsync            入口
internal/cli         cobra 命令（只做参数解析与输出）
internal/workflow    编排：shell-init / status / doctor / 仓库定位
internal/exec        外部命令唯一出口：Real / DryRun / Recording（测试用）
internal/mirrors     镜像注册表（单一事实源）
internal/config      local.toml（本机）+ dsync.toml（共享清单）
internal/platform    OS / WSL / 发行版探测
internal/{chezmoi,mise,gitrepo}  子进程包装器
internal/shellinit   shell 脚本生成
internal/provision   系统级步骤（Step：Check → Apply；无 dsync.toml 时用与 v1 一致的默认清单）
internal/deps        mise / chezmoi 固定版本与 sha256（versions.go 由 scripts/update-deps.sh 生成）；下载、校验、原子安装
internal/release     GitHub Release API 客户端（私有仓库，带 Token）
internal/host        Windows 宿主步骤（经 wsl.exe / winget / powershell.exe 子进程，无 build tag，Linux 上可完整测试）
internal/starter     内嵌的 v2 dotfiles 模板仓库（init --create；也是 v2 布局的测试 fixture）
install/install.sh   POSIX sh 安装脚本：下载 Release 二进制 → 校验 → dsync init
install/install.ps1  PowerShell 5.1 安装脚本（须保留 UTF-8 BOM）：下载 dsync.exe → 校验 → 加入 PATH → dsync host setup
e2e/provision.sh     容器 E2E：root / 普通用户+sudo 各跑两次，第二次不得有待办
e2e/init.sh          容器 E2E：真实下载、克隆、部署，跑两次后 status 必须完全收敛
e2e/wsl-host.sh      真实 WSL E2E（仅本地，需在 WSL 内）：临时发行版中执行 host setup 的 wsl-init
```

## 开发

```bash
go test ./...                                         # 全部测试
go test ./internal/shellinit -run Golden -update      # 更新 golden（先确认输出变化符合预期）
bash e2e/provision.sh                                 # 容器 E2E（需 docker；镜像站不可用时 DSYNC_GEO=official）
bash e2e/init.sh                                      # init 容器 E2E
bash e2e/wsl-host.sh                                  # 真实 WSL E2E（临时发行版，结束后自动注销）
bash scripts/update-deps.sh 2026.9.0 2.72.1           # 升级 mise / chezmoi 固定版本（重新生成 versions.go）
go build -o dsync ./cmd/dsync
```

关键测试：

| 测试 | 作用 |
|---|---|
| `internal/shellinit/parity_test.go` | M1 退出标准：用 v1 快照作伪 HOME，比较 `. ~/.zshenv` 与 `eval "$(dsync shell-init)"` 产生的环境变量，只允许白名单差异（PATH、DSYNC_GEO_SOURCE） |
| `internal/mirrors/mirrors_test.go` | 移植 v1 `tests/test_mirrors.sh` 的全部断言 |
| `internal/gitrepo/gitrepo_test.go` | 用真实 git 做契约测试 |
| `internal/workflow/contract_test.go` | v1 switch-geo 缺陷的回归测试：用真实 chezmoi 渲染 v1 fixture，断言 `.npmrc`、cargo 配置和 geo 文件能在 cn/official 间来回切换 |
| `internal/workflow/chezmoiconfig_test.go` | v2 布局：用真实 chezmoi 渲染 starter，验证注入的模板数据、`geo set` 往返与 `verify` |
| `internal/workflow/tool_test.go` | 移植 v1 `test_dsync_add.sh` 的全部场景：fake mise/chezmoi + 真实 git |

## 发布

仓库私有，Release 资产需 Token 下载（`install.sh` / `self-update` 读取 `GITHUB_TOKEN`，或已登录的 `gh`）。

```bash
git tag v2.0.0-alpha.1 && git push origin v2.0.0-alpha.1   # 触发 .github/workflows/release.yml
```

工作流先跑测试，再用 `scripts/build-release.sh` 构建 `dsync_<os>_<arch>[.exe]`（linux/darwin 的 amd64、arm64，windows/amd64）与 `checksums.txt`。tag 含 `-` 时标记为预发布。

与 v1 的行为差异：
- `sync`：`mise install --locked` 失败时直接报错，不再退化为无锁安装
- `tool add`：拉取失败即中止，不再在可能冲突的状态上继续提交；无改动时不产生空提交
- `provision`：完成与否以实际检测为准（不依赖 run_once 标记），可随时重跑；执行前逐步重新检测，单次运行即可收敛
- WSL 判定：`/proc/version` 含 microsoft **且**存在 `/run/WSL`（v1 只看前者，会把 WSL 宿主上的 docker 容器误判为 WSL）
- oh-my-zsh 安装时设 `KEEP_ZSHRC=yes`，不再改写 chezmoi 管理的 `~/.zshrc`
- `init`：mise / chezmoi 不再 `curl | sh`，而是下载固定版本并校验 sha256；有 lockfile 时严格按锁定版本安装，没有时才普通安装
- chezmoi 由 dsync 直接下载（原计划经 mise 安装）：避免改动受 chezmoi 管理的 mise 配置，也避免先有鸡还是先有蛋
- 下载：连接 / 握手 / 响应头超时，60 秒无数据即中止；边下载边写临时文件与校验，不整体读入内存
- `host setup` 的 Docker Desktop 集成：v1 修改的键 `Integration<发行版>` 实际并不存在（自动勾选从未生效），改为真实的 `WslEngineEnabled` / `IntegratedWslDistros`，首次修改前备份为 `.bak.dsync`
- `host setup` 调用 `wsl.exe --exec`：实测从 Go 程序用 `--` 传参时，参数会被拼成一行交给默认 shell 再解析，破坏 `'\''` 引号转义
- WSL 迁移：确认导出的备份文件存在才 unregister，失败时发行版保持原状
