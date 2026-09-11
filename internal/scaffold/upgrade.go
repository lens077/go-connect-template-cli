package scaffold

import (
	"bytes"
	"context"
	"fmt"
	"go/format"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lens077/go-connect-template-cli/internal/manifest"
)

// co new 是一次性的:生成完之后模板继续演进,已生成的服务不会跟着动。
// 时间一长每个服务都停在自己生成那天的模板上,同一份基础设施代码在 N 个服务里
// 各自漂移 —— 这正是 co 想消灭的东西,却从生成的那一刻就开始重新长出来。
//
// upgrade 的做法是「重新生成一份干净的,再和现状比」:
// 用同样的身份(name/module/layout)和反推出来的 feature 生成到临时目录,
// 逐文件比对。这样不需要在产物里埋版本号或状态文件 —— 那种状态一旦和实际代码
// 对不上(比如用户手动改过),后续所有决策都建立在假事实上。
//
// 默认只报告不写盘。理由与 README 里「减法比改写可靠」是同一条:
// 我们无法区分「这个文件和模板不一样」是因为模板演进了,还是因为用户故意改的。
// 猜错的代价是静默吞掉用户的修改,所以把判断权交回给人,--write 由 git 兜底。
//
// 唯一一处主动「合并」是锚点:参考副本不带资源,锚点上方是空的,而服务里那里
// 堆着历次插进去的接线。不处理的话这几个文件永远 modified,--write 会把接线
// 抹掉且 go build 不报错。所以比对前先把锚点上方的接线搬进副本(upgrade_anchor.go)。

// ChangeKind 是一个文件相对模板新版的状态。
type ChangeKind int

const (
	// ChangeAdded 模板新增,服务里还没有。这类改动最安全:不覆盖任何东西。
	ChangeAdded ChangeKind = iota
	// ChangeModified 两边都有但内容不同。可能是模板演进,也可能是用户改的,
	// upgrade 分辨不了,所以默认只报告。
	ChangeModified
)

// String 让 ChangeKind 能直接打给用户看。
func (k ChangeKind) String() string {
	switch k {
	case ChangeAdded:
		return "added"
	case ChangeModified:
		return "modified"
	default:
		return "unknown"
	}
}

// Change 是一处待处理的差异。
type Change struct {
	// Path 相对服务根目录
	Path string
	Kind ChangeKind
	// Blocked 非空表示这个文件 co 拒绝自动写入,内容是原因。
	//
	// 目前只有一种情况:模板这份文件带 +co:anchor,而服务里那份没有 ——
	// 它是锚点机制之前生成的 legacy 文件,接线没有锚点可依附,carryAnchors
	// 搬不动。盖上去会把 NewXxx, / mux.Handle(...) 抹掉且 go build 不报错。
	Blocked string
}

// Upgrade 是一次比对的结果,同时持有那份临时生成物。
// 用完必须 Close,否则临时目录会留在磁盘上。
type Upgrade struct {
	// ServiceRoot 现有服务目录(绝对路径)
	ServiceRoot string
	// Name / Module / Layout 是反推出来的身份,打给用户确认用
	Name   string
	Module string
	Layout string
	// Features 反推出来的、当前启用的 feature(已排序)
	Features []string
	// KeepExample 反推出来的「生成时带了 --keep-example」
	KeepExample bool
	// Changes 已按路径排序
	Changes []Change
	// Warnings 是比对过程中无法自动处理、需要人看一眼的情况(比如模板删掉了某个锚点)
	Warnings []string

	// next 是每个 Change 将要写入的内容:参考副本经 gofmt 归一化、
	// 并把服务里锚点上方的接线搬进来之后的结果。写盘与 --diff 都用它,
	// 而不是回头读参考副本 —— 那份没有接线,直接盖上去会把服务写坏。
	next map[string][]byte

	freshRoot string
	tempDir   string
}

