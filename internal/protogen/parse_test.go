package protogen

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeProto(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	return p
}

const simpleProto = `syntax = "proto3";

package order.v1;

option go_package = "github.com/acme/shop/api/order/v1;orderv1";

service OrderService {
  rpc CreateOrder(CreateOrderRequest) returns (CreateOrderReply) {}
  rpc GetOrder(GetOrderRequest) returns (GetOrderReply) {}
}

message CreateOrderRequest {}
message CreateOrderReply {}
message GetOrderRequest {}
message GetOrderReply {}
`

func TestParseFile(t *testing.T) {
	f, err := ParseFile(writeProto(t, "order.proto", simpleProto))
	require.NoError(t, err)

	assert.Equal(t, "order.v1", f.Package)
	// go_package 的 ";orderv1" 是别名,不属于导入路径
	assert.Equal(t, "github.com/acme/shop/api/order/v1", f.GoPackage)
	assert.Equal(t, "orderv1connect", f.ConnectPkg())

	require.Len(t, f.Services, 1)
	assert.Equal(t, "OrderService", f.Services[0].Name)
	assert.Equal(t, []Method{
		{Name: "CreateOrder", Request: "CreateOrderRequest", Response: "CreateOrderReply", RequestType: "CreateOrderRequest", ResponseType: "CreateOrderReply"},
		{Name: "GetOrder", Request: "GetOrderRequest", Response: "GetOrderReply", RequestType: "GetOrderRequest", ResponseType: "GetOrderReply"},
	}, f.Services[0].Methods)
}

// rpc 带花括号选项体是旧正则解析器的死穴:`([^}]+)` 在 option 那一行的 `}`
// 处就截断了,后面的方法全部静默丢失。这里锁死「一个都不能少」。
func TestParseFileRPCWithOptionBody(t *testing.T) {
	const src = `syntax = "proto3";
package order.v1;
option go_package = "github.com/acme/shop/api/order/v1;orderv1";

import "google/api/annotations.proto";

service OrderService {
  rpc CreateOrder(CreateOrderRequest) returns (CreateOrderReply) {
    option (google.api.http) = {
      post: "/v1/orders"
      body: "*"
    };
  }
  rpc GetOrder(GetOrderRequest) returns (GetOrderReply) {
    option (google.api.http) = { get: "/v1/orders/{id}" };
  }
  rpc ListOrders(ListOrdersRequest) returns (ListOrdersReply) {}
}
`
	f, err := ParseFile(writeProto(t, "order.proto", src))
	require.NoError(t, err)

	svc, err := f.Service("")
	require.NoError(t, err)

	var names []string
	for _, m := range svc.Methods {
		names = append(names, m.Name)
	}
	assert.Equal(t, []string{"CreateOrder", "GetOrder", "ListOrders"}, names)
}

func TestParseFileStreaming(t *testing.T) {
	const src = `syntax = "proto3";
package chat.v1;
service ChatService {
  rpc Unary(Req) returns (Rep) {}
  rpc Upload(stream Req) returns (Rep) {}
  rpc Watch(Req) returns (stream Rep) {}
  rpc Session(stream Req) returns (stream Rep) {}
}
`
	f, err := ParseFile(writeProto(t, "chat.proto", src))
	require.NoError(t, err)

	svc, err := f.Service("")
	require.NoError(t, err)
	assert.Equal(t, []Method{
		{Name: "Unary", Request: "Req", Response: "Rep", RequestType: "Req", ResponseType: "Rep"},
		{Name: "Upload", Request: "Req", Response: "Rep", RequestType: "Req", ResponseType: "Rep", ClientStream: true},
		{Name: "Watch", Request: "Req", Response: "Rep", RequestType: "Req", ResponseType: "Rep", ServerStream: true},
		{Name: "Session", Request: "Req", Response: "Rep", RequestType: "Req", ResponseType: "Rep", ClientStream: true, ServerStream: true},
	}, svc.Methods)
}

func TestParseFileQualifiedTypes(t *testing.T) {
	// 跨包引用要剥掉包限定,生成的 handler 里统一用 v1. 前缀
	const src = `syntax = "proto3";
package order.v1;
import "google/protobuf/empty.proto";
service OrderService {
  rpc Ping(.order.v1.PingRequest) returns (google.protobuf.Empty) {}
}
`
	f, err := ParseFile(writeProto(t, "order.proto", src))
	require.NoError(t, err)

	svc, err := f.Service("")
	require.NoError(t, err)
	assert.Equal(t, "PingRequest", svc.Methods[0].Request)
	assert.Equal(t, "Empty", svc.Methods[0].Response)
}

func TestParseFileNoGoPackage(t *testing.T) {
	f, err := ParseFile(writeProto(t, "a.proto", "syntax = \"proto3\";\npackage a.v1;\nservice S { rpc M(R) returns (P) {} }\n"))
	require.NoError(t, err)
	assert.Empty(t, f.GoPackage)
	assert.Equal(t, "av1connect", f.ConnectPkg())
}

func TestParseFileErrors(t *testing.T) {
	t.Run("文件不存在", func(t *testing.T) {
		_, err := ParseFile(filepath.Join(t.TempDir(), "nope.proto"))
		require.Error(t, err)
	})

	t.Run("语法坏掉时报错而不是当作零个 service", func(t *testing.T) {
		_, err := ParseFile(writeProto(t, "bad.proto", "syntax = \"proto3\"\npackage ;;;\nservice {\n"))
		require.Error(t, err)
	})
}

func TestFileService(t *testing.T) {
	const multi = `syntax = "proto3";
package order.v1;
service OrderService { rpc A(R) returns (P) {} }
service AdminService { rpc B(R) returns (P) {} }
`
	f, err := ParseFile(writeProto(t, "order.proto", multi))
	require.NoError(t, err)

	t.Run("多个 service 且没指定时报错,并列出可选项", func(t *testing.T) {
		_, err := f.Service("")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "OrderService")
		assert.Contains(t, err.Error(), "AdminService")
		assert.Contains(t, err.Error(), "--service")
	})

	t.Run("按名字取", func(t *testing.T) {
		svc, err := f.Service("AdminService")
		require.NoError(t, err)
		assert.Equal(t, "AdminService", svc.Name)
	})

	t.Run("名字不存在时报错", func(t *testing.T) {
		_, err := f.Service("GhostService")
		require.Error(t, err)
	})

	t.Run("一个 service 都没有时报错", func(t *testing.T) {
		empty, err := ParseFile(writeProto(t, "e.proto", "syntax = \"proto3\";\npackage e.v1;\nmessage M {}\n"))
		require.NoError(t, err)
		_, err = empty.Service("")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no service")
	})
}

func TestGoImportPathAndBaseType(t *testing.T) {
	assert.Equal(t, "a/b/c", goImportPath("a/b/c;cv1"))
	assert.Equal(t, "a/b/c", goImportPath("a/b/c"))
	assert.Equal(t, "", goImportPath(";alias"))

	assert.Equal(t, "Reply", baseType(".order.v1.Reply"))
	assert.Equal(t, "Reply", baseType("Reply"))

	assert.Equal(t, `a"b`, unquote(`"a"b"`))
	assert.Equal(t, "x", unquote("'x'"))
	assert.Equal(t, "x", unquote("x"))
}
