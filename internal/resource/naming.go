// Package resource 从 .co/scaffold/resource 渲染出一整套资源代码
// (proto + SQL + biz + data + service)。
package resource

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/gertd/go-pluralize"
)

var pluralizer = pluralize.NewClient()

// nameRe 限定资源名:小写字母开头,只允许小写字母、数字、下划线。
//
// 卡得这么死是因为这个名字同时要当 Go 标识符、proto package、数据表名和目录名。
// 任一处不合法都得等到 buf/sqlc/go build 才报错,那时错误信息已经和 co new
// 隔了三层,很难看出根因。
var nameRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// goKeywords 里的词不能当包名/标识符。表名不受影响,但 proto package
// 和生成的 Go 包名会直接编译不过。
var goKeywords = map[string]bool{
	"break": true, "case": true, "chan": true, "const": true, "continue": true,
	"default": true, "defer": true, "else": true, "fallthrough": true, "for": true,
	"func": true, "go": true, "goto": true, "if": true, "import": true,
	"interface": true, "map": true, "package": true, "range": true, "return": true,
	"select": true, "struct": true, "switch": true, "type": true, "var": true,
}

// Names 是一个资源在各处的写法。所有模板变量都从这里来,
// 避免同一个名字在不同模板里各自推导出不一致的形式。
type Names struct {
	// Name 原始资源名,小写下划线,单数。如 order_item
	Name string
	// Pascal 大驼峰单数。如 OrderItem
	Pascal string
	// PascalPlural 大驼峰复数,用于 List 方法名。如 OrderItems
	PascalPlural string
	// Camel 小驼峰,用于局部变量与私有类型。如 orderItem
	Camel string
	// Table 数据表名,小写下划线复数。如 order_items
	Table string
	// ProtoPackage proto 的 package。如 order_item.v1
	ProtoPackage string
	// APIDir proto 与生成物所在目录,相对 module 根。如 api/order_item/v1
	APIDir string
	// GoPkgAlias protoc-gen-go 生成的包名。如 order_itemv1
	GoPkgAlias string
	// ConnectPkg protoc-gen-connect-go 生成的包名。如 order_itemv1connect
	ConnectPkg string
}

// NewNames 由资源名推导出全部写法。
func NewNames(name string) (Names, error) {
	name = strings.TrimSpace(name)
	if !nameRe.MatchString(name) {
		return Names{}, fmt.Errorf("invalid name %q: must match %s (lower snake_case, e.g. order_item)",
			name, nameRe.String())
	}
	if goKeywords[name] {
		return Names{}, fmt.Errorf("invalid name %q: it is a Go keyword", name)
	}

	// 先转单数再推导:用户写 carts 或 cart 都该得到同一套名字,
	// 否则会生成 CartsService / carts 表这种复数打架的形状。
	singular := pluralizer.Singular(name)
	plural := pluralizer.Plural(singular)

	return Names{
		Name:         singular,
		Pascal:       pascal(singular),
		PascalPlural: pascal(plural),
		Camel:        camel(singular),
		Table:        plural,
		ProtoPackage: singular + ".v1",
		APIDir:       "api/" + singular + "/v1",
		GoPkgAlias:   singular + "v1",
		ConnectPkg:   singular + "v1connect",
	}, nil
}

// pascal 把 order_item 变成 OrderItem。
// 不用 strings.Title(已废弃)也不用 x/text/cases:那个是按语言做标题化的,
// 对 "id" 会给出 "Id" 之外的本地化结果,这里只需要纯 ASCII 的首字母大写。
func pascal(s string) string {
	var b strings.Builder
	for _, part := range strings.Split(s, "_") {
		if part == "" {
			continue
		}
		b.WriteString(strings.ToUpper(part[:1]))
		b.WriteString(part[1:])
	}
	return b.String()
}

func camel(s string) string {
	p := pascal(s)
	if p == "" {
		return p
	}
	return strings.ToLower(p[:1]) + p[1:]
}
