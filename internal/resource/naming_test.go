package resource

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewNames(t *testing.T) {
	cases := []struct {
		in   string
		want Names
	}{
		{
			in: "cart",
			want: Names{
				Name: "cart", Pascal: "Cart", PascalPlural: "Carts", Camel: "cart",
				Table: "carts", ProtoPackage: "cart.v1", APIDir: "api/cart/v1",
				GoPkgAlias: "cartv1", ConnectPkg: "cartv1connect",
			},
		},
		{
			// 用户写复数时得到和单数完全一样的一套,否则会长出 CartsService
			in: "carts",
			want: Names{
				Name: "cart", Pascal: "Cart", PascalPlural: "Carts", Camel: "cart",
				Table: "carts", ProtoPackage: "cart.v1", APIDir: "api/cart/v1",
				GoPkgAlias: "cartv1", ConnectPkg: "cartv1connect",
			},
		},
		{
			in: "order_item",
			want: Names{
				Name: "order_item", Pascal: "OrderItem", PascalPlural: "OrderItems",
				Camel: "orderItem", Table: "order_items", ProtoPackage: "order_item.v1",
				APIDir: "api/order_item/v1", GoPkgAlias: "order_itemv1",
				ConnectPkg: "order_itemv1connect",
			},
		},
		{
			// 不规则复数走 pluralize,不是简单加 s
			in: "category",
			want: Names{
				Name: "category", Pascal: "Category", PascalPlural: "Categories",
				Camel: "category", Table: "categories", ProtoPackage: "category.v1",
				APIDir: "api/category/v1", GoPkgAlias: "categoryv1",
				ConnectPkg: "categoryv1connect",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := NewNames(tc.in)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestNewNamesRejects(t *testing.T) {
	bad := []string{
		"",
		"Cart",      // 大写
		"cart-item", // 连字符不能当 Go 标识符
		"1cart",     // 数字开头
		"cart item", // 空格
		"type",      // Go 关键字
		"func",      // Go 关键字
		"_cart",     // 下划线开头
		"cart.item", // 点
	}
	for _, s := range bad {
		_, err := NewNames(s)
		assert.Error(t, err, "%q 应当被拒绝", s)
	}
}

func TestPascalCamel(t *testing.T) {
	assert.Equal(t, "OrderItem", pascal("order_item"))
	assert.Equal(t, "Cart", pascal("cart"))
	assert.Equal(t, "", pascal(""))
	assert.Equal(t, "OrderItem", pascal("order__item"), "空段应被忽略")

	assert.Equal(t, "orderItem", camel("order_item"))
	assert.Equal(t, "cart", camel("cart"))
	assert.Equal(t, "", camel(""))
}
