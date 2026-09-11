package scaffold

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var upgradeFixtureFeatures = []string{"postgres", "redis", "elasticsearch", "casdoor", "minio", "consul", "config-file"}

// generateFixture 按 co new 的方式生成一个**带资源**的服务,返回服务目录。
//
// 必须带资源:upgrade 内部的参考副本是 NoResource 的,锚点上方是空的;
// 真实服务的锚点上方有 NewCartUseCase, / mux.Handle(...) 这些接线。
// fixture 若也用 NoResource,就和实现的盲区完全重合 —— 曾经就是这样,
// 六个测试全绿,而真实 co new 完立刻 upgrade 报 4 处假差异,--write 把接线抹掉。
func generateFixture(t *testing.T, features []string) (string, Source) {
	t.Helper()

	src, m := templateSource(t)
	dest := t.TempDir()

	plan, err := NewPlan(src, m, Options{
		Name:     "cart",
		Module:   "github.com/acme/cart",
		Dest:     dest,
		Layout:   "standalone",
		Features: features,
	})
	require.NoError(t, err)
	// 不跑 buf/sqlc/go mod tidy;它们的产物不参与比对,见 skipCompare
	plan.Hooks = nil
	require.NoError(t, Apply(context.Background(), plan, silentReporter{}))

	return filepath.Join(dest, plan.ServiceDir), src
}

// wiringLines 是 co new 插在锚点上方的接线,upgrade 前后都必须在。
var wiringLines = map[string][]string{
	"internal/biz/biz.go":         {"NewCartUseCase,"},
	"internal/data/data.go":       {"NewCartRepo,"},
	"internal/service/service.go": {"NewCartService,"},
	"internal/server/server.go": {
		`"github.com/acme/cart/api/cart/v1/cartv1connect"`,
		"cartService cartv1connect.CartServiceHandler,",
		"mux.Handle(cartv1connect.NewCartServiceHandler(cartService, handlerOptions(connectOptions)...))",
	},
}

func assertWiringIntact(t *testing.T, serviceRoot string) {
	t.Helper()
	for rel, lines := range wiringLines {
		data, err := os.ReadFile(filepath.Join(serviceRoot, filepath.FromSlash(rel)))
		require.NoError(t, err, rel)
		for _, ln := range lines {
			assert.Contains(t, string(data), ln, "%s 的接线丢了", rel)
		}
	}
}

// 刚生成出来的服务,拿同一份模板去比必须没有差异。
//
// 这一条同时压住了五件事:feature 反推、布局识别、目标根目录反推、逐文件比对、
// 锚点接线搬运。其中任何一环错位都会冒出一堆假差异 —— 那正是 upgrade 最坏的
// 失败模式:报一屏噪音,用户从此不再看它。
func TestPlanUpgradeCleanAfterFreshGeneration(t *testing.T) {
	serviceRoot, src := generateFixture(t, upgradeFixtureFeatures)
	assertWiringIntact(t, serviceRoot)

	_, m := templateSource(t)
	up, err := PlanUpgrade(context.Background(), src, m, UpgradeOptions{ServiceDir: serviceRoot})
	require.NoError(t, err)
	defer func() { _ = up.Close() }()

	assert.Equal(t, "standalone", up.Layout)
	assert.Equal(t, "cart", up.Name)
	assert.Empty(t, up.Warnings)
	assert.Empty(t, up.Changes, "刚生成的服务不该有差异")
}

// 删一个文件、改一个文件,必须被分别识别成 added / modified。
func TestPlanUpgradeDetectsAddedAndModified(t *testing.T) {
	serviceRoot, src := generateFixture(t, upgradeFixtureFeatures)

	// 挑两个一定存在、且不参与 feature 反推的文件,避免动完之后
	// DetectFeatures 的结论跟着变 —— 那会让断言测到两件事。
	removed := "Makefile"
	touched := "Dockerfile"
	require.FileExists(t, filepath.Join(serviceRoot, removed))
	require.FileExists(t, filepath.Join(serviceRoot, touched))

	require.NoError(t, os.Remove(filepath.Join(serviceRoot, removed)))
	require.NoError(t, os.WriteFile(filepath.Join(serviceRoot, touched),
		[]byte("# 本地改过\n"), 0o644))

	_, m := templateSource(t)
	up, err := PlanUpgrade(context.Background(), src, m, UpgradeOptions{ServiceDir: serviceRoot})
	require.NoError(t, err)
	defer func() { _ = up.Close() }()

	got := map[string]ChangeKind{}
	for _, c := range up.Changes {
		got[c.Path] = c.Kind
	}
	assert.Equal(t, ChangeAdded, got[removed], "模板有而服务没有 → added")
	assert.Equal(t, ChangeModified, got[touched], "两边都有但内容不同 → modified")
	assert.Len(t, up.Changes, 2, "不该报出其他差异")
}

