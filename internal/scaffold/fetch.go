package scaffold

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"

	"github.com/lens077/co-cli/internal/manifest"
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
		return Source{Root: root}, nil
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
		return Source{Root: dest, FromCache: true, Ref: opts.Ref}, nil
	}

	// 半个 clone 留在缓存里比没有 clone 更糟:下次 co new 会把它当成有效缓存
	// 直接用,而它可能缺文件。失败就整个删掉,退回到「没有缓存」。
	if err := clone(ctx, repo, opts.Ref, dest); err != nil {
		_ = os.RemoveAll(dest)
		return Source{}, err
	}
	return Source{Root: dest, Ref: opts.Ref}, nil
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
