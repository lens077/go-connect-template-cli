// Package manifest 解析模板仓库里的 .co/manifest.yaml。
//
// manifest 是模板与 CLI 之间唯一的契约:模板负责声明「哪个 feature 由哪些文件
// 和哪些依赖构成」,CLI 只负责按声明做减法。CLI 里不写死任何文件路径 ——
// 模板挪一个文件,改 manifest 即可,不必跟着发一版 CLI。
package manifest

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Path 是 manifest 在模板仓库里的相对位置。
const Path = ".co/manifest.yaml"

// RootDir 是模板仓库里存放 co 自己那套输入的目录。
// 以 . 开头是有意的:Go 工具链忽略这样的目录,模板照常 go build ./...。
// manifest 里所有相对路径(overlay 等)都以它为基准。
const RootDir = ".co"

// ScaffoldDir 是模板目录在模板仓库里的相对位置。
const ScaffoldDir = ".co/scaffold"

// Manifest 对应 .co/manifest.yaml 的全文。
type Manifest struct {
	Version      int                `yaml:"version"`
	Module       string             `yaml:"module"`
	Placeholders Placeholders       `yaml:"placeholders"`
	Anchors      map[string]string  `yaml:"anchors"`
	Example      Example            `yaml:"example"`
	Features     map[string]Feature `yaml:"features"`
	Groups       map[string]Group   `yaml:"groups"`
	Layouts      map[string]Layout  `yaml:"layouts"`
	Hooks        Hooks              `yaml:"hooks"`
	Tools        []Tool             `yaml:"tools"`
}

// Placeholders 是模板里需要按目标服务替换掉的字面量。
type Placeholders struct {
	ServiceName string `yaml:"service_name"`
}

// Example 描述模板自带的示例资源。co new 默认把它整套删掉。
type Example struct {
	Name  string   `yaml:"name"`
	Needs []string `yaml:"needs"`
	Files []string `yaml:"files"`
	// Keep 是「属于示例但不能删」的文件。典型是 sqlc 生成的 models/db.go:
	// 它只含 DBTX 接口,data 层的公共代码依赖它。
	Keep []string `yaml:"keep"`
}

// Feature 是一个可选能力。
type Feature struct {
	// Name 由 Load 从 map key 回填,便于把 Feature 单独传来传去。
	Name string `yaml:"-"`

	Title string `yaml:"title"`
	Group string `yaml:"group"`
	// Default 决定交互式表单里的预选状态。
	Default bool `yaml:"default"`
	// Always 表示不可关闭。config-file 是这样:它零依赖,留着才能保证
	// 生成出来的项目不接任何外部组件也能起来。
	Always bool `yaml:"always"`
	// Needs 是前置 feature,选中本项时自动带上。
	Needs []string `yaml:"needs"`
	// Files 在本 feature 未启用时删除。路径不存在不算错误 —— 模板可能
	// 已经删掉了某个文件,但 manifest 还没跟上,不该让 co new 整个失败。
	Files []string `yaml:"files"`
	// Requires 是未启用时从 go.mod 里 DropRequire 的直接依赖。
	// 间接依赖交给 go mod tidy,不在这里枚举。
	Requires []string `yaml:"requires"`
}

// Group 是一组互斥或并列的 feature,对应交互表单里的一个问题。
type Group struct {
	Name string `yaml:"-"`

	Title string `yaml:"title"`
	// Order 决定交互表单里的提问顺序,小的在前。
	// 不按名字排:字典序会把「配置数据源」排到「数据库」前面,
	// 而实际上应该先定最基础的存储,再问周边。
	Order int `yaml:"order"`
	// Required 表示必须选一个,不允许 none。
	Required bool `yaml:"required"`
	// Exclusive 表示组内最多选一个(单选),否则是多选。
	Exclusive bool     `yaml:"exclusive"`
	Members   []string `yaml:"members"`
}

