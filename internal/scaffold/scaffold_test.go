package scaffold

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lens077/co-cli/internal/manifest"
	"github.com/lens077/co-cli/internal/resource"
)

// templateSource 找到本地模板仓库。
//
// 不走 Fetch 的 clone 分支:测试要验的是引擎,不是网络。CI 里把模板 checkout
// 到任意位置,用 CO_TEMPLATE_DIR 指过来即可。
func templateSource(t *testing.T) (Source, *manifest.Manifest) {
	t.Helper()

	dir := os.Getenv("CO_TEMPLATE_DIR")
	if dir == "" {
		// 默认按并排 checkout 猜:co-cli 与 go-connect-template 同级
		dir = filepath.Join("..", "..", "..", "go-connect-template")
	}
	abs, err := filepath.Abs(dir)
	require.NoError(t, err)

	if _, err := os.Stat(filepath.Join(abs, manifest.Path)); err != nil {
		t.Skipf("模板不在 %s;设置 CO_TEMPLATE_DIR 后重跑", abs)
	}

	src, err := Fetch(context.Background(), FetchOptions{Dir: abs})
	require.NoError(t, err)
	m, err := manifest.Load(src.Root)
	require.NoError(t, err)
	return src, m
}

// combo 是矩阵里的一格。
type combo struct {
	name     string
	features []string
	opts     func(*Options)
}

// 矩阵刻意不含 database 轴:manifest 目前只有 postgres 一个成员,
// mysql/sqlite 按用户要求延后。加进来时在这里补一格即可。
var matrix = []combo{
	{
		name:     "full",
		features: []string{"postgres", "redis", "elasticsearch", "casdoor", "minio", "consul", "config-consul", "config-file"},
	},
	{
		name:     "minimal",
		features: []string{"postgres"},
	},
	{
		name:     "no-cache",
		features: []string{"postgres", "elasticsearch", "casdoor", "minio", "consul", "config-consul"},
	},
	{
		name:     "no-search",
		features: []string{"postgres", "redis", "casdoor", "minio", "consul", "config-consul"},
	},
	{
		name:     "no-iam",
		features: []string{"postgres", "redis", "elasticsearch", "minio", "consul", "config-consul"},
	},
	{
		name:     "no-store",
		features: []string{"postgres", "redis", "elasticsearch", "casdoor", "consul", "config-consul"},
	},
	{
		name:     "keep-example",
		features: []string{"postgres", "redis", "elasticsearch", "casdoor", "minio", "consul", "config-consul"},
		opts:     func(o *Options) { o.KeepExample = true },
	},
	{
		// 一个 handler 都不注册的骨架。这一格专门守着 server.go 里那个
		// handlerOptions:写成局部变量的话,未使用的局部变量会让它编译不过。
		name:     "no-resource",
		features: []string{"postgres", "redis", "consul", "config-consul"},
		opts:     func(o *Options) { o.NoResource = true },
	},
}

// TestPlanMatrix 只算 plan,不落盘也不联网,永远跑。
func TestPlanMatrix(t *testing.T) {
	src, m := templateSource(t)

	for _, c := range matrix {
		t.Run(c.name, func(t *testing.T) {
			opts := Options{
				Name:     "cart",
				Module:   "github.com/acme/shop",
				Dest:     t.TempDir(),
				Layout:   "standalone",
				Features: c.features,
			}
			if c.opts != nil {
				c.opts(&opts)
			}

			p, err := NewPlan(src, m, opts)
			require.NoError(t, err)

			assert.Equal(t, "github.com/acme/shop", p.ServiceModule)
			assert.Equal(t, "cart", p.Names.Name)
			assert.IsIncreasing(t, p.Deletes, "Deletes 必须有序且去重,否则 --dry-run 的输出不可复现")

			// 关掉的 feature,其文件必须出现在删除清单里
			for _, f := range m.SortedFeatures() {
				if p.Features.Has(f.Name) {
					for _, file := range f.Files {
						assert.NotContains(t, p.Deletes, file, "%s 开着,%s 不该被删", f.Name, file)
					}
				}
			}

			if opts.NoResource {
				assert.Nil(t, p.Resource)
				assert.Empty(t, p.Inserts)
			} else {
				require.NotNil(t, p.Resource)
				assert.Equal(t, "cart", p.Resource.Name)
				assert.NotEmpty(t, p.Inserts)

				// schema 的序号要按「删除执行完之后」的目录算:示例资源被删掉时
				// 0001 空了出来,新资源就该占它,而不是跳到 0002 留个洞
				wantSeq := "0001"
				if opts.KeepExample {
					wantSeq = "0002" // 模板自带的 0001_products.sql 留着
				}
				assert.Equal(t, wantSeq, p.Resource.SchemaSeq)
				assert.Contains(t, resource.Targets(*p.Resource),
					filepath.Join("internal", "data", "schema", wantSeq+"_carts.sql"))
			}

			// 示例资源在不 --keep-example 时必须整套删掉
			exampleDeleted := containsAll(p.Deletes, m.Example.Files)
			assert.Equal(t, !opts.KeepExample, exampleDeleted)
		})
	}
}

