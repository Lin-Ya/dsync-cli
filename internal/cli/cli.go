// Package cli 定义 dsync 的命令行界面：只负责参数解析与输出格式，
// 业务逻辑全部在 workflow 包中。
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/Lin-Ya/dsync-cli/internal/exec"
	"github.com/Lin-Ya/dsync-cli/internal/shellinit"
	"github.com/Lin-Ya/dsync-cli/internal/workflow"
)

// Version 由构建时 -ldflags "-X github.com/Lin-Ya/dsync-cli/internal/cli.Version=…" 注入。
var Version = "dev"

// ExitError 携带期望的退出码（例如 doctor 发现 fail 项）。
type ExitError struct{ Code int }

func (e ExitError) Error() string { return fmt.Sprintf("退出码 %d", e.Code) }

type globalFlags struct {
	json   bool
	dryRun bool
}

// New 构造根命令。env 的 Runner 会按 --dry-run 包装。
func New(env workflow.Env) *cobra.Command {
	var g globalFlags
	root := &cobra.Command{
		Use:           "dsync",
		Short:         "跨设备开发环境同步（编排 mise + chezmoi + git）",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().BoolVar(&g.json, "json", false, "以 JSON 输出（供脚本 / CI 使用）")
	root.PersistentFlags().BoolVar(&g.dryRun, "dry-run", false, "只打印将执行的写操作，不实际执行")
	root.SetOut(env.Out)
	root.SetErr(env.Err)

	envFor := func() workflow.Env {
		e := env
		if g.dryRun {
			e.Runner = exec.DryRun{Inner: env.Runner, Out: env.Err}
			e.DryRun = true
		}
		return e
	}

	root.AddCommand(
		&cobra.Command{
			Use:   "version",
			Short: "显示版本",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				_, err := fmt.Fprintf(cmd.OutOrStdout(), "dsync %s\n", Version)
				return err
			},
		},
		&cobra.Command{
			Use:       "shell-init <zsh|bash>",
			Short:     "输出 shell 初始化脚本：eval \"$(dsync shell-init zsh)\"",
			Long:      "输出镜像环境变量、代理与 WSL ssh-agent 桥接脚本，供 ~/.zshenv 中 eval 使用。\n取代 v1 ~/.zshenv 中 source mirrors.sh / proxy.env 的逻辑。",
			Args:      cobra.ExactArgs(1),
			ValidArgs: shellinit.Shells,
			RunE: func(_ *cobra.Command, args []string) error {
				return envFor().ShellInit(args[0])
			},
		},
		&cobra.Command{
			Use:   "status",
			Short: "查看本机与仓库的同步状态",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				r, err := envFor().Status(cmd.Context())
				if err != nil {
					return err
				}
				if g.json {
					return workflow.WriteJSON(cmd.OutOrStdout(), r)
				}
				r.WriteText(cmd.OutOrStdout())
				return nil
			},
		},
		&cobra.Command{
			Use:   "doctor",
			Short: "诊断环境问题（只读）",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				r, err := envFor().Doctor(cmd.Context())
				if err != nil {
					return err
				}
				if g.json {
					err = workflow.WriteJSON(cmd.OutOrStdout(), r)
				} else {
					r.WriteText(cmd.OutOrStdout())
				}
				if err == nil && r.Failed() {
					return ExitError{Code: 1}
				}
				return err
			},
		},
	)
	root.AddCommand(writeCommands(envFor)...)
	return root
}

func jsonOut(cmd *cobra.Command) bool {
	v, _ := cmd.Flags().GetBool("json")
	return v
}

