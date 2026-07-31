package resource

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNextSchemaSeqFrom(t *testing.T) {
	cases := []struct {
		name  string
		files []string
		want  string
	}{
		{"空目录", nil, "0001"},
		{"接着最大的往下排", []string{"0001_products.sql", "0002_orders.sql"}, "0003"},
		{"按数值而不是字典序取最大", []string{"0009_a.sql", "0010_b.sql"}, "0011"},
		{"有洞时不填洞", []string{"0001_a.sql", "0005_b.sql"}, "0006"},
		{"非 .sql 文件不算数", []string{"0007_notes.md", "0001_a.sql"}, "0002"},
		{
			// 旧版生成的文件没有前缀,在字典序里排在所有 000N_ 之后 ——
			// 它没有可依赖的位置,算进来只会凭空跳号
			"无前缀的文件不参与计数",
			[]string{"0001_products.sql", "demos.sql"},
			"0002",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, NextSchemaSeqFrom(tc.files))
		})
	}
}

func TestNextSchemaSeqPadsToFourDigits(t *testing.T) {
	// 补零是为了让字典序等于数值序 —— sqlc 按文件名排序读 schema,
	// 不补零的话 10_x.sql 会排在 9_x.sql 前面,建表顺序就反了
	assert.Equal(t, "0010", NextSchemaSeqFrom([]string{"0009_a.sql"}))
}

func TestNextSchemaSeqReadsDir(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "0003_x.sql"), nil, 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "0099_notadir.sql"), 0o755))

	// 目录名恰好长得像 schema 文件时不能算进去
	assert.Equal(t, "0004", NextSchemaSeq(dir))

	// 目录不存在时退回 0001,而不是报错 —— 全新的服务里本来就没有 schema/
	assert.Equal(t, "0001", NextSchemaSeq(filepath.Join(dir, "nope")))
}
