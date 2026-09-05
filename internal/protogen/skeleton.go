package protogen

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/lens077/go-connect-template-cli/internal/resource"
)

// SkeletonSpec 描述要新建的 proto 文件。
type SkeletonSpec struct {
	// Path 目标文件路径,形如 api/order/v1/order.proto
	Path string
	// Package proto package,如 order.v1
	Package string
	// GoPackage go_package 选项的完整值,含 ;alias
	GoPackage string
	// Service service 名,如 OrderService
	Service string
	// Names 资源各种写法,模板里用来拼 Create/List 等方法名
	Names resource.Names
}

// NewSkeletonSpec 由目标路径推出 package / go_package / service 名。
//
// 约定路径形如 <dir>/<name>/<version>/<name>.proto —— 这是模板与 buf
// managed mode 都依赖的布局,不满足时直接报错而不是猜,猜错了生成的 proto
// 与 buf 的输出目录对不上,go_package 也会指向不存在的包。
func NewSkeletonSpec(path string) (SkeletonSpec, error) {
	path = filepath.ToSlash(filepath.Clean(path))
	if !strings.HasSuffix(path, ".proto") {
		return SkeletonSpec{}, fmt.Errorf("%q is not a .proto path", path)
	}

	parts := strings.Split(path, "/")
	if len(parts) < 3 {
		return SkeletonSpec{}, fmt.Errorf("%q is too shallow; expected <dir>/<name>/<version>/<name>.proto, e.g. api/order/v1/order.proto", path)
	}
	version := parts[len(parts)-2]
	name := parts[len(parts)-3]

	if !strings.HasPrefix(version, "v") {
		return SkeletonSpec{}, fmt.Errorf("%q: expected a version directory (v1, v2...) above the file, got %q", path, version)
	}

	names, err := resource.NewNames(name)
	if err != nil {
		return SkeletonSpec{}, err
	}

	dir := strings.Join(parts[:len(parts)-1], "/")
	imp, err := importPathOf(dir)
	if err != nil {
		return SkeletonSpec{}, err
	}

	pkg := names.Name + "." + version
	return SkeletonSpec{
		Path:      path,
		Package:   pkg,
		GoPackage: imp + ";" + strings.ReplaceAll(pkg, ".", ""),
		Service:   names.Pascal + "Service",
		Names:     names,
	}, nil
}

var skeletonTmpl = template.Must(template.New("proto").Parse(`syntax = "proto3";

package {{.Package}};

option go_package = "{{.GoPackage}}";

import "google/protobuf/timestamp.proto";

service {{.Service}} {
  rpc Create{{.Names.Pascal}}(Create{{.Names.Pascal}}Request) returns (Create{{.Names.Pascal}}Reply) {}
  rpc Get{{.Names.Pascal}}(Get{{.Names.Pascal}}Request) returns (Get{{.Names.Pascal}}Reply) {}
  rpc List{{.Names.PascalPlural}}(List{{.Names.PascalPlural}}Request) returns (List{{.Names.PascalPlural}}Reply) {}
  rpc Update{{.Names.Pascal}}(Update{{.Names.Pascal}}Request) returns (Update{{.Names.Pascal}}Reply) {}
  rpc Delete{{.Names.Pascal}}(Delete{{.Names.Pascal}}Request) returns (Delete{{.Names.Pascal}}Reply) {}
}

message {{.Names.Pascal}} {
  string id = 1;
  google.protobuf.Timestamp created_at = 2;
  google.protobuf.Timestamp updated_at = 3;
}

message Create{{.Names.Pascal}}Request {}
message Create{{.Names.Pascal}}Reply {
  {{.Names.Pascal}} {{.Names.Name}} = 1;
}

message Get{{.Names.Pascal}}Request {
  string id = 1;
}
message Get{{.Names.Pascal}}Reply {
  {{.Names.Pascal}} {{.Names.Name}} = 1;
}

// 分页用 page/page_size 而不是 offset/limit:两者等价,但前者是这套服务
// 里既有的写法,换一种会让前端在不同接口间来回换算。
message List{{.Names.PascalPlural}}Request {
  uint32 page = 1;
  uint32 page_size = 2;
}
message List{{.Names.PascalPlural}}Reply {
  repeated {{.Names.Pascal}} {{.Names.Table}} = 1;
  uint32 total = 2;
}

message Update{{.Names.Pascal}}Request {
  string id = 1;
}
message Update{{.Names.Pascal}}Reply {
  {{.Names.Pascal}} {{.Names.Name}} = 1;
}

message Delete{{.Names.Pascal}}Request {
  string id = 1;
}
message Delete{{.Names.Pascal}}Reply {}
`))

// RenderSkeleton 渲染一个新的 .proto。
func RenderSkeleton(spec SkeletonSpec) ([]byte, error) {
	var b bytes.Buffer
	if err := skeletonTmpl.Execute(&b, spec); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