// TestPlanLayoutForcedFeatures 盯住 layouts.<name>.features:
// 用户没选 config-configcenter,monorepo 也必须替他打开 —— 否则
// source.go 里那段 configcenter 分支会被裁掉,而 monorepo 的默认
// CONFIG_SOURCE 正指着它,生成出来的服务一起动就报 unknown config source。
func TestPlanLayoutForcedFeatures(t *testing.T) {
	src, m := templateSource(t)

	forced := m.Layouts["monorepo"].Features
	require.NotEmpty(t, forced, "monorepo 应当声明强制启用的 feature")

	opts := Options{
		Name:     "cart",
		Module:   "github.com/acme/shop",
		Dest:     t.TempDir(),
		Layout:   "monorepo",
		Features: []string{"postgres", "config-file"}, // 刻意不选强制项
	}
	p, err := NewPlan(src, m, opts)
	require.NoError(t, err)

	for _, f := range forced {
		assert.True(t, p.Features.Has(f), "布局强制的 %s 没被启用", f)
		for _, file := range m.Features[f].Files {
			assert.NotContains(t, p.Deletes, file, "%s 被强制启用,%s 不该删", f, file)
		}
	}

	// standalone 不强制,同样的选择下这些 feature 的文件该删就删
	opts.Dest, opts.Layout = t.TempDir(), "standalone"
	sp, err := NewPlan(src, m, opts)
	require.NoError(t, err)
	for _, f := range forced {
		assert.False(t, sp.Features.Has(f), "standalone 不该替用户打开 %s", f)
	}
}

func TestPlanRejects(t *testing.T) {
	src, m := templateSource(t)
	base := Options{Name: "cart", Module: "github.com/acme/shop", Layout: "standalone", Features: []string{"postgres"}}

	t.Run("布局名不存在", func(t *testing.T) {
		o := base
		o.Dest, o.Layout = t.TempDir(), "nope"
		_, err := NewPlan(src, m, o)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unknown layout")
	})

	t.Run("module 路径非法", func(t *testing.T) {
		o := base
		o.Dest, o.Module = t.TempDir(), "not a module"
		_, err := NewPlan(src, m, o)
		require.Error(t, err)
	})

	t.Run("资源名非法", func(t *testing.T) {
		o := base
		o.Dest, o.Name = t.TempDir(), "Cart"
		_, err := NewPlan(src, m, o)
		require.Error(t, err)
	})

	t.Run("必选组一个都没选", func(t *testing.T) {
		o := base
		o.Dest, o.Features = t.TempDir(), []string{"redis"}
		_, err := NewPlan(src, m, o)
		require.Error(t, err)
	})
}

func TestApplyRefusesNonEmptyDest(t *testing.T) {
	src, m := templateSource(t)

	dest := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dest, "README.md"), []byte("x"), 0o644))

	p, err := NewPlan(src, m, Options{
		Name: "cart", Module: "github.com/acme/shop", Dest: dest,
		Layout: "standalone", Features: []string{"postgres"},
	})
	require.NoError(t, err)

	// 往非空目录里生成会把两边的文件混在一起,且无法回滚
	err = Apply(context.Background(), p, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not empty")
}

