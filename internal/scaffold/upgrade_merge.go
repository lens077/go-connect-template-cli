package scaffold

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// 三方合并借 git merge-file,不自己实现 diff3。
//
// git 已经是这条命令的依赖(--diff、--write 的干净检查都靠它),而 diff3 的边角
// (相邻 hunk、空文件、无结尾换行)自己写一遍只会引入新 bug。
// 找不到 git 时退回两方比对 + 锚点搬运,并告警 —— 结果更保守,不会更危险。

// mergeResult 是一次三方合并的产物。
type mergeResult struct {
	// Merged 合并后的内容;有冲突时带 <<<<<<< / ======= / >>>>>>> 标记
	Merged []byte
	// Conflicts 冲突块数,0 表示干净合并
	Conflicts int
}

// merge3 以 base 为共同祖先,把 current(服务现状)与 fresh(模板新版)合并。
//
//	base == current, fresh 改了   → 取 fresh(模板演进)
//	base == fresh,   current 改了 → 取 current(用户定制,包括新增的接线)
//	两边改同一处                  → 冲突,不自动决定
func merge3(gitPath string, current, base, fresh []byte) (mergeResult, error) {
	tmp, err := os.MkdirTemp("", "co-merge-")
	if err != nil {
		return mergeResult{}, err
	}
	defer os.RemoveAll(tmp)

	cur := filepath.Join(tmp, "current")
	bas := filepath.Join(tmp, "base")
	fre := filepath.Join(tmp, "template")
	for p, data := range map[string][]byte{cur: current, bas: base, fre: fresh} {
		if err := os.WriteFile(p, data, 0o644); err != nil {
			return mergeResult{}, err
		}
	}

	// -p 输出到 stdout;-L 给冲突标记起名,让用户一眼看出哪边是模板哪边是自己
	cmd := exec.Command(gitPath, "merge-file", "-p",
		"-L", "current (this service)", "-L", "base (template at generation)", "-L", "template (new)",
		cur, bas, fre)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()

	// 退出码约定:0 干净;1..127 = 冲突数;255 才是真错误
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return mergeResult{Merged: stdout.Bytes()}, nil
	case errors.As(err, &exitErr) && exitErr.ExitCode() > 0 && exitErr.ExitCode() < 128:
		return mergeResult{Merged: stdout.Bytes(), Conflicts: exitErr.ExitCode()}, nil
	default:
		return mergeResult{}, fmt.Errorf("git merge-file: %w: %s", err, bytes.TrimSpace(stderr.Bytes()))
	}
}
