package protogen

import (
	"bytes"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/mod/modfile"
)

// ServerSpec 是渲染一个 handler 骨架所需的全部信息。
type ServerSpec struct {
	Service Service
	// PBImport protoc-gen-go 生成物的导入路径
	PBImport string
	// ConnectImport protoc-gen-connect-go 生成物的导入路径
	ConnectImport string
	// ConnectPkg connect 生成物的包名
	ConnectPkg string
	// Package 目标文件的 Go 包名
	Package string
}

// NewServerSpec 由 proto 文件推出 handler 的导入路径。
func NewServerSpec(f File, svc Service, pkg string) (ServerSpec, error) {
	pb := f.GoPackage
	if pb == "" {
		// 没写 go_package 时按「module 路径 + proto 所在相对目录」推。
		// buf 的 managed mode 也是这么算的,两边结果一致。
		var err error
		if pb, err = importPathOf(filepath.Dir(f.Path)); err != nil {
			return ServerSpec{}, fmt.Errorf("%s has no go_package option and it cannot be inferred: %w", f.Path, err)
		}
	}
	if pkg == "" {
		pkg = "service"
	}
	return ServerSpec{
		Service:       svc,
		PBImport:      pb,
		ConnectImport: pb + "/" + f.ConnectPkg(),
		ConnectPkg:    f.ConnectPkg(),
		Package:       pkg,
	}, nil
}

// RenderServer 生成一个编译得过的 handler 骨架。
//
// 刻意不注入 biz.XxxUseCase:proto 里没有任何信息能保证那个 UseCase 存在,
// 猜错了生成的就是编译不过的代码。需要连好 biz/data 的完整一套时用
// co resource add —— 那条路径上 biz 是和 handler 一起生成的。
func RenderServer(spec ServerSpec) ([]byte, error) {
	recv := strings.ToLower(spec.Service.Name[:1])
	var b bytes.Buffer

	fmt.Fprintf(&b, "package %s\n\n", spec.Package)
	// 没有方法时 context/errors/connect/v1 一个都用不上,照写会是四个
	// 「imported and not used」编译错误 —— 空 service 虽然少见,但生成的
	// 东西必须无条件编译得过,否则用户第一反应是 co 坏了。
	b.WriteString("import (\n")
	if len(spec.Service.Methods) > 0 {
		fmt.Fprintf(&b, "\t\"context\"\n\t\"errors\"\n\n\t\"connectrpc.com/connect\"\n\tv1 %q\n", spec.PBImport)
	}
	fmt.Fprintf(&b, "\t%q\n)\n\n", spec.ConnectImport)

	fmt.Fprintf(&b, "var _ %s.%sHandler = (*%s)(nil)\n\n", spec.ConnectPkg, spec.Service.Name, spec.Service.Name)
	fmt.Fprintf(&b, "type %s struct{}\n\n", spec.Service.Name)
	fmt.Fprintf(&b, "func New%s() %s.%sHandler {\n\treturn &%s{}\n}\n",
		spec.Service.Name, spec.ConnectPkg, spec.Service.Name, spec.Service.Name)

	for _, m := range spec.Service.Methods {
		b.WriteString("\n")
		writeMethod(&b, recv, spec.Service.Name, m)
	}

	// 格式化顺带当语法检查:拼串拼错了会在这儿报,而不是等到用户 go build
	out, err := format.Source(b.Bytes())
	if err != nil {
		return nil, fmt.Errorf("generated code is not valid Go: %w", err)
	}
	return out, nil
}

func writeMethod(b *bytes.Buffer, recv, svc string, m Method) {
	switch {
	case m.ClientStream && m.ServerStream:
		fmt.Fprintf(b, "func (%s *%s) %s(ctx context.Context, stream *connect.BidiStream[v1.%s, v1.%s]) error {\n",
			recv, svc, m.Name, m.Request, m.Response)
		fmt.Fprintf(b, "\treturn %s\n}\n", errValue(svc, m.Name))
	case m.ClientStream:
		fmt.Fprintf(b, "func (%s *%s) %s(ctx context.Context, stream *connect.ClientStream[v1.%s]) (*connect.Response[v1.%s], error) {\n",
			recv, svc, m.Name, m.Request, m.Response)
		fmt.Fprintf(b, "\treturn nil, %s\n}\n", errValue(svc, m.Name))
	case m.ServerStream:
		fmt.Fprintf(b, "func (%s *%s) %s(ctx context.Context, req *connect.Request[v1.%s], stream *connect.ServerStream[v1.%s]) error {\n",
			recv, svc, m.Name, m.Request, m.Response)
		fmt.Fprintf(b, "\treturn %s\n}\n", errValue(svc, m.Name))
	default:
		fmt.Fprintf(b, "func (%s *%s) %s(ctx context.Context, req *connect.Request[v1.%s]) (*connect.Response[v1.%s], error) {\n",
			recv, svc, m.Name, m.Request, m.Response)
		fmt.Fprintf(b, "\treturn nil, %s\n}\n", errValue(svc, m.Name))
	}
}

// errValue 返回一个 Unimplemented 错误。
// 用返回错误而不是 panic:handler 跑在服务进程里,panic 会带走整个进程,
// 而没实现的方法本该只让这一次调用失败。
func errValue(svc, method string) string {
	return fmt.Sprintf("connect.NewError(connect.CodeUnimplemented, errors.New(%q))", svc+"."+method+" is not implemented")
}

// importPathOf 由 dir 上方最近的 go.mod 推出该目录的导入路径。
func importPathOf(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	root, mod, err := findModule(abs)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return "", err
	}
	if rel == "." {
		return mod, nil
	}
	return mod + "/" + filepath.ToSlash(rel), nil
}

// findModule 从 start 往上找 go.mod,返回其目录与 module 路径。
func findModule(start string) (root, module string, err error) {
	dir := start
	for {
		p := filepath.Join(dir, "go.mod")
		if data, rerr := os.ReadFile(p); rerr == nil {
			m := modfile.ModulePath(data)
			if m == "" {
				return "", "", fmt.Errorf("%s has no module directive", p)
			}
			return dir, m, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", "", fmt.Errorf("no go.mod at or above %s", start)
		}
		dir = parent
	}
}
