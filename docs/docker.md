# Docker

## 架构决策

| 平台 | 运行时 | 说明 |
|---|---|---|
| Windows | **Docker Desktop**（WSL2 后端） | 本机已装则复用，只补配置 |
| macOS | **OrbStack**（轻量替代） | 由 `dsync provision` 的 docker-check 步骤经 brew 安装 |

Docker **不属于** mise 管理（它是系统级服务）。本方案只负责：安装引导、WSL 集成配置、镜像加速（registry-mirrors）。

## 常见报错：`The command 'docker' could not be found in this WSL 2 distro`

**原因**：Docker Desktop 未对该 Ubuntu 发行版启用 **WSL Integration**（或 Docker Desktop 未启动 / 未切换到 Linux 引擎）。

**解决（优先）——GUI 方式**：

1. 启动 Docker Desktop（首次需同意条款）。
2. `Settings → Resources → WSL Integration`。
3. 勾选 `Ubuntu-24.04`（Enable integration with my default WSL distro 也建议打开）。
4. `Apply & Restart`。
5. 重开 WSL 终端，验证：`docker version`。

**解决（脚本方式）**：`dsync host setup` 会自动：

- 调用 `DockerCli.exe -SwitchLinuxEngine` 切换到 Linux 容器引擎；
- 尽力写入 `%APPDATA%\Docker\settings-store.json`（新版）或 `settings.json`（旧版）中的 WSL 集成开关。

> ⚠️ Docker Desktop 的设置文件 **schema 随版本变化**，脚本可能无法写入。此时以 GUI 方式为准（两步搞定）。因此这不是阻碍，只是首次初始化的一次性配置。

**兜底——只装 CLI 客户端**（集成始终无法启用时）：

```bash
~/.config/dsync/scripts/install-docker-cli.sh
```

该脚本在 WSL 内安装 docker CLI，并把 `DOCKER_HOST=unix:///var/run/docker.sock` 写入 `~/.zshrc.local`，指向 Windows 侧 Docker Desktop 的守护进程。

## 验证

```bash
docker version          # 应能看到 Client 与 Server
docker run --rm hello-world
```

`dsync doctor` 会检查 `docker info`。

## 镜像加速（registry-mirrors）

- 配置文件：`~/.docker/daemon.json`（由 chezmoi 按 GEO 渲染）。
- cn 时写入 `https://docker.m.daocloud.io` 等公共加速器；official 时为空。
- ⚠️ Docker Hub 公共加速器普遍**不稳定**，且多数只对官方镜像有效。如果你有本地代理，更推荐给 Docker daemon 配置代理（Docker Desktop `Settings → Resources → Proxies`），拉镜像最稳。

## OrbStack（macOS）

- 由 `dsync provision` 的 docker-check 步骤安装。首次需**手动打开一次**完成初始化，之后 `docker` 命令才可用。
- OrbStack 对 `~/.docker/daemon.json` 的支持有限，registry-mirrors 建议在其 GUI `Settings → Docker` 中配置，或直接走系统代理。
