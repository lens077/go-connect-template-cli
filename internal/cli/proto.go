package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/lens077/co-cli/internal/protogen"
	"github.com/lens077/co-cli/internal/ui"
)

func newProtoCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "proto <command>",
		Short: "手写 proto 的辅助命令",
		Long: `proto 相关的零散操作。

需要 proto + SQL + biz/data/service 一整套时用 co resource add;
这里两条命令是给已经手写好 proto、只想补一份 handler 的情况用的。`,
	}
	cmd.AddCommand(newProtoAddCmd(), newProtoServerCmd())
	return cmd
}

func newProtoAddCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "add <path>",
		Short: "新建一个带 CRUD 骨架的 proto",
		Long: `按 <dir>/<name>/<version>/<name>.proto 的布局新建一个 proto 文件。

package 与 go_package 由路径和最近的 go.mod 推出,不需要手填。`,
		Args:    cobra.ExactArgs(1),
		Example: `  co proto add api/order/v1/order.proto`,
		RunE: func(cmd *cobra.Command, args []string) error {
			p := ui.New()

			spec, err := protogen.NewSkeletonSpec(args[0])
			if err != nil {
				return err
			}
			out, err := protogen.RenderSkeleton(spec)
			if err != nil {
				return err
			}
			if err := writeFile(spec.Path, out, force); err != nil {
				return err
			}

			p.Step("wrote %s", spec.Path)
			p.Dim("  package     %s", spec.Package)
			p.Dim("  go_package  %s", spec.GoPackage)
			p.Dim("\n下一步:")
			p.Command("buf generate            # 或 make api")
			p.Command("co proto server " + spec.Path)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "覆盖已存在的文件")
	return cmd
}

func newProtoServerCmd() *cobra.Command {
	var (
		target  string
		svcName string
		pkgName string
		force   bool
		stdout  bool
	)

	cmd := &cobra.Command{
		Use:   "server <path>",
		Short: "按 proto 里的 service 生成 handler 骨架",
		Long: `解析 proto 里的 service,生成一份实现了 ConnectRPC Handler 接口的骨架,
每个方法返回 Unimplemented。一元/客户端流/服务端流/双向流四种签名都会正确生成。

生成的结构体不带任何依赖 —— proto 里没有信息能保证某个 biz.XxxUseCase 存在。
需要连好 biz/data 的完整一套时用 co resource add。

要求 proto 的生成物已经存在(先跑 buf generate),否则生成的文件里
两个 import 都指向不存在的包。`,
		Args: cobra.ExactArgs(1),
		Example: `  co proto server api/order/v1/order.proto
  co proto server api/order/v1/order.proto --service OrderAdminService
  co proto server api/order/v1/order.proto --stdout`,
		RunE: func(cmd *cobra.Command, args []string) error {
			p := ui.New()

			f, err := protogen.ParseFile(args[0])
			if err != nil {
				return err
			}
			svc, err := f.Service(svcName)
			if err != nil {
				return err
			}
			if pkgName == "" {
				pkgName = filepath.Base(target)
			}

			spec, err := protogen.NewServerSpec(f, svc, pkgName)
			if err != nil {
				return err
			}
			out, err := protogen.RenderServer(spec)
			if err != nil {
				return err
			}

			if stdout {
				_, werr := cmd.OutOrStdout().Write(out)
				return werr
			}

			dest := filepath.Join(target, snakeFile(svc.Name)+".go")
			if err := writeFile(dest, out, force); err != nil {
				return err
			}
			p.Step("wrote %s (%d method(s))", dest, len(svc.Methods))
			return nil
		},
	}

	f := cmd.Flags()
	f.StringVarP(&target, "target", "t", "internal/service", "输出目录")
	f.StringVar(&svcName, "service", "", "proto 里有多个 service 时指定用哪个")
	f.StringVar(&pkgName, "package", "", "输出文件的 Go 包名,默认取 --target 的目录名")
	f.BoolVar(&force, "force", false, "覆盖已存在的文件")
	f.BoolVar(&stdout, "stdout", false, "打到标准输出,不写文件")
	return cmd
}

func writeFile(path string, data []byte, force bool) error {
	if !force {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("%s already exists (use --force to overwrite)", path)
		}
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(path, data, 0o644)
}

// snakeFile 把 OrderService 变成 order_service。
func snakeFile(s string) string {
	var out []rune
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				out = append(out, '_')
			}
			r += 'a' - 'A'
		}
		out = append(out, r)
	}
	return string(out)
}
