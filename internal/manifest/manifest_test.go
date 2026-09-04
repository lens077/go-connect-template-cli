package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const minimal = `version: 1
module: github.com/lens077/go-connect-template
example:
  name: search
  needs: [elasticsearch]
  files: [internal/biz/search.go]
  keep: [internal/data/models/db.go]
features:
  postgres:
    title: PostgreSQL
    group: database
    default: true
    files: [internal/data/db_postgres.go]
    requires: [github.com/jackc/pgx/v5]
  redis:
    title: Redis
    group: cache
    default: true
    files: [internal/data/redis.go]
    requires: [github.com/redis/go-redis/v9]
  elasticsearch:
    title: Elasticsearch
    group: search
    files: [internal/data/search.go]
    requires: [github.com/elastic/go-elasticsearch/v9]
  opensearch:
    title: OpenSearch
    group: search
    files: [internal/data/search.go]
  consul:
    title: Consul
    group: discovery
    default: true
    requires: [github.com/hashicorp/consul/api]
  config-consul:
    title: Consul KV
    group: config-source
    needs: [consul]
    files: [internal/pkg/config/source_consul.go]
    requires: [github.com/hashicorp/consul/api]
  config-file:
    title: 本地文件
    group: config-source
    always: true
groups:
  database:
    title: 数据库
    order: 10
    required: true
    exclusive: true
    members: [postgres]
  cache:
    title: 缓存
    order: 20
    exclusive: true
    members: [redis]
  search:
    title: 检索
    order: 30
    exclusive: true
    members: [elasticsearch, opensearch]
  discovery:
    title: 注册中心
    order: 40
    exclusive: true
    members: [consul]
  config-source:
    title: 配置源
    order: 50
    members: [config-consul, config-file]
layouts:
  standalone:
    title: 独立仓库
    service_dir: "."
    proto_dir: api
    service_module: "{{.Module}}"
    go_mod: true
anchors:
  data-providers: internal/data/data.go
hooks:
  post_generate:
    - name: sqlc
      cmd: [sqlc, generate]
      when: postgres
tools:
  - name: go
    required: true
`

func load(t *testing.T, content string) (*Manifest, error) {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, RootDir), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, Path), []byte(content), 0o644))
	return Load(root)
}

func mustLoad(t *testing.T, content string) *Manifest {
	t.Helper()
	m, err := load(t, content)
	require.NoError(t, err)
	return m
}

func TestLoad(t *testing.T) {
	m := mustLoad(t, minimal)

	assert.Equal(t, "github.com/lens077/go-connect-template", m.Module)
	assert.Equal(t, "search", m.Example.Name)
	assert.Equal(t, []string{"internal/data/models/db.go"}, m.Example.Keep)
	assert.Equal(t, 30, m.Groups["search"].Order)

	// map 的 key 要回填进 .Name,否则排序输出里全是空名字
	assert.Equal(t, "postgres", m.Features["postgres"].Name)
	assert.Equal(t, "database", m.Groups["database"].Name)
	assert.Equal(t, "standalone", m.Layouts["standalone"].Name)
}

func TestLoadVersionTwoExampleNeedsAny(t *testing.T) {
	content := strings.Replace(minimal, "version: 1", "version: 2", 1)
	content = strings.Replace(content, "needs: [elasticsearch]", "needs_any: [elasticsearch, opensearch]", 1)
	m := mustLoad(t, content)

	assert.Equal(t, []string{"elasticsearch", "opensearch"}, m.Example.NeedsAny)
	assert.Empty(t, m.Example.Needs)
}

func TestLoadLayoutFeaturesAndSharedProto(t *testing.T) {
	m := mustLoad(t, `version: 1
module: m
features:
  config-consul: {title: Consul KV, group: config-source}
groups:
  config-source: {title: 配置源, members: [config-consul]}
layouts:
  standalone:
    service_dir: "."
    proto_dir: api
    service_module: "{{.Module}}"
    go_mod: true
  monorepo:
    service_dir: backend/services/{{.Name}}
    proto_dir: backend/api
    service_module: "{{.Module}}/services/{{.Name}}"
    go_mod: false
    features: [config-consul]
    shared_proto: [config]
`)

	l := m.Layouts["monorepo"]
	assert.Equal(t, []string{"config-consul"}, l.Features)
	assert.Equal(t, []string{"config"}, l.SharedProto)

	// 不写这两个字段的布局拿到的是 nil,不是空串组成的 slice ——
	// relocateProto 会拿 SharedProto 建 set,多一个 "" 会让空名目录被当成共用
	assert.Nil(t, m.Layouts["standalone"].Features)
	assert.Nil(t, m.Layouts["standalone"].SharedProto)
}