// upgrade 比的是**源码**,不是构建产物。
//
// co new 生成完会跑 hook(gofmt / go mod tidy / buf generate / sqlc generate),
// 参考副本不跑它们 —— 跑一遍要 buf、sqlc 和网络,而这是一条只读命令。
// 代价是 hook 的产物两边必然不同,所以这里把它们排除掉:它们由用户自己的
// make api / make conf / sqlc generate 重新生成,拿模板里那份去覆盖只会帮倒忙。
//
// gofmt 的差异不排除而是归一化(见 normalize):它影响每个 .go 文件,
// 排除等于把整个功能架空。
var skipCompareExact = map[string]bool{
	// go mod tidy 的产物:indirect 依赖随依赖图变化,与模板演进无关。
	// go.mod 还额外危险 —— 覆盖它会把 module 路径改回模板自己的。
	"go.mod": true,
	"go.sum": true,
}

// skipComparePrefix 是按前缀排除的目录。
//
// models/ 是 sqlc 从 migrations/queries 生成的。这里写死路径是已知的局限:
// 真实出口在 sqlc.yaml 里,模板改了出口这里不会跟着变。
var skipComparePrefix = []string{
	"internal/data/models/",
}

// skipCompareSuffix 是按后缀排除的生成物。
var skipCompareSuffix = []string{
	".pb.go",
	"_pb.ts",
	".connect.go",
}

func skipCompare(slashRel string) bool {
	if skipCompareExact[slashRel] {
		return true
	}
	for _, p := range skipComparePrefix {
		if strings.HasPrefix(slashRel, p) {
			return true
		}
	}
	for _, s := range skipCompareSuffix {
		if strings.HasSuffix(slashRel, s) {
			return true
		}
	}
	return false
}

// normalize 把内容压成「可比形态」。
//
// .go 走 gofmt:模板的 hook 会 gofmt 一遍生成物,而裁剪掉 +co: 标记行之后
// 缩进和空行必然和 gofmt 后的结果对不上。不归一化的话每个 .go 文件都会
// 被报成 modified —— 一屏噪音,用户看两次就再也不看了。
//
// 解析不了就原样返回:那说明这份内容本来就不是合法 Go(比如模板里的片段),
// 强行报错会让一次比对整个失败。
func normalize(slashRel string, data []byte) []byte {
	if !strings.HasSuffix(slashRel, ".go") {
		return data
	}
	if out, err := format.Source(data); err == nil {
		return out
	}
	return data
}

// UpgradeOptions 控制一次比对。
type UpgradeOptions struct {
	// ServiceDir 服务目录(monorepo 下是 backend/services/<name>,不是仓库根)
	ServiceDir string
	// Name 服务名。空则按布局反推;反推不出合法名字时报错要求显式指定。
	Name string

	// 以下是生成时填进模板、产物里没有任何地方记录的渲染参数。
	// 参考副本要用同样的值渲染,否则 monorepo 的 Makefile 永远 modified。
	// 调用方(CLI)负责给出与 co new 相同的默认值。
	ServiceName     string
	DockerRegistry  string
	DockerNamespace string
	ConsulAddr      string
}

