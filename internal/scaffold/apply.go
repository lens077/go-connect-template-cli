package scaffold

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lens077/co-cli/internal/manifest"
	"github.com/lens077/co-cli/internal/resource"
)

// Reporter 让调用方决定进度怎么显示 —— 引擎不直接 fmt.Print,
// 这样它既能被 CLI 用(带颜色),也能被测试用(丢弃输出)。
type Reporter interface {
	Step(format string, args ...any)
	Warn(format string, args ...any)
}

// DiscardReporter 丢弃所有输出。
type DiscardReporter struct{}

func (DiscardReporter) Step(string, ...any) {}
func (DiscardReporter) Warn(string, ...any) {}

// skipFromTemplate 是从模板拷贝时永远不拷的顶层项。
//
// .git 不拷是显然的;.co 不拷是因为它是 CLI 的输入而不是产物 ——
// 生成出来的服务不是模板,带着一份 manifest 只会让人误以为能对它再跑一次 co new。
var skipFromTemplate = []string{".git", ".co"}

// Apply 执行一份 Plan。
//
// 步骤顺序不是随意的,几处强耦合:
//   - 删除必须在裁剪之前 —— 已经删掉的文件不必再去解析标记
//   - 改名(module / 服务名)必须在裁剪之前 —— 裁剪后的文件更少,替换更快,
//     且 module 路径不会出现在标记里,两者互不干扰
//   - 锚点插入必须在裁剪之后 —— 插进去的代码不带标记,不该被再裁一遍
//   - hook 永远最后 —— buf/sqlc 要看到完整的 proto 与 SQL
func Apply(ctx context.Context, p *Plan, rep Reporter) error {
	if rep == nil {
		rep = DiscardReporter{}
	}

	serviceRoot := filepath.Join(p.Dest, p.ServiceDir)
	if err := ensureEmpty(serviceRoot); err != nil {
		return err
	}

	rep.Step("copying template")
	if err := copyTree(p.Source, serviceRoot, PathSkipper(skipFromTemplate...)); err != nil {
		return fmt.Errorf("copy template: %w", err)
	}

	rep.Step("removing %d unused path(s)", len(p.Deletes))
	if err := deleteAll(serviceRoot, p.Deletes); err != nil {
		return err
	}

	if err := p.applyGoMod(serviceRoot, rep); err != nil {
		return err
	}

	rep.Step("rewriting module path and service name")
	if err := p.applyRenames(serviceRoot); err != nil {
		return err
	}

	rep.Step("pruning +co: markers")
	n, err := PruneTree(serviceRoot, p.Features, PathSkipper(skipFromTemplate...))
	if err != nil {
		return err
	}
	rep.Step("pruned %d file(s)", n)

	if len(p.Overlays) > 0 {
		rep.Step("applying %s overlay (%d file(s))", p.Layout.Name, len(p.Overlays))
		if err := p.applyOverlays(serviceRoot); err != nil {
			return err
		}
	}

	if p.Resource != nil {
		rep.Step("generating resource %q", p.Resource.Name)
		if err := p.applyResource(serviceRoot); err != nil {
			return err
		}
	}

	if len(p.Inserts) > 0 {
		rep.Step("wiring %d provider(s) at anchors", len(p.Inserts))
		if err := p.applyInserts(serviceRoot); err != nil {
			return err
		}
	}

	// proto 要搬到服务目录之外(monorepo),放在最后:
	// 之前所有步骤都按「一切都在 serviceRoot 下」处理,搬走之后路径就不成立了
	if err := p.relocateProto(serviceRoot, rep); err != nil {
		return err
	}

	p.runHooks(ctx, serviceRoot, rep)
	return nil
}

// ensureEmpty 保证目标目录不存在或为空。
//
// 往非空目录里生成是不可逆的:模板里的文件会覆盖同名文件,而已存在的
// 其它文件留在原地,最后得到一个两边混在一起、谁也说不清的目录。
func ensureEmpty(dir string) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return os.MkdirAll(dir, 0o755)
	}
	if err != nil {
		return err
	}
	if len(entries) > 0 {
		return fmt.Errorf("%s already exists and is not empty", dir)
	}
	return nil
}

func (p *Plan) applyGoMod(root string, rep Reporter) error {
	goMod := filepath.Join(root, "go.mod")

	if !p.Layout.GoMod {
		// go.mod 通常已经在 layout.drop 里被删了,这里兜一手:
		// monorepo 下留着服务自己的 go.mod,go build ./... 在仓库根会直接忽略
		// 这个子目录,症状是「代码明明在,却没被编译」,很难往这上面想
		for _, f := range []string{"go.mod", "go.sum"} {
			if err := os.RemoveAll(filepath.Join(root, f)); err != nil {
				return err
			}
		}
		return nil
	}

	if _, err := os.Stat(goMod); err != nil {
		rep.Warn("template has no go.mod; skipping module rewrite")
		return nil
	}
	rep.Step("go.mod: module %s, dropping %d require(s)", p.Opts.Module, len(p.DropRequires))
	return RewriteGoMod(goMod, p.Opts.Module, p.DropRequires)
}

