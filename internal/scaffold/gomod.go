package scaffold

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
)

// go.mod 的改写走 x/mod/modfile 而不是字符串替换。
//
// 字符串替换在这里特别容易出错:模块路径会作为子串出现在 require 块、
// replace 指令甚至注释里,而 require 行还有单行/块两种写法。modfile
// 解析出的是真正的语法树,DropRequire 只动 require,SetModule 只动 module。

// ValidateModulePath 检查 module 路径是否合法。
// 提前拦是因为 go.mod 里写了个非法路径后,go build 的报错是
// "malformed module path",不会告诉你它是 co new 的 --module 传进来的。
func ValidateModulePath(path string) error {
	if path == "" {
		return fmt.Errorf("module path is empty")
	}
	if err := module.CheckPath(path); err != nil {
		return fmt.Errorf("invalid module path %q: %w", path, err)
	}
	return nil
}

// RewriteGoMod 把 go.mod 的 module 改成 newModule,并删掉 drop 里的直接依赖。
//
// 只删 require,不碰 go 指令与 toolchain:目标项目该用哪个 Go 版本由模板决定,
// 而不是由跑 co 的那台机器上装的 Go 决定。
func RewriteGoMod(path, newModule string, drop []string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read go.mod: %w", err)
	}

	f, err := modfile.Parse(filepath.Base(path), data, nil)
	if err != nil {
		return fmt.Errorf("parse go.mod: %w", err)
	}

	if err := f.AddModuleStmt(newModule); err != nil {
		return fmt.Errorf("set module: %w", err)
	}

	for _, dep := range drop {
		// 同一个模块可能有多条 require(不同版本、indirect 与直接各一条),
		// 所以要按当前解析结果逐条删,不能只删第一条
		for _, req := range f.Require {
			if req.Mod.Path != dep {
				continue
			}
			if err := f.DropRequire(dep); err != nil {
				return fmt.Errorf("drop require %s: %w", dep, err)
			}
		}
	}

	// Cleanup 回收 DropRequire 留下的空行与空 require 块。
	// 不调它的话 go.mod 里会剩一堆孤立的 require ( ) 空壳。
	f.Cleanup()

	out, err := f.Format()
	if err != nil {
		return fmt.Errorf("format go.mod: %w", err)
	}
	return os.WriteFile(path, out, 0o644)
}

// ReadModulePath 读出一个 go.mod 声明的 module 路径。
// monorepo 模式下用它从根 go.mod 推断服务的导入前缀。
func ReadModulePath(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	m := modfile.ModulePath(data)
	if m == "" {
		return "", fmt.Errorf("%s: no module directive", path)
	}
	return m, nil
}
