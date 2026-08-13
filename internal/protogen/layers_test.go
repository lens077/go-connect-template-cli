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

const cartLikeProto = `syntax = "proto3";

package cart.v1;

option go_package = "github.com/acme/shop/api/cart/v1;cartv1";

import "google/protobuf/struct.proto";
import "google/protobuf/wrappers.proto";
import "google/protobuf/timestamp.proto";

service CartService {
  rpc GetCart(GetCartRequest) returns (GetCartResponse) {}
  rpc AddProductToCart(AddProductToCartRequest) returns (AddProductToCartResponse) {}
  rpc Watch(WatchRequest) returns (stream Event) {}
}

enum CartStatus {
  CART_STATUS_UNKNOWN = 0;
  CART_STATUS_ACTIVE = 1;
}

message AddProductToCartRequest {
  uint64 spu_id = 1;
  string merchant_id = 2;
  google.protobuf.Struct sku_attributes = 3;
  CartStatus status = 4;
}

message AddProductToCartResponse {
  uint32 cart_item_quantity = 1;
}

message GetCartRequest {}

message CartItem {
  uint64 cart_item_id = 1;
  string merchant_id = 2;
  CartStatus status = 3;
}

message GetCartResponse {
  repeated CartItem items = 1;
  google.protobuf.BoolValue is_cart_empty = 2;
  google.protobuf.Timestamp created_at = 3;
}

message WatchRequest {}
message Event {}
`

func TestParseFileMessagesAndEnums(t *testing.T) {
	f, err := ParseFile(writeProto(t, "cart.proto", cartLikeProto))
	require.NoError(t, err)

	require.Len(t, f.Enums, 1)
	assert.Equal(t, "CartStatus", f.Enums[0].Name)
	assert.Equal(t, "CartStatusUnknown", f.Enums[0].Values[0].GoName)
	assert.Equal(t, "CartStatusActive", f.Enums[0].Values[1].GoName)

	item, ok := f.Message("CartItem")
	require.True(t, ok)
	assert.Equal(t, []string{"cart_item_id", "merchant_id", "status"}, fieldNames(item))
	assert.Equal(t, "CartItemId", item.Fields[0].GoName)

	add, ok := f.Message("AddProductToCartRequest")
	require.True(t, ok)
	var sku Field
	for _, field := range add.Fields {
		if field.Name == "sku_attributes" {
			sku = field
		}
	}
	assert.Equal(t, "google.protobuf.Struct", sku.Type)
}

func TestParseFileMapOptionalNested(t *testing.T) {
	const src = `syntax = "proto3";
package shop.v1;
service ShopService { rpc Get(GetRequest) returns (GetReply) {} }
message GetRequest {
  optional string name = 1;
  map<string, Item> items = 2;
  message Inner { string x = 1; }
  Inner inner = 3;
  oneof kind { string a = 4; int32 b = 5; }
}
message Item { string id = 1; }
message GetReply {}
`
	f, err := ParseFile(writeProto(t, "shop.proto", src))
	require.NoError(t, err)

	req, ok := f.Message("GetRequest")
	require.True(t, ok)
	byName := map[string]Field{}
	for _, field := range req.Fields {
		byName[field.Name] = field
	}
	assert.True(t, byName["name"].Optional)
	assert.True(t, byName["items"].IsMap)
	assert.Equal(t, "string", byName["items"].MapKey)
	assert.Equal(t, "Item", byName["items"].MapVal)
	assert.True(t, byName["a"].Optional)
	assert.True(t, byName["b"].Optional)
	assert.Equal(t, "kind", byName["a"].Oneof)
	assert.Equal(t, "kind", byName["b"].Oneof)

	_, ok = f.Message("GetRequest_Inner")
	assert.True(t, ok, "nested message 应展成 Parent_Child")
}

