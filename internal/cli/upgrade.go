package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/lens077/go-connect-template-cli/internal/scaffold"
	"github.com/lens077/go-connect-template-cli/internal/ui"
)

type upgradeOptions struct {
	tmpl   templateFlags
	render renderFlags

	name          string
	baseRef       string
	noBase        bool
	write         bool
	writeModified bool
	only          []string
	allowDirty    bool
	diff          bool
}

func newUpgradeCmd() *cobra.Command {
	o := &upgradeOptions{}

	cmd := &cobra.Command{
		Use:   "upgrade [dir]",
		Short: "比对已生成的服务与模板新版",
		Long: `把一个已经生成好的服务和模板的当前版本做比对。

co new 是一次性的:生成完之后模板继续演进,已生成的服务不会跟着动,
时间一长同一份基础设施代码就在各个服务里各自漂移。upgrade 用同样的身份
和反推出来的 feature 重新生成一份干净的副本,再逐文件比对。

默认只报告不写盘 —— 一个文件和模板不一样,可能是模板演进了,也可能是你
故意改的,co 分辨不了。写回按风险分层:

  --write            只写 added(模板新增、服务里没有的文件),不覆盖任何东西
  --write-modified   连 modified 一起写;先用 --diff 看过再开
  --only <path>      只写点名的文件,可重复;点名的 modified 视为已同意
  blocked            模板带 +co:anchor 而服务里没有的 legacy 文件,永远不写:
                     盖上去会抹掉接线且 go build 不报错。先手工补上锚点行再来

写入要求 git 工作区干净(--allow-dirty 只跳过这项检查,不解锁任何一层),
且全部成功或全部不写,不会留下半升级状态。

比对范围是服务骨架与基础设施;业务资源(proto/biz/data/service)生成之后
就归你了,不参与比对。+co:anchor 上方的接线(NewXxx, / mux.Handle(...))
视为服务自己的内容,比对与写回都会原样保留。

生成时传过 --service-name / --docker-registry / --docker-namespace / --consul-addr
的,这里要再传一遍:产物里没有记录这些值,不传就按默认值渲染参考副本。`,
		Args: cobra.MaximumNArgs(1),
		Example: `  co upgrade                          # 在服务目录里看差异
  co upgrade backend/services/cart    # 指定服务目录
  co upgrade --diff                   # 连具体改了哪几行一起看
  co upgrade --write                  # 只写 added
  co upgrade --write-modified         # 连 modified 一起写
  co upgrade --only Makefile --only internal/server/server.go`,
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "."
			if len(args) == 1 {
				dir = args[0]
			}
			return runUpgrade(cmd, dir, o)
		},
	}

	o.tmpl.register(cmd)
	o.render.register(cmd)
	f := cmd.Flags()
	f.StringVar(&o.name, "name", "", "服务名,默认按布局反推(monorepo 取目录名,standalone 取 module 末段)")
	f.BoolVar(&o.write, "write", false, "写回 added 文件(要求 git 工作区干净)")
	f.BoolVar(&o.writeModified, "write-modified", false, "连 modified 一起写回(隐含 --write)")
	f.StringSliceVar(&o.only, "only", nil, "只写这些路径,可重复(隐含 --write;点名的 modified 视为已同意)")
	f.BoolVar(&o.allowDirty, "allow-dirty", false, "跳过 git 干净检查(不解锁 blocked)")
	f.BoolVar(&o.diff, "diff", false, "打印每个差异文件的 diff")
	f.StringVar(&o.baseRef, "base-ref", "", "三方合并的 base(模板 commit/tag/分支),默认读服务里的 .co-origin.yaml")
	f.BoolVar(&o.noBase, "no-base", false, "忽略 origin 与 --base-ref,只做两方比对")

	return cmd
}