// TestGenerateMatrix 真生成并编译。
//
// 这是整套东西唯一的端到端保障:标记裁剪删错一行、锚点插错位置、go.mod 少删
// 一条依赖,都只在这里暴露。代价是每一格都要跑 buf/sqlc/go mod tidy,
// 联网且以分钟计,所以 -short 直接跳过。
func TestGenerateMatrix(t *testing.T) {
	if testing.Short() {
		t.Skip("生成矩阵要联网并调用 buf/sqlc,-short 下跳过")
	}
	requireTools(t, "go", "buf", "sqlc")

	src, m := templateSource(t)

	for _, c := range matrix {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			opts := Options{
				Name:     "cart",
				Module:   "github.com/acme/shop",
				Dest:     t.TempDir(),
				Layout:   "standalone",
				Features: c.features,
			}
			if c.opts != nil {
				c.opts(&opts)
			}

			p, err := NewPlan(src, m, opts)
			require.NoError(t, err)
			require.NoError(t, Apply(context.Background(), p, nil))

			root := filepath.Join(p.Dest, p.ServiceDir)
			goBuild(t, root)
		})
	}
}

// TestGenerateMonorepo 单独一格:monorepo 的 proto 落在仓库根,
// buf 也只能在根上跑,和 standalone 完全不是一条路径。
func TestGenerateMonorepo(t *testing.T) {
	if testing.Short() {
		t.Skip("生成矩阵要联网并调用 buf/sqlc,-short 下跳过")
	}
	requireTools(t, "go")

	src, m := templateSource(t)

	dest := t.TempDir()
	p, err := NewPlan(src, m, Options{
		Name:     "cart",
		Module:   "github.com/acme/ecommerce/backend",
		Dest:     dest,
		Layout:   "monorepo",
		Features: []string{"postgres", "redis", "consul", "config-consul"},
	})
	require.NoError(t, err)
	require.NoError(t, Apply(context.Background(), p, nil))

	root := filepath.Join(dest, p.ServiceDir)

	// monorepo 不生成 go.mod —— 用根仓库那个
	_, err = os.Stat(filepath.Join(root, "go.mod"))
	assert.True(t, os.IsNotExist(err), "monorepo 下服务目录不该有 go.mod")

	// proto 搬到了仓库根的 api/ 下,服务目录里不该留空壳
	_, err = os.Stat(filepath.Join(dest, p.ProtoDir, "cart", "v1", "cart.proto"))
	assert.NoError(t, err)
	_, err = os.Stat(filepath.Join(root, "api"))
	assert.True(t, os.IsNotExist(err), "api/ 应当整棵搬走,不留空目录")

	// configcenter 配置源由 layouts.monorepo.features 强制启用(上面的 Features
	// 里没选它),而且必须真的被接进 NewSource 的分支里 —— 只落文件不接线的话,
	// CONFIG_SOURCE=configcenter 会报 unknown,而文件明明在那儿
	cfgDir := filepath.Join(root, "internal", "pkg", "config")
	assertFileContains(t, filepath.Join(cfgDir, "source_configcenter.go"), "func NewConfigCenterSource(")
	assertFileContains(t, filepath.Join(cfgDir, "source.go"), "return NewConfigCenterSource()")
	assertFileContains(t, filepath.Join(cfgDir, "source.go"), "ConfigSourceConfigCenter,")

	// configcenter 随身带的 config-service 契约也要跟着搬到仓库根
	sharedProto := filepath.Join(dest, p.ProtoDir, "config", "v1", "config.proto")
	_, err = os.Stat(sharedProto)
	assert.NoError(t, err, "api/config 应当搬到 %s", p.ProtoDir)

	// 往同一个 monorepo 里再生成一个服务:api/config 已经在那儿了,
	// shared_proto 必须让它通过,而不是报「refusing to overwrite」。
	// 这是 monorepo 的常态 —— 谁都不会只生成一个服务。
	before, err := os.ReadFile(sharedProto)
	require.NoError(t, err)

	p2, err := NewPlan(src, m, Options{
		Name:     "order",
		Module:   "github.com/acme/ecommerce/backend",
		Dest:     dest,
		Layout:   "monorepo",
		Features: []string{"postgres", "redis", "consul", "config-consul"},
	})
	require.NoError(t, err)
	require.NoError(t, Apply(context.Background(), p2, nil), "第二个服务不该被共用 proto 挡住")

	after, err := os.ReadFile(sharedProto)
	require.NoError(t, err)
	assert.Equal(t, before, after, "共用 proto 该保留仓库里那份,不能被模板覆盖回去")
	_, err = os.Stat(filepath.Join(dest, p2.ProtoDir, "order", "v1", "order.proto"))
	assert.NoError(t, err)

	// 到这里为止都只是在断言字符串和路径 —— 而 monorepo 真正会错的地方是导入路径:
	// proto 被搬出了服务目录,导入前缀就得跟着仓库根走(<Module>/api),不能再用
	// <ServiceModule>/api。只有编译才验得出来,所以这里必须真编一次。
	requireTools(t, "buf")
	buildMonorepo(t, src, dest, p, []string{"cart", "order"})
}

