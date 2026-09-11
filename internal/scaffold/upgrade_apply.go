package scaffold

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// --write 的安全边界。
//
// 「git 工作区干净」只保证写坏了能撤销,保证不了「这个文件完全属于模板」:
// 已经 commit 的业务定制、锚点机制之前生成的 legacy 接线,git status 都看不见。
// 所以写入按风险分层,而不是一把梭:
//
//   - added:模板新增、服务里没有,不覆盖任何东西 —— 默认就写
//   - modified:两边都有但不同,可能是模板演进也可能是用户改的 —— 要显式要求
//   - blocked:legacy 锚点文件,盖上去会静默抹掉接线 —— 永远不自动写,
//     --only 点名也不写;修法是先给文件补上 +co:anchor,让 carryAnchors 能工作
//
// 「added 不覆盖任何东西」对 Go 文件还差一层:同一个包里的文件是一个编译单元。
// 模板把 data.go 拆成 data.go + cache_redis.go 时,新文件是 added,而旧 data.go
// 里还留着同名的 NewRedisClient —— 只写 added 会得到 "redeclared in this block"。
// 所以同包里有 blocked 的 Go 文件跟着 blocked,有未选中的 modified 的跟着跳过。
//
// --allow-dirty 只跳过 git 检查,不解锁任何一层。

// WritePolicy 决定 Apply 写哪些文件。
type WritePolicy struct {
	// Modified 为 true 时连 modified 一起写;默认只写 added。
	Modified bool
	// Only 非空时只写这些路径(相对服务根,正斜杠)。点名的 modified 视为已显式同意,
	// 不再要求 Modified;点名的 blocked 仍然拒绝。
	Only []string
}

// Selection 是按策略挑出来的写入清单,打给用户看再执行。
type Selection struct {
	// Write 将要写入的文件(已排序)
	Write []Change
	// Skipped 因策略跳过的:没开 Modified 也没被 --only 点名的 modified,
	// 以及同包里有这类文件的 added .go(见 Select)。跳过原因写在 Change.Blocked 里。
	Skipped []Change
	// Blocked 拒绝写入的文件,不论策略
	Blocked []Change
}

// Select 按策略把 Changes 分成写 / 跳过 / 拒绝三类。
//
// --only 点了不存在的路径直接报错:多半是路径拼错了,静默忽略等于让用户
// 以为写了其实没写。
func (u *Upgrade) Select(policy WritePolicy) (Selection, error) {
	only := map[string]bool{}
	for _, p := range policy.Only {
		only[strings.TrimPrefix(ToSlash(p), "./")] = true
	}
	if len(only) > 0 {
		known := map[string]bool{}
		for _, c := range u.Changes {
			known[c.Path] = true
		}
		var missing []string
		for p := range only {
			if !known[p] {
				missing = append(missing, p)
			}
		}
		if len(missing) > 0 {
			sort.Strings(missing)
			return Selection{}, fmt.Errorf("--only: not in the change list: %s", strings.Join(missing, ", "))
		}
	}

	// 第一遍:按策略定每个文件的去向
	const (
		write = iota
		skip
		block
	)
	fate := make(map[string]int, len(u.Changes))
	for _, c := range u.Changes {
		switch {
		case c.Blocked != "":
			fate[c.Path] = block
		case len(only) > 0:
			if only[c.Path] {
				fate[c.Path] = write
			} else {
				fate[c.Path] = skip
			}
		case c.Kind == ChangeAdded || policy.Modified:
			fate[c.Path] = write
		default:
			fate[c.Path] = skip
		}
	}

	// 第二遍:同包耦合。
	//   - 选中的 Go 文件(added 或 modified)同包里有 blocked 的 → 跟着 blocked:
	//     模板新版的 health.go 配的是新版 server.go,server.go 留在旧版就对不上
	//   - added 的 Go 文件同包里有未选中的 modified → 跳过:新文件里的符号
	//     多半是从旧文件里拆出来的,单独加进去会 redeclared
	//   - modified 的 Go 文件同包里有未选中的 → 照写:它本来就在服务里,
	//     少写一个 added 的 _test.go 不会让包编译不过;这是 --only 的正常用法
	reason := map[string]string{}
	for _, c := range u.Changes {
		if !strings.HasSuffix(c.Path, ".go") || fate[c.Path] != write {
			continue
		}
		dir := path.Dir(c.Path)
		for _, o := range u.Changes {
			if o.Path == c.Path || path.Dir(o.Path) != dir || !strings.HasSuffix(o.Path, ".go") {
				continue
			}
			switch fate[o.Path] {
			case block:
				fate[c.Path] = block
				reason[c.Path] = "same Go package as blocked " + path.Base(o.Path) + "; writing it alone would break the package"
			case skip:
				if c.Kind == ChangeAdded && fate[c.Path] != block {
					fate[c.Path] = skip
					reason[c.Path] = "same Go package as unselected " + path.Base(o.Path) + "; write them together (--write-modified or --only)"
				}
			}
		}
	}

	var sel Selection
	for _, c := range u.Changes {
		if r, ok := reason[c.Path]; ok {
			c.Blocked = r
		}
		switch fate[c.Path] {
		case write:
			sel.Write = append(sel.Write, c)
		case skip:
			sel.Skipped = append(sel.Skipped, c)
		default:
			sel.Blocked = append(sel.Blocked, c)
		}
	}
	return sel, nil
}