// PlanUpgrade 比对一个已生成的服务与模板新版。
func PlanUpgrade(ctx context.Context, src Source, m *manifest.Manifest, opts UpgradeOptions) (*Upgrade, error) {
	info, err := InspectTarget(opts.ServiceDir)
	if err != nil {
		return nil, err
	}

	layoutName, err := detectLayout(info, m)
	if err != nil {
		return nil, err
	}
	layout := m.Layouts[layoutName]

	name := opts.Name
	if name == "" {
		name = detectName(info, layout)
	}

	// 先确认「这个目录确实长得像该布局生成出来的服务」。
	// 少了这一步,在仓库根跑 upgrade 会拿根目录当服务目录去比,
	// 结果是一屏毫无意义的 added。
	if err := checkServiceDir(info.Root, layout, name); err != nil {
		return nil, err
	}

	features, err := DetectFeatures(info.Root, m)
	if err != nil {
		return nil, fmt.Errorf("detect features: %w", err)
	}
	enabled := enabledFeatures(features)

	tempDir, err := os.MkdirTemp("", "co-upgrade-")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}

	keepExample := detectKeepExample(info.Root, m)

	up := &Upgrade{
		ServiceRoot: info.Root,
		Name:        name,
		Module:      info.Module,
		Layout:      layoutName,
		Features:    enabled,
		KeepExample: keepExample,
		tempDir:     tempDir,
	}

	// NoResource:只比骨架与基础设施。业务资源(proto/biz/data/service)生成之后
	// 就归用户了,几乎必然被改过,拿它们比对只会淹没真正该看的那几行。
	//
	// KeepExample 跟着服务走:示例资源在,参考副本就也保留它 —— 这样 +co:example
	// 标记的那些行(handler 参数、注册块)在副本里是正当内容,而不是靠锚点搬运
	// 才留下来的「服务自己的东西」。示例文件本身不在锚点旁,不保留就会被报成
	// 一堆 modified。
	plan, err := NewPlan(src, m, Options{
		Name:            name,
		Module:          info.Module,
		Dest:            tempDir,
		Layout:          layoutName,
		Features:        enabled,
		NoResource:      true,
		KeepExample:     keepExample,
		ServiceName:     opts.ServiceName,
		DockerRegistry:  opts.DockerRegistry,
		DockerNamespace: opts.DockerNamespace,
		ConsulAddr:      opts.ConsulAddr,
	})
	if err != nil {
		up.cleanup()
		return nil, err
	}

	// 比对不需要 buf/sqlc/go mod tidy 的产物,跑它们只是拖慢一次只读操作;
	// 而且 hook 失败只告警,留下的半成品会被当成「模板新版」参与比对。
	plan.Hooks = nil

	if err := Apply(ctx, plan, silentReporter{}); err != nil {
		up.cleanup()
		return nil, fmt.Errorf("generate reference copy: %w", err)
	}

	up.freshRoot = filepath.Join(tempDir, plan.ServiceDir)
	changes, next, warnings, err := diffTrees(up.freshRoot, info.Root)
	if err != nil {
		up.cleanup()
		return nil, err
	}
	up.Changes = changes
	up.next = next
	up.Warnings = warnings
	return up, nil
}

// Diff 返回某个文件「将要写入的内容」与当前内容,供调用方打 diff。
// 给用户看的必须和 --write 落盘的是同一份,否则 diff 里没有的改动会悄悄写进去。
func (u *Upgrade) Diff(path string) (next, current []byte, err error) {
	next, ok := u.next[path]
	if !ok {
		return nil, nil, fmt.Errorf("%s is not in the change list", path)
	}
	current, err = os.ReadFile(filepath.Join(u.ServiceRoot, filepath.FromSlash(path)))
	if os.IsNotExist(err) {
		return next, nil, nil
	}
	return next, current, err
}

// Close 删掉临时生成物。
func (u *Upgrade) Close() error { return u.cleanup() }

func (u *Upgrade) cleanup() error {
	if u.tempDir == "" {
		return nil
	}
	err := os.RemoveAll(u.tempDir)
	u.tempDir = ""
	return err
}

// detectLayout 从「go.mod 在哪」反推布局。
//
// standalone 的 go.mod 就在服务目录里,monorepo 的在仓库根 —— 这个差别是
// layouts.go_mod 直接决定的,所以反推的依据和生成时的依据是同一个事实。
func detectLayout(info ServiceInfo, m *manifest.Manifest) (string, error) {
	wantGoMod := info.ModuleRoot == info.Root

	var candidates []string
	for name, l := range m.Layouts {
		if l.GoMod == wantGoMod {
			candidates = append(candidates, name)
		}
	}
	sort.Strings(candidates)

	switch len(candidates) {
	case 1:
		return candidates[0], nil
	case 0:
		return "", fmt.Errorf("no layout in %s matches this service (go.mod %s the service dir)",
			manifest.Path, goModWhere(wantGoMod))
	default:
		return "", fmt.Errorf("cannot tell which layout this service uses; candidates: %s",
			strings.Join(candidates, ", "))
	}
}

func goModWhere(inService bool) string {
	if inService {
		return "is in"
	}
	return "is above"
}

// checkServiceDir 确认 serviceRoot 的形状与该布局的 service_dir 一致。
//
// 以 manifest 为准而不是写死「往上两级」:模板改了 service_dir 之后,
// 写死的推算会悄悄错位,而这里会直接报错。
func checkServiceDir(serviceRoot string, layout manifest.Layout, name string) error {
	rel := strings.TrimSpace(expandName(layout.ServiceDir, name))
	if rel == "" || rel == "." {
		return nil
	}
	relNative := filepath.FromSlash(rel)
	if strings.HasSuffix(serviceRoot, string(filepath.Separator)+relNative) {
		return nil
	}
	return fmt.Errorf("%s does not look like a %q service (expected path to end with %q); "+
		"run this inside the generated service directory", serviceRoot, layout.Name, rel)
}

