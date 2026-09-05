package scaffold

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/lens077/go-connect-template-cli/internal/manifest"
)

// +co: 标记的裁剪。模板里所有可选代码都靠它标出来,是整个引擎最吃重的一块。
//
// 三种形态:
//
//	行标记   NewRedisClient,   // +co:redis
//	         registry.Module,  // 服务注册/发现 +co:consul
//	         逗号是「与」:// +co:example,elasticsearch 要两个都开才保留。
//	         竖线是「或」:// +co:elasticsearch|meilisearch 任一个开着就保留。
//
//	块标记   // +co:begin redis
//	         ...
//	         // +co:end
//	         可嵌套。
//
//	锚点     // +co:anchor data-providers
//	         co resource add 的插入点。
//
// 裁剪后的输出里不留任何 begin/end/行标记 —— 生成的项目是给人读的,
// 满屏 +co: 注释只会碍事。锚点是唯一的例外:它要留着给后续 resource add 用。

const (
	markerPrefix = "+co:"
	beginToken   = "+co:begin"
	endToken     = "+co:end"
	anchorToken  = "+co:anchor"
)

// commentPrefixFor 按扩展名给出行注释前缀。
// 认不出的类型返回空串,调用方据此整file 跳过 —— 猜错前缀会把源码本身当注释删掉。
func commentPrefixFor(path string) string {
	if strings.HasSuffix(path, ".yaml.example") || strings.HasSuffix(path, ".yml.example") {
		return "#"
	}
	switch filepath.Ext(path) {
	case ".go":
		return "//"
	case ".yaml", ".yml", ".toml":
		return "#"
	case ".sql":
		return "--"
	}
	// 无扩展名的按文件名认
	switch filepath.Base(path) {
	case "Makefile", "Dockerfile", ".gitignore", ".dockerignore", ".stignore":
		return "#"
	}
	return ""
}

// Prune 按启用的 feature 裁剪一份文件内容。
// 返回裁剪后的内容与「本文件是否发生了改动」。
func Prune(path, content string, set manifest.FeatureSet) (string, bool, error) {
	prefix := commentPrefixFor(path)
	if prefix == "" || !strings.Contains(content, markerPrefix) {
		return content, false, nil
	}

	// 保留原始行尾:模板里可能混有 CRLF(Windows 上 clone 出来的),
	// 统一改成 LF 会让整个文件在 diff 里全红。
	lines := splitLines(content)
	out := make([]line, 0, len(lines))

	// depth > 0 表示正处在一个「要删掉的块」内部。
	// 单独记 dropDepth 而不是布尔:块可嵌套,里层的 +co:end 不能提前结束外层。
	depth := 0
	dropAt := -1

	for i, ln := range lines {
		body := strings.TrimSpace(ln.text)
		kind, feats := classify(body, prefix)

		switch kind {
		case markerBegin:
			depth++
			if dropAt < 0 && !matchesFeatureExpression(set, feats) {
				dropAt = depth
			}
			continue // begin 行本身永远不输出

		case markerEnd:
			if depth == 0 {
				return "", false, fmt.Errorf("%s:%d: %s without a matching %s", path, i+1, endToken, beginToken)
			}
			if dropAt == depth {
				dropAt = -1
			}
			depth--
			continue // end 行本身永远不输出

		case markerAnchor:
			// 锚点在任何情况下都原样保留,包括处在被删块内部时 ——
			// 不过那说明模板写错了:锚点被删掉,后续 resource add 就没地方插了。
			if dropAt >= 0 {
				return "", false, fmt.Errorf("%s:%d: anchor inside a pruned +co:begin block", path, i+1)
			}
			out = append(out, ln)
			continue
		}

		if dropAt >= 0 {
			continue
		}

		if kind == markerLine {
			if !matchesFeatureExpression(set, feats) {
				continue
			}
			ln.text = stripLineMarker(ln.text, prefix)
		}
		out = append(out, ln)
	}

	if depth != 0 {
		return "", false, fmt.Errorf("%s: %d unclosed %s block(s)", path, depth, beginToken)
	}

	result := joinLines(out)
	return result, result != content, nil
}

type markerKind int

const (
	markerNone markerKind = iota
	markerLine
	markerBegin
	markerEnd
	markerAnchor
)