// Layout 是目录布局:独立仓库还是 monorepo。
type Layout struct {
	Name string `yaml:"-"`

	Title string `yaml:"title"`
	// ServiceDir 是服务在目标根目录下的位置,支持 {{.Name}}。
	ServiceDir string `yaml:"service_dir"`
	// ProtoDir 是 api/ 整棵树的落点:模板里 api/<x>/... 搬到 <proto_dir>/<x>/...。
	// standalone 下它就是 api,等于原地不动。
	ProtoDir string `yaml:"proto_dir"`
	// ServiceModule 是服务包的导入前缀,支持 {{.Module}} 与 {{.Name}}。
	ServiceModule string `yaml:"service_module"`
	// GoMod 为 false 时不生成 go.mod(monorepo 用根 module)。
	GoMod bool `yaml:"go_mod"`
	// Features 是本布局强制启用的 feature,与用户的选择取并集。
	// 用于「只有这个布局才成立的能力」:configcenter 数据源要求同仓库跑着
	// config-service,独立仓库里给用户这个选项只会让他选一个连不上的源。
	Features []string `yaml:"features"`
	// Drop 是该布局下要删掉的文件/目录(由根仓库统一提供的那些)。
	Drop []string `yaml:"drop"`
	// SharedProto 是 api/ 下由整个仓库共用、而非本服务独有的子树名。
	// 搬 proto 时若目标已存在,保留目标那份而不是报冲突 —— 同一个 monorepo 里
	// 生成第二个服务时,这些子树必然已经被第一个服务带进去了。
	SharedProto []string `yaml:"shared_proto"`
	// Overlay 是覆盖文件所在目录,相对模板仓库根。目录结构即目标路径。
	Overlay string `yaml:"overlay"`
	// Inserts 是按锚点插入的代码片段。
	Inserts []Insert `yaml:"inserts"`
}

// Insert 是一段按锚点插进已有文件的代码。
type Insert struct {
	Anchor string `yaml:"anchor"`
	Text   string `yaml:"text"`
}

// Hooks 是生成完成后要跑的外部命令。
type Hooks struct {
	PostGenerate []Hook `yaml:"post_generate"`
}

// Hook 单条命令。缺工具时跳过并告警,不让 co new 失败 ——
// 用户可以先拿到骨架,再按 co doctor 的提示补齐工具重跑。
type Hook struct {
	Name string   `yaml:"name"`
	Cmd  []string `yaml:"cmd"`
	// When 非空时,仅在该 feature 启用时执行。
	When string `yaml:"when"`
	// WhenLayout 非空时,仅在该布局下执行。
	WhenLayout string `yaml:"when_layout"`
}

// Tool 是 co doctor 检查的外部可执行文件。
type Tool struct {
	Name     string `yaml:"name"`
	Required bool   `yaml:"required"`
	Hint     string `yaml:"hint"`
}

// Load 从模板仓库根目录读取并校验 manifest。
func Load(repoRoot string) (*Manifest, error) {
	p := filepath.Join(repoRoot, Path)
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}

	var m Manifest
	// KnownFields 打开:manifest 里写错一个字段名(features 下拼成 require)
	// 默认会被静默忽略,结果是该依赖没被删掉,而现场看不出任何异常。
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", Path, err)
	}

	// 回填 map key,让 Feature/Group/Layout 能脱离所属 map 单独使用
	for name, f := range m.Features {
		f.Name = name
		m.Features[name] = f
	}
	for name, g := range m.Groups {
		g.Name = name
		m.Groups[name] = g
	}
	for name, l := range m.Layouts {
		l.Name = name
		m.Layouts[name] = l
	}

	if err := m.validate(); err != nil {
		return nil, fmt.Errorf("invalid %s: %w", Path, err)
	}
	return &m, nil
}

// SupportedVersion 是本 CLI 能处理的 manifest 版本。
const SupportedVersion = 1

