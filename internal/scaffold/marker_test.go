package scaffold

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lens077/co-cli/internal/manifest"
)

func set(names ...string) manifest.FeatureSet {
	s := manifest.FeatureSet{}
	for _, n := range names {
		s[n] = true
	}
	return s
}

func TestPrune(t *testing.T) {
	cases := []struct {
		name string
		path string
		set  manifest.FeatureSet
		in   string
		want string
	}{
		{
			name: "行标记:feature 开着,只摘掉标记本身",
			path: "x.go",
			set:  set("redis"),
			in:   "\tNewRedisClient, // +co:redis\n",
			want: "\tNewRedisClient,\n",
		},
		{
			name: "行标记:feature 关着,整行删掉",
			path: "x.go",
			set:  set(),
			in:   "\tNewA,\n\tNewRedisClient, // +co:redis\n\tNewB,\n",
			want: "\tNewA,\n\tNewB,\n",
		},
		{
			name: "行标记:标记前还有说明文字时,文字要留下",
			path: "x.go",
			set:  set("consul"),
			in:   "\tregistry.Module, // 服务注册与发现 +co:consul\n",
			want: "\tregistry.Module, // 服务注册与发现\n",
		},
		{
			name: "行标记:逗号是「与」,缺一个就删",
			path: "x.go",
			set:  set("example"),
			in:   "\tkeep\n\tdrop // +co:example,elasticsearch\n",
			want: "\tkeep\n",
		},
		{
			name: "行标记:「与」的两个都开着才保留",
			path: "x.go",
			set:  set("example", "elasticsearch"),
			in:   "\tdrop // +co:example,elasticsearch\n",
			want: "\tdrop\n",
		},
		{
			name: "块标记:feature 关着,连同 begin/end 一起删",
			path: "x.go",
			set:  set(),
			in:   "a\n// +co:begin redis\nb\nc\n// +co:end\nd\n",
			want: "a\nd\n",
		},
		{
			name: "块标记:feature 开着,块体留下但 begin/end 消失",
			path: "x.go",
			set:  set("redis"),
			in:   "a\n// +co:begin redis\nb\n// +co:end\nd\n",
			want: "a\nb\nd\n",
		},
		{
			name: "块标记:嵌套时里层的 end 不会提前结束外层",
			path: "x.go",
			set:  set("redis"),
			in:   "a\n// +co:begin minio\nb\n// +co:begin redis\nc\n// +co:end\nd\n// +co:end\ne\n",
			want: "a\ne\n",
		},
		{
			name: "块标记:外层开、里层关",
			path: "x.go",
			set:  set("minio"),
			in:   "// +co:begin minio\nb\n// +co:begin redis\nc\n// +co:end\nd\n// +co:end\n",
			want: "b\nd\n",
		},
		{
			name: "锚点原样保留",
			path: "x.go",
			set:  set(),
			in:   "a\n\t// +co:anchor data-providers\nb\n",
			want: "a\n\t// +co:anchor data-providers\nb\n",
		},
		{
			name: "YAML 用 # 前缀,语义相同",
			path: "configs/dev.yml",
			set:  set("postgres"),
			in:   "data:\n  redis: {} # +co:redis\n  pg: {} # +co:postgres\n",
			want: "data:\n  pg: {}\n",
		},
		{
			name: "SQL 用 -- 前缀",
			path: "q.sql",
			set:  set(),
			in:   "-- +co:begin example\nSELECT 1;\n-- +co:end\nSELECT 2;\n",
			want: "SELECT 2;\n",
		},
		{
			name: "无扩展名按文件名认:Makefile 用 #",
			path: "Makefile",
			set:  set(),
			in:   "all:\n\techo hi # +co:redis\n\techo bye\n",
			want: "all:\n\techo bye\n",
		},
		{
			name: "认不出注释语法的文件整份跳过",
			path: "logo.svg",
			set:  set(),
			in:   "<svg>+co:redis</svg>\n",
			want: "<svg>+co:redis</svg>\n",
		},
		{
			name: "CRLF 行尾保持不变",
			path: "x.go",
			set:  set(),
			in:   "a\r\nb // +co:redis\r\nc\r\n",
			want: "a\r\nc\r\n",
		},
		{
			name: "末行无换行符时不补",
			path: "x.go",
			set:  set("redis"),
			in:   "a\nb // +co:redis",
			want: "a\nb",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, changed, err := Prune(tc.path, tc.in, tc.set)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.in != tc.want, changed, "changed 标志应与内容是否变化一致")
		})
	}
}

func TestPruneErrors(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		set     manifest.FeatureSet
		wantErr string
	}{
		{
			name:    "没有 begin 的 end",
			in:      "a\n// +co:end\n",
			set:     set(),
			wantErr: "without a matching",
		},
		{
			name:    "没有闭合的 begin",
			in:      "// +co:begin redis\na\n",
			set:     set("redis"),
			wantErr: "unclosed",
		},
		{
			name:    "锚点落在会被删掉的块里(模板写错了)",
			in:      "// +co:begin redis\n// +co:anchor data-providers\n// +co:end\n",
			set:     set(),
			wantErr: "anchor inside a pruned",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := Prune("x.go", tc.in, tc.set)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestInsertAtAnchor(t *testing.T) {
	const src = "var Module = fx.Provide(\n\tNewData,\n\t// +co:anchor data-providers\n)\n"

	t.Run("插在锚点前,缩进跟着锚点走", func(t *testing.T) {
		got, err := InsertAtAnchor("x.go", src, "data-providers", "NewCartRepo,")
		require.NoError(t, err)
		assert.Equal(t,
			"var Module = fx.Provide(\n\tNewData,\n\tNewCartRepo,\n\t// +co:anchor data-providers\n)\n",
			got)
	})

	t.Run("锚点保留,可以连着插两次", func(t *testing.T) {
		once, err := InsertAtAnchor("x.go", src, "data-providers", "NewCartRepo,")
		require.NoError(t, err)
		twice, err := InsertAtAnchor("x.go", once, "data-providers", "NewOrderRepo,")
		require.NoError(t, err)
		assert.Contains(t, twice, "\tNewCartRepo,\n\tNewOrderRepo,\n")
		assert.Contains(t, twice, "+co:anchor data-providers")
	})

	t.Run("多行插入每行都对齐", func(t *testing.T) {
		got, err := InsertAtAnchor("x.go", src, "data-providers", "A,\nB,")
		require.NoError(t, err)
		assert.Contains(t, got, "\tA,\n\tB,\n")
	})

	t.Run("锚点不存在时报错,而不是静默丢掉插入内容", func(t *testing.T) {
		_, err := InsertAtAnchor("x.go", src, "nope", "X,")
		require.Error(t, err)
	})
}

func TestCommentPrefixFor(t *testing.T) {
	cases := map[string]string{
		"a.go":              "//",
		"a.yaml":            "#",
		"a.yml":             "#",
		"a.sql":             "--",
		"Makefile":          "#",
		"Dockerfile":        "#",
		".gitignore":        "#",
		"deploy/Dockerfile": "#",
		"a.md":              "",
		"a.proto":           "", // proto 刻意不参与裁剪:protoc 会把注释搬进生成物
		"a.ts":              "",
	}
	for path, want := range cases {
		assert.Equal(t, want, commentPrefixFor(path), path)
	}
}