// Apply 之后再比一次必须干净:写回去的内容就是模板那份。
func TestUpgradeApplyConverges(t *testing.T) {
	serviceRoot, src := generateFixture(t, upgradeFixtureFeatures)

	require.NoError(t, os.Remove(filepath.Join(serviceRoot, "Makefile")))
	require.NoError(t, os.WriteFile(filepath.Join(serviceRoot, "Dockerfile"),
		[]byte("# 本地改过\n"), 0o644))

	_, m := templateSource(t)
	up, err := PlanUpgrade(context.Background(), src, m, UpgradeOptions{ServiceDir: serviceRoot})
	require.NoError(t, err)
	sel, err := up.Select(WritePolicy{Modified: true})
	require.NoError(t, err)
	n, err := up.Apply(silentReporter{}, sel)
	require.NoError(t, err)
	assert.Equal(t, 2, n)
	require.NoError(t, up.Close())

	again, err := PlanUpgrade(context.Background(), src, m, UpgradeOptions{ServiceDir: serviceRoot})
	require.NoError(t, err)
	defer func() { _ = again.Close() }()
	assert.Empty(t, again.Changes, "应用之后应当收敛")
}

// 锚点文件被模板改动时,--write 必须带着模板改动落盘、同时保留服务自己的接线。
//
// 这是 upgrade 最危险的路径:锚点文件在参考副本里没有接线,直接盖上去
// go build 照样绿(没人引用的构造函数不报错),服务却不再注册 handler。
func TestUpgradeKeepsAnchorWiring(t *testing.T) {
	serviceRoot, src := generateFixture(t, upgradeFixtureFeatures)

	// 模拟「模板演进」:服务里的 biz.go 还是旧样子(少了模板新加的一行),
	// 且用户自己手写了一个 provider。都在锚点段里,是最容易被搬错的位置。
	bizPath := filepath.Join(serviceRoot, "internal", "biz", "biz.go")
	orig, err := os.ReadFile(bizPath)
	require.NoError(t, err)
	stale := strings.Replace(string(orig),
		"\t\tNewCartUseCase,\n",
		"\t\tNewCartUseCase,\n\t\tNewHandWrittenUseCase,\n", 1)
	require.NotEqual(t, string(orig), stale)
	// 服务里去掉一行模板注释,让它相对模板「过时」
	stale = strings.Replace(stale, "// +co:anchor 是 co-cli 插入新资源 provider 的位置标记;", "// (old comment)", 1)
	require.NoError(t, os.WriteFile(bizPath, []byte(stale), 0o644))

	_, m := templateSource(t)
	up, err := PlanUpgrade(context.Background(), src, m, UpgradeOptions{ServiceDir: serviceRoot})
	require.NoError(t, err)
	defer func() { _ = up.Close() }()

	require.Len(t, up.Changes, 1, "只有 biz.go 该报 modified: %+v", up.Changes)
	assert.Equal(t, "internal/biz/biz.go", up.Changes[0].Path)

	next, current, err := up.Diff("internal/biz/biz.go")
	require.NoError(t, err)
	assert.Equal(t, stale, string(current))
	assert.Contains(t, string(next), "NewCartUseCase,", "co new 的接线必须保留")
	assert.Contains(t, string(next), "NewHandWrittenUseCase,", "手写的接线必须保留")
	assert.Contains(t, string(next), "+co:anchor 是 co-cli 插入新资源 provider 的位置标记", "模板的改动必须带上")
	assert.NotContains(t, string(next), "(old comment)")

	sel, err := up.Select(WritePolicy{Modified: true})
	require.NoError(t, err)
	_, err = up.Apply(silentReporter{}, sel)
	require.NoError(t, err)
	assertWiringIntact(t, serviceRoot)

	again, err := PlanUpgrade(context.Background(), src, m, UpgradeOptions{ServiceDir: serviceRoot})
	require.NoError(t, err)
	defer func() { _ = again.Close() }()
	assert.Empty(t, again.Changes, "应用之后应当收敛")
}