func (p *Plan) applyRenames(root string) error {
	// api/ 那棵树的导入前缀始终是 <Module>/api,而不是 <ServiceModule>/api ——
	// monorepo 下 proto 会被搬出服务目录(relocateProto),导入路径跟着仓库根走。
	// 资源模板里的 go_package 也是这么拼的({{.Module}}/{{.APIDir}}),两边必须一致。
	//
	// 顺序有意义:这条先跑,把模板里 api/ 的导入吃掉,下面那条通用规则就碰不到它们了。
	// 漏掉这条的症状是 monorepo 下模板自带的 proto(--keep-example 的
	// api/search)导入 <ServiceModule>/api/search/v1,而文件实际在 <Module>/api/search/v1,
	// go build 报 "no required module provides package"。
	// standalone 下 Module == ServiceModule,这条等价于通用规则,无副作用。
	reps := []Replacement{
		{Old: p.Manifest.Module + "/api", New: p.Opts.Module + "/api"},
		{Old: p.Manifest.Module, New: p.ServiceModule},
	}
	if ph := p.Manifest.Placeholders.ServiceName; ph != "" {
		reps = append(reps, Replacement{Old: ph, New: p.Data().ServiceName})
	}

	// go.mod 已经用 modfile 精确改过了,再做一次文本替换会把 require 块里
	// 恰好同前缀的依赖也改掉
	skip := PathSkipper(append(append([]string{}, skipFromTemplate...), "go.mod", "go.sum")...)
	_, err := ReplaceInTree(root, reps, skip)
	return err
}