func runUpgrade(cmd *cobra.Command, dir string, o *upgradeOptions) error {
	p := ui.New()

	src, m, err := o.tmpl.fetch(cmd.Context())
	if err != nil {
		return err
	}
	if src.FromCache {
		p.Dim("using cached template at %s (--no-cache to refresh)", src.Root)
	}

	// origin 里记录了生成时的渲染参数,用户没显式传的就用它 —— 否则 monorepo 的
	// Makefile 每次都因为镜像仓库不同报 modified
	if !o.noBase {
		if origin, oerr := scaffold.ReadOrigin(dir); oerr == nil && origin != nil {
			o.render.applyOrigin(cmd, origin.Render)
		}
	}

	up, err := scaffold.PlanUpgrade(cmd.Context(), src, m, scaffold.UpgradeOptions{
		ServiceDir:      dir,
		Name:            o.name,
		ServiceName:     o.render.serviceName,
		DockerRegistry:  o.render.dockerRegistry,
		DockerNamespace: o.render.dockerNamespace,
		ConsulAddr:      o.render.consulAddr,
		BaseRev:         o.baseRef,
		NoBase:          o.noBase,
		Fetch:           o.tmpl.fetchOptions(),
	})
	if err != nil {
		return err
	}
	defer func() { _ = up.Close() }()

	p.Dim("service   %s", up.ServiceRoot)
	p.Dim("layout    %s", up.Layout)
	p.Dim("module    %s", up.Module)
	p.Dim("features  %s", strings.Join(up.Features, ", "))
	switch {
	case up.Base != "":
		p.Dim("base      %s (three-way merge)", up.Base[:min(12, len(up.Base))])
	default:
		p.Dim("base      none (two-way; wiring kept via +co:anchor; give --base-ref or a %s for three-way merge)", scaffold.OriginFile)
	}

	// 先于差异列表打印:这些是 co 已经放弃自动处理的地方,
	// 用户要带着它们去看下面的 modified,而不是看完列表才发现有坑
	for _, w := range up.Warnings {
		p.Warn("%s", w)
	}

	if len(up.Changes) == 0 {
		p.Title("\n已是模板最新状态,无差异")
		if o.write || o.writeModified {
			advanceOrigin(p, up, src, o)
		}
		return nil
	}

	// 先按策略算出去向,列表里才能把「会写 / 不写 / 拒绝」标出来 ——
	// 只读模式下也算:用户要在看列表时就知道 --write 会做什么,而不是加了 flag 才发现
	sel, err := up.Select(scaffold.WritePolicy{Modified: o.writeModified, Only: o.only})
	if err != nil {
		return err
	}
	status := map[string]string{}
	legacy := 0
	for _, c := range sel.Blocked {
		switch {
		case c.Kind == scaffold.ChangeConflict:
			status[c.Path] = "[" + c.Blocked + "]"
		case strings.Contains(c.Blocked, "same Go package"):
			status[c.Path] = "[blocked: " + c.Blocked + "]"
		default:
			status[c.Path] = "[blocked]"
			legacy++
		}
	}
	for _, c := range sel.Skipped {
		if strings.Contains(c.Blocked, "same Go package") {
			status[c.Path] = "[skipped: " + c.Blocked + "]"
		}
	}

	p.Title(fmt.Sprintf("\n%d 处差异", len(up.Changes)))
	for _, c := range up.Changes {
		if st, ok := status[c.Path]; ok {
			p.Step("%-9s %s  %s", c.Kind, c.Path, st)
			continue
		}
		p.Step("%-9s %s", c.Kind, c.Path)
	}
	if legacy > 0 {
		// 原因只打一次:每个 legacy 文件的原因都一样,逐行重复只会把列表淹掉
		p.Warn("%d 个文件 blocked:模板在这里有 +co:anchor 而服务里没有(锚点机制之前生成的),"+
			"接线搬不动,co 不会自动覆盖。给一个 base(--base-ref <模板 commit>)走三方合并,"+
			"或手工补上锚点行(见模板同名文件)再重跑。", legacy)
	}

	if o.diff {
		printDiffs(p, up)
	}

	write := o.write || o.writeModified || len(o.only) > 0
	if !write {
		p.Dim("\n只读比对,未改动任何文件。--diff 看具体内容;--write 写 added,--write-modified 连 modified 一起写。")
		// 模板删过的文件不会出现在上面:判断「服务里这个文件是模板留下的还是你
		// 自己加的」需要生成时的状态,而 co 刻意不在产物里存那份状态。
		p.Dim("注意:upgrade 只增改不删,模板删掉的文件需要你自己确认。")
		return nil
	}

	if len(sel.Skipped) > 0 {
		p.Dim("\n%d 个文件未选中,不写(--write-modified 或 --only 点名)", len(sel.Skipped))
	}
	if len(sel.Write) == 0 {
		p.Title("\n没有可写入的文件")
		return nil
	}

	if !o.allowDirty {
		if err := requireCleanWorktree(up.ServiceRoot); err != nil {
			return err
		}
	}

	n, err := up.Apply(p, sel)
	if err != nil {
		return err
	}

	p.Title(fmt.Sprintf("\n已写入 %d 个文件", n))
	// 全部差异都写完了,服务就等于模板新版:把 origin 推到新 commit,下次 upgrade 的
	// base 才是对的。有跳过或拒绝的文件时不推 —— 那些文件还停在旧 base 上
	if len(sel.Skipped) == 0 && len(sel.Blocked) == 0 {
		advanceOrigin(p, up, src, o)
	} else {
		recordBaseRef(p, up, src, o)
		if up.Base != "" {
			p.Dim("%s 未推进:还有文件没写入,base 仍是 %s", scaffold.OriginFile, up.Base[:min(12, len(up.Base))])
		}
	}
	p.Dim("\n下一步:")
	steps := []step{{cmd: "git diff", note: "逐条 review,模板不认识你的本地修改"}}
	// hook 产物不参与比对,写了源头就得重新生成:conf.proto 改了而 pb.go 没动,
	// go build 会报一个和 upgrade 看不出关系的 "has no field or method"
	var proto, sql bool
	for _, c := range sel.Write {
		switch {
		case strings.HasSuffix(c.Path, ".proto"):
			proto = true
		case strings.HasSuffix(c.Path, ".sql"):
			sql = true
		}
	}
	if proto {
		if up.Layout == "monorepo" {
			steps = append(steps, step{cmd: "make api && make conf", note: "在仓库根跑,proto 变了要重新生成"})
		} else {
			steps = append(steps, step{cmd: "buf generate", note: "proto 变了要重新生成"})
		}
	}
	if sql {
		steps = append(steps, step{cmd: "sqlc generate", note: "schema/queries 变了要重新生成"})
	}
	steps = append(steps, step{cmd: "go build ./...", note: ""}, step{cmd: "go test ./...", note: ""})
	printSteps(p, steps)
	return nil
}

