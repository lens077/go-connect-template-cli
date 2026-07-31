package scaffold

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lens077/co-cli/internal/manifest"
	"github.com/lens077/co-cli/internal/resource"
)

// AddOptions 是 co resource add 的输入。
type AddOptions struct {
	// Dir 已存在的服务目录
	Dir string
	// Name 新资源名
	Name string
	// Features 非空时覆盖自动探测的结果
	Features []string
	// Force 允许覆盖已存在的同名文件
	Force bool
	// DryRun 只打印不落盘
	DryRun bool
}

// AddPlan 是往已有服务里加一个资源的操作清单。
type AddPlan struct {
	Info     ServiceInfo
	Names    resource.Names
	Features manifest.FeatureSet
	Spec     resource.Spec

	// Files 相对服务目录的落点(proto 可能落在服务目录之外,故会是 ../.. 开头)
	Files []resource.File
	// Inserts 要插的 provider
	Inserts []manifest.Insert
	// Hooks 生成后要跑的命令
	Hooks []manifest.Hook
}

// NewAddPlan 计算往已有服务里加资源要做的事。
func NewAddPlan(src Source, m *manifest.Manifest, opts AddOptions) (*AddPlan, error) {
	info, err := InspectService(opts.Dir)
	if err != nil {
		return nil, err
	}
	names, err := resource.NewNames(opts.Name)
	if err != nil {
		return nil, err
	}

	set, err := DetectFeatures(info.Root, m)
	if err != nil {
		return nil, err
	}
	if len(opts.Features) > 0 {
		if set, err = m.Resolve(opts.Features); err != nil {
			return nil, err
		}
	}

	spec := resource.Spec{
		Names:         names,
		Module:        info.Module,
		ServiceModule: info.ServiceModule,
		ServiceName:   names.Name + "-service",
		Features:      set,
		// 接着服务里已有的 schema 往下排。新表引用老表时,建表顺序就是
		// 文件名顺序,排在前面会让外键建不起来。
		SchemaSeq: resource.NextSchemaSeq(filepath.Join(info.Root, filepath.FromSlash(resource.SchemaDir))),
	}

	files, err := resource.Render(src.ScaffoldDir(), spec)
	if err != nil {
		return nil, err
	}

	// proto 的落点由服务的实际布局决定,不是模板里那个固定的 api/<name>/v1。
	// monorepo 下 api/ 被抽到了仓库根,渲染出的相对路径要重新换算。
	relAPI, err := RelAPIDir(info, names.APIDir)
	if err != nil {
		return nil, err
	}
	for i := range files {
		if rest, ok := trimPathPrefix(files[i].Path, filepath.FromSlash(names.APIDir)); ok {
			files[i].Path = filepath.Join(relAPI, rest)
		}
	}

	if !opts.Force {
		var exist []string
		for _, f := range files {
			if _, serr := os.Stat(filepath.Join(info.Root, f.Path)); serr == nil {
				exist = append(exist, ToSlash(f.Path))
			}
		}
		if len(exist) > 0 {
			sort.Strings(exist)
			return nil, fmt.Errorf("these files already exist (use --force to overwrite):\n  %s",
				joinLines2(exist))
		}
	}

	p := &AddPlan{
		Info:     info,
		Names:    names,
		Features: set,
		Spec:     spec,
		Files:    files,
		Inserts:  spec.Anchors(),
	}

	// hook 的 cmd 都是相对服务目录写的,而 monorepo 下 go.mod / buf.yaml
	// 都在仓库根,那些命令在服务目录里跑只会报找不到文件。
	// 服务目录自己有没有 go.mod,恰好就是这两种布局的分界。
	layout := "monorepo"
	if info.Root == info.ModuleRoot {
		layout = "standalone"
	}
	// buf 另外还要求 buf.yaml 真的在服务目录里 —— 独立仓库里也有人把
	// buf.yaml 放在上一层
	_, bufErr := os.Stat(filepath.Join(info.Root, "buf.yaml"))
	hasBuf := bufErr == nil

	for _, h := range m.Hooks.PostGenerate {
		if h.When != "" && !set.Has(h.When) {
			continue
		}
		if h.WhenLayout != "" && h.WhenLayout != layout {
			continue
		}
		if strings.HasPrefix(h.Name, "buf") && !hasBuf {
			continue
		}
		p.Hooks = append(p.Hooks, h)
	}
	return p, nil
}

// Apply 执行一次 resource add。
func (p *AddPlan) Apply(ctx context.Context, m *manifest.Manifest, rep Reporter) error {
	if rep == nil {
		rep = DiscardReporter{}
	}

	rep.Step("writing %d file(s)", len(p.Files))
	for _, f := range p.Files {
		dest := filepath.Join(p.Info.Root, f.Path)
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dest, f.Content, 0o644); err != nil {
			return err
		}
	}

	rep.Step("wiring %d provider(s) at anchors", len(p.Inserts))
	shim := &Plan{Manifest: m, Inserts: p.Inserts}
	if err := shim.applyInserts(p.Info.Root); err != nil {
		return err
	}

	shim.Hooks = p.Hooks
	shim.runHooks(ctx, p.Info.Root, rep)
	return nil
}

// Describe 打印将要发生的事,供 --dry-run 使用。
func (p *AddPlan) Describe(w *os.File) {
	fmt.Fprintf(w, "service    %s\n", p.Info.Root)
	fmt.Fprintf(w, "module     %s\n", p.Info.ServiceModule)
	fmt.Fprintf(w, "features   %v\n", p.Features.Names())
	fmt.Fprintf(w, "\ncreate (%d)\n", len(p.Files))
	for _, f := range p.Files {
		fmt.Fprintf(w, "  + %s\n", ToSlash(f.Path))
	}
	fmt.Fprintf(w, "\ninsert at anchor (%d)\n", len(p.Inserts))
	for _, ins := range p.Inserts {
		fmt.Fprintf(w, "  @ %-24s %s\n", ins.Anchor, firstLine(ins.Text))
	}
	if len(p.Hooks) > 0 {
		fmt.Fprintf(w, "\npost-generate\n")
		for _, h := range p.Hooks {
			fmt.Fprintf(w, "  $ %s\n", strings.Join(h.Cmd, " "))
		}
	}
}

func trimPathPrefix(p, prefix string) (string, bool) {
	rel, err := filepath.Rel(prefix, p)
	if err != nil || rel == ".." || filepath.IsAbs(rel) {
		return "", false
	}
	if len(rel) >= 2 && rel[0] == '.' && rel[1] == '.' {
		return "", false
	}
	return rel, true
}

func joinLines2(in []string) string {
	out := ""
	for i, s := range in {
		if i > 0 {
			out += "\n  "
		}
		out += s
	}
	return out
}
