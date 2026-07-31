package protogen

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mkModule 造一个带 go.mod 的临时仓库,返回根目录。
// importPathOf 靠向上找 go.mod 推导包路径,没有它就没法测 go_package 缺失的分支。
func mkModule(t *testing.T, module string) string {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"),
		[]byte("module "+module+"\n\ngo 1.26.1\n"), 0o644))
	return root
}

func TestNewServerSpec(t *testing.T) {
	f := File{
		Path:      "api/order/v1/order.proto",
		Package:   "order.v1",
		GoPackage: "github.com/acme/shop/api/order/v1",
	}
	spec, err := NewServerSpec(f, Service{Name: "OrderService"}, "service")
	require.NoError(t, err)

	assert.Equal(t, "github.com/acme/shop/api/order/v1", spec.PBImport)
	assert.Equal(t, "github.com/acme/shop/api/order/v1/orderv1connect", spec.ConnectImport)
	assert.Equal(t, "orderv1connect", spec.ConnectPkg)
	assert.Equal(t, "service", spec.Package)
}

func TestNewServerSpecInfersImport(t *testing.T) {
	root := mkModule(t, "github.com/acme/shop")
	dir := filepath.Join(root, "api", "order", "v1")
	require.NoError(t, os.MkdirAll(dir, 0o755))

	f := File{Path: filepath.Join(dir, "order.proto"), Package: "order.v1"}
	spec, err := NewServerSpec(f, Service{Name: "OrderService"}, "")
	require.NoError(t, err)

	assert.Equal(t, "github.com/acme/shop/api/order/v1", spec.PBImport)
	assert.Equal(t, "service", spec.Package, "pkg 为空时退到 service")
}

func TestNewServerSpecNoModule(t *testing.T) {
	// 既没有 go_package 又找不到 go.mod 时必须报错。
	// 静默用空导入路径生成出来的文件,错误信息会指向 import "" 而不是根因。
	dir := filepath.Join(t.TempDir(), "api", "order", "v1")
	require.NoError(t, os.MkdirAll(dir, 0o755))

	_, err := NewServerSpec(File{Path: filepath.Join(dir, "order.proto")}, Service{Name: "S"}, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "go_package")
}

func TestRenderServer(t *testing.T) {
	spec := ServerSpec{
		Service: Service{
			Name: "OrderService",
			Methods: []Method{
				{Name: "CreateOrder", Request: "CreateOrderRequest", Response: "CreateOrderReply"},
				{Name: "Upload", Request: "Chunk", Response: "UploadReply", ClientStream: true},
				{Name: "Watch", Request: "WatchRequest", Response: "Event", ServerStream: true},
				{Name: "Session", Request: "In", Response: "Out", ClientStream: true, ServerStream: true},
			},
		},
		PBImport:      "github.com/acme/shop/api/order/v1",
		ConnectImport: "github.com/acme/shop/api/order/v1/orderv1connect",
		ConnectPkg:    "orderv1connect",
		Package:       "service",
	}

	out, err := RenderServer(spec)
	require.NoError(t, err)
	src := string(out)

	// RenderServer 内部走 format.Source,这里再解析一次确认结构完整
	_, perr := parser.ParseFile(token.NewFileSet(), "order_service.go", src, parser.AllErrors)
	require.NoError(t, perr)

	assert.Contains(t, src, "package service\n")
	assert.Contains(t, src, `v1 "github.com/acme/shop/api/order/v1"`)
	// 接口断言:handler 少实现一个方法时在编译期就炸,而不是注册时
	assert.Contains(t, src, "var _ orderv1connect.OrderServiceHandler = (*OrderService)(nil)")
	assert.Contains(t, src, "func NewOrderService() orderv1connect.OrderServiceHandler {")

	assert.Contains(t, src, "func (o *OrderService) CreateOrder(ctx context.Context, req *connect.Request[v1.CreateOrderRequest]) (*connect.Response[v1.CreateOrderReply], error)")
	assert.Contains(t, src, "func (o *OrderService) Upload(ctx context.Context, stream *connect.ClientStream[v1.Chunk]) (*connect.Response[v1.UploadReply], error)")
	assert.Contains(t, src, "func (o *OrderService) Watch(ctx context.Context, req *connect.Request[v1.WatchRequest], stream *connect.ServerStream[v1.Event]) error")
	assert.Contains(t, src, "func (o *OrderService) Session(ctx context.Context, stream *connect.BidiStream[v1.In, v1.Out]) error")

	// 未实现的方法返回错误而不是 panic —— panic 会带走整个服务进程
	assert.Contains(t, src, `connect.NewError(connect.CodeUnimplemented, errors.New("OrderService.CreateOrder is not implemented"))`)
	assert.NotContains(t, src, "panic(")
}