func TestNewLayerSpecRejectsUnsupportedConversions(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{
			name: "oneof",
			body: `message GetRequest { oneof kind { string id = 1; int32 number = 2; } }
message GetReply {}`,
			wantErr: "oneof",
		},
		{
			name: "imported application message",
			body: `message GetRequest { external.v1.Item item = 1; }
message GetReply {}`,
			wantErr: "imported or nested application messages",
		},
		{
			name: "unsupported WKT",
			body: `message GetRequest { google.protobuf.Any payload = 1; }
message GetReply {}`,
			wantErr: "google.protobuf.Any",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := `syntax = "proto3";
package shop.v1;
option go_package = "github.com/acme/shop/api/shop/v1;shopv1";
service ShopService { rpc Get(GetRequest) returns (GetReply) {} }
` + tt.body
			f, err := ParseFile(writeProto(t, "shop.proto", src))
			require.NoError(t, err)
			svc, err := f.Service("")
			require.NoError(t, err)

			_, err = NewLayerSpec(f, svc, "github.com/acme/shop")
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestRenderLayersCartLike(t *testing.T) {
	root := mkModule(t, "github.com/acme/shop")
	dir := filepath.Join(root, "api", "cart", "v1")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	p := filepath.Join(dir, "cart.proto")
	require.NoError(t, os.WriteFile(p, []byte(cartLikeProto), 0o644))

	f, err := ParseFile(p)
	require.NoError(t, err)
	svc, err := f.Service("")
	require.NoError(t, err)

	spec, err := NewLayerSpec(f, svc, "github.com/acme/shop/services/cart")
	require.NoError(t, err)
	assert.Equal(t, "Cart", spec.Domain)
	assert.Equal(t, "cart", spec.Stem)

	files, err := RenderLayers(spec)
	require.NoError(t, err)
	require.Len(t, files, 3)

	byRel := map[string]string{}
	for _, file := range files {
		byRel[filepath.ToSlash(file.Rel)] = string(file.Content)
		_, perr := parser.ParseFile(token.NewFileSet(), file.Rel, file.Content, parser.AllErrors)
		require.NoError(t, perr, file.Rel)
	}

	biz := byRel["internal/biz/cart.go"]
	assert.Contains(t, biz, "CartStatusActive")
	assert.Contains(t, biz, "type CartRepo interface")
	assert.Contains(t, biz, "GetCart(ctx context.Context, req GetCartRequest) (*GetCartResponse, error)")
	assert.Contains(t, biz, "AddProductToCart(ctx context.Context, req AddProductToCartRequest)")
	assert.NotContains(t, biz, "Watch(")
	assert.Contains(t, biz, "SkuAttributes json.RawMessage")
	assert.Contains(t, biz, "[]*CartItem")
	assert.Contains(t, biz, "IsCartEmpty bool")
	assert.Contains(t, biz, "CreatedAt")
	assert.Contains(t, biz, "time.Time")

	data := byRel["internal/data/cart.go"]
	assert.Contains(t, data, "var _ biz.CartRepo = (*cartRepo)(nil)")
	assert.Contains(t, data, "func NewCartRepo(data *Data, logger *zap.Logger) biz.CartRepo")
	assert.Contains(t, data, `"github.com/acme/shop/services/cart/internal/biz"`)
	assert.Contains(t, data, `not implemented`)

	svcSrc := byRel["internal/service/cart.go"]
	assert.Contains(t, svcSrc, "var _ cartv1connect.CartServiceHandler = (*CartService)(nil)")
	assert.Contains(t, svcSrc, "func NewCartService(uc *biz.CartUseCase, log *zap.Logger)")
	assert.Contains(t, svcSrc, "s.uc.GetCart(ctx, toGetCartRequest(c.Msg))")
	assert.Contains(t, svcSrc, "toGetCartResponsePB(out)")
	assert.Contains(t, svcSrc, "func toCartItem(")
	assert.Contains(t, svcSrc, "biz.CartStatus(")
	assert.Contains(t, svcSrc, "json.Marshal")
	assert.Contains(t, svcSrc, "wrapperspb.Bool")
	assert.Contains(t, svcSrc, "timestamppb.New")
	assert.Contains(t, svcSrc, "[]*biz.CartItem")
	assert.Contains(t, svcSrc, "is not implemented")
	assert.NotContains(t, svcSrc, "panic(")
	assert.NotContains(t, svcSrc, "durationpb")
	assert.NotContains(t, svcSrc, "emptypb")

	items := spec.WireItems()
	var anchors []string
	for _, w := range items {
		anchors = append(anchors, w.Anchor)
	}
	assert.Equal(t, []string{
		"biz-providers", "data-providers", "service-providers",
		"server-imports", "server-handler-params", "server-handler-register",
	}, anchors)
	assert.Equal(t, "NewCartUseCase,", items[0].Text)
	assert.Equal(t, "NewCartRepo,", items[1].Text)
	assert.Equal(t, "NewCartService,", items[2].Text)
}

func TestRenderLayersEmptyWKT(t *testing.T) {
	root := mkModule(t, "github.com/acme/shop")
	dir := filepath.Join(root, "api", "ping", "v1")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	const src = `syntax = "proto3";
package ping.v1;
option go_package = "github.com/acme/shop/api/ping/v1;pingv1";
import "google/protobuf/empty.proto";
service PingService {
  rpc Ping(google.protobuf.Empty) returns (google.protobuf.Empty) {}
}
`
	p := filepath.Join(dir, "ping.proto")
	require.NoError(t, os.WriteFile(p, []byte(src), 0o644))

	f, err := ParseFile(p)
	require.NoError(t, err)
	svc, err := f.Service("")
	require.NoError(t, err)
	spec, err := NewLayerSpec(f, svc, "m/services/ping")
	require.NoError(t, err)

	files, err := RenderLayers(spec)
	require.NoError(t, err)
	var biz, service string
	for _, file := range files {
		_, perr := parser.ParseFile(token.NewFileSet(), file.Rel, file.Content, parser.AllErrors)
		require.NoError(t, perr, file.Rel)
		switch filepath.Base(file.Rel) {
		case "ping.go":
			if filepath.Base(filepath.Dir(file.Rel)) == "biz" {
				biz = string(file.Content)
			}
			if filepath.Base(filepath.Dir(file.Rel)) == "service" {
				service = string(file.Content)
			}
		}
	}
	assert.Contains(t, biz, "Ping(ctx context.Context) error")
	assert.NotContains(t, biz, "type Empty struct")
	assert.Contains(t, service, "emptypb.Empty")
	assert.Contains(t, service, "s.uc.Ping(ctx)")
	assert.NotContains(t, service, "encoding/json")
	assert.NotContains(t, service, "durationpb")
	assert.NotContains(t, service, "structpb")
	assert.NotContains(t, service, "timestamppb")
	assert.NotContains(t, service, "wrapperspb")
}

func TestRenderLayersStreamOnly(t *testing.T) {
	root := mkModule(t, "github.com/acme/shop")
	dir := filepath.Join(root, "api", "chat", "v1")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	const src = `syntax = "proto3";
package chat.v1;
option go_package = "github.com/acme/shop/api/chat/v1;chatv1";
service ChatService { rpc Watch(WatchRequest) returns (stream Event) {} }
message WatchRequest {}
message Event {}
`
	p := filepath.Join(dir, "chat.proto")
	require.NoError(t, os.WriteFile(p, []byte(src), 0o644))

	f, err := ParseFile(p)
	require.NoError(t, err)
	svc, err := f.Service("")
	require.NoError(t, err)
	spec, err := NewLayerSpec(f, svc, "github.com/acme/shop")
	require.NoError(t, err)

	files, err := RenderLayers(spec)
	require.NoError(t, err)
	for _, file := range files {
		_, perr := parser.ParseFile(token.NewFileSet(), file.Rel, file.Content, parser.AllErrors)
		require.NoError(t, perr, file.Rel)
		if filepath.Base(filepath.Dir(file.Rel)) != "service" {
			assert.NotContains(t, string(file.Content), `"context"`)
			assert.NotContains(t, string(file.Content), `"fmt"`)
		}
	}
}

func TestEnumValueGoName(t *testing.T) {
	assert.Equal(t, "CartStatusActive", enumValueGoName("CartStatus", "CART_STATUS_ACTIVE"))
	assert.Equal(t, "CartStatusUnknown", enumValueGoName("CartStatus", "CART_STATUS_UNKNOWN"))
}

func fieldNames(m Message) []string {
	var out []string
	for _, f := range m.Fields {
		out = append(out, f.Name)
	}
	return out
}