// --keep-example 生成的服务:示例资源的接线(searchv1connect import、handler 参数、
// 多行注册块)都紧贴锚点,且示例文件在服务里而参考副本里没有。
// 刚生成的必须零差异;模板演进后 --write-modified 必须保住示例接线。
func TestPlanUpgradeCleanWithKeepExample(t *testing.T) {
	src, m := templateSource(t)
	dest := t.TempDir()
	plan, err := NewPlan(src, m, Options{
		Name:        "cart",
		Module:      "github.com/acme/cart",
		Dest:        dest,
		Layout:      "standalone",
		Features:    upgradeFixtureFeatures,
		KeepExample: true,
	})
	require.NoError(t, err)
	plan.Hooks = nil
	require.NoError(t, Apply(context.Background(), plan, silentReporter{}))
	serviceRoot := filepath.Join(dest, plan.ServiceDir)

	exampleWiring := map[string][]string{
		"internal/server/server.go": {
			"searchv1connect.SearchServiceHandler",
			"searchv1connect.NewSearchServiceHandler(",
		},
	}
	for rel, lines := range exampleWiring {
		for _, ln := range lines {
			assertFileContains(t, filepath.Join(serviceRoot, filepath.FromSlash(rel)), ln)
		}
	}

	up, err := PlanUpgrade(context.Background(), src, m, UpgradeOptions{ServiceDir: serviceRoot})
	require.NoError(t, err)
	defer func() { _ = up.Close() }()
	assert.True(t, up.KeepExample, "服务里有示例资源,应当反推出 --keep-example")
	assert.Empty(t, up.Warnings)
	assert.Empty(t, up.Changes, "keep-example 刚生成的服务不该有差异")

	// 模板演进:server.go 一处与锚点无关的改动
	serverPath := filepath.Join(serviceRoot, "internal", "server", "server.go")
	orig, err := os.ReadFile(serverPath)
	require.NoError(t, err)
	stale := strings.Replace(string(orig), "// 应用本身的健康检查", "// (old comment)", 1)
	require.NotEqual(t, string(orig), stale)
	require.NoError(t, os.WriteFile(serverPath, []byte(stale), 0o644))

	again, err := PlanUpgrade(context.Background(), src, m, UpgradeOptions{ServiceDir: serviceRoot})
	require.NoError(t, err)
	defer func() { _ = again.Close() }()
	require.Len(t, again.Changes, 1, "%+v", again.Changes)
	assert.Equal(t, "internal/server/server.go", again.Changes[0].Path)
	assert.Empty(t, again.Changes[0].Blocked)

	sel, err := again.Select(WritePolicy{Modified: true})
	require.NoError(t, err)
	_, err = again.Apply(silentReporter{}, sel)
	require.NoError(t, err)
	for rel, lines := range exampleWiring {
		for _, ln := range lines {
			assertFileContains(t, filepath.Join(serviceRoot, filepath.FromSlash(rel)), ln)
		}
	}
	assertFileContains(t, serverPath, "// 应用本身的健康检查")
	assertWiringIntact(t, serviceRoot)
}