func TestRenderServerNoMethods(t *testing.T) {
	// 空 service 也得生成得出编译得过的文件:context/errors/connect/v1
	// 一个都用不上,照写就是四个 "imported and not used"
	out, err := RenderServer(ServerSpec{
		Service:       Service{Name: "EmptyService"},
		PBImport:      "m/api/a/v1",
		ConnectImport: "m/api/a/v1/av1connect",
		ConnectPkg:    "av1connect",
		Package:       "service",
	})
	require.NoError(t, err)
	src := string(out)

	assert.Contains(t, src, "type EmptyService struct{}")
	assert.Contains(t, src, `"m/api/a/v1/av1connect"`)
	assert.NotContains(t, src, `"context"`)
	assert.NotContains(t, src, `"errors"`)
	assert.NotContains(t, src, `"connectrpc.com/connect"`)
	assert.NotContains(t, src, `v1 "m/api/a/v1"`)
}

func TestNewSkeletonSpec(t *testing.T) {
	root := mkModule(t, "github.com/acme/shop")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "api", "order", "v1"), 0o755))

	wd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() { _ = os.Chdir(wd) })

	spec, err := NewSkeletonSpec("api/order/v1/order.proto")
	require.NoError(t, err)

	assert.Equal(t, "order.v1", spec.Package)
	assert.Equal(t, "github.com/acme/shop/api/order/v1;orderv1", spec.GoPackage)
	assert.Equal(t, "OrderService", spec.Service)
	assert.Equal(t, "orders", spec.Names.Table)
}

func TestNewSkeletonSpecRejects(t *testing.T) {
	cases := []struct {
		name string
		path string
	}{
		{"不是 .proto", "api/order/v1/order.txt"},
		{"层级太浅", "order.proto"},
		{"版本目录名不以 v 开头", "api/order/proto/order.proto"},
		{"资源名不是合法标识符", "api/Order/v1/Order.proto"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewSkeletonSpec(tc.path)
			require.Error(t, err)
		})
	}
}

func TestRenderSkeleton(t *testing.T) {
	root := mkModule(t, "github.com/acme/shop")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "api", "category", "v1"), 0o755))

	wd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() { _ = os.Chdir(wd) })

	spec, err := NewSkeletonSpec("api/category/v1/category.proto")
	require.NoError(t, err)

	out, err := RenderSkeleton(spec)
	require.NoError(t, err)

	// 渲染出来的东西得能被自己的解析器读回去 —— 这是最省事的语法检查
	p := filepath.Join(root, "api", "category", "v1", "category.proto")
	require.NoError(t, os.WriteFile(p, out, 0o644))

	f, err := ParseFile(p)
	require.NoError(t, err)
	assert.Equal(t, "category.v1", f.Package)
	assert.Equal(t, "github.com/acme/shop/api/category/v1", f.GoPackage)

	svc, err := f.Service("")
	require.NoError(t, err)
	var names []string
	for _, m := range svc.Methods {
		names = append(names, m.Name)
	}
	// 复数走 pluralize,List 用的是 Categories 而不是 Categorys
	assert.Equal(t, []string{
		"CreateCategory", "GetCategory", "ListCategories", "UpdateCategory", "DeleteCategory",
	}, names)
}
