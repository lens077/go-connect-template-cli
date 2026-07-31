// Package ui 负责终端输出与交互表单。
//
// 单独成包是为了让引擎(scaffold/)完全不知道终端的存在:引擎只往
// scaffold.Reporter 写,ui 提供一个带颜色的实现,测试则用 DiscardReporter。
package ui

import (
	"fmt"
	"io"
	"os"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-isatty"
)

var (
	stepStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	warnStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	errStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Bold(true)
	titleStyle = lipgloss.NewStyle().Bold(true)
	dimStyle   = lipgloss.NewStyle().Faint(true)
	cmdStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))
)

// Printer 是带样式的输出器,同时实现 scaffold.Reporter。
type Printer struct {
	Out io.Writer
	Err io.Writer
}

// New 返回写往 stdout/stderr 的 Printer。
func New() *Printer { return &Printer{Out: os.Stdout, Err: os.Stderr} }

// Step 打印一步进度。进度写 stderr 而不是 stdout ——
// stdout 留给 --dry-run 的操作清单,这样 `co new --dry-run > plan.txt`
// 得到的是干净的清单,进度照常显示在终端上。
func (p *Printer) Step(format string, args ...any) {
	fmt.Fprintln(p.Err, stepStyle.Render("→ ")+fmt.Sprintf(format, args...))
}

// Warn 打印警告。
func (p *Printer) Warn(format string, args ...any) {
	fmt.Fprintln(p.Err, warnStyle.Render("! ")+fmt.Sprintf(format, args...))
}

// Error 打印错误。
func (p *Printer) Error(format string, args ...any) {
	fmt.Fprintln(p.Err, errStyle.Render("✗ ")+fmt.Sprintf(format, args...))
}

// Title 打印小标题。
func (p *Printer) Title(s string) { fmt.Fprintln(p.Err, titleStyle.Render(s)) }

// Dim 打印次要信息。
func (p *Printer) Dim(format string, args ...any) {
	fmt.Fprintln(p.Err, dimStyle.Render(fmt.Sprintf(format, args...)))
}

// Command 打印一条建议用户执行的命令。
func (p *Printer) Command(s string) { fmt.Fprintln(p.Err, "  "+cmdStyle.Render(s)) }

// Interactive 判断当前是否适合弹交互表单。
//
// 非 TTY 下必须直接失败而不是弹表单:CI 里跑 co new 时表单会读到 EOF,
// huh 的行为是返回一个含糊的错误,而真正的问题是「缺参数」。
func Interactive() bool {
	return isatty.IsTerminal(os.Stdin.Fd()) && isatty.IsTerminal(os.Stderr.Fd())
}