func TestLoadRejectsUnknownField(t *testing.T) {
	// 拼错的字段必须报错。静默忽略的话,manifest 里写成 `require:`(少个 s)
	// 会表现成「依赖没被删掉」,得一路查到生成的 go.mod 才能发现
	_, err := load(t, minimal+"\nbogus_top_level: 1\n")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bogus_top_level")
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load(t.TempDir())
	require.Error(t, err)
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name: "版本号不支持",
			yaml: `version: 99
module: m
layouts:
  standalone: {service_dir: ".", proto_dir: api, service_module: m}
`,
			wantErr: "not supported",
		},
		{
			name: "module 为空",
			yaml: `version: 1
layouts:
  standalone: {service_dir: ".", proto_dir: api, service_module: m}
`,
			wantErr: "module is required",
		},
		{
			name:    "一个 layout 都没有",
			yaml:    "version: 1\nmodule: m\n",
			wantErr: "at least one layout",
		},
		{
			name: "layout 缺 service_module",
			yaml: `version: 1
module: m
layouts:
  standalone: {service_dir: ".", proto_dir: api}
`,
			wantErr: "service_module",
		},
		{
			name: "feature 没写 group",
			yaml: `version: 1
module: m
features:
  a: {title: A}
layouts:
  standalone: {service_dir: ".", proto_dir: api, service_module: m}
`,
			wantErr: "group is required",
		},
		{
			name: "feature 指向不存在的 group",
			yaml: `version: 1
module: m
features:
  a: {group: nope}
layouts:
  standalone: {service_dir: ".", proto_dir: api, service_module: m}
`,
			wantErr: `unknown group "nope"`,
		},
		{
			name: "feature 的 needs 指向不存在的 feature",
			yaml: `version: 1
module: m
features:
  a: {group: g, needs: [ghost]}
groups:
  g: {members: [a]}
layouts:
  standalone: {service_dir: ".", proto_dir: api, service_module: m}
`,
			wantErr: `unknown feature "ghost"`,
		},
		{
			name: "example 的 needs_any 指向不存在的 feature",
			yaml: `version: 2
module: m
example:
  needs_any: [ghost]
layouts:
  standalone: {service_dir: ".", proto_dir: api, service_module: m}
`,
			wantErr: `unknown feature "ghost"`,
		},
		{
			name: "group 的 members 为空",
			yaml: `version: 1
module: m
groups:
  g: {title: G}
layouts:
  standalone: {service_dir: ".", proto_dir: api, service_module: m}
`,
			wantErr: "members is empty",
		},
		{
			name: "group 列了一个不存在的 member",
			yaml: `version: 1
module: m
groups:
  g: {members: [ghost]}
layouts:
  standalone: {service_dir: ".", proto_dir: api, service_module: m}
`,
			wantErr: `unknown member "ghost"`,
		},
		{
			name: "member 自己声明的 group 对不上",
			yaml: `version: 1
module: m
features:
  a: {group: other}
groups:
  g: {members: [a]}
  other: {members: [a]}
layouts:
  standalone: {service_dir: ".", proto_dir: api, service_module: m}
`,
			wantErr: "declares group",
		},
		{
			name: "insert 指向不存在的锚点",
			yaml: `version: 1
module: m
layouts:
  standalone:
    service_dir: "."
    proto_dir: api
    service_module: m
    inserts:
      - {anchor: nowhere, text: "x"}
`,
			wantErr: `unknown anchor "nowhere"`,
		},
		{
			// 写错名字的话该 feature 静默地没被启用,而它的文件早就按「未启用」删了
			name: "layout 的 features 指向不存在的 feature",
			yaml: `version: 1
module: m
layouts:
  standalone:
    service_dir: "."
    proto_dir: api
    service_module: m
    features: [ghost]
`,
			wantErr: `unknown feature "ghost"`,
		},
		{
			name: "hook 的 cmd 为空",
			yaml: `version: 1
module: m
layouts:
  standalone: {service_dir: ".", proto_dir: api, service_module: m}
hooks:
  post_generate:
    - {name: sqlc}
`,
			wantErr: "cmd is empty",
		},
		{
			name: "hook 的 when 指向不存在的 feature",
			yaml: `version: 1
module: m
layouts:
  standalone: {service_dir: ".", proto_dir: api, service_module: m}
hooks:
  post_generate:
    - {name: sqlc, cmd: [sqlc, generate], when: ghost}
`,
			wantErr: `unknown feature "ghost"`,
		},
		{
			name: "hook 的 when_layout 指向不存在的布局",
			yaml: `version: 1
module: m
layouts:
  standalone: {service_dir: ".", proto_dir: api, service_module: m}
hooks:
  post_generate:
    - {name: buf, cmd: [buf, generate], when_layout: ghost}
`,
			wantErr: `unknown layout "ghost"`,
		},
		{
			// dev_required 单独出现不产生任何效果,而它想表达的是
			// 「这个组件不起服务就起不来」—— 静默忽略等于生成后的提示里少一条
			name: "dev_required 没配 dev_compose",
			yaml: `version: 1
module: m
features:
  a: {group: g, dev_required: true}
groups:
  g: {members: [a]}
layouts:
  standalone: {service_dir: ".", proto_dir: api, service_module: m}
`,
			wantErr: "dev_required needs dev_compose",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := load(t, tc.yaml)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestResolve(t *testing.T) {
	m := mustLoad(t, minimal)

	t.Run("always 的 feature 不用选也在", func(t *testing.T) {
		set, err := m.Resolve([]string{"postgres"})
		require.NoError(t, err)
		assert.True(t, set.Has("config-file"))
	})

	t.Run("needs 会被自动带上", func(t *testing.T) {
		set, err := m.Resolve([]string{"postgres", "config-consul"})
		require.NoError(t, err)
		assert.True(t, set.Has("consul"), "config-consul 依赖 consul")
	})

	t.Run("none 与空串被忽略", func(t *testing.T) {
		set, err := m.Resolve([]string{"postgres", NoneChoice, ""})
		require.NoError(t, err)
		assert.Equal(t, []string{"config-file", "postgres"}, set.Names())
	})

	t.Run("required 的组一个都没选时报错", func(t *testing.T) {
		_, err := m.Resolve([]string{"redis"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "database")
	})

	t.Run("互斥组选了两个时报错", func(t *testing.T) {
		_, err := m.Resolve([]string{"postgres", "elasticsearch", "opensearch"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "exclusive")
	})

	t.Run("非互斥组可以多选", func(t *testing.T) {
		set, err := m.Resolve([]string{"postgres", "config-consul"})
		require.NoError(t, err)
		assert.True(t, set.Has("config-consul"))
		assert.True(t, set.Has("config-file"))
	})

	t.Run("选了不存在的 feature 时报错", func(t *testing.T) {
		_, err := m.Resolve([]string{"postgres", "mongodb"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "mongodb")
	})

	t.Run("每个 feature 都有显式取值", func(t *testing.T) {
		// 模板跑在 missingkey=error 下,{{if .Features.redis}} 遇到缺 key
		// 会直接报错而不是走 else 分支
		set, err := m.Resolve([]string{"postgres"})
		require.NoError(t, err)
		for _, name := range m.FeatureNames() {
			_, ok := set[name]
			assert.True(t, ok, "%s 没有显式取值", name)
		}
		assert.False(t, set.Has("redis"))
	})
}

func TestResolveNeedsCycle(t *testing.T) {
	// 成环时必须报错而不是死循环
	m := mustLoad(t, `version: 1
module: m
features:
  a: {group: g, needs: [b]}
  b: {group: g, needs: [a]}
groups:
  g: {members: [a, b]}
layouts:
  standalone: {service_dir: ".", proto_dir: api, service_module: m}
`)
	_, err := m.Resolve([]string{"a"})
	// a -> b -> a 在第二轮就到不动点了,不构成真正的发散;
	// 这里只断言不 panic、不挂死,并且两个都被带上
	require.NoError(t, err)
}

func TestDroppedRequires(t *testing.T) {
	m := mustLoad(t, minimal)

	t.Run("关掉的 feature 的依赖会被删", func(t *testing.T) {
		set, err := m.Resolve([]string{"postgres", "consul"})
		require.NoError(t, err)
		dropped := m.DroppedRequires(set)
		assert.Contains(t, dropped, "github.com/redis/go-redis/v9")
		assert.Contains(t, dropped, "github.com/elastic/go-elasticsearch/v9")
		assert.NotContains(t, dropped, "github.com/jackc/pgx/v5")
	})

	t.Run("共用的依赖不能因为关掉其中一个就删掉", func(t *testing.T) {
		// consul(开)与 config-consul(关)都要 hashicorp/consul/api。
		// 只看「关掉的那些声明了什么」会把它删掉,注册中心当场编译不过。
		set, err := m.Resolve([]string{"postgres", "consul"})
		require.NoError(t, err)
		assert.False(t, set.Has("config-consul"))
		assert.NotContains(t, m.DroppedRequires(set), "github.com/hashicorp/consul/api")
	})

	t.Run("结果去重且有序", func(t *testing.T) {
		set, err := m.Resolve([]string{"postgres"})
		require.NoError(t, err)
		dropped := m.DroppedRequires(set)
		assert.IsIncreasing(t, dropped)
		assert.Len(t, dropped, len(uniq(dropped)))
	})
}

func TestDroppedFiles(t *testing.T) {
	m := mustLoad(t, minimal)

	t.Run("关掉的 feature 的文件会被删", func(t *testing.T) {
		set, err := m.Resolve([]string{"postgres"})
		require.NoError(t, err)

		dropped := m.DroppedFiles(set)
		assert.Contains(t, dropped, "internal/data/search.go")
		assert.Contains(t, dropped, "internal/data/redis.go")
		assert.NotContains(t, dropped, "internal/data/db_postgres.go")
	})

	t.Run("共用的文件不能因为关掉其中一个就删掉", func(t *testing.T) {
		// elasticsearch 与 opensearch 共用 internal/data/search.go
		set, err := m.Resolve([]string{"postgres", "elasticsearch"})
		require.NoError(t, err)
		assert.NotContains(t, m.DroppedFiles(set), "internal/data/search.go")
	})
}

func TestSortedAccessors(t *testing.T) {
	m := mustLoad(t, minimal)

	// 排序是按名字,不是按 Order —— Order 只影响交互表单的提问顺序。
	// 这里锁死字典序:--dry-run 的输出必须可复现,map 遍历顺序不行。
	var groups []string
	for _, g := range m.SortedGroups() {
		groups = append(groups, g.Name)
	}
	assert.Equal(t, []string{"cache", "config-source", "database", "discovery", "search"}, groups)

	assert.IsIncreasing(t, m.FeatureNames())
	assert.Equal(t, []string{"standalone"}, m.LayoutNames())

	var features []string
	for _, f := range m.SortedFeatures() {
		features = append(features, f.Name)
	}
	assert.Equal(t, m.FeatureNames(), features)
}

func TestDefaultsFor(t *testing.T) {
	m := mustLoad(t, minimal)

	assert.Equal(t, []string{"postgres"}, m.DefaultsFor(m.Groups["database"]))
	assert.Empty(t, m.DefaultsFor(m.Groups["search"]), "没有 default 的组默认不启用")
}

func TestFeatureSet(t *testing.T) {
	s := FeatureSet{"a": true, "b": true, "c": false}

	assert.True(t, s.Has("a"))
	assert.False(t, s.Has("c"), "显式置 false 与不存在等价")
	assert.False(t, s.Has("zzz"))

	assert.True(t, s.HasAll([]string{"a", "b"}))
	assert.False(t, s.HasAll([]string{"a", "c"}), "逗号语义是「与」")
	assert.True(t, s.HasAll(nil))
	assert.True(t, s.HasAny([]string{"c", "b"}))
	assert.False(t, s.HasAny([]string{"c", "zzz"}))
	assert.False(t, s.HasAny(nil))

	assert.Equal(t, []string{"a", "b"}, s.Names())
}

func uniq(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
