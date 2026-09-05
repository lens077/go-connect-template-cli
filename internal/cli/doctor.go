package cli

import (
	"fmt"
	"os/exec"

	"github.com/spf13/cobra"

	"github.com/lens077/go-connect-template-cli/internal/manifest"
	"github.com/lens077/go-connect-template-cli/internal/ui"
)

func newDoctorCmd() *cobra.Command {
	var tmpl templateFlags

	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "检查生成后要用到的外部工具",
		Long: `检查 PATH 上有没有模板 manifest 声明的工具。

co new 本身只要有 git 就能跑完;这些工具是生成之后跑 make api / make sqlc
要用的。缺了不会让 co new 失败,只是那几步得等你装完再手动补。`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			p := ui.New()

			_, m, err := tmpl.fetch(cmd.Context())
			if err != nil {
				return err
			}

			var missingRequired []string
			for _, t := range m.Tools {
				path, lerr := exec.LookPath(t.Name)
				switch {
				case lerr == nil:
					p.Step("%-24s %s", t.Name, path)
				case t.Required:
					missingRequired = append(missingRequired, t.Name)
					p.Error("%-24s 未找到(必需)", t.Name)
					printHint(p, t)
				default:
					p.Warn("%-24s 未找到(可选)", t.Name)
					printHint(p, t)
				}
			}

			if len(missingRequired) > 0 {
				return fmt.Errorf("missing required tool(s): %v", missingRequired)
			}
			return nil
		},
	}

	tmpl.register(cmd)
	return cmd
}

func printHint(p *ui.Printer, t manifest.Tool) {
	if t.Hint != "" {
		p.Command(t.Hint)
	}
}