func (p *Plan) applyOverlays(root string) error {
	data := p.Data()
	for _, o := range p.Overlays {
		dest := filepath.Join(root, o.Dest)
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}

		var body []byte
		var err error
		if strings.HasSuffix(o.Src, ".tmpl") {
			body, err = resource.RenderOverlay(o.Src, data)
		} else {
			body, err = os.ReadFile(o.Src)
		}
		if err != nil {
			return fmt.Errorf("overlay %s: %w", o.Dest, err)
		}

		// overlay 里的文件也可能带标记(比如 Makefile 里的 consul 目标),
		// 主体裁剪已经跑完了,所以这里要单独再裁一次
		pruned, _, err := Prune(o.Dest, string(body), p.Features)
		if err != nil {
			return fmt.Errorf("overlay %s: %w", o.Dest, err)
		}
		if err := os.WriteFile(dest, []byte(pruned), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func (p *Plan) applyResource(root string) error {
	files, err := resource.Render(filepath.Join(p.Source, manifest.ScaffoldDir), *p.Resource)
	if err != nil {
		return err
	}
	for _, f := range files {
		dest := filepath.Join(root, f.Path)
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dest, f.Content, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// applyInserts 把 provider 一条条插进锚点。
//
// 按文件分组一次性读写,而不是每条插入都读一遍写一遍:同一个文件里有多个锚点
// (server.go 就有三个),逐条读写不但慢,中途出错还会留下改了一半的文件。
func (p *Plan) applyInserts(root string) error {
	byFile := map[string][]manifest.Insert{}
	for _, ins := range p.Inserts {
		rel, ok := p.Manifest.Anchors[ins.Anchor]
		if !ok {
			return fmt.Errorf("insert refers to unknown anchor %q", ins.Anchor)
		}
		byFile[rel] = append(byFile[rel], ins)
	}

	files := make([]string, 0, len(byFile))
	for f := range byFile {
		files = append(files, f)
	}
	sort.Strings(files)

	for _, rel := range files {
		p9 := filepath.Join(root, filepath.FromSlash(rel))
		data, err := os.ReadFile(p9)
		if os.IsNotExist(err) {
			// 锚点所在文件被裁掉了(比如整个 feature 关掉),对应的插入自然也不需要
			continue
		}
		if err != nil {
			return err
		}

		content := string(data)
		for _, ins := range byFile[rel] {
			content, err = InsertAtAnchor(rel, content, ins.Anchor, ins.Text)
			if err != nil {
				return err
			}
		}
		if err := os.WriteFile(p9, []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// relocateProto 把 api/ 整棵树搬到布局指定的位置。
// standalone 下两个路径相同,直接返回。
func (p *Plan) relocateProto(serviceRoot string, rep Reporter) error {
	from := filepath.Join(serviceRoot, "api")
	to := filepath.Join(p.Dest, p.ProtoDir)
	if from == to {
		return nil
	}
	if _, err := os.Stat(from); os.IsNotExist(err) {
		return nil
	}

	rep.Step("moving api/ to %s", ToSlash(p.ProtoDir))
	if err := os.MkdirAll(to, 0o755); err != nil {
		return err
	}
	shared := map[string]bool{}
	for _, name := range p.Layout.SharedProto {
		shared[name] = true
	}

	// 逐个子目录搬,而不是整体 rename:目标目录可能已经有别的服务的 proto,
	// 直接 rename 会失败(非空)或覆盖(不同平台行为还不一样)
	entries, err := os.ReadDir(from)
	if err != nil {
		return err
	}
	for _, e := range entries {
		dst := filepath.Join(to, e.Name())
		if _, err := os.Stat(dst); err == nil {
			if !shared[e.Name()] {
				return fmt.Errorf("%s already exists; refusing to overwrite another service's proto", dst)
			}
			// 共用子树已经在仓库里了。以仓库里那份为准:它可能比模板新,
			// 覆盖回去等于把整个仓库的契约降级。
			rep.Step("keeping existing %s (shared)", ToSlash(filepath.Join(p.ProtoDir, e.Name())))
			continue
		}
		if err := os.Rename(filepath.Join(from, e.Name()), dst); err != nil {
			return err
		}
	}
	return os.RemoveAll(from)
}

// runHooks 跑生成后的命令。失败只告警不中断 ——
// 用户已经拿到骨架了,缺 buf/sqlc 是可以事后补的,把已生成的目录判为失败没有意义。
func (p *Plan) runHooks(ctx context.Context, root string, rep Reporter) {
	for _, h := range p.Hooks {
		if _, err := exec.LookPath(h.Cmd[0]); err != nil {
			rep.Warn("%s not found in PATH; skipping `%s` (run it yourself later)",
				h.Cmd[0], strings.Join(h.Cmd, " "))
			continue
		}
		rep.Step("$ %s", strings.Join(h.Cmd, " "))

		cmd := exec.CommandContext(ctx, h.Cmd[0], h.Cmd[1:]...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			rep.Warn("`%s` failed: %v\n%s", strings.Join(h.Cmd, " "), err, strings.TrimSpace(string(out)))
		}
	}
}

func deleteAll(root string, rels []string) error {
	for _, rel := range rels {
		target := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.RemoveAll(target); err != nil {
			return fmt.Errorf("delete %s: %w", rel, err)
		}
		// 删完文件要把空掉的父目录一起收走。manifest 里列的是文件
		// (api/search/v1/search.proto 等),四个文件删干净后 api/search/v1/
		// 会剩成空壳;proto 搬家那一步按子目录搬,空壳会被原样搬到
		// backend/api/search,看着像是漏删了示例资源。
		if err := pruneEmptyDirs(root, filepath.Dir(target)); err != nil {
			return err
		}
	}
	return nil
}

// pruneEmptyDirs 从 dir 起向上删空目录,到 root 为止(不含 root)。
func pruneEmptyDirs(root, dir string) error {
	for {
		if dir == root || !strings.HasPrefix(dir, root+string(filepath.Separator)) {
			return nil
		}
		entries, err := os.ReadDir(dir)
		if os.IsNotExist(err) {
			// 父目录本身已经被前一次 RemoveAll 带走了,继续往上看
			dir = filepath.Dir(dir)
			continue
		}
		if err != nil {
			return err
		}
		if len(entries) > 0 {
			return nil
		}
		if err := os.Remove(dir); err != nil {
			return err
		}
		dir = filepath.Dir(dir)
	}
}

// PruneTree 对目录下所有可识别注释语法的文件做标记裁剪,返回被改动的文件数。
func PruneTree(root string, set manifest.FeatureSet, skip func(rel string) bool) (int, error) {
	var changed int

	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return rerr
		}
		if d.IsDir() {
			if skip != nil && rel != "." && skip(rel) {
				return filepath.SkipDir
			}
			return nil
		}
		if (skip != nil && skip(rel)) || commentPrefixFor(rel) == "" {
			return nil
		}

		data, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		if !isText(data) {
			return nil
		}

		out, modified, perr := Prune(rel, string(data), set)
		if perr != nil {
			return perr
		}
		if !modified {
			return nil
		}

		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		if werr := os.WriteFile(p, []byte(out), info.Mode().Perm()); werr != nil {
			return werr
		}
		changed++
		return nil
	})

	return changed, err
}

func copyTree(src, dst string, skip func(rel string) bool) error {
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(src, p)
		if rerr != nil {
			return rerr
		}
		if rel == "." {
			return os.MkdirAll(dst, 0o755)
		}
		if skip != nil && skip(rel) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		target := filepath.Join(dst, rel)
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}

		if d.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm()|0o700)
		}
		// 符号链接原样重建。跟着解引用会把链接变成文件副本,
		// 模板里若有指向仓库外的链接,还会把外面的内容抄进来
		if info.Mode()&os.ModeSymlink != 0 {
			link, lerr := os.Readlink(p)
			if lerr != nil {
				return lerr
			}
			return os.Symlink(link, target)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		return copyFile(p, target, info.Mode().Perm())
	})
}

func copyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
