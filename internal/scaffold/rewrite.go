package scaffold

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// 文本替换只用在两处,且两处的被替换串都是全局唯一的长串:
//
//   - module 路径     github.com/lens077/go-connect-template
//   - 服务名占位符     org-service-v1
//
// 这是与老版 CLI 最本质的差别 —— 老版拿正则去改写源码结构
// (把 *pgxpool.Pool 换成 *sql.DB 再追加一段字符串),模板换个格式就静默失效。
// 现在结构性的差异一律由「删文件 / 删标记行 / 覆盖文件」表达,
// 文本替换只负责改名字。

// Replacement 一对替换。
type Replacement struct {
	Old string
	New string
}

// ReplaceInTree 对目录下所有文本文件做替换,返回被改动的文件数。
//
// 跳过二进制文件而不是无脑替换:模板里有 .png/.jar 之类的资源时,
// 按字节替换会破坏文件且没有任何报错。
func ReplaceInTree(root string, reps []Replacement, skip func(rel string) bool) (int, error) {
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
		if skip != nil && skip(rel) {
			return nil
		}

		data, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		if !isText(data) {
			return nil
		}

		out := data
		for _, r := range reps {
			if r.Old == "" || r.Old == r.New {
				continue
			}
			out = bytes.ReplaceAll(out, []byte(r.Old), []byte(r.New))
		}
		if bytes.Equal(out, data) {
			return nil
		}

		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		if werr := os.WriteFile(p, out, info.Mode().Perm()); werr != nil {
			return werr
		}
		changed++
		return nil
	})

	return changed, err
}

// isText 判断内容是否是可安全做文本替换的。
// 判据是「合法 UTF-8 且不含 NUL」—— 与 git 判定二进制的方式一致,
// 对源码、YAML、证书 PEM 都成立,对图片和编译产物为假。
func isText(data []byte) bool {
	const sniff = 8000
	if len(data) > sniff {
		data = data[:sniff]
		// 截断可能切在多字节字符中间,退回到最后一个完整字符边界
		for len(data) > 0 && !utf8.Valid(data) {
			data = data[:len(data)-1]
		}
	}
	return !bytes.ContainsRune(data, 0) && utf8.Valid(data)
}

// PathSkipper 返回一个跳过指定顶层目录/文件的判断函数。
func PathSkipper(names ...string) func(rel string) bool {
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[filepath.FromSlash(n)] = true
	}
	return func(rel string) bool {
		if set[rel] {
			return true
		}
		// 顶层目录被跳过时,其下所有内容一并跳过
		for p := rel; ; {
			dir := filepath.Dir(p)
			if dir == "." || dir == p {
				return false
			}
			if set[dir] {
				return true
			}
			p = dir
		}
	}
}

// ToSlash 统一成正斜杠,便于跨平台比较与展示。
func ToSlash(p string) string { return strings.ReplaceAll(p, string(filepath.Separator), "/") }
