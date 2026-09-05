// Package cli 是 cobra 命令层。
//
// 这一层只做三件事:绑定 flag、把 flag 变成 scaffold.Options、把错误打出来。
// 任何生成逻辑都不在这里 —— 那样命令与引擎才能各自被测试。
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/lens077/go-connect-template-cli/internal/manifest"
	"github.com/lens077/go-connect-template-cli/internal/scaffold"
	"github.com/lens077/go-connect-template-cli/internal/ui"
)

// templateFlags 是「模板从哪儿来」这一组 flag,new / resource / doctor 共用。
type templateFlags struct {
	dir     string
	repo    string
	ref     string
	noCache bool
}

func (t *templateFlags) register(cmd *cobra.Command) {
	f := cmd.Flags()
	f.StringVar(&t.dir, "template-dir", "", "使用本地模板目录,跳过 clone(开发模板时用)")
	f.StringVar(&t.repo, "template", scaffold.DefaultTemplateRepo, "模板仓库地址")
	f.StringVar(&t.ref, "template-ref", "", "模板分支或 tag,默认取远端默认分支")
	f.BoolVar(&t.noCache, "no-cache", false, "忽略本地缓存,强制重新 clone")
}

func (t *templateFlags) fetch(ctx context.Context) (scaffold.Source, *manifest.Manifest, error) {
	src, err := scaffold.Fetch(ctx, scaffold.FetchOptions{
		Dir: t.dir, Repo: t.repo, Ref: t.ref, NoCache: t.noCache,
	})
	if err != nil {
		return scaffold.Source{}, nil, err
	}
	m, err := manifest.Load(src.Root)
	if err != nil {
		return scaffold.Source{}, nil, err
	}
	return src, m, nil
}

func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "co",
		Short: "go-connect-template 的脚手架",
		Long: `co 从 go-connect-template 生成 ConnectRPC 微服务骨架。

模板本身是一份所有能力都打开、能直接编译运行的参考实现;co 按你的选择
做减法(删文件、删标记行、删 go.mod 依赖),再生成一套资源代码。
减法比改写可靠 —— 模板编译得过,裁剪后的结果就编译得过。`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	cmd.AddCommand(
		newNewCmd(),
		newResourceCmd(),
		newProtoCmd(),
		newDoctorCmd(),
		newVersionCmd(),
	)
	return cmd
}

// Execute 是程序入口。
func Execute() int {
	// Ctrl-C 要能中断 clone 与 post-generate hook。
	// 没有这个的话,一次慢 clone 只能等它自己超时。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	err := newRootCmd().ExecuteContext(ctx)
	if err == nil {
		return 0
	}
	if errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "canceled")
		return 130
	}
	ui.New().Error("%v", err)
	return 1
}