// advanceOrigin 把 .co-origin.yaml 推到模板当前 commit。
// 只在「服务已与模板新版一致」时调用;origin 记的是事实,写一个不成立的 commit 比没有更糟。
func advanceOrigin(p *ui.Printer, up *scaffold.Upgrade, src scaffold.Source, o *upgradeOptions) {
	if src.Commit == "" || src.Commit == up.Base || o.noBase {
		return
	}
	if err := scaffold.WriteOrigin(up.ServiceRoot, scaffold.Origin{
		Template: src.Repo,
		Commit:   src.Commit,
		Dirty:    src.Dirty,
		Co:       version(),
		Render:   o.render.params(),
	}); err != nil {
		p.Warn("write %s: %v", scaffold.OriginFile, err)
		return
	}
	p.Step("%s → %s", scaffold.OriginFile, src.Commit[:min(12, len(src.Commit))])
}

// recordBaseRef 把用户显式给的 --base-ref 写成 origin(如果之前没有)。
// 用户断言「这个服务对应模板的这个 commit」是事实,记下来下次就不用再传;
// 已有 origin 时不动 —— 这次的 --base-ref 只是一次性的覆盖。
func recordBaseRef(p *ui.Printer, up *scaffold.Upgrade, src scaffold.Source, o *upgradeOptions) {
	if o.baseRef == "" || o.noBase || up.Base == "" {
		return
	}
	if existing, err := scaffold.ReadOrigin(up.ServiceRoot); err != nil || existing != nil {
		return
	}
	if err := scaffold.WriteOrigin(up.ServiceRoot, scaffold.Origin{
		Template: src.Repo,
		Commit:   up.Base,
		Co:       version(),
		Render:   o.render.params(),
	}); err != nil {
		p.Warn("write %s: %v", scaffold.OriginFile, err)
		return
	}
	p.Step("%s ← base %s(--base-ref 已记录,下次不必再传)", scaffold.OriginFile, up.Base[:min(12, len(up.Base))])
}

// requireCleanWorktree 确认服务目录在 git 里且没有未提交改动。
//
// --write 会整份覆盖文件,用户对这些文件的修改就没了。要求 git 干净等于
// 保证「写坏了能 git checkout 回来」—— 没有这层兜底,一次误判就是不可逆的。
func requireCleanWorktree(serviceRoot string) error {
	git, err := exec.LookPath("git")
	if err != nil {
		return fmt.Errorf("--write 需要 git 来兜底(写坏了好撤销),但找不到 git;" +
			"确认这个目录能撤销后用 --allow-dirty 跳过")
	}

	cmd := exec.Command(git, "status", "--porcelain", "--", ".")
	cmd.Dir = serviceRoot
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("%s 不在 git 仓库里(或 git status 失败);"+
			"确认这个目录能撤销后用 --allow-dirty 跳过", serviceRoot)
	}
	if len(strings.TrimSpace(string(out))) > 0 {
		return fmt.Errorf("%s 有未提交改动,先提交或 stash;"+
			"确实要盖上去就用 --allow-dirty", serviceRoot)
	}
	return nil
}

// printDiffs 借 git diff --no-index 打差异。
//
// 不自己实现 diff:为了一条只读命令引入一个 diff 库不划算,而 git 几乎
// 一定在场(这是个给 Go 服务用的脚手架)。没有 git 就退化成只报文件名。
func printDiffs(p *ui.Printer, up *scaffold.Upgrade) {
	git, err := exec.LookPath("git")
	if err != nil {
		p.Warn("找不到 git,--diff 只能列文件名")
		return
	}

	for _, c := range up.Changes {
		if c.Kind == scaffold.ChangeAdded {
			continue
		}
		fresh, current, derr := up.Diff(c.Path)
		if derr != nil {
			p.Warn("%s: %v", c.Path, derr)
			continue
		}

		tmp, terr := os.MkdirTemp("", "co-diff-")
		if terr != nil {
			p.Warn("%s: %v", c.Path, terr)
			continue
		}
		base := filepath.Join(tmp, "current")
		next := filepath.Join(tmp, "template")
		_ = os.WriteFile(base, current, 0o644)
		_ = os.WriteFile(next, fresh, 0o644)

		p.Title("\n--- " + c.Path)
		// git diff --no-index 有差异时退出码是 1,不是错误
		cmd := exec.Command(git, "--no-pager", "diff", "--no-index",
			"--src-prefix=current/", "--dst-prefix=template/", "--", base, next)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		_ = cmd.Run()
		_ = os.RemoveAll(tmp)
	}
}