// monorepo:布局识别走「go.mod 在上层」,Makefile 由 overlay 按渲染参数生成。
//
// 渲染参数(镜像仓库、Consul 地址)产物里没有记录,upgrade 必须拿同样的值
// 渲染参考副本。曾经的表现:standalone 零差异,monorepo 却永远报 Makefile
// modified,--write 会把 REGISTER / CONSUL_ADDR 抹成空串。
func TestPlanUpgradeMonorepoClean(t *testing.T) {
	src, m := templateSource(t)
	repo := t.TempDir()

	render := struct{ registry, namespace, consul string }{"reg.example.com", "acme", "consul.internal"}
	plan, err := NewPlan(src, m, Options{
		Name:            "cart",
		Module:          "github.com/acme/shop/backend",
		Dest:            repo,
		Layout:          "monorepo",
		Features:        upgradeFixtureFeatures,
		DockerRegistry:  render.registry,
		DockerNamespace: render.namespace,
		ConsulAddr:      render.consul,
	})
	require.NoError(t, err)
	plan.Hooks = nil
	require.NoError(t, Apply(context.Background(), plan, silentReporter{}))
	serviceRoot := filepath.Join(repo, plan.ServiceDir)

	// monorepo 的 go.mod 在仓库根,由用户维护,co new 不生成
	modRoot := filepath.Dir(filepath.Dir(serviceRoot))
	require.NoError(t, os.WriteFile(filepath.Join(modRoot, "go.mod"),
		[]byte("module github.com/acme/shop/backend\n\ngo 1.26\n"), 0o644))

	up, err := PlanUpgrade(context.Background(), src, m, UpgradeOptions{
		ServiceDir:      serviceRoot,
		DockerRegistry:  render.registry,
		DockerNamespace: render.namespace,
		ConsulAddr:      render.consul,
	})
	require.NoError(t, err)
	defer func() { _ = up.Close() }()

	assert.Equal(t, "monorepo", up.Layout)
	assert.Equal(t, "cart", up.Name)
	assert.Empty(t, up.Warnings)
	assert.Empty(t, up.Changes, "同样的渲染参数下不该有差异")

	// 参数对不上必须看得见:不是静默相同,也不是把用户的值当成模板演进悄悄写掉
	off, err := PlanUpgrade(context.Background(), src, m, UpgradeOptions{ServiceDir: serviceRoot})
	require.NoError(t, err)
	defer func() { _ = off.Close() }()
	require.Len(t, off.Changes, 1)
	assert.Equal(t, "Makefile", off.Changes[0].Path)
}

// 模板删掉了一个锚点:接线搬不过去,必须告警而不是静默丢掉。
func TestCarryAnchorsWarnsWhenAnchorGone(t *testing.T) {
	fresh := "package biz\n\nvar Module = fx.Provide(\n)\n"
	current := "package biz\n\nvar Module = fx.Provide(\n\tNewCartUseCase,\n\t// +co:anchor biz-providers\n)\n"

	got, warnings := carryAnchors("internal/biz/biz.go", fresh, current)
	assert.Equal(t, fresh, got, "没有可搬运的目标,原样返回模板内容")
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "biz-providers")
	assert.Contains(t, warnings[0], "internal/biz/biz.go")
}

// 接线里含有到处都有的行(`)`),不能被截断。
//
// 这是 --keep-example 的真实形状:模板的 +co:begin example … +co:end 块
// 在服务里已被去掉标记,块体紧贴锚点;参考副本里没有这个块。
func TestCarryAnchorsKeepsMultiLineBlock(t *testing.T) {
	fresh := strings.Join([]string{
		"package server",
		"",
		"func New() {",
		"\tmux := http.NewServeMux()",
		"\t// +co:anchor server-handler-register",
		"\tmux.HandleFunc(\"/healthz\", nil)",
		"}",
		"",
	}, "\n")
	current := strings.Join([]string{
		"package server",
		"",
		"func New() {",
		"\tmux := http.NewServeMux()",
		"\tp, h := searchv1connect.NewSearchServiceHandler(",
		"\t\tsvc,",
		"\t)",
		"\tmux.Handle(p, h)",
		"\t// +co:anchor server-handler-register",
		"\tmux.HandleFunc(\"/healthz\", nil)",
		"}",
		"",
	}, "\n")

	got, warnings := carryAnchors("internal/server/server.go", fresh, current)
	assert.Empty(t, warnings)
	assert.Equal(t, current, got, "整段接线都要搬过去,包括中间的 `)`")
}

