package scaffold

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/lens077/go-connect-template-cli/internal/manifest"
)

// DefaultTemplateRepo 是默认的模板仓库。
const DefaultTemplateRepo = "https://github.com/lens077/go-connect-template"

// FetchOptions 控制模板从哪儿来。
type FetchOptions struct {
	// Dir 非空时直接用本地目录,不做任何网络访问。
	// 开发模板本身时用它:改完 .co/ 立刻 co new 验证,不必先 push。
	Dir string
	// Repo / Ref 走 git clone。Ref 为空时取远端默认分支。
	Repo string
	Ref  string
	// NoCache 强制重新 clone,忽略本地缓存。
	NoCache bool
	// CacheDir 缓存根目录,空则用 os.UserCacheDir()/co-cli。
	CacheDir string
}

// Source 是一份就绪的模板,可以直接读。
type Source struct {
	// Root 模板仓库根目录
	Root string
	// FromCache 为 true 表示复用了已有缓存,没走网络
	FromCache bool
	// Ref 实际使用的 ref,本地目录模式为空
	Ref string

	// Repo 模板来源:clone 时是 URL,本地目录模式是绝对路径。写进 .co-origin.yaml。
	Repo string
	// Commit 是这份模板对应的 git commit(40 位)。本地目录不是 git 仓库时为空。
	// 它是 co upgrade 三方合并的 base 依据 —— 记录的是历史事实,不是代码现状,
	// 用户怎么改生成物它都不会变,所以可以安全地存进产物。
	Commit string
	// Dirty 本地目录模式下工作树有未提交改动:生成物和 Commit 对不上,base 只是近似。
	Dirty bool
}

// ScaffoldDir 返回模板目录的绝对路径。
func (s Source) ScaffoldDir() string { return filepath.Join(s.Root, manifest.ScaffoldDir) }

// Fetch 准备好模板目录。
func Fetch(ctx context.Context, opts FetchOptions) (Source, error) {
	if opts.Dir != "" {
		root, err := filepath.Abs(opts.Dir)
		if err != nil {
			return Source{}, err
		}
		if _, err := os.Stat(filepath.Join(root, manifest.Path)); err != nil {
			return Source{}, fmt.Errorf("%s is not a co template (missing %s)", root, manifest.Path)
		}
		src := Source{Root: root, Repo: root}
		src.Commit, src.Dirty = localHead(root)
		return src, nil
	}

	repo := opts.Repo
	if repo == "" {
		repo = DefaultTemplateRepo
	}

	dest, err := cachePath(opts.CacheDir, repo, opts.Ref)
	if err != nil {
		return Source{}, err
	}

	if opts.NoCache {
		if err := os.RemoveAll(dest); err != nil {
			return Source{}, fmt.Errorf("clear cache: %w", err)
		}
	} else if _, err := os.Stat(filepath.Join(dest, manifest.Path)); err == nil {
		src := Source{Root: dest, FromCache: true, Ref: opts.Ref, Repo: repo}
		src.Commit, _ = localHead(dest)
		return src, nil
	}

	// 半个 clone 留在缓存里比没有 clone 更糟:下次 co new 会把它当成有效缓存
	// 直接用,而它可能缺文件。失败就整个删掉,退回到「没有缓存」。
	if err := clone(ctx, repo, opts.Ref, dest); err != nil {
		_ = os.RemoveAll(dest)
		return Source{}, err
	}
	src := Source{Root: dest, Ref: opts.Ref, Repo: repo}
	src.Commit, _ = localHead(dest)
	return src, nil
}

