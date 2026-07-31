// Package protogen 读写单个 .proto 文件:生成骨架,以及按已有 service
// 生成 ConnectRPC handler。
//
// 解析走 go-protoparser 而不是正则。正则版本(旧 CLI 的 parseProtoService)
// 用 `service\s+(\w+)\s*\{([^}]+)\}` 匹配,遇到 rpc 带花括号选项体
// (`rpc Foo(Bar) returns (Baz) { option ...; }`)时,`[^}]+` 会在第一个 `}`
// 处截断,后面的方法全部丢失 —— 而且不报错,只是少生成几个方法。
package protogen

import (
	"fmt"
	"os"
	"strings"

	protoparser "github.com/yoheimuta/go-protoparser/v4"
	"github.com/yoheimuta/go-protoparser/v4/parser"
)

// Method 是一个 rpc。
type Method struct {
	Name string
	// Request/Response 是去掉包限定后的消息名,直接可拼成 v1.XxxRequest
	Request  string
	Response string
	// 流式方法的 handler 签名与一元方法完全不同,单独标出来
	ClientStream bool
	ServerStream bool
}

// Service 是一个 service 块。
type Service struct {
	Name    string
	Methods []Method
}

// File 是一个 .proto 文件里 co 关心的部分。
type File struct {
	// Path 文件路径
	Path string
	// Package proto 的 package,如 order.v1
	Package string
	// GoPackage go_package 选项里分号前的导入路径(没写则为空)
	GoPackage string
	Services  []Service
}

// ParseFile 读取并解析一个 .proto。
func ParseFile(path string) (File, error) {
	f, err := os.Open(path)
	if err != nil {
		return File{}, err
	}
	defer f.Close()

	// Permissive 模式容忍非规范但 protoc 接受的写法(如尾随逗号)。
	// 这里的目的是读出 service,不是当 linter,严格解析只会平白拒掉能编译的文件。
	got, err := protoparser.Parse(f,
		protoparser.WithFilename(path),
		protoparser.WithPermissive(true),
	)
	if err != nil {
		return File{}, fmt.Errorf("parse %s: %w", path, err)
	}

	out := File{Path: path}
	for _, v := range got.ProtoBody {
		switch e := v.(type) {
		case *parser.Package:
			out.Package = e.Name
		case *parser.Option:
			if e.OptionName == "go_package" {
				out.GoPackage = goImportPath(unquote(e.Constant))
			}
		case *parser.Service:
			out.Services = append(out.Services, convertService(e))
		}
	}
	return out, nil
}

func convertService(s *parser.Service) Service {
	svc := Service{Name: s.ServiceName}
	for _, v := range s.ServiceBody {
		rpc, ok := v.(*parser.RPC)
		if !ok {
			continue
		}
		svc.Methods = append(svc.Methods, Method{
			Name:         rpc.RPCName,
			Request:      baseType(rpc.RPCRequest.MessageType),
			Response:     baseType(rpc.RPCResponse.MessageType),
			ClientStream: rpc.RPCRequest.IsStream,
			ServerStream: rpc.RPCResponse.IsStream,
		})
	}
	return svc
}

// Service 按名字取一个 service;name 为空时取第一个。
func (f File) Service(name string) (Service, error) {
	if len(f.Services) == 0 {
		return Service{}, fmt.Errorf("%s declares no service", f.Path)
	}
	if name == "" {
		if len(f.Services) > 1 {
			var names []string
			for _, s := range f.Services {
				names = append(names, s.Name)
			}
			return Service{}, fmt.Errorf("%s declares %d services (%s); pick one with --service",
				f.Path, len(f.Services), strings.Join(names, ", "))
		}
		return f.Services[0], nil
	}
	for _, s := range f.Services {
		if s.Name == name {
			return s, nil
		}
	}
	return Service{}, fmt.Errorf("no service %q in %s", name, f.Path)
}

// ConnectPkg 是 protoc-gen-connect-go 为该文件生成的包名:
// proto package 去掉点再加 connect,如 order.v1 -> orderv1connect。
func (f File) ConnectPkg() string {
	return strings.ReplaceAll(f.Package, ".", "") + "connect"
}

// baseType 去掉消息类型的包限定与前导点:.order.v1.GetReply -> GetReply。
func baseType(t string) string {
	if i := strings.LastIndex(t, "."); i >= 0 {
		return t[i+1:]
	}
	return t
}

// goImportPath 取 go_package 里分号前的部分。
// go_package 的写法是 "import/path;alias",别名部分不是导入路径的一部分。
func goImportPath(s string) string {
	if i := strings.Index(s, ";"); i >= 0 {
		return s[:i]
	}
	return s
}

func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}