// buildMonorepo 把生成结果补成一个能编译的仓库再 go build。
//
// monorepo 布局刻意不生成 go.mod / buf.yaml / third_party(layout.drop 里列着),
// 因为真实仓库里这些由根统一提供。测试得把这份「根」补出来 —— 直接拿模板的那几份,
// 改掉 module 行即可,这也正是从零起一个 monorepo 时要做的事。
func buildMonorepo(t *testing.T, src Source, dest string, p *Plan, services []string) {
	t.Helper()

	// p.ServiceDir 是 backend/services/<name>,module 根在 backend/ ——
	// 也就是服务目录往上两级。不写死 "backend",布局改了这里跟着变。
	modRoot := filepath.Dir(filepath.Dir(filepath.Join(dest, p.ServiceDir)))

	for _, f := range []string{"go.mod", "go.sum", "buf.yaml", "buf.lock", "buf.gen.yaml", "third_party"} {
		from, to := filepath.Join(src.Root, f), filepath.Join(modRoot, f)
		info, err := os.Stat(from)
		require.NoErrorf(t, err, "模板里没有 %s", f)
		if info.IsDir() {
			require.NoError(t, copyTree(from, to, nil))
		} else {
			require.NoError(t, copyFile(from, to, info.Mode().Perm()))
		}
	}

	goMod := filepath.Join(modRoot, "go.mod")
	require.NoError(t, RewriteGoMod(goMod, p.Opts.Module, nil))

	// buf 必须在仓库根跑,--path 不可省:third_party 下那份 WKT 副本与 buf 内置的
	// 同名,整模块构建会报 name conflict over google.protobuf.Any
	paths := []string{filepath.Join("api", "config")}
	for _, s := range services {
		paths = append(paths,
			filepath.Join("api", s),
			filepath.Join("services", s, "internal", "conf"))
	}
	for _, rel := range paths {
		cmd := exec.Command("buf", "generate", "--template", "buf.gen.yaml", "--path", filepath.ToSlash(rel))
		cmd.Dir = modRoot
		out, err := cmd.CombinedOutput()
		require.NoErrorf(t, err, "buf generate --path %s 失败:\n%s", rel, out)
	}

	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir = modRoot
	if out, err := tidy.CombinedOutput(); err != nil {
		t.Logf("go mod tidy 报错(继续尝试编译):\n%s", out)
	}
	goBuild(t, modRoot)
}