func (m *Manifest) validate() error {
	if m.Version != SupportedVersion {
		return fmt.Errorf("version %d not supported by this co (expect %d); upgrade co or pin an older template ref",
			m.Version, SupportedVersion)
	}
	if m.Module == "" {
		return fmt.Errorf("module is required")
	}
	if len(m.Layouts) == 0 {
		return fmt.Errorf("at least one layout is required")
	}
	for _, l := range m.SortedLayouts() {
		if l.ServiceDir == "" || l.ProtoDir == "" || l.ServiceModule == "" {
			return fmt.Errorf("layout %q: service_dir, proto_dir and service_module are all required", l.Name)
		}
		// 写错名字的话这个 feature 会静默地没被启用,而它对应的文件早就被当作
		// 「未启用」删掉了 —— 症状是生成物少一整块功能,查起来毫无线索
		for _, f := range l.Features {
			if _, ok := m.Features[f]; !ok {
				return fmt.Errorf("layout %q: features refers to unknown feature %q", l.Name, f)
			}
		}
	}

	// 每个 feature 的 group / needs 必须指向真实存在的东西,
	// 否则表单会渲染出一个空组,或者 needs 静默失效
	for _, f := range m.SortedFeatures() {
		if f.Group == "" {
			return fmt.Errorf("feature %q: group is required", f.Name)
		}
		if _, ok := m.Groups[f.Group]; !ok {
			return fmt.Errorf("feature %q: unknown group %q", f.Name, f.Group)
		}
		for _, need := range f.Needs {
			if _, ok := m.Features[need]; !ok {
				return fmt.Errorf("feature %q: needs unknown feature %q", f.Name, need)
			}
		}
	}

	for _, g := range m.SortedGroups() {
		if len(g.Members) == 0 {
			return fmt.Errorf("group %q: members is empty", g.Name)
		}
		for _, member := range g.Members {
			f, ok := m.Features[member]
			if !ok {
				return fmt.Errorf("group %q: unknown member %q", g.Name, member)
			}
			if f.Group != g.Name {
				return fmt.Errorf("group %q: member %q declares group %q", g.Name, member, f.Group)
			}
		}
	}

	// 锚点插入指向不存在的锚点时,插入会静默丢失 —— 生成的项目能编译,
	// 但少了一个 case 分支,只有运行到那条路径才炸。这里提前拦住。
	for _, l := range m.SortedLayouts() {
		for _, ins := range l.Inserts {
			if _, ok := m.Anchors[ins.Anchor]; !ok {
				return fmt.Errorf("layout %q: insert refers to unknown anchor %q", l.Name, ins.Anchor)
			}
		}
	}

	for _, h := range m.Hooks.PostGenerate {
		if len(h.Cmd) == 0 {
			return fmt.Errorf("hook %q: cmd is empty", h.Name)
		}
		if h.When != "" {
			if _, ok := m.Features[h.When]; !ok {
				return fmt.Errorf("hook %q: when refers to unknown feature %q", h.Name, h.When)
			}
		}
		if h.WhenLayout != "" {
			if _, ok := m.Layouts[h.WhenLayout]; !ok {
				return fmt.Errorf("hook %q: when_layout refers to unknown layout %q", h.Name, h.WhenLayout)
			}
		}
	}

	return nil
}

// SortedFeatures 按名字排序返回所有 feature。
// 遍历 map 的顺序是随机的,而 co --dry-run 的输出、生成的文件内容都必须可复现,
// 否则两次同样的调用会给出不同的 diff。
func (m *Manifest) SortedFeatures() []Feature {
	names := make([]string, 0, len(m.Features))
	for n := range m.Features {
		names = append(names, n)
	}
	sort.Strings(names)

	out := make([]Feature, 0, len(names))
	for _, n := range names {
		out = append(out, m.Features[n])
	}
	return out
}

// SortedGroups 按名字排序返回所有分组。
func (m *Manifest) SortedGroups() []Group {
	names := make([]string, 0, len(m.Groups))
	for n := range m.Groups {
		names = append(names, n)
	}
	sort.Strings(names)

	out := make([]Group, 0, len(names))
	for _, n := range names {
		out = append(out, m.Groups[n])
	}
	return out
}

// SortedLayouts 按名字排序返回所有布局。
func (m *Manifest) SortedLayouts() []Layout {
	names := make([]string, 0, len(m.Layouts))
	for n := range m.Layouts {
		names = append(names, n)
	}
	sort.Strings(names)

	out := make([]Layout, 0, len(names))
	for _, n := range names {
		out = append(out, m.Layouts[n])
	}
	return out
}

// LayoutNames 返回排序后的布局名,用于 flag 的错误提示。
func (m *Manifest) LayoutNames() []string {
	out := make([]string, 0, len(m.Layouts))
	for n := range m.Layouts {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