// localHead 读出目录所属 git 仓库的 HEAD commit,以及工作树是否有未提交改动。
// 不是 git 仓库就返回空 —— 没有 commit 只是少了 base,不是错误。
func localHead(dir string) (commit string, dirty bool) {
	repo, err := git.PlainOpenWithOptions(dir, &git.PlainOpenOptions{DetectDotGit: true})
	if err != nil {
		return "", false
	}
	head, err := repo.Head()
	if err != nil {
		return "", false
	}
	wt, err := repo.Worktree()
	if err != nil {
		return head.Hash().String(), false
	}
	st, err := wt.Status()
	if err != nil {
		return head.Hash().String(), false
	}
	for _, fs := range st {
		// 未跟踪文件(.DS_Store、临时产物)不算脏:它们不进模板,也就不影响生成物
		if fs.Worktree != git.Untracked && (fs.Worktree != git.Unmodified || fs.Staging != git.Unmodified) {
			return head.Hash().String(), true
		}
	}
	return head.Hash().String(), false
}

// FetchAt 取模板在某个 revision(commit / tag / 分支)的快照,导出到缓存目录。
//
// 这是 co upgrade 三方合并的 base 来源。快照按 commit 缓存,同一个 commit 只导出一次。
// 本地目录模式从该目录所属的 git 仓库导出;远端模式先完整 clone 一份 bare 仓库
// (模板仓库很小,完整历史比按 SHA 浅 clone 省事 —— 后者要服务端开
// allowReachableSHA1InWant,并非处处可用),再导出。
func FetchAt(ctx context.Context, opts FetchOptions, rev string) (Source, error) {
	if rev == "" {
		return Source{}, errors.New("FetchAt: empty revision")
	}

	var repo *git.Repository
	var repoName string
	if opts.Dir != "" {
		root, err := filepath.Abs(opts.Dir)
		if err != nil {
			return Source{}, err
		}
		repo, err = git.PlainOpenWithOptions(root, &git.PlainOpenOptions{DetectDotGit: true})
		if err != nil {
			return Source{}, fmt.Errorf("%s is not a git repository, cannot fetch base revision %s: %w", root, rev, err)
		}
		repoName = root
	} else {
		repoName = opts.Repo
		if repoName == "" {
			repoName = DefaultTemplateRepo
		}
		var err error
		repo, err = openOrCloneBare(ctx, opts.CacheDir, repoName)
		if err != nil {
			return Source{}, err
		}
	}

	hash, err := repo.ResolveRevision(plumbing.Revision(rev))
	if err != nil && opts.Dir == "" {
		// 缓存的 bare 仓库可能落后于远端,拉一次再试
		if ferr := repo.FetchContext(ctx, &git.FetchOptions{
			RefSpecs: []config.RefSpec{"+refs/heads/*:refs/heads/*", "+refs/tags/*:refs/tags/*"},
			Tags:     git.AllTags,
		}); ferr != nil && !errors.Is(ferr, git.NoErrAlreadyUpToDate) {
			return Source{}, fmt.Errorf("fetch %s: %w", repoName, ferr)
		}
		hash, err = repo.ResolveRevision(plumbing.Revision(rev))
	}
	if err != nil {
		return Source{}, fmt.Errorf("resolve %q in %s: %w", rev, repoName, err)
	}

	dest, err := cachePath(opts.CacheDir, repoName, "commit-"+hash.String())
	if err != nil {
		return Source{}, err
	}
	if _, err := os.Stat(filepath.Join(dest, manifest.Path)); err == nil {
		return Source{Root: dest, FromCache: true, Ref: rev, Repo: repoName, Commit: hash.String()}, nil
	}

	commit, err := repo.CommitObject(*hash)
	if err != nil {
		return Source{}, fmt.Errorf("%q resolves to %s which is not a commit: %w", rev, hash, err)
	}
	if err := exportTree(commit, dest); err != nil {
		_ = os.RemoveAll(dest)
		return Source{}, fmt.Errorf("export %s@%s: %w", repoName, hash, err)
	}
	if _, err := os.Stat(filepath.Join(dest, manifest.Path)); err != nil {
		_ = os.RemoveAll(dest)
		return Source{}, fmt.Errorf("%s@%s is not a co template (missing %s); pick a later base revision", repoName, rev, manifest.Path)
	}
	return Source{Root: dest, Ref: rev, Repo: repoName, Commit: hash.String()}, nil
}

