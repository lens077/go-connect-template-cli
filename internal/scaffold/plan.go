package scaffold

import (
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"github.com/lens077/co-cli/internal/manifest"
	"github.com/lens077/co-cli/internal/resource"
)

// Options 是 co new 的全部输入。
type Options struct {
	// Name 服务名,同时也是默认生成的那套资源的名字
	Name string
	// Module 目标 module 路径。monorepo 下是根 module。
	Module string
	// Dest 目标根目录。monorepo 下是仓库根,不是服务目录。
	Dest string
	// Layout 布局名,必须是 manifest.layouts 里的 key
	Layout string
	// Features 用户选中的 feature,needs 会自动展开
	Features []string
	// ServiceName 服务注册名,空则取 <name>-service
	ServiceName string
	// KeepExample 保留模板自带的示例资源
	KeepExample bool
	// NoResource 只出骨架,不生成资源
	NoResource bool

	// 以下几项只有 monorepo 的 Makefile 模板用得上,都是部署环境相关的默认值
	DockerRegistry  string
	DockerNamespace string
	ConsulAddr      string
}

// Plan 是一次生成的完整操作清单。
//
// 先把要做的事全部算出来、能打印出来(--dry-run),再统一执行。
// 好处是「会发生什么」和「怎么发生」分开了:排查生成结果不对时,
// 先看 plan 就能判断是选型算错了还是执行环节出了问题。
type Plan struct {
	Opts     Options
	Manifest *manifest.Manifest
	Layout   manifest.Layout
	Features manifest.FeatureSet
	Names    resource.Names

	// Source 模板仓库根目录
	Source string
	// Dest 目标根目录(绝对路径)
	Dest string
	// ServiceDir 服务目录,相对 Dest
	ServiceDir string
	// ProtoDir api/ 树的落点,相对 Dest
	ProtoDir string
	// ServiceModule 服务包的导入前缀
	ServiceModule string

	// Deletes 要删除的路径,相对 ServiceDir(已排序去重)
	Deletes []string
	// DropRequires 要从 go.mod 删掉的直接依赖
	DropRequires []string
	// Overlays 布局覆盖文件:模板内绝对路径 -> 相对 ServiceDir 的落点
	Overlays []Overlay
	// Inserts 按锚点插入的代码
	Inserts []manifest.Insert
	// Resource 要生成的资源;NoResource 时为 nil
	Resource *resource.Spec
	// Hooks 生成后要跑的命令(已按 when/when_layout 过滤)
	Hooks []manifest.Hook
}

// Overlay 一个布局覆盖文件。
type Overlay struct {
	Src  string // 模板仓库内的绝对路径
	Dest string // 相对 ServiceDir
}

// TemplateData 是所有模板(资源模板与布局覆盖)共用的渲染数据。
type TemplateData struct {
	resource.Names

	Module        string
	ServiceModule string
	ServiceName   string
	Features      manifest.FeatureSet

	DockerRegistry  string
	DockerNamespace string
	ConsulAddr      string
}

