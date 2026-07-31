package scaffold

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const sampleGoMod = `module github.com/lens077/go-connect-template

go 1.26.1

require (
	github.com/jackc/pgx/v5 v5.7.6
	github.com/redis/go-redis/v9 v9.7.0
	go.uber.org/fx v1.24.0
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
)
`

func writeGoMod(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "go.mod")
	require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	return p
}

func TestRewriteGoMod(t *testing.T) {
	t.Run("改 module 路径并删掉指定依赖", func(t *testing.T) {
		p := writeGoMod(t, sampleGoMod)
		require.NoError(t, RewriteGoMod(p, "github.com/acme/shop",
			[]string{"github.com/redis/go-redis/v9"}))

		got, err := os.ReadFile(p)
		require.NoError(t, err)
		s := string(got)

		assert.Contains(t, s, "module github.com/acme/shop")
		assert.NotContains(t, s, "go-redis")
		assert.Contains(t, s, "github.com/jackc/pgx/v5")
		assert.Contains(t, s, "go.uber.org/fx")
	})

	t.Run("删不存在的依赖不算错 —— feature 可能本来就没声明这条", func(t *testing.T) {
		p := writeGoMod(t, sampleGoMod)
		require.NoError(t, RewriteGoMod(p, "github.com/acme/shop",
			[]string{"github.com/nope/nope"}))

		mod, err := ReadModulePath(p)
		require.NoError(t, err)
		assert.Equal(t, "github.com/acme/shop", mod)
	})

	t.Run("indirect 块不受影响", func(t *testing.T) {
		p := writeGoMod(t, sampleGoMod)
		require.NoError(t, RewriteGoMod(p, "github.com/acme/shop", nil))

		got, err := os.ReadFile(p)
		require.NoError(t, err)
		assert.Contains(t, string(got), "github.com/cespare/xxhash/v2")
	})

	t.Run("go.mod 不存在时报错", func(t *testing.T) {
		err := RewriteGoMod(filepath.Join(t.TempDir(), "go.mod"), "github.com/acme/shop", nil)
		require.Error(t, err)
	})
}

func TestReadModulePath(t *testing.T) {
	p := writeGoMod(t, sampleGoMod)
	mod, err := ReadModulePath(p)
	require.NoError(t, err)
	assert.Equal(t, "github.com/lens077/go-connect-template", mod)
}

func TestValidateModulePath(t *testing.T) {
	ok := []string{
		"github.com/acme/shop",
		"example.com/a/b/c",
		"github.com/acme/ecommerce/backend/services/cart",
	}
	for _, s := range ok {
		assert.NoError(t, ValidateModulePath(s), s)
	}

	bad := []string{
		"",
		"github.com/acme/shop/", // 尾斜杠
		"has space/x",
		"github.com//acme",
	}
	for _, s := range bad {
		assert.Error(t, ValidateModulePath(s), s)
	}
}