// classify 判断一行属于哪种标记,并取出它挂的 feature 表达式。
// 返回值外层是「或」,内层是「与」。
func classify(body, prefix string) (markerKind, [][]string) {
	if !strings.Contains(body, markerPrefix) {
		return markerNone, nil
	}

	// begin/end/anchor 必须独占一行:它们前面只能有注释前缀。
	// 不允许 `foo() // +co:begin x` 这种写法 —— 那样删块时会把 foo() 一起带走,
	// 而作者多半只是想标一行。
	if after, ok := strings.CutPrefix(body, prefix); ok {
		t := strings.TrimSpace(after)
		switch {
		case t == endToken:
			return markerEnd, nil
		case strings.HasPrefix(t, beginToken+" "):
			return markerBegin, parseFeatureExpression(strings.TrimPrefix(t, beginToken+" "))
		case strings.HasPrefix(t, anchorToken+" "):
			return markerAnchor, nil
		case strings.HasPrefix(t, markerPrefix):
			// `// +co:redis` 独占一行 —— 当行标记处理,整行删掉/保留
			return markerLine, parseFeatureExpression(strings.TrimPrefix(t, markerPrefix))
		}
	}

	// 行标记:+co:xxx 必须是整行的最后一个 token。
	// 这条限制换来的是不用写 Go 词法分析器 —— 只有形如
	// s := "// +co:redis" 且该字符串结尾恰好是标记的代码会被误判,
	// 现实中不存在,而模板自身的 CI 会立刻发现。
	fields := strings.Fields(body)
	last := fields[len(fields)-1]
	if !strings.HasPrefix(last, markerPrefix) {
		return markerNone, nil
	}
	// 标记必须真的在注释里,而不是在代码里
	if !strings.Contains(body, prefix) {
		return markerNone, nil
	}
	return markerLine, parseFeatureExpression(strings.TrimPrefix(last, markerPrefix))
}

func parseFeatureExpression(s string) [][]string {
	var alternatives [][]string
	for _, alternative := range strings.Split(strings.TrimSpace(s), "|") {
		var all []string
		for _, part := range strings.Split(alternative, ",") {
			if feature := strings.TrimSpace(part); feature != "" {
				all = append(all, feature)
			}
		}
		if len(all) > 0 {
			alternatives = append(alternatives, all)
		}
	}
	return alternatives
}

func matchesFeatureExpression(set manifest.FeatureSet, alternatives [][]string) bool {
	for _, all := range alternatives {
		if set.HasAll(all) {
			return true
		}
	}
	return false
}

// stripLineMarker 把行尾的 +co:xxx 去掉,注释因此变空的话连注释一起去掉。
//
//	NewRedisClient,   // +co:redis          -> NewRedisClient,
//	registry.Module,  // 服务注册/发现 +co:consul -> registry.Module,  // 服务注册/发现
func stripLineMarker(text, prefix string) string {
	idx := strings.LastIndex(text, markerPrefix)
	if idx < 0 {
		return text
	}
	head := strings.TrimRight(text[:idx], " \t")

	// 注释里只剩前缀,说明这条注释是纯粹为标记而写的,一并删掉
	if c := strings.LastIndex(head, prefix); c >= 0 && strings.TrimSpace(head[c+len(prefix):]) == "" {
		head = strings.TrimRight(head[:c], " \t")
	}
	return head
}

// InsertAtAnchor 在锚点行之前插入一段文本,缩进对齐锚点行。
// 锚点本身保留在原处,所以同一个锚点可以插入不同内容;完全相同的内容只插一次。
func InsertAtAnchor(path, content, anchor, text string) (string, error) {
	next, _, err := InsertAtAnchorOnce(path, content, anchor, text)
	return next, err
}

// InsertAtAnchorOnce 与 InsertAtAnchor 相同,并额外报告这次是否真的插入了内容。
func InsertAtAnchorOnce(path, content, anchor, text string) (string, bool, error) {
	prefix := commentPrefixFor(path)
	if prefix == "" {
		return "", false, fmt.Errorf("%s: unknown comment syntax, cannot insert at anchor %q", path, anchor)
	}
	want := prefix + " " + anchorToken + " " + anchor

	lines := splitLines(content)
	for i, ln := range lines {
		if strings.TrimSpace(ln.text) != want {
			continue
		}

		indent := ln.text[:len(ln.text)-len(strings.TrimLeft(ln.text, " \t"))]
		var block []line
		for _, t := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
			if t == "" {
				block = append(block, line{text: "", eol: ln.eol})
				continue
			}
			block = append(block, line{text: indent + t, eol: ln.eol})
		}
		if containsLineBlock(lines[:i], block) {
			return content, false, nil
		}

		out := make([]line, 0, len(lines)+len(block))
		out = append(out, lines[:i]...)
		out = append(out, block...)
		out = append(out, lines[i:]...)
		return joinLines(out), true, nil
	}

	return "", false, fmt.Errorf("%s: anchor %q not found", path, anchor)
}

func containsLineBlock(lines, block []line) bool {
	if len(block) == 0 || len(block) > len(lines) {
		return false
	}
	for i := 0; i <= len(lines)-len(block); i++ {
		match := true
		for j := range block {
			if lines[i+j].text != block[j].text {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// line 保留每行原本的行尾符,使裁剪不改变文件的换行风格。
type line struct {
	text string
	eol  string // "\n" / "\r\n" / "" (文件末尾无换行)
}

func splitLines(s string) []line {
	var out []line
	for len(s) > 0 {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			out = append(out, line{text: s})
			break
		}
		text, eol := s[:i], "\n"
		if strings.HasSuffix(text, "\r") {
			text, eol = text[:len(text)-1], "\r\n"
		}
		out = append(out, line{text: text, eol: eol})
		s = s[i+1:]
	}
	return out
}

func joinLines(lines []line) string {
	var b strings.Builder
	for _, ln := range lines {
		b.WriteString(ln.text)
		b.WriteString(ln.eol)
	}
	return b.String()
}