// writeCommands 是会修改本机或仓库的命令（均支持 --dry-run）。
func writeCommands(envFor func() workflow.Env) []*cobra.Command {
	geo := &cobra.Command{Use: "geo", Short: "镜像源设置"}
	geo.AddCommand(&cobra.Command{
		Use:       "set <cn|official>",
		Short:     "切换镜像源并重新渲染相关配置（.npmrc / cargo / docker 等）",
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"cn", "official"},
		RunE: func(cmd *cobra.Command, args []string) error {
			return envFor().GeoSet(cmd.Context(), args[0])
		},
	})

	tool := &cobra.Command{Use: "tool", Short: "管理 mise 全局工具"}
	var addOpts workflow.ToolAddOptions
	add := &cobra.Command{
		Use:     "add <tool>[@<version>]...",
		Short:   "新增/升级工具：编辑仓库源 → 部署 → 安装 → lock → 提交",
		Example: "  dsync tool add node@22.11.0\n  dsync tool add npm:typescript@5.6.2 --push",
		Args:    cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			addOpts.Specs = args
			return envFor().ToolAdd(cmd.Context(), addOpts)
		},
	}
	add.Flags().BoolVar(&addOpts.Push, "push", false, "提交后推送到远端")
	tool.AddCommand(add)

	var capOpts workflow.CaptureOptions
	capture := &cobra.Command{
		Use:     "capture <file>...",
		Short:   "把本机对已管理文件的改动写回仓库并提交",
		Example: "  dsync capture ~/.gitconfig",
		Args:    cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			capOpts.Targets = args
			return envFor().Capture(cmd.Context(), capOpts)
		},
	}
	capture.Flags().BoolVar(&capOpts.NoCommit, "no-commit", false, "只写回源文件，不提交")
	capture.Flags().BoolVar(&capOpts.Push, "push", false, "提交后推送到远端")

	var saveOpts workflow.SaveOptions
	save := &cobra.Command{
		Use:   "save",
		Short: "提交仓库中的全部改动并推送",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return envFor().Save(cmd.Context(), saveOpts)
		},
	}
	save.Flags().StringVarP(&saveOpts.Message, "message", "m", "", "提交信息（默认 chore: dsync save (<主机名>)）")
	save.Flags().BoolVar(&saveOpts.NoPush, "no-push", false, "只提交不推送")

	sync := &cobra.Command{
		Use:   "sync",
		Short: "拉取仓库 → chezmoi apply → mise install --locked",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return envFor().Sync(cmd.Context())
		},
	}
	var only []string
	var checkOnly bool
	prov := &cobra.Command{
		Use:   "provision",
		Short: "检测并执行系统级安装步骤（apt / homebrew / oh-my-zsh / chsh / npiperelay / docker 检查）",
		Long:  "每个步骤先检测再执行，只处理待办项，可随时重跑（取代 v1 的 run_once 脚本与 reinstall-run-once.sh）。\n仓库无 dsync.toml 时使用与 v1 一致的默认清单。",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			e := envFor()
			if !checkOnly {
				if jsonOut(cmd) {
					return errors.New("--json 仅可与 --check 一起使用")
				}
				return e.Provision(cmd.Context(), only)
			}
			r, err := e.ProvisionCheck(cmd.Context(), only)
			if err != nil {
				return err
			}
			if jsonOut(cmd) {
				return workflow.WriteJSON(cmd.OutOrStdout(), r)
			}
			workflow.WritePlan(cmd.OutOrStdout(), r.Items)
			return nil
		},
	}
	prov.Flags().StringSliceVar(&only, "only", nil, "只处理指定步骤（逗号分隔）")
	prov.Flags().BoolVar(&checkOnly, "check", false, "只检测并输出计划，不执行")

	var initOpts workflow.InitOptions
	initCmd := &cobra.Command{
		Use:   "init [<dotfiles 仓库地址>]",
		Short: "在新机器上从零部署：装 mise/chezmoi → 获取仓库 → apply → provision → mise install",
		Long: "每一步先检测再执行，可重复运行。不带参数时沿用 local.toml 中记录的仓库。\n" +
			"仓库地址会克隆到 ~/" + workflow.DefaultCloneDir + "；已有本地仓库用 --source。",
		Example: "  dsync init https://github.com/you/dotfiles.git\n  dsync init --source ~/dsync --geo official\n  dsync init --create --github you/dotfiles",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				initOpts.Repo = args[0]
			}
			return envFor().Init(cmd.Context(), initOpts)
		},
	}
	initCmd.Flags().StringVar(&initOpts.Source, "source", "", "使用已有的本地仓库目录（不克隆）")
	initCmd.Flags().StringVar(&initOpts.Geo, "geo", "", "镜像源 cn | official（默认：local.toml > 环境变量 DSYNC_GEO > cn）")
	initCmd.Flags().BoolVar(&initOpts.Create, "create", false, "用内置 starter 模板新建 dotfiles 仓库（v2 布局）")
	initCmd.Flags().StringVar(&initOpts.GitHub, "github", "", "与 --create 合用：用 gh 创建私有仓库 <owner/name> 并推送")

	var upOpts workflow.SelfUpdateOptions
	selfUpdate := &cobra.Command{
		Use:   "self-update",
		Short: "从 GitHub Release 更新 dsync 自身（私有仓库，需要 GITHUB_TOKEN 或 gh auth）",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return envFor().SelfUpdate(cmd.Context(), Version, upOpts)
		},
	}
	selfUpdate.Flags().StringVar(&upOpts.Version, "version", "", "目标版本 tag（默认最新，含预发布）")
	selfUpdate.Flags().BoolVar(&upOpts.Force, "force", false, "已是该版本时仍重新安装")
	var verifyOpts workflow.VerifyOptions
	verify := &cobra.Command{
		Use:   "verify",
		Short: "校验 v2 dotfiles 仓库：dsync.toml 合法，全部模板在 linux/darwin × cn/official 下可渲染（需要 chezmoi）",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			r, err := envFor().Verify(cmd.Context(), verifyOpts)
			if err != nil {
				return err
			}
			if jsonOut(cmd) {
				err = workflow.WriteJSON(cmd.OutOrStdout(), r)
			} else {
				r.WriteText(cmd.OutOrStdout())
			}
			if err == nil && len(r.Errors) > 0 {
				return ExitError{Code: 1}
			}
			return err
		},
	}
	verify.Flags().StringVar(&verifyOpts.Source, "source", "", "dotfiles 仓库根目录（默认本机仓库）")
	return []*cobra.Command{geo, tool, capture, save, sync, prov, initCmd, selfUpdate, verify, hostCommand(envFor)}
}