// openOrCloneBare 维护一份完整历史的 bare clone,给 FetchAt 解析任意 revision 用。
func openOrCloneBare(ctx context.Context, cacheDir, repoURL string) (*git.Repository, error) {
	dest, err := cachePath(cacheDir, repoURL, "")
	if err != nil {
		return nil, err
	}
	dest = filepath.Join(filepath.Dir(dest), "..", "repos", filepath.Base(dest)+".git")
	if r, err := git.PlainOpen(dest); err == nil {
		return r, nil
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return nil, err
	}
	r, err := git.PlainCloneContext(ctx, dest, true, &git.CloneOptions{URL: repoURL, Tags: git.AllTags})
	if err != nil {
		_ = os.RemoveAll(dest)
		return nil, fmt.Errorf("clone %s (full history, for base revisions): %w", repoURL, err)
	}
	return r, nil
}

// exportTree 把一个 commit 的文件树写到 dest,不带 .git。
func exportTree(commit *object.Commit, dest string) error {
	tree, err := commit.Tree()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	return tree.Files().ForEach(func(f *object.File) error {
		target := filepath.Join(dest, filepath.FromSlash(f.Name))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		mode, err := f.Mode.ToOSFileMode()
		if err != nil {
			mode = 0o644
		}
		r, err := f.Reader()
		if err != nil {
			return err
		}
		defer r.Close()
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode.Perm())
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, r); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	})
}

func clone(ctx context.Context, repo, ref, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}

	opts := &git.CloneOptions{
		URL: repo,
		// Depth 1:模板只用最新一份快照,历史一点用不上,
		// 而完整历史在网络差的环境里能差出一个数量级的时间
		Depth:             1,
		SingleBranch:      true,
		Tags:              git.NoTags,
		RecurseSubmodules: git.NoRecurseSubmodules,
	}
	if ref != "" {
		// ref 可能是分支也可能是 tag,先按分支试,失败再按 tag 试。
		// go-git 的 ReferenceName 必须写全 refs/heads/ 或 refs/tags/,
		// 它不像 git CLI 那样会自己猜。
		opts.ReferenceName = plumbing.NewBranchReferenceName(ref)
	}

	if _, err := git.PlainCloneContext(ctx, dest, false, opts); err != nil {
		if ref == "" {
			return fmt.Errorf("clone %s: %w", repo, err)
		}
		_ = os.RemoveAll(dest)
		opts.ReferenceName = plumbing.NewTagReferenceName(ref)
		if _, err2 := git.PlainCloneContext(ctx, dest, false, opts); err2 != nil {
			return fmt.Errorf("clone %s at ref %q (tried branch and tag): %w", repo, ref, err)
		}
	}

	if _, err := os.Stat(filepath.Join(dest, manifest.Path)); err != nil {
		return fmt.Errorf("%s@%s is not a co template (missing %s)", repo, refOrHead(ref), manifest.Path)
	}
	return nil
}

func refOrHead(ref string) string {
	if ref == "" {
		return "HEAD"
	}
	return ref
}

// cachePath 把 repo+ref 映射到一个缓存目录。
// 目录名用清洗过的 repo 路径而不是哈希:缓存出问题时用户能一眼看出
// 哪个目录对应哪个仓库,自己 rm 掉就行。
func cachePath(base, repo, ref string) (string, error) {
	if base == "" {
		dir, err := os.UserCacheDir()
		if err != nil {
			return "", fmt.Errorf("locate cache dir: %w", err)
		}
		base = filepath.Join(dir, "co-cli")
	}
	name := sanitize(repo)
	if ref != "" {
		name += "@" + sanitize(ref)
	}
	return filepath.Join(base, "templates", name), nil
}

func sanitize(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '.', r == '_':
			out = append(out, r)
		default:
			out = append(out, '-')
		}
	}
	return string(out)
}
