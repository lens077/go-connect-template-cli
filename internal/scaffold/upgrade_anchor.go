package scaffold

import (
	"fmt"
	"strings"
)

// 锚点文件是 upgrade 的盲区,也是它最容易帮倒忙的地方。
//
// 参考副本用 NoResource 生成,锚点上方是空的;真实服务的锚点上方却堆着
// co new / resource add / proto gen 一次次插进去的接线(NewCartUseCase, /
// mux.Handle(...)),还可能有用户手写的那几行。直接拿副本去比,这几个文件
// 永远是 modified;拿副本去盖,接线全没了 —— go build 照样绿(没人引用的
// 构造函数不报错),服务却不再注册 handler。这是一种静默失败。
//
// 所以约定:**锚点上方那段属于服务,不属于模板。** 比对前先把它搬进参考副本,
// 之后所有比对、diff、写盘都基于搬完的那份。

// carryAnchors 把 current 里每个锚点段中「模板没有的行」插进 fresh 的对应位置。
//
// 判断「模板没有」用的是两侧锚点段的 LCS 对齐,而不是「这一行在模板里出现过」:
// 接线里常有 `)` 这种到处都有的行,按全局查找会把一段接线拦腰截断。
//
// 对齐之后,current 里没对上的行按 hunk 分两类:
//   - 纯插入:同一个空隙里 fresh 侧没有未匹配行。这是接线或用户加的行,
//     在段里任何位置都搬 —— gofmt 会把 import 排序,co new 插的 cartv1connect
//     会被排到模板自带的 searchv1connect 前面,不再紧贴锚点。
//   - 替换:同一个空隙里 fresh 侧也有未匹配行,多半是模板自己改了那几行。
//     只有紧贴锚点的那个空隙保留(分不清时宁多不少,diff 可见);段中间的丢掉,
//     否则旧模板行会被复制一份。
//
// 返回搬完的内容,以及无法搬运的告警(模板改名/删掉了锚点)。
func carryAnchors(path, fresh, current string) (string, []string) {
	prefix := commentPrefixFor(path)
	if prefix == "" || !strings.Contains(current, anchorToken) {
		return fresh, nil
	}

	freshLines := splitLines(fresh)
	curLines := splitLines(current)
	freshAnchors := anchorIndexes(freshLines, prefix)
	curAnchors := anchorIndexes(curLines, prefix)
	if len(curAnchors) == 0 {
		return fresh, nil
	}

	// carry[freshIdx] = 要插在 fresh 该行之前的行
	carry := map[int][]line{}
	// anchorText[freshIdx] = 锚点行在服务里的原样(缩进可能不同,见下)
	anchorText := map[int]string{}
	var warnings []string

	prevCur, prevFresh := -1, -1
	for _, ci := range curAnchors {
		key := strings.TrimSpace(curLines[ci].text)
		fi := findAnchor(freshLines, freshAnchors, key, prevFresh)
		if fi < 0 {
			warnings = append(warnings, fmt.Sprintf(
				"%s: anchor %q is gone from the template; lines above it are not carried over, review by hand",
				path, key))
			prevCur = ci
			continue
		}

		// Go 文件里,锚点不在 import 块内时不搬 import 行:段首到锚点之间往往
		// 包着整个 import 块,legacy 文件多出来的 import 会被当纯插入搬过来,而用到
		// 它们的代码在锚点下方已被模板替换 —— 结果是一串 "imported and not used"。
		// 锚点本身在 import 块里(server-imports)时,import 行就是接线,照搬。
		dropImports := strings.HasSuffix(path, ".go") && !insideImportBlock(freshLines, fi)
		for at, block := range carriedInsertions(freshLines[prevFresh+1:fi], curLines[prevCur+1:ci]) {
			for _, b := range block {
				if dropImports && looksLikeImportSpec(b.text) {
					continue
				}
				carry[prevFresh+1+at] = append(carry[prevFresh+1+at], b)
			}
		}
		// gofmt 给「只剩一条注释的 fx.Provide(」和「有元素的」排的缩进不一样,
		// 而且之后再 gofmt 也不会把注释缩进改回去。锚点行的缩进以服务为准。
		anchorText[fi] = curLines[ci].text
		prevCur, prevFresh = ci, fi
	}

	if len(carry) == 0 && len(anchorText) == 0 {
		return fresh, warnings
	}

	out := make([]line, 0, len(freshLines)+8)
	for i, ln := range freshLines {
		if block, ok := carry[i]; ok {
			for _, b := range block {
				// 行尾跟着目标文件走,别把 CRLF 混进 LF 文件
				out = append(out, line{text: b.text, eol: ln.eol})
			}
		}
		if text, ok := anchorText[i]; ok {
			ln.text = text
		}
		out = append(out, ln)
	}
	return joinLines(out), warnings
}

