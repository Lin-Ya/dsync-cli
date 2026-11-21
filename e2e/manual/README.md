# 手动测试：新设备首次使用（docker）

用一个尽量接近全新 WSL Ubuntu 24.04 的容器，亲手走一遍 `dsync init` 及日常命令。
与 `e2e/init.sh` 互补：那边用 fixture 自动断言，这里用真实 dotfiles 仓库（`Lin-Ya/dsync` 默认分支）手动操作。

## 前提

- docker、mise（编译时 `mise x go@1.25`）
- 能访问你的 dotfiles 仓库的 GitHub token：`$GITHUB_TOKEN`，未设置时用 `gh auth token`（dsync-cli 已公开，安装它不需要 Token；这个 Token 只为克隆你的仓库）
- 需要代理时在宿主导出 `http_proxy` / `https_proxy`，会透传进容器

## 用法

```bash
bash e2e/manual/run.sh          # 编译 dsync → 启动全新容器，以 ubuntu 用户进入 shell（退出即删除）
```

容器内：

```bash
dsync init https://github.com/Lin-Ya/dsync.git    # 镜像站 403 时加 --geo official
exec zsh
dsync status; dsync doctor
```

之后的检查点见 `docs/testing-guide.md` 第 3、4 节。

**快照**：init 跑完后，想反复测日常命令、不想每次重新下载时，在另一个终端执行：

```bash
docker commit dsync-manual dsync-manual:after-init
bash e2e/manual/run.sh dsync-manual:after-init     # 从快照启动（dsync 仍是本次重新编译的版本）
```

## 环境说明

| 项目 | 情况 |
|---|---|
| 用户 | `ubuntu`（uid 1000），免密 sudo |
| 预装 | 只有 git / curl / sudo / ca-certificates，其余交给 dsync |
| dsync | 宿主编译，只读挂载到 `/usr/local/bin/dsync`，不经过 Release 下载（`install.sh` / `self-update` 不在覆盖范围内） |
| 凭据 | token 以文件挂载到 `/run/secrets/github_token`，由 `/etc/gitconfig` 中的 credential helper 读取，不写进镜像或快照 |
| 测不到 | WSL 相关行为（无 `/run/WSL`，`wsl-ssh-agent` 步骤不执行）、systemd；`docker-check` 为 WARN |

⚠️ 克隆的是真实 GitHub 仓库，`gh auth token` 有写权限：`dsync save`、`tool add --push`、`capture --push` 会**真正推送**。
只想读时，换成仅 Contents: Read-only 的 fine-grained token（`GITHUB_TOKEN=... bash e2e/manual/run.sh`）。
