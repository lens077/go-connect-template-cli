package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/lens077/go-connect-template-cli/internal/manifest"
	"github.com/lens077/go-connect-template-cli/internal/scaffold"
	"github.com/lens077/go-connect-template-cli/internal/ui"
)

// groupFlags 把 manifest 里的分组暴露成好记的 flag。
//
// 这张表写死在 CLI 里,而分组本身来自 manifest —— 两边对不上时会在
// 运行时报错(见 collectPresets),不会静默忽略用户传的参数。
// 之所以不做成纯动态的 --with group=member,是因为 --cache redis 这种写法
// 用户不用查文档就知道怎么写,而 flag 必须在读到 manifest 之前就注册好。
var groupFlags = []struct {
	flag  string
	group string
	help  string
}{
	{"database", "database", "数据库"},
	{"cache", "cache", "缓存"},
	{"search", "search", "检索"},
	{"iam", "iam", "身份认证"},
	{"store", "store", "对象存储"},
	{"discovery", "discovery", "服务注册与发现"},
	{"config-source", "config-source", "配置数据源(可逗号分隔多个)"},
}

type newOptions struct {
	tmpl   templateFlags
	render renderFlags

	module   string
	dir      string
	layout   string
	features []string

	keepExample bool
	noResource  bool
	dryRun      bool
	yes         bool
}

// renderFlags 是「生成时填进模板、之后不再有任何地方记录」的渲染参数。
//
// co new 和 co upgrade 必须共用同一份默认值:upgrade 要重新生成一份参考副本
// 来比对,这几个值对不上,monorepo 的 Makefile 每次都会报 modified,
// --write 还会把 REGISTER / CONSUL_ADDR 抹成空串。
// 生成时传了非默认值的,upgrade 时要再传一遍(产物里没有存)。
type renderFlags struct {
	serviceName     string
	dockerRegistry  string
	dockerNamespace string
	consulAddr      string
}

// params 把 flag 值压成可写进 origin 的结构。
func (r *renderFlags) params() scaffold.RenderParams {
	return scaffold.RenderParams{
		ServiceName:     r.serviceName,
		DockerRegistry:  r.dockerRegistry,
		DockerNamespace: r.dockerNamespace,
		ConsulAddr:      r.consulAddr,
	}
}

// applyOrigin 用 origin 里记录的渲染参数补上用户这次没显式传的 flag。
// 显式传了的以用户为准 —— 他可能就是想换个镜像仓库。
func (r *renderFlags) applyOrigin(cmd *cobra.Command, o scaffold.RenderParams) {
	f := cmd.Flags()
	if !f.Changed("service-name") && o.ServiceName != "" {
		r.serviceName = o.ServiceName
	}
	if !f.Changed("docker-registry") && o.DockerRegistry != "" {
		r.dockerRegistry = o.DockerRegistry
	}
	if !f.Changed("docker-namespace") && o.DockerNamespace != "" {
		r.dockerNamespace = o.DockerNamespace
	}
	if !f.Changed("consul-addr") && o.ConsulAddr != "" {
		r.consulAddr = o.ConsulAddr
	}
}

func (r *renderFlags) register(cmd *cobra.Command) {
	f := cmd.Flags()
	f.StringVar(&r.serviceName, "service-name", "", "服务注册名,默认 <name>-service")
	f.StringVar(&r.dockerRegistry, "docker-registry", "ccr.ccs.tencentyun.com", "镜像仓库地址(monorepo)")
	f.StringVar(&r.dockerNamespace, "docker-namespace", "sumery", "镜像命名空间(monorepo)")
	f.StringVar(&r.consulAddr, "consul-addr", "consul.app.com", "服务注册用的 Consul 地址(monorepo)")
}