// 段中间的模板改动不能被当成接线搬过去,否则旧模板行会被复制一份。
// 只有紧贴锚点的尾部连续段才是接线。
func TestCarryAnchorsOnlyTrailingRun(t *testing.T) {
	fresh := strings.Join([]string{
		"var Module = fx.Provide(",
		"\tNewData, // renamed comment",
		"\tNewCache,",
		"\t// +co:anchor data-providers",
		")",
		"",
	}, "\n")
	current := strings.Join([]string{
		"var Module = fx.Provide(",
		"\tNewData, // old comment",
		"\tNewCache,",
		"\tNewCartRepo,",
		"\t// +co:anchor data-providers",
		")",
		"",
	}, "\n")
	want := strings.Join([]string{
		"var Module = fx.Provide(",
		"\tNewData, // renamed comment",
		"\tNewCache,",
		"\tNewCartRepo,",
		"\t// +co:anchor data-providers",
		")",
		"",
	}, "\n")

	got, warnings := carryAnchors("internal/data/data.go", fresh, current)
	assert.Empty(t, warnings)
	assert.Equal(t, want, got)
}

// 模板改动的行恰好紧贴接线时分不清「模板改了」还是「用户加的」,
// 此时选择保留(结果里旧行会多出一份,diff 里看得见),而不是丢弃 ——
// 丢错了的是用户的接线,且 go build 不会报错。
func TestCarryAnchorsPrefersKeepingWhenAmbiguous(t *testing.T) {
	fresh := strings.Join([]string{
		"var Module = fx.Provide(",
		"\tNewData, // renamed comment",
		"\t// +co:anchor data-providers",
		")",
		"",
	}, "\n")
	current := strings.Join([]string{
		"var Module = fx.Provide(",
		"\tNewData, // old comment",
		"\tNewCartRepo,",
		"\t// +co:anchor data-providers",
		")",
		"",
	}, "\n")

	got, warnings := carryAnchors("internal/data/data.go", fresh, current)
	assert.Empty(t, warnings)
	assert.Contains(t, got, "NewCartRepo,", "接线必须在")
	assert.Contains(t, got, "NewData, // renamed comment", "模板改动必须在")
	assert.Contains(t, got, "NewData, // old comment", "分不清时保留,交给人看 diff")
}

// 锚点行本身的缩进以服务为准:gofmt 对「只剩一条注释的 fx.Provide(」
// 排的缩进比有元素时少一级,之后再 gofmt 也不会改回来。
// 曾经的表现:接线搬对了,biz.go / service.go 仍因锚点行差一个 tab 报 modified。
func TestCarryAnchorsTakesAnchorIndentFromService(t *testing.T) {
	fresh := "fx.Provide(\n\t// +co:anchor biz-providers\n),\n"
	current := "fx.Provide(\n\t\tNewCartUseCase,\n\t\t// +co:anchor biz-providers\n),\n"

	got, warnings := carryAnchors("internal/biz/biz.go", fresh, current)
	assert.Empty(t, warnings)
	assert.Equal(t, current, got)
}

// 非锚点文件、没有注释语法的文件,一律原样返回。
func TestCarryAnchorsNoop(t *testing.T) {
	got, warnings := carryAnchors("README.txt", "a\n", "b\n")
	assert.Equal(t, "a\n", got)
	assert.Empty(t, warnings)

	got, warnings = carryAnchors("x.go", "package a\n", "package b\n")
	assert.Equal(t, "package a\n", got)
	assert.Empty(t, warnings)
}

// hook 的产物不参与比对。
//
// 曾经的表现:刚 co new 完立刻 upgrade,报 16 处差异,其中包括 go.mod ——
// 那是 go mod tidy / buf / sqlc 的产物,拿模板那份覆盖回去会把 module 路径
// 改回模板自己的。
func TestSkipCompareExcludesGeneratedArtifacts(t *testing.T) {
	for _, p := range []string{
		"go.mod",
		"go.sum",
		"internal/conf/v1/conf.pb.go",
		"internal/data/models/db.go",
	} {
		assert.True(t, skipCompare(p), "%s 是构建产物,不该参与比对", p)
	}
	for _, p := range []string{"Makefile", "cmd/server/main.go", "internal/pkg/log/log.go"} {
		assert.False(t, skipCompare(p), "%s 是源码,必须参与比对", p)
	}
}

