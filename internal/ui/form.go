package ui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/huh"

	"github.com/lens077/co-cli/internal/manifest"
)

// Answers 是表单收上来的结果。
type Answers struct {
	Module   string
	Layout   string
	Features []string
}

// AskOptions 是弹表单前已经由命令行确定下来的部分。
// 已给出的项不再问 —— 表单只补缺口,不复述用户已经说过的话。
type AskOptions struct {
	Name   string
	Module string
	Layout string
	// Preset 是已经由 flag 指定的分组:group -> 选中的成员(可能是 none)。
	Preset map[string][]string
}

// Ask 弹出交互表单,补齐 module、layout 与各分组的 feature 选择。
func Ask(m *manifest.Manifest, opts AskOptions) (Answers, error) {
	ans := Answers{Module: opts.Module, Layout: opts.Layout}

	var groups []*huh.Group

	if ans.Module == "" {
		// 默认值给一个明显是占位的路径,而不是 github.com/you/xxx 之类
		// 看起来像真的的东西 —— 用户很容易直接回车放过去
		ans.Module = "example.com/" + opts.Name
		groups = append(groups, huh.NewGroup(
			huh.NewInput().
				Title("Go module path").
				Description("生成项目的 module,如 github.com/acme/"+opts.Name).
				Value(&ans.Module).
				Validate(func(s string) error {
					if strings.TrimSpace(s) == "" {
						return fmt.Errorf("module path is required")
					}
					return nil
				}),
		))
	}

	if ans.Layout == "" {
		layoutOpts := make([]huh.Option[string], 0, len(m.Layouts))
		for _, l := range m.SortedLayouts() {
			layoutOpts = append(layoutOpts, huh.NewOption(fmt.Sprintf("%s — %s", l.Name, l.Title), l.Name))
		}
		ans.Layout = "standalone"
		groups = append(groups, huh.NewGroup(
			huh.NewSelect[string]().Title("目录布局").Options(layoutOpts...).Value(&ans.Layout),
		))
	}

	// 每个分组一个问题。互斥组用单选(带 none),非互斥组用多选。
	// 指针切片是因为 huh 要在表单跑完后往这些变量里写回结果。
	singles := map[string]*string{}
	multis := map[string]*[]string{}

	for _, g := range sortedGroups(m) {
		if _, ok := opts.Preset[g.Name]; ok {
			continue
		}

		if g.Exclusive {
			choices := make([]huh.Option[string], 0, len(g.Members)+1)
			for _, member := range g.Members {
				f := m.Features[member]
				choices = append(choices, huh.NewOption(labelFor(f), member))
			}
			if !g.Required {
				choices = append(choices, huh.NewOption("none — 不启用", manifest.NoneChoice))
			}

			def := manifest.NoneChoice
			if d := m.DefaultsFor(g); len(d) > 0 {
				def = d[0]
			}
			v := def
			singles[g.Name] = &v
			groups = append(groups, huh.NewGroup(
				huh.NewSelect[string]().Title(g.Title).Options(choices...).Value(&v),
			))
			continue
		}

		choices := make([]huh.Option[string], 0, len(g.Members))
		for _, member := range g.Members {
			f := m.Features[member]
			// always 项不给用户选:它不可关闭,列出来只会让人以为能取消
			if f.Always {
				continue
			}
			choices = append(choices, huh.NewOption(labelFor(f), member).Selected(f.Default))
		}
		if len(choices) == 0 {
			continue
		}

		var v []string
		multis[g.Name] = &v
		groups = append(groups, huh.NewGroup(
			huh.NewMultiSelect[string]().Title(g.Title).Options(choices...).Value(&v),
		))
	}

	if len(groups) > 0 {
		if err := huh.NewForm(groups...).Run(); err != nil {
			return Answers{}, err
		}
	}

	// 收集:先放 flag 已定的,再放表单选的
	seen := map[string]bool{}
	add := func(names ...string) {
		for _, n := range names {
			if n == "" || n == manifest.NoneChoice || seen[n] {
				continue
			}
			seen[n] = true
			ans.Features = append(ans.Features, n)
		}
	}
	for _, g := range sortedGroups(m) {
		add(opts.Preset[g.Name]...)
		if v, ok := singles[g.Name]; ok {
			add(*v)
		}
		if v, ok := multis[g.Name]; ok {
			add(*v...)
		}
	}
	sort.Strings(ans.Features)

	return ans, nil
}

func labelFor(f manifest.Feature) string {
	if f.Title == "" {
		return f.Name
	}
	return fmt.Sprintf("%s — %s", f.Name, f.Title)
}

// sortedGroups 按 order 再按名字排序,让表单的提问顺序稳定且符合直觉
// (先问数据库,再问缓存/检索这些可选项)。
func sortedGroups(m *manifest.Manifest) []manifest.Group {
	gs := m.SortedGroups()
	sort.SliceStable(gs, func(i, j int) bool { return gs[i].Order < gs[j].Order })
	return gs
}