func newNewCmd() *cobra.Command {
	o := &newOptions{}

	cmd := &cobra.Command{
		Use:   "new <name>",
		Short: "生成一个新服务",
		Long: `从模板生成一个新服务,并按服务名生成一套资源代码(proto + SQL + biz/data/service)。

未指定的选项会弹交互表单;--yes 用默认值跳过交互。
先跑一次 --dry-run 可以看清将要发生的每一步删除与生成。`,
		Args: cobra.ExactArgs(1),
		Example: `  co new cart --module github.com/acme/shop --yes
  co new cart --layout monorepo --dir ~/src/ecommerce --module github.com/acme/ecommerce/backend
  co new cart --cache none --search none --dry-run`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runNew(cmd, args[0], o)
		},
	}

	o.tmpl.register(cmd)
	f := cmd.Flags()
	f.StringVarP(&o.module, "module", "m", "", "目标 module 路径")
	f.StringVarP(&o.dir, "dir", "d", "", "输出目录,默认 ./<name>(monorepo 默认当前目录)")
	f.StringVarP(&o.layout, "layout", "l", "", "目录布局:standalone | monorepo")
	f.StringSliceVar(&o.features, "feature", nil, "直接按名字启用 feature,可重复")
	f.BoolVar(&o.keepExample, "keep-example", false, "保留模板自带的示例资源")
	f.BoolVar(&o.noResource, "no-resource", false, "只出骨架,不生成资源代码")
	f.BoolVar(&o.dryRun, "dry-run", false, "只打印将要执行的操作,不落盘")
	f.BoolVarP(&o.yes, "yes", "y", false, "不交互,未指定项一律用默认值")
	o.render.register(cmd)

	// 分组 flag 的值不绑到结构体上,统一由 collectPresets 从 cmd.Flags() 读:
	// 它要区分「传了 none」和「没传」,而这个区别只有 Flags().Changed() 知道
	for _, g := range groupFlags {
		f.String(g.flag, "", g.help+",none 表示不启用")
	}

	return cmd
}

func runNew(cmd *cobra.Command, name string, o *newOptions) error {
	p := ui.New()

	src, m, err := o.tmpl.fetch(cmd.Context())
	if err != nil {
		return err
	}
	if src.FromCache {
		p.Dim("using cached template at %s (--no-cache to refresh)", src.Root)
	}

	presets, err := collectPresets(cmd, m)
	if err != nil {
		return err
	}

	layout := o.layout
	module := o.module
	features := append([]string{}, o.features...)
	for _, vs := range presets {
		features = append(features, vs...)
	}

	// 缺项:能交互就问,不能就用默认值。
	// 判断顺序是「用户明确说了 --yes」>「不是 TTY」>「问」——
	// 非 TTY 下弹表单会读到 EOF,错误信息完全指不到真正的问题(缺参数)。
	needAsk := module == "" || layout == "" || len(presets) < len(m.Groups)
	if needAsk && !o.yes && ui.Interactive() {
		ans, aerr := ui.Ask(m, ui.AskOptions{
			Name: name, Module: module, Layout: layout, Preset: presets,
		})
		if aerr != nil {
			return aerr
		}
		module, layout, features = ans.Module, ans.Layout, ans.Features
	} else {
		if layout == "" {
			layout = "standalone"
		}
		if module == "" {
			if !o.yes {
				return fmt.Errorf("--module is required in non-interactive mode")
			}
			return fmt.Errorf("--module is required (e.g. --module github.com/acme/%s)", name)
		}
		for _, g := range m.SortedGroups() {
			if _, ok := presets[g.Name]; !ok {
				features = append(features, m.DefaultsFor(g)...)
			}
		}
	}

	dir := o.dir
	if dir == "" {
		if layout == "monorepo" {
			// monorepo 的输出根是仓库根,服务落在 backend/services/<name>。
			// 默认当前目录,因为通常就是在仓库里跑这条命令。
			dir = "."
		} else {
			dir = name
		}
	}

	plan, err := scaffold.NewPlan(src, m, scaffold.Options{
		Name:            name,
		Module:          module,
		Dest:            dir,
		Layout:          layout,
		Features:        dedupe(features),
		ServiceName:     o.render.serviceName,
		KeepExample:     o.keepExample,
		NoResource:      o.noResource,
		DockerRegistry:  o.render.dockerRegistry,
		DockerNamespace: o.render.dockerNamespace,
		ConsulAddr:      o.render.consulAddr,
	})
	if err != nil {
		return err
	}

	if o.dryRun {
		plan.Describe(os.Stdout)
		return nil
	}

	if err := scaffold.Apply(cmd.Context(), plan, p); err != nil {
		return err
	}

	// 生成记录:只记历史事实(模板 commit、渲染参数),给 co upgrade 做三方合并的 base。
	// 模板不是 git 仓库(比如解压的目录)时没有 commit,就不写 —— 没有 base 的服务
	// upgrade 退回两方比对,这是合法状态。
	if src.Commit != "" {
		if err := scaffold.WriteOrigin(filepath.Join(plan.Dest, plan.ServiceDir), scaffold.Origin{
			Template: src.Repo,
			Commit:   src.Commit,
			Dirty:    src.Dirty,
			Co:       version(),
			Render:   o.render.params(),
		}); err != nil {
			return fmt.Errorf("write %s: %w", scaffold.OriginFile, err)
		}
		if src.Dirty {
			p.Warn("模板工作树有未提交改动,%s 记录的 commit 只是近似;提交后再生成,upgrade 的 base 才准确", scaffold.OriginFile)
		}
	}

	printNextSteps(p, plan)
	return nil
}