// Apply 把 sel.Write 写回服务目录,全部成功或全部不写。
//
// 只写不删:模板删掉某个文件时,upgrade 不会跟着删。判断「服务里这个文件是模板
// 留下的还是用户自己加的」需要生成时的状态,而那份状态我们刻意没有存。
//
// 原子性靠「先落到同一文件系统的暂存目录,再逐个 rename」+ 失败回滚:
// rename 在同一文件系统内是原子的,而且暂存阶段已经把所有内容写完、
// 权限设好,所以进入 rename 阶段后几乎不会失败;万一失败(权限、目录变成文件),
// 把已 rename 的那几个从备份换回来。之前的版本是逐个 os.WriteFile,
// 第 N 个失败时前 N-1 个已经落盘 —— 服务停在一个谁也说不清的半升级状态。
func (u *Upgrade) Apply(rep Reporter, sel Selection) (int, error) {
	if rep == nil {
		rep = DiscardReporter{}
	}
	if len(sel.Write) == 0 {
		return 0, nil
	}

	// 暂存目录放在服务目录下:跨文件系统 rename 会退化成 copy+delete,不再原子。
	// 用 . 前缀且名字固定前缀,万一进程被杀留下来,用户一眼能认出是什么。
	stage, err := os.MkdirTemp(u.ServiceRoot, ".co-upgrade-")
	if err != nil {
		return 0, fmt.Errorf("create staging dir: %w", err)
	}
	defer os.RemoveAll(stage)
	stagedDir := filepath.Join(stage, "new")
	backupDir := filepath.Join(stage, "old")

	// 1. 暂存:把所有内容写完。这一步失败不影响服务目录。
	for _, c := range sel.Write {
		data, ok := u.next[c.Path]
		if !ok {
			return 0, fmt.Errorf("no content prepared for %s", c.Path)
		}
		mode := fs.FileMode(0o644)
		if st, serr := os.Stat(filepath.Join(u.freshRoot, filepath.FromSlash(c.Path))); serr == nil {
			mode = st.Mode().Perm()
		}
		p := filepath.Join(stagedDir, filepath.FromSlash(c.Path))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return 0, err
		}
		if err := writeFileSync(p, data, mode); err != nil {
			return 0, fmt.Errorf("stage %s: %w", c.Path, err)
		}
	}

	// 2. 换入。done 记录已经换过的,失败时按它回滚。
	type moved struct {
		change Change
		backed bool // 旧文件已挪到 backupDir
	}
	var done []moved
	rollback := func() error {
		var errs []error
		for i := len(done) - 1; i >= 0; i-- {
			m := done[i]
			dst := filepath.Join(u.ServiceRoot, filepath.FromSlash(m.change.Path))
			if m.backed {
				if err := os.Rename(filepath.Join(backupDir, filepath.FromSlash(m.change.Path)), dst); err != nil {
					errs = append(errs, err)
				}
			} else if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	}

	for _, c := range sel.Write {
		rel := filepath.FromSlash(c.Path)
		dst := filepath.Join(u.ServiceRoot, rel)
		src := filepath.Join(stagedDir, rel)
		bak := filepath.Join(backupDir, rel)

		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return 0, u.failApply(rep, rollback, c, err)
		}
		m := moved{change: c}
		if st, serr := os.Lstat(dst); serr == nil {
			if st.IsDir() {
				// 服务里这个路径是目录,模板要在这里放文件。把目录整个挪进备份再盖
				// 文件上去在技术上可行,但那是在替用户决定删一棵树 —— 不做
				return 0, u.failApply(rep, rollback, c, errors.New("target is a directory"))
			}
			if err := os.MkdirAll(filepath.Dir(bak), 0o755); err != nil {
				return 0, u.failApply(rep, rollback, c, err)
			}
			if err := os.Rename(dst, bak); err != nil {
				return 0, u.failApply(rep, rollback, c, err)
			}
			m.backed = true
		}
		done = append(done, m)
		if err := os.Rename(src, dst); err != nil {
			return 0, u.failApply(rep, rollback, c, err)
		}
		// 和比对列表区分开:那一屏是「将要怎样」,这一行是「已经写了」
		rep.Step("wrote %s", c.Path)
	}
	return len(done), nil
}

func (u *Upgrade) failApply(rep Reporter, rollback func() error, c Change, cause error) error {
	if rerr := rollback(); rerr != nil {
		// 回滚也失败是最坏情况:明确告诉用户目录已不可信,让他去 git
		return fmt.Errorf("write %s: %w; rollback also failed (%v) — service dir is inconsistent, use git to restore",
			c.Path, cause, rerr)
	}
	rep.Warn("write %s failed, nothing was changed", c.Path)
	return fmt.Errorf("write %s: %w (rolled back, no file was changed)", c.Path, cause)
}

// writeFileSync 写文件并 fsync,让 rename 换进去的内容在掉电后也完整。
func writeFileSync(path string, data []byte, mode fs.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