// hostCommand 是 Windows 宿主相关命令（取代 v1 bootstrap.ps1）。
func hostCommand(envFor func() workflow.Env) *cobra.Command {
	hostCmd := &cobra.Command{Use: "host", Short: "Windows 宿主初始化（winget / WSL / Docker Desktop / ssh-agent）"}
	var o workflow.HostOptions
	var checkOnly bool
	setup := &cobra.Command{
		Use:   "setup [<dotfiles 仓库地址>]",
		Short: "初始化 Windows 宿主，并在 WSL 内安装 dsync、执行 dsync init（需管理员 PowerShell）",
		Long: "依次：winget 补装 Git / Windows Terminal / Docker Desktop → 安装 WSL 发行版（可迁移到自定义目录）→\n" +
			"Docker Desktop WSL 集成 → 启用 ssh-agent → 在 WSL 内安装 dsync 并 dsync init。\n" +
			"每步先检测再执行，可重复运行；需要重启 / 首次登录时会暂停并说明，完成后重新运行即可。\n" +
			"在本地仓库目录内运行且未给仓库参数时，默认 --source 当前目录（与 v1 一致）。",
		Example: "  dsync host setup --source D:\\Develop\\dsync\n  dsync host setup https://github.com/you/dotfiles.git --wsl-install-dir D:\\Linux\n  dsync host setup --check",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				o.Repo = args[0]
			}
			o.InstallDirSet = cmd.Flags().Changed("wsl-install-dir")
			o.Version = Version
			e := envFor()
			if !checkOnly {
				if jsonOut(cmd) {
					return errors.New("--json 仅可与 --check 一起使用")
				}
				return e.HostSetup(cmd.Context(), o)
			}
			r, err := e.HostCheck(cmd.Context(), o)
			if err != nil {
				return err
			}
			if jsonOut(cmd) {
				return workflow.WriteJSON(cmd.OutOrStdout(), r)
			}
			workflow.WriteHostPlan(cmd.OutOrStdout(), r.Items)
			return nil
		},
	}
	f := setup.Flags()
	f.StringVar(&o.Source, "source", "", "Windows 侧本地仓库目录：整个拷贝进 WSL 后 dsync init --source（WSL 内无需凭据）")
	f.StringVar(&o.Distro, "distro", workflow.DefaultDistro, "WSL 发行版")
	f.StringVar(&o.InstallDir, "wsl-install-dir", "", `发行版安装根目录（vhdx 位于 <目录>\<发行版>\）；默认 WSL_INSTALL_DIR 或 D:\Linux（D 盘存在时）；给空值使用系统默认位置`)
	f.StringVar(&o.WSLUser, "wsl-user", "", "迁移后创建的默认用户（默认取 Windows 用户名的合法小写形式）")
	f.StringVar(&o.Geo, "geo", "", "镜像源 cn | official（默认：环境变量 DSYNC_GEO > cn）")
	f.StringVar(&o.LinuxBinary, "linux-binary", "", "linux 版 dsync 的本地路径（默认从 Release 下载，需要 GITHUB_TOKEN）")
	f.StringVar(&o.Proxy, "proxy", "", `WSL 内执行 dsync init 时使用的代理，如 'http://${WSL_HOST_IP}:7890'（${WSL_HOST_IP} 自动替换为 Windows 宿主 IP；代理软件需允许局域网连接）`)
	f.BoolVar(&checkOnly, "check", false, "只检测并输出计划，不执行")
	hostCmd.AddCommand(setup)
	return hostCmd
}

// Main 运行命令并返回进程退出码。
func Main(ctx context.Context, env workflow.Env, args []string) int {
	root := New(env)
	root.SetArgs(args)
	err := root.ExecuteContext(ctx)
	if err == nil {
		return 0
	}
	var ee ExitError
	if errors.As(err, &ee) {
		return ee.Code
	}
	fmt.Fprintf(env.Err, "dsync: %v\n", err)
	return 2
}

// DefaultEnv 基于真实进程环境构造 Env。
func DefaultEnv(out, errOut io.Writer) workflow.Env {
	return workflow.Env{
		Getenv: os.Getenv,
		Runner: exec.Real{},
		RootFS: os.DirFS("/"),
		Out:    out,
		Err:    errOut,
	}
}
