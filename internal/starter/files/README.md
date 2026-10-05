# dotfiles

由 [dsync](https://github.com/Lin-Ya/dsync-cli) 管理的开发环境配置（chezmoi 源目录 + mise 工具清单）。

| 路径 | 内容 |
|---|---|
| `dsync.toml` | 多机共享清单：系统包、shell、provision 步骤、模板数据 |
| `home/` | chezmoi 源目录（`.chezmoiroot`）；`dot_` 前缀映射为 `~/.`，`.tmpl` 为模板 |
| `home/dot_config/mise/config.toml` | 全局工具清单：`dsync tool add <tool>@<version>` 维护 |

常用命令（完整说明见 dsync-cli 仓库的 [使用指南](https://github.com/Lin-Ya/dsync-cli/blob/main/docs/usage-guide.md)）：

```bash
dsync sync                      # 拉取 → 部署配置 → 系统步骤 → 安装工具
dsync tool add node@22          # 新增 / 升级工具并提交
dsync capture ~/.gitconfig      # 本机改动写回仓库并提交
dsync save                      # 提交并推送
dsync verify                    # 校验 dsync.toml 并渲染全部模板（CI 同样执行）
```

模板可用数据：`.dsync.geo`（cn | official）、`.dsync.mirrors.<组件>`（official 下为空串）、
`.dsync.wsl`、`.dsync.tags`、`.dsync.user.name` / `.dsync.user.email`，以及 `dsync.toml` 中 `[data]` 的自定义项。
