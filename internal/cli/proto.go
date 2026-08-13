package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/lens077/co-cli/internal/protogen"
	"github.com/lens077/co-cli/internal/scaffold"
	"github.com/lens077/co-cli/internal/ui"
)

func newProtoCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "proto <command>",
		Short: "手写 proto 的辅助命令",
		Long: `proto 相关的零散操作。

需要 proto + SQL + biz/data/service 一整套 CRUD 时用 co resource add。
已经手写好 proto 时:
  co proto gen     按 rpc 生成 service/biz/data 三层示例并接到 fx 锚点
  co proto server  只生成 handler 骨架(每个方法 Unimplemented)
  co proto add     按约定布局新建一个 .proto`,
	}
	cmd.AddCommand(newProtoAddCmd(), newProtoGenCmd(), newProtoServerCmd())
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
			p.Command("co proto gen " + spec.Path + " -t .")
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "覆盖已存在的文件")
	return cmd
}

func newProtoGenCmd() *cobra.Command {
	var (
		target  string
		svcName string
		force   bool
		dryRun  bool
		noWire  bool
	)

	cmd := &cobra.Command{
		Use:   "gen <path>",
		Short: "按 proto 的 rpc 生成 service/biz/data 三层示例",
		Long: `解析 proto 里的 service 与 message,在目标服务目录写出三层示例代码:

  internal/service/<name>.go  Connect handler,把 proto 译成 biz 再调 UseCase
  internal/biz/<name>.go      领域结构体、Repo 接口、UseCase
  internal/data/<name>.go     Repo 占位实现(返回 not implemented,留给 sqlc)

形状对齐 ecommerce/backend/services/cart:service 只做翻译,biz 不认 protobuf,
data 接 *Data 与 logger。流式 rpc 只在 service 层生成 Unimplemented,不进 biz/data。

默认把 NewXxx 插进各 fx.Module 的 +co:anchor(与 co new 产物一致)。
目标目录没有锚点时跳过接线并警告,三个文件照样写。

要求 proto 的生成物已经存在(先跑 buf generate / make api),否则
service 层两个 import 指向不存在的包。`,
		Args: cobra.ExactArgs(1),
		Example: `  co proto gen api/merchant/v1/merchant.proto -t services/merchant/
  co proto gen api/cart/v1/cart.proto -t . --dry-run
  co proto gen api/order/v1/order.proto -t services/order --service OrderAdminService`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProtoGen(cmd, args[0], target, svcName, force, dryRun, noWire)
		},
	}

	f := cmd.Flags()
	f.StringVarP(&target, "target", "t", ".", "服务目录(含 internal/)")
	f.StringVar(&svcName, "service", "", "proto 里有多个 service 时指定用哪个")
	f.BoolVar(&force, "force", false, "覆盖已存在的文件")
	f.BoolVar(&dryRun, "dry-run", false, "只打印将要写入的文件,不落盘")
	f.BoolVar(&noWire, "no-wire", false, "不往 fx.Module 锚点插 provider")
	return cmd
}

func runProtoGen(cmd *cobra.Command, protoPath, target, svcName string, force, dryRun, noWire bool) error {
	p := ui.New()

	f, err := protogen.ParseFile(protoPath)
	if err != nil {
		return err
	}
	svc, err := f.Service(svcName)
	if err != nil {
		return err
	}

	info, err := scaffold.InspectTarget(target)
	if err != nil {
		return err
	}

	spec, err := protogen.NewLayerSpec(f, svc, info.ServiceModule)
	if err != nil {
		return err
	}
	files, err := protogen.RenderLayers(spec)
	if err != nil {
		return err
	}

	if dryRun {
		fmt.Fprintf(cmd.OutOrStdout(), "service    %s\n", info.Root)
		fmt.Fprintf(cmd.OutOrStdout(), "module     %s\n", info.ServiceModule)
		fmt.Fprintf(cmd.OutOrStdout(), "proto      %s (%s)\n", protoPath, svc.Name)
		fmt.Fprintf(cmd.OutOrStdout(), "\ncreate (%d)\n", len(files))
		for _, file := range files {
			fmt.Fprintf(cmd.OutOrStdout(), "  + %s\n", filepath.ToSlash(file.Rel))
		}
		if !noWire {
			fmt.Fprintf(cmd.OutOrStdout(), "\ninsert at anchor (%d)\n", len(spec.WireItems()))
			for _, w := range spec.WireItems() {
				fmt.Fprintf(cmd.OutOrStdout(), "  @ %-24s %s\n", w.Anchor, w.Text)
			}
		}
		return nil
	}

	for _, file := range files {
		dest := filepath.Join(info.Root, file.Rel)
		if !force {
			if _, err := os.Stat(dest); err == nil {
				return fmt.Errorf("%s already exists (use --force to overwrite)", dest)
			} else if !os.IsNotExist(err) {
				return err
			}
		}
	}

	for _, file := range files {
		dest := filepath.Join(info.Root, file.Rel)
		if err := writeFile(dest, file.Content, true); err != nil {
			return err
		}
		p.Step("wrote %s", dest)
	}

	if !noWire {
		wired, skipped := 0, 0
		for _, w := range spec.WireItems() {
			dest := filepath.Join(info.Root, filepath.FromSlash(w.File))
			data, rerr := os.ReadFile(dest)
			if rerr != nil {
				p.Warn("skip %s: %v", w.File, rerr)
				skipped++
				continue
			}
			next, inserted, ierr := scaffold.InsertAtAnchorOnce(w.File, string(data), w.Anchor, w.Text)
			if ierr != nil {
				p.Warn("skip @%s in %s: %v", w.Anchor, w.File, ierr)
				skipped++
				continue
			}
			if !inserted {
				continue
			}
			if err := os.WriteFile(dest, []byte(next), 0o644); err != nil {
				return err
			}
			wired++
		}
		if wired > 0 {
			p.Step("wired %d provider(s) at anchors", wired)
		}
		if skipped > 0 {
			p.Warn("%d anchor(s) skipped; wire New%s / New%sUseCase / New%sRepo by hand",
				skipped, spec.Service.Name, spec.Domain, spec.Domain)
		}
	}

	p.Dim("\n下一步:")
	p.Command("buf generate --path " + filepath.ToSlash(filepath.Dir(protoPath)) + "   # 或 make api")
	p.Command("go build ./...")
	return nil
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