// 只差 gofmt 的两份内容算同一份。
//
// 裁剪掉 +co: 标记行之后缩进和空行会和 gofmt 后的结果对不上,
// 不归一化的话每个 .go 文件都会被报成 modified。
func TestNormalizeHidesGofmtOnlyDifferences(t *testing.T) {
	unformatted := []byte("package a\n\nimport  \"fmt\"\n\nfunc F(){fmt.Println( 1 )}\n")
	formatted := []byte("package a\n\nimport \"fmt\"\n\nfunc F() { fmt.Println(1) }\n")

	assert.NotEqual(t, unformatted, formatted, "前提:两份原始字节确实不同")
	assert.Equal(t,
		string(normalize("x.go", unformatted)),
		string(normalize("x.go", formatted)),
		"gofmt 之后应当相同")

	// 非 .go 不做归一化,否则会把 YAML 之类的内容改坏
	assert.NotEqual(t,
		string(normalize("x.yaml", unformatted)),
		string(normalize("x.yaml", formatted)))
}

// 在错误的目录上跑要明确报错,而不是把一整棵树报成 added。
func TestPlanUpgradeRejectsWrongDir(t *testing.T) {
	src, m := templateSource(t)

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"),
		[]byte("module github.com/acme/not-a-service\n\ngo 1.26\n"), 0o644))

	_, err := PlanUpgrade(context.Background(), src, m, UpgradeOptions{ServiceDir: dir})
	require.Error(t, err)
}

// 纯插入的接线不在尾部也要搬:gofmt 会把 import 排序,co new 插的 cartv1connect
// 排到模板自带的 searchv1connect 前面,不再紧贴锚点。
// 曾经的表现:--keep-example 服务 upgrade 后 cartv1connect import 丢了。
func TestCarryAnchorsKeepsSortedInsertions(t *testing.T) {
	fresh := strings.Join([]string{
		"import (",
		"\t\"connectrpc.com/validate\"",
		"\t\"github.com/acme/cart/api/search/v1/searchv1connect\"",
		"\t// +co:anchor server-imports",
		"\t\"go.uber.org/fx\"",
		")",
		"",
	}, "\n")
	current := strings.Join([]string{
		"import (",
		"\t\"connectrpc.com/validate\"",
		"\t\"github.com/acme/cart/api/cart/v1/cartv1connect\"",
		"\t\"github.com/acme/cart/api/search/v1/searchv1connect\"",
		"\t// +co:anchor server-imports",
		"\t\"go.uber.org/fx\"",
		")",
		"",
	}, "\n")

	got, warnings := carryAnchors("internal/server/server.go", fresh, current)
	assert.Empty(t, warnings)
	assert.Equal(t, current, got)
}

// 锚点不在 import 块里时,legacy 文件多出来的 import 不搬:用到它们的代码在
// 锚点下方已被模板替换,搬过来只剩 "imported and not used"。
// 锚点在 import 块里(server-imports)时 import 行就是接线,必须搬。
func TestCarryAnchorsDropsStrayImportsOutsideImportBlock(t *testing.T) {
	fresh := strings.Join([]string{
		"package data",
		"",
		"import (",
		"\t\"go.uber.org/fx\"",
		")",
		"",
		"var Module = fx.Provide(",
		"\tNewData,",
		"\t// +co:anchor data-providers",
		")",
		"",
	}, "\n")
	current := strings.Join([]string{
		"package data",
		"",
		"import (",
		"\t\"crypto/tls\"",
		"\tpgx \"github.com/jackc/pgx/v5\"",
		"\t\"go.uber.org/fx\"",
		")",
		"",
		"var Module = fx.Provide(",
		"\tNewData,",
		"\tNewCartRepo,",
		"\t// +co:anchor data-providers",
		")",
		"",
	}, "\n")
	got, warnings := carryAnchors("internal/data/data.go", fresh, current)
	assert.Empty(t, warnings)
	assert.Contains(t, got, "NewCartRepo,")
	assert.NotContains(t, got, "crypto/tls")
	assert.NotContains(t, got, "pgx/v5")

	// 同样的行在 server-imports 锚点段里是接线
	fresh = "import (\n\t\"go.uber.org/fx\"\n\t// +co:anchor server-imports\n)\n"
	current = "import (\n\t\"go.uber.org/fx\"\n\tpgx \"github.com/jackc/pgx/v5\"\n\t// +co:anchor server-imports\n)\n"
	got, _ = carryAnchors("internal/server/server.go", fresh, current)
	assert.Equal(t, current, got)
}