// legacyAnchorFile 判断「模板这份带锚点,服务那份一个锚点都没有」。
//
// 这是锚点机制之前生成的服务的形状:接线在,但没有任何标记告诉我们它在哪。
// carryAnchors 对这种文件是空操作,直接覆盖会把接线抹掉,所以要挡在写入之前。
// 只要服务里有任意一个锚点就不算 legacy:缺的那几个由 carryAnchors 单独告警。
func legacyAnchorFile(path string, fresh, current []byte) bool {
	prefix := commentPrefixFor(path)
	if prefix == "" {
		return false
	}
	return len(anchorIndexes(splitLines(string(fresh)), prefix)) > 0 &&
		len(anchorIndexes(splitLines(string(current)), prefix)) == 0
}

// insideImportBlock 判断第 i 行是否处在 `import (` … `)` 之间。
func insideImportBlock(lines []line, i int) bool {
	for k := i - 1; k >= 0; k-- {
		t := strings.TrimSpace(lines[k].text)
		switch {
		case t == "import (":
			return true
		case t == ")" && !strings.HasPrefix(lines[k].text, "\t") && !strings.HasPrefix(lines[k].text, " "):
			// 顶格的 ) 只会是 import/var/const 块的结尾
			return false
		case strings.HasPrefix(t, "func ") || strings.HasPrefix(t, "type ") || strings.HasPrefix(t, "var ") || strings.HasPrefix(t, "const "):
			return false
		}
	}
	return false
}

// looksLikeImportSpec 判断一行是不是 import 项:可选别名 + 双引号路径,别无其他。
func looksLikeImportSpec(text string) bool {
	t := strings.TrimSpace(text)
	if i := strings.Index(t, "//"); i >= 0 {
		t = strings.TrimSpace(t[:i])
	}
	fields := strings.Fields(t)
	switch len(fields) {
	case 1:
		return len(fields[0]) >= 2 && fields[0][0] == '"' && fields[0][len(fields[0])-1] == '"'
	case 2:
		return len(fields[1]) >= 2 && fields[1][0] == '"' && fields[1][len(fields[1])-1] == '"'
	}
	return false
}

// anchorIndexes 列出所有锚点行的下标。
func anchorIndexes(lines []line, prefix string) []int {
	var idx []int
	for i, ln := range lines {
		if kind, _ := classify(strings.TrimSpace(ln.text), prefix); kind == markerAnchor {
			idx = append(idx, i)
		}
	}
	return idx
}

// findAnchor 在 after 之后找文本相同的锚点;按顺序找,同名锚点重复出现时也不会错位。
func findAnchor(lines []line, anchors []int, key string, after int) int {
	for _, i := range anchors {
		if i > after && strings.TrimSpace(lines[i].text) == key {
			return i
		}
	}
	return -1
}

// carriedInsertions 对齐一个锚点段,返回要搬的行:key 是 fresh 里的下标(插在该行之前),
// key == len(fresh) 表示插在段尾(即锚点之前)。
//
// 比对用 TrimSpace 之后的文本:两侧都过了 gofmt,缩进本该一致,
// 但 YAML 之类不走 gofmt 的文件缩进可能因裁剪而漂移;搬运时保留 cur 的原样缩进。
func carriedInsertions(fresh, cur []line) map[int][]line {
	pairs := lcsPairs(fresh, cur)
	// 哨兵:段尾当作一对「匹配」,这样最后一个空隙和中间的空隙走同一段逻辑
	pairs = append(pairs, [2]int{len(fresh), len(cur)})

	out := map[int][]line{}
	pi, pj := -1, -1
	for _, pr := range pairs {
		i, j := pr[0], pr[1]
		freshGap := i - pi - 1
		curGap := cur[pj+1 : j]
		trailing := i == len(fresh)
		if len(curGap) > 0 && (freshGap == 0 || trailing) {
			block := make([]line, len(curGap))
			copy(block, curGap)
			out[i] = append(out[i], block...)
		}
		pi, pj = i, j
	}
	return out
}

// lcsPairs 返回 fresh 与 cur 的最长公共子序列,按 (i, j) 下标对给出。
// 文件都是几十到几百行,O(n·m) 足够。
func lcsPairs(fresh, cur []line) [][2]int {
	n, m := len(fresh), len(cur)
	if n == 0 || m == 0 {
		return nil
	}

	eq := func(i, j int) bool {
		return strings.TrimSpace(fresh[i].text) == strings.TrimSpace(cur[j].text)
	}

	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			switch {
			case eq(i, j):
				dp[i][j] = dp[i+1][j+1] + 1
			case dp[i+1][j] >= dp[i][j+1]:
				dp[i][j] = dp[i+1][j]
			default:
				dp[i][j] = dp[i][j+1]
			}
		}
	}

	var pairs [][2]int
	for i, j := 0, 0; i < n && j < m; {
		switch {
		case eq(i, j):
			pairs = append(pairs, [2]int{i, j})
			i++
			j++
		case dp[i+1][j] >= dp[i][j+1]:
			i++
		default:
			j++
		}
	}
	return pairs
}