func TestGeneratePrunesMarkers(t *testing.T) {
	src, m := templateSource(t)

	dest := t.TempDir()
	p, err := NewPlan(src, m, Options{
		Name:     "cart",
		Module:   "github.com/acme/shop",
		Dest:     dest,
		Layout:   "standalone",
		Features: []string{"postgres", "consul", "config-consul"},
	})
	require.NoError(t, err)
	require.NoError(t, Apply(context.Background(), p, nil))

	root := filepath.Join(dest, p.ServiceDir)

	// 生成物里不该残留任何裁剪标记(锚点除外 —— 它要留给 resource add 用)
	require.NoError(t, filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if commentPrefixFor(relTo(root, path)) == "" {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		for _, bad := range []string{beginToken, endToken, markerPrefix + "redis"} {
			assert.NotContains(t, string(data), bad, "%s 里残留了标记", relTo(root, path))
		}
		return nil
	}))

	// 关掉的 feature:文件、provider 行、go.mod 依赖三处都得干净
	_, err = os.Stat(filepath.Join(root, "internal", "data", "redis.go"))
	assert.True(t, os.IsNotExist(err))

	gomod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	require.NoError(t, err)
	assert.NotContains(t, string(gomod), "go-redis")
	assert.Contains(t, string(gomod), "module github.com/acme/shop")

	// 留下的东西也要真的留下
	assertFileContains(t, filepath.Join(root, "internal", "data", "data.go"), "NewPostgres")

	// schema 落盘时必须带 000N_ 前缀:sqlc 按文件名排序读整个 schema 目录,
	// 不带前缀的文件在字典序里排到所有 000N_ 之后,后续资源引用它就建不起外键
	assertFileContains(t, filepath.Join(root, "internal", "data", "schema", "0001_carts.sql"),
		"CREATE TABLE IF NOT EXISTS carts")
}

func TestAddResource(t *testing.T) {
	if testing.Short() {
		t.Skip("要先完整生成一个服务,-short 下跳过")
	}
	src, m := templateSource(t)

	dest := t.TempDir()
	p, err := NewPlan(src, m, Options{
		Name:     "cart",
		Module:   "github.com/acme/shop",
		Dest:     dest,
		Layout:   "standalone",
		Features: []string{"postgres", "redis", "consul", "config-consul"},
	})
	require.NoError(t, err)
	require.NoError(t, Apply(context.Background(), p, nil))

	root := filepath.Join(dest, p.ServiceDir)

	add, err := NewAddPlan(src, m, AddOptions{Dir: root, Name: "order"})
	require.NoError(t, err)

	// feature 从已生成的服务里探测出来,不该冒出 elasticsearch
	assert.True(t, add.Features.Has("postgres"))
	assert.False(t, add.Features.Has("elasticsearch"))

	require.NoError(t, add.Apply(context.Background(), m, nil))

	assertFileContains(t, filepath.Join(root, "internal", "biz", "order.go"), "OrderUseCase")
	// 序号要接着服务里已有的 schema 往下走(cart 占了 0001),
	// 否则两套资源都叫 0001_,谁先建表就成了字典序的巧合
	assert.Equal(t, "0002", add.Spec.SchemaSeq)
	assertFileContains(t, filepath.Join(root, "internal", "data", "schema", "0002_orders.sql"),
		"CREATE TABLE IF NOT EXISTS orders")
	assertFileContains(t, filepath.Join(root, "api", "order", "v1", "order.proto"), "service OrderService")
	// 新资源要被接到锚点上,否则生成了却没人用
	assertFileContains(t, filepath.Join(root, "internal", "server", "server.go"), "orderv1connect")

	t.Run("重复添加时报错而不是悄悄覆盖", func(t *testing.T) {
		_, err := NewAddPlan(src, m, AddOptions{Dir: root, Name: "order"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--force")
	})
}

func requireTools(t *testing.T, names ...string) {
	t.Helper()
	for _, n := range names {
		if _, err := exec.LookPath(n); err != nil {
			t.Skipf("PATH 里没有 %s", n)
		}
	}
}

func goBuild(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("go", "build", "./...")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "go build ./... 在 %s 失败:\n%s", dir, out)
}

func assertFileContains(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), want, path)
}

func relTo(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(rel)
}

func containsAll(hay, needles []string) bool {
	if len(needles) == 0 {
		return false
	}
	set := map[string]bool{}
	for _, h := range hay {
		set[h] = true
	}
	for _, n := range needles {
		if !set[n] {
			return false
		}
	}
	return true
}