func expandName(s, name string) string {
	s = strings.ReplaceAll(s, "{{.Name}}", name)
	return strings.ReplaceAll(s, "{{ .Name }}", name)
}

// detectName 反推服务名。
//
// 名字藏在哪由布局决定:service_dir 里带 {{.Name}} 的(monorepo),目录名就是名字;
// 不带的(standalone),目录可以叫任何东西,只有 module 末段还留着生成时的名字。
// 拿目录名硬套会在 standalone 下取到一个随机名字,再被资源命名规则挡下来,
// 报出一条和真实原因毫无关系的错。
func detectName(info ServiceInfo, layout manifest.Layout) string {
	if strings.Contains(layout.ServiceDir, ".Name") {
		return filepath.Base(info.Root)
	}
	return path.Base(info.Module)
}

// detectKeepExample 反推「生成时是否带了 --keep-example」。
//
// 示例资源的源文件(biz/data/service 那三个,不算 hook 产物)还在,就是保留了。
// 只看源文件:.pb.go 之类是 buf 的产物,用户可能没跑过 buf。任一存在即算保留 ——
// 半套示例(用户删了一部分)比对时多几处差异,比整套误判成「没保留」安全。
func detectKeepExample(root string, m *manifest.Manifest) bool {
	for _, f := range m.Example.Files {
		if skipCompare(f) {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(f))); err == nil {
			return true
		}
	}
	return false
}

// enabledFeatures 把反推结果压成排序好的名字列表。
func enabledFeatures(set manifest.FeatureSet) []string {
	var out []string
	for name, on := range set {
		if on {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// diffTrees 以模板新版为基准,逐文件比对现有服务。
//
// 只遍历模板那一侧:服务里多出来的文件(用户自己写的业务代码、生成的 pb.go)
// 不是 upgrade 该管的,遍历它们只会产出一堆噪音。
//
// 返回的 next 是每个差异文件将要写入的内容:模板那份经 gofmt,再把服务里
// 锚点上方的接线搬进来(见 carryAnchors)。比对、--diff、--write 三处用的
// 都是这一份,不会出现「看到的和写进去的不一样」。
func diffTrees(freshRoot, serviceRoot string) ([]Change, map[string][]byte, []string, error) {
	var (
		changes  []Change
		warnings []string
		next     = map[string][]byte{}
	)

	err := filepath.WalkDir(freshRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return fs.SkipDir
			}
			return nil
		}

		rel, rerr := filepath.Rel(freshRoot, path)
		if rerr != nil {
			return rerr
		}
		slashRel := ToSlash(rel)
		if skipCompare(slashRel) {
			return nil
		}

		fresh, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}

		fresh = normalize(slashRel, fresh)

		current, rerr := os.ReadFile(filepath.Join(serviceRoot, rel))
		if os.IsNotExist(rerr) {
			changes = append(changes, Change{Path: slashRel, Kind: ChangeAdded})
			next[slashRel] = fresh
			return nil
		}
		if rerr != nil {
			return rerr
		}
		current = normalize(slashRel, current)

		merged, warns := carryAnchors(slashRel, string(fresh), string(current))
		warnings = append(warnings, warns...)
		// 搬完接线再过一遍 gofmt:插入的位置可能让 import 顺序或对齐变了
		want := normalize(slashRel, []byte(merged))

		if !bytes.Equal(want, current) {
			c := Change{Path: slashRel, Kind: ChangeModified}
			if legacyAnchorFile(slashRel, fresh, current) {
				c.Blocked = "template has +co:anchor here but this file has none (generated before anchors existed); " +
					"its wiring cannot be carried over — add the anchor lines by hand, then rerun"
			}
			changes = append(changes, c)
			next[slashRel] = want
		}
		return nil
	})
	if err != nil {
		return nil, nil, nil, err
	}

	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	return changes, next, warnings, nil
}

// silentReporter 吞掉生成参考副本时的进度输出 —— 那是一次内部操作,
// 用户要看的是比对结果,不是「又生成了一遍」的过程。
type silentReporter struct{}

func (silentReporter) Step(string, ...any) {}
func (silentReporter) Warn(string, ...any) {}