// collectPresets 读出用户通过分组 flag 指定的选择。
func collectPresets(cmd *cobra.Command, m *manifest.Manifest) (map[string][]string, error) {
	out := map[string][]string{}

	for _, g := range groupFlags {
		if !cmd.Flags().Changed(g.flag) {
			continue
		}
		group, ok := m.Groups[g.group]
		if !ok {
			return nil, fmt.Errorf("--%s is not supported by this template (no group %q in %s)",
				g.flag, g.group, manifest.Path)
		}

		raw, _ := cmd.Flags().GetString(g.flag)
		var vals []string
		for _, v := range strings.Split(raw, ",") {
			v = strings.TrimSpace(v)
			if v == "" || v == manifest.NoneChoice {
				continue
			}
			if !slicesContains(group.Members, v) {
				return nil, fmt.Errorf("--%s=%q is not valid; choose one of: %s, %s",
					g.flag, v, strings.Join(group.Members, ", "), manifest.NoneChoice)
			}
			vals = append(vals, v)
		}
		// 显式传了 none 也要记进 map:它表示「这一组已经定了,别再问我」,
		// 与「没传」是两回事
		out[g.group] = vals
	}
	return out, nil
}

func printNextSteps(p *ui.Printer, plan *scaffold.Plan) {
	svc := filepath.Join(plan.Dest, plan.ServiceDir)
	p.Title("\ndone → " + svc)

	rel, err := filepath.Rel(mustCwd(), svc)
	if err != nil || strings.HasPrefix(rel, "..") {
		rel = svc
	}

	steps := []step{{cmd: "cd " + rel}}
	if plan.Layout.Name == "monorepo" {
		// monorepo 的 buf 必须在仓库根跑,co 代跑不了,只能让用户自己来
		steps = append(steps, step{"make api && make conf", "proto 在仓库根统一生成"})
	}
	steps = append(steps, step{cmd: "go build ./..."})

	// make dev 只是不走配置中心,不等于不接外部组件:postgres 这类硬依赖
	// 连不上时服务在启动健康检查那一步就退出了。不在这里把要起的容器列清楚,
	// 用户看到的会是一条 fx 的依赖注入错误 —— 第一行是「could not build
	// arguments for main.NewApp.func3」,真正的原因埋在第四层嵌套里。
	for _, d := range plan.DevDependencies() {
		// 只区分「不起就起不来」和「可以先不起」。可选项不起来的后果各不相同
		// (ES 是 /healthz 变红,consul 是 make dev 根本不连它),CLI 说不准,
		// 说错了比不说更糟 —— 让用户按需去看该组件的文档。
		note := "必需"
		if !d.Required {
			note = "可选"
		}
		steps = append(steps, step{"docker compose -f " + scaffold.ToSlash(d.Compose) + " up -d", note})
	}
	steps = append(steps, step{"make dev", "从 configs/dev.yml 读整份配置,不走配置中心"})

	p.Dim("\n下一步:")
	printSteps(p, steps)
}

// step 是「下一步」里的一条命令,note 非空时以 # 注释跟在后面。
type step struct{ cmd, note string }

// printSteps 打印命令列表,把注释对齐到同一列。
func printSteps(p *ui.Printer, steps []step) {
	width := 0
	for _, s := range steps {
		if s.note != "" && len(s.cmd) > width {
			width = len(s.cmd)
		}
	}
	for _, s := range steps {
		if s.note == "" {
			p.Command(s.cmd)
			continue
		}
		p.Command(fmt.Sprintf("%-*s   # %s", width, s.cmd, s.note))
	}
}

func mustCwd() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func slicesContains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}