// NewPlan 计算一次生成要做的全部操作,不碰磁盘上的目标目录。
func NewPlan(src Source, m *manifest.Manifest, opts Options) (*Plan, error) {
	layout, ok := m.Layouts[opts.Layout]
	if !ok {
		return nil, fmt.Errorf("unknown layout %q (available: %s)",
			opts.Layout, strings.Join(m.LayoutNames(), ", "))
	}

	names, err := resource.NewNames(opts.Name)
	if err != nil {
		return nil, err
	}
	if err := ValidateModulePath(opts.Module); err != nil {
		return nil, err
	}

	// 布局强制启用的 feature 与用户的选择取并集。放在 Resolve 之前而不是之后,
	// 是为了让它照常走 needs 展开与互斥校验 —— 强制项也可能有前置依赖。
	selected := append(append([]string{}, opts.Features...), layout.Features...)
	set, err := m.Resolve(selected)
	if err != nil {
		return nil, err
	}

	// 保留示例资源却关掉它依赖的 feature,生成物必然编译不过。
	// 这里直接拦下并说清是哪个 feature,比让用户去读 go build 的报错强。
	if opts.KeepExample && !set.HasAll(m.Example.Needs) {
		return nil, fmt.Errorf("--keep-example requires feature(s) %s, which are not enabled",
			strings.Join(m.Example.Needs, ", "))
	}

	dest, err := filepath.Abs(opts.Dest)
	if err != nil {
		return nil, err
	}

	serviceName := opts.ServiceName
	if serviceName == "" {
		serviceName = names.Name + "-service"
	}

	// 布局字段本身也是模板:service_dir 里有 {{.Name}},service_module 里有 {{.Module}}
	pre := TemplateData{Names: names, Module: opts.Module}
	serviceDir, err := expand("service_dir", layout.ServiceDir, pre)
	if err != nil {
		return nil, err
	}
	protoDir, err := expand("proto_dir", layout.ProtoDir, pre)
	if err != nil {
		return nil, err
	}
	serviceModule, err := expand("service_module", layout.ServiceModule, pre)
	if err != nil {
		return nil, err
	}
	if err := ValidateModulePath(serviceModule); err != nil {
		return nil, fmt.Errorf("layout %q produced an invalid service_module: %w", layout.Name, err)
	}

	p := &Plan{
		Opts:          opts,
		Manifest:      m,
		Layout:        layout,
		Features:      set,
		Names:         names,
		Source:        src.Root,
		Dest:          dest,
		ServiceDir:    filepath.FromSlash(serviceDir),
		ProtoDir:      filepath.FromSlash(protoDir),
		ServiceModule: serviceModule,
		DropRequires:  m.DroppedRequires(set),
	}

	p.Deletes = planDeletes(m, set, opts.KeepExample, layout)

	overlays, err := planOverlays(src.Root, layout)
	if err != nil {
		return nil, err
	}
	p.Overlays = overlays
	p.Inserts = append(p.Inserts, layout.Inserts...)

	if !opts.NoResource {
		spec := &resource.Spec{
			Names:         names,
			Module:        opts.Module,
			ServiceModule: serviceModule,
			ServiceName:   serviceName,
			Features:      set,
			SchemaSeq:     p.nextSchemaSeq(),
		}
		p.Resource = spec
		p.Inserts = append(p.Inserts, spec.Anchors()...)
	}

	for _, h := range m.Hooks.PostGenerate {
		if h.When != "" && !set.Has(h.When) {
			continue
		}
		if h.WhenLayout != "" && h.WhenLayout != layout.Name {
			continue
		}
		p.Hooks = append(p.Hooks, h)
	}

	return p, nil
}

// nextSchemaSeq 算新资源 schema 文件的 000N_ 前缀。
//
// 要站在「删除已经执行完」的视角上算,所以得排掉 p.Deletes ——
// 不带 --keep-example 时模板那份 0001_products.sql 会被删掉,新资源就该占
// 0001;照着模板原样数会让它跳到 0002,序号里空一个洞,看着像丢了一次迁移。
//
// 调用点在 p.Deletes 赋值之后,顺序不能换。
func (p *Plan) nextSchemaSeq() string {
	dir := filepath.Join(p.Source, filepath.FromSlash(resource.SchemaDir))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return resource.NextSchemaSeqFrom(nil)
	}

	deleted := make(map[string]bool, len(p.Deletes))
	for _, d := range p.Deletes {
		deleted[d] = true
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || deleted[path.Join(resource.SchemaDir, e.Name())] {
			continue
		}
		names = append(names, e.Name())
	}
	return resource.NextSchemaSeqFrom(names)
}

// planDeletes 汇总所有要删的路径:示例资源 + 未启用 feature 的文件 + 布局丢弃项。
func planDeletes(m *manifest.Manifest, set manifest.FeatureSet, keepExample bool, layout manifest.Layout) []string {
	seen := map[string]bool{}
	add := func(paths ...string) {
		for _, p := range paths {
			if p != "" {
				seen[path.Clean(p)] = true
			}
		}
	}

	if !keepExample {
		add(m.Example.Files...)
	}
	add(m.DroppedFiles(set)...)
	add(layout.Drop...)

	// keep 优先级最高,放在最后减掉。models/db.go 就属于这类:
	// 它按目录归在示例资源里,但公共的 data 层依赖它。
	for _, k := range m.Example.Keep {
		delete(seen, path.Clean(k))
	}

	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func planOverlays(root string, layout manifest.Layout) ([]Overlay, error) {
	if layout.Overlay == "" {
		return nil, nil
	}
	// overlay 是相对 .co/ 写的(manifest 里其他相对路径也都是),
	// 不是相对仓库根 —— 相对仓库根的话 scaffold/ 会和模板自己的目录同名。
	base := filepath.Join(root, manifest.RootDir, filepath.FromSlash(layout.Overlay))
	if _, err := os.Stat(base); os.IsNotExist(err) {
		return nil, fmt.Errorf("layout %q: overlay dir %s does not exist under %s/ in the template",
			layout.Name, layout.Overlay, manifest.RootDir)
	}

	var out []Overlay
	err := filepath.WalkDir(base, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, rerr := filepath.Rel(base, p)
		if rerr != nil {
			return rerr
		}
		// overlay 目录的结构就是目标路径,只去掉 .tmpl 后缀。
		// 非 .tmpl 文件原样拷贝(不渲染),用于放二进制或含大量 {{ }} 的文件。
		out = append(out, Overlay{Src: p, Dest: strings.TrimSuffix(rel, ".tmpl")})
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Dest < out[j].Dest })
	return out, nil
}

