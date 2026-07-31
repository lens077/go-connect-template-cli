package cli

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/lens077/co-cli/internal/scaffold"
	"github.com/lens077/co-cli/internal/ui"
)

func newResourceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "resource <command>",
		Short:   "在已有服务里增删资源",
		Aliases: []string{"res"},
	}
	cmd.AddCommand(newResourceAddCmd())
	return cmd
}

func newResourceAddCmd() *cobra.Command {
	var (
		tmpl     templateFlags
		dir      string
		features []string
		force    bool
		dryRun   bool
	)

	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "生成一套资源(proto + SQL + biz/data/service)",
		Long: `在已有服务里生成一套资源代码,并把 provider 插进各 fx.Module。

服务启用了哪些 feature 由 co 自动判断(看 manifest 声明的文件还在不在),
判断不准时用 --feature 覆盖。

生成完还需要跑 buf 才能编译:
  make api && make conf   (monorepo)
  buf generate            (独立仓库)`,
		Args: cobra.ExactArgs(1),
		Example: `  co resource add order
  co resource add order --dir backend/services/shop --dry-run`,
		RunE: func(cmd *cobra.Command, args []string) error {
			p := ui.New()

			src, m, err := tmpl.fetch(cmd.Context())
			if err != nil {
				return err
			}

			plan, err := scaffold.NewAddPlan(src, m, scaffold.AddOptions{
				Dir: dir, Name: args[0], Features: features, Force: force,
			})
			if err != nil {
				return err
			}

			if dryRun {
				plan.Describe(os.Stdout)
				return nil
			}
			if err := plan.Apply(cmd.Context(), m, p); err != nil {
				return err
			}

			p.Title("\ndone")
			p.Dim("\n下一步:")
			p.Command("make api && make conf   # 或 buf generate")
			p.Command("go build ./...")
			return nil
		},
	}

	tmpl.register(cmd)
	f := cmd.Flags()
	f.StringVarP(&dir, "dir", "d", ".", "服务目录")
	f.StringSliceVar(&features, "feature", nil, "覆盖自动探测到的 feature,可重复")
	f.BoolVar(&force, "force", false, "覆盖已存在的同名文件")
	f.BoolVar(&dryRun, "dry-run", false, "只打印将要执行的操作,不落盘")

	return cmd
}
