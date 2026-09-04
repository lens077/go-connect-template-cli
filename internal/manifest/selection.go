package manifest

import (
	"fmt"
	"sort"
	"strings"
)

// FeatureSet 是一次生成最终启用的 feature 集合。
// 它是整个引擎的输入:裁哪些文件、删哪些 require、模板里 {{if .Features.minio}}
// 走哪一支,全都由它决定。
type FeatureSet map[string]bool

// Has 判断 feature 是否启用。
func (s FeatureSet) Has(name string) bool { return s[name] }

// HasAll 判断一组 feature 是否全部启用。行标记的逗号语义就是「与」。
func (s FeatureSet) HasAll(names []string) bool {
	for _, n := range names {
		if !s[n] {
			return false
		}
	}
	return true
}

// HasAny 判断一组 feature 是否至少启用一个。空集合返回 false。
func (s FeatureSet) HasAny(names []string) bool {
	for _, n := range names {
		if s[n] {
			return true
		}
	}
	return false
}

// Names 返回排序后的已启用 feature 名。
func (s FeatureSet) Names() []string {
	out := make([]string, 0, len(s))
	for n, on := range s {
		if on {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// Resolve 把「用户选了哪些 feature」变成最终集合:
// 补上 always 项、递归展开 needs、再校验分组约束。
//
// 展开 needs 放在校验之前:用户选了 config-consul 却没选 consul 时,
// 正确的行为是自动把 consul 带上,而不是报错让他自己去猜依赖关系。
func (m *Manifest) Resolve(selected []string) (FeatureSet, error) {
	set := FeatureSet{}

	for _, f := range m.SortedFeatures() {
		if f.Always {
			set[f.Name] = true
		}
	}

	for _, name := range selected {
		if name == "" || name == NoneChoice {
			continue
		}
		if _, ok := m.Features[name]; !ok {
			return nil, fmt.Errorf("unknown feature %q (available: %s)",
				name, strings.Join(m.FeatureNames(), ", "))
		}
		set[name] = true
	}

	if err := m.expandNeeds(set); err != nil {
		return nil, err
	}
	if err := m.checkGroups(set); err != nil {
		return nil, err
	}
	m.Complete(set)
	return set, nil
}

// Complete 给每个声明过的 feature 都填上显式的 true/false。
//
// 缺的 key 与 false 在 Has() 眼里没区别,但在 text/template 眼里有:
// 模板用 missingkey=error 跑(为的是拦住 {{.Pascl}} 这种拼写错误),
// 于是 {{if .Features.minio}} 在没选 minio 时会直接报
// "map has no entry for key",而不是走 else 分支。
func (m *Manifest) Complete(set FeatureSet) {
	for name := range m.Features {
		if _, ok := set[name]; !ok {
			set[name] = false
		}
	}
}

// expandNeeds 反复展开直到不动点。needs 可以链式依赖(a 需要 b、b 需要 c),
// 一趟遍历补不全,所以循环到没有新增为止。
// 上限按 feature 总数计:needs 成环时到不了不动点,这里保证一定会退出并报错。
func (m *Manifest) expandNeeds(set FeatureSet) error {
	for round := 0; round <= len(m.Features); round++ {
		changed := false
		for _, name := range set.Names() {
			for _, need := range m.Features[name].Needs {
				if !set[need] {
					set[need] = true
					changed = true
				}
			}
		}
		if !changed {
			return nil
		}
	}
	return fmt.Errorf("feature needs form a cycle; check the needs: field in %s", Path)
}

func (m *Manifest) checkGroups(set FeatureSet) error {
	for _, g := range m.SortedGroups() {
		var on []string
		for _, member := range g.Members {
			if set[member] {
				on = append(on, member)
			}
		}
		if g.Required && len(on) == 0 {
			return fmt.Errorf("group %q requires one of: %s", g.Name, strings.Join(g.Members, ", "))
		}
		if g.Exclusive && len(on) > 1 {
			return fmt.Errorf("group %q is exclusive but %s were selected", g.Name, strings.Join(on, ", "))
		}
	}
	return nil
}

// NoneChoice 是交互表单和 flag 里表示「这一组都不要」的取值。
const NoneChoice = "none"

// FeatureNames 返回排序后的所有 feature 名。
func (m *Manifest) FeatureNames() []string {
	out := make([]string, 0, len(m.Features))
	for n := range m.Features {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// DefaultsFor 返回某个分组里默认开启的成员。
func (m *Manifest) DefaultsFor(group Group) []string {
	var out []string
	for _, member := range group.Members {
		if m.Features[member].Default {
			out = append(out, member)
		}
	}
	return out
}

// DroppedRequires 返回所有未启用 feature 声明的直接依赖。
//
// 关键在于「减去启用项声明的依赖」:两个 feature 可能共用一个库
// (consul 注册中心与 config-consul 都用 hashicorp/consul/api),
// 只关掉其中一个就把共用的库删掉,剩下那个立刻编译不过。
func (m *Manifest) DroppedRequires(set FeatureSet) []string {
	keep := map[string]bool{}
	for _, f := range m.SortedFeatures() {
		if set[f.Name] {
			for _, req := range f.Requires {
				keep[req] = true
			}
		}
	}

	seen := map[string]bool{}
	var out []string
	for _, f := range m.SortedFeatures() {
		if set[f.Name] {
			continue
		}
		for _, req := range f.Requires {
			if keep[req] || seen[req] {
				continue
			}
			seen[req] = true
			out = append(out, req)
		}
	}
	sort.Strings(out)
	return out
}

// DroppedFiles 返回所有未启用 feature 声明的文件,同样要减去启用项还需要的文件。
func (m *Manifest) DroppedFiles(set FeatureSet) []string {
	keep := map[string]bool{}
	for _, f := range m.SortedFeatures() {
		if set[f.Name] {
			for _, file := range f.Files {
				keep[file] = true
			}
		}
	}

	seen := map[string]bool{}
	var out []string
	for _, f := range m.SortedFeatures() {
		if set[f.Name] {
			continue
		}
		for _, file := range f.Files {
			if keep[file] || seen[file] {
				continue
			}
			seen[file] = true
			out = append(out, file)
		}
	}
	sort.Strings(out)
	return out
}