// Data 返回渲染模板用的数据。
func (p *Plan) Data() TemplateData {
	serviceName := p.Opts.ServiceName
	if serviceName == "" {
		serviceName = p.Names.Name + "-service"
	}
	return TemplateData{
		Names:           p.Names,
		Module:          p.Opts.Module,
		ServiceModule:   p.ServiceModule,
		ServiceName:     serviceName,
		Features:        p.Features,
		DockerRegistry:  p.Opts.DockerRegistry,
		DockerNamespace: p.Opts.DockerNamespace,
		ConsulAddr:      p.Opts.ConsulAddr,
	}
}

// DevDependency 是一个要在本地跑起来的外部组件。
type DevDependency struct {
	// Feature 声明它的 feature 名
	Feature string
	// Compose compose 文件路径,相对服务目录
	Compose string
	// Required 为 true 表示服务启动阶段硬依赖它,连不上就起不来
	Required bool
}

// DevDependencies 列出已启用的 feature 声明的本地依赖,必需的排在前面。
//
// 用在生成结束后的「下一步」里。少了这份清单,用户照着提示直接 make dev
// 会撞上一条 fx 的依赖注入错误 —— 那个报错的第一行是 "could not build
// arguments for function main.NewApp.func3",真正的原因("连不上 5432")
// 埋在第四层嵌套里。
func (p *Plan) DevDependencies() []DevDependency {
	var out []DevDependency
	for _, name := range p.Features.Names() {
		f, ok := p.Manifest.Features[name]
		if !ok || f.DevCompose == "" {
			continue
		}
		out = append(out, DevDependency{
			Feature:  name,
			Compose:  f.DevCompose,
			Required: f.DevRequired,
		})
	}
	// 必需的排前面,组内保持 Names() 的字典序(稳定,便于测试)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Required && !out[j].Required })
	return out
}

// Describe 打印操作清单,供 --dry-run 使用。
func (p *Plan) Describe(w io.Writer) {
	fmt.Fprintf(w, "template   %s\n", p.Source)
	fmt.Fprintf(w, "layout     %s\n", p.Layout.Name)
	fmt.Fprintf(w, "module     %s\n", p.Opts.Module)
	if p.ServiceModule != p.Opts.Module {
		fmt.Fprintf(w, "svc module %s\n", p.ServiceModule)
	}
	fmt.Fprintf(w, "service    %s\n", filepath.Join(p.Dest, p.ServiceDir))
	fmt.Fprintf(w, "proto      %s\n", filepath.Join(p.Dest, p.ProtoDir))
	fmt.Fprintf(w, "features   %s\n", strings.Join(p.Features.Names(), " "))

	if len(p.Deletes) > 0 {
		fmt.Fprintf(w, "\ndelete (%d)\n", len(p.Deletes))
		for _, d := range p.Deletes {
			fmt.Fprintf(w, "  - %s\n", d)
		}
	}
	if len(p.DropRequires) > 0 {
		fmt.Fprintf(w, "\ngo.mod drop require (%d)\n", len(p.DropRequires))
		for _, d := range p.DropRequires {
			fmt.Fprintf(w, "  - %s\n", d)
		}
	}
	if len(p.Overlays) > 0 {
		fmt.Fprintf(w, "\noverlay (%d)\n", len(p.Overlays))
		for _, o := range p.Overlays {
			fmt.Fprintf(w, "  + %s\n", ToSlash(o.Dest))
		}
	}
	if p.Resource != nil {
		fmt.Fprintf(w, "\nresource %s\n", p.Resource.Name)
		for _, f := range resource.Targets(*p.Resource) {
			fmt.Fprintf(w, "  + %s\n", ToSlash(f))
		}
	}
	if len(p.Inserts) > 0 {
		fmt.Fprintf(w, "\ninsert at anchor (%d)\n", len(p.Inserts))
		for _, ins := range p.Inserts {
			fmt.Fprintf(w, "  @ %-24s %s\n", ins.Anchor, firstLine(ins.Text))
		}
	}
	if len(p.Hooks) > 0 {
		fmt.Fprintf(w, "\npost-generate\n")
		for _, h := range p.Hooks {
			fmt.Fprintf(w, "  $ %s\n", strings.Join(h.Cmd, " "))
		}
	}
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i]) + " …"
	}
	return s
}

func expand(name, text string, data TemplateData) (string, error) {
	t, err := template.New(name).Option("missingkey=error").Parse(text)
	if err != nil {
		return "", fmt.Errorf("parse %s: %w", name, err)
	}
	var b strings.Builder
	if err := t.Execute(&b, data); err != nil {
		return "", fmt.Errorf("render %s: %w", name, err)
	}
	return b.String(), nil
}
