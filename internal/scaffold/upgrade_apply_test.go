package scaffold

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// anchorLine 匹配任何语言的 +co:anchor 行(含行尾)。
var anchorLine = regexp.MustCompile(`(?m)^[ \t]*(//|#|--|<!--)[ \t]*\+co:anchor[^\n]*\n`)

// stripAnchors 把服务里所有锚点行删掉,得到「锚点机制之前生成」的 legacy 形状:
// 接线还在,但没有任何标记指出它在哪。这正是当前 ecommerce 里 cart 等服务的样子。
func stripAnchors(t *testing.T, serviceRoot string) []string {
	t.Helper()
	var stripped []string
	err := filepath.WalkDir(serviceRoot, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		if !anchorLine.Match(data) {
			return nil
		}
		rel, _ := filepath.Rel(serviceRoot, p)
		stripped = append(stripped, ToSlash(rel))
		return os.WriteFile(p, anchorLine.ReplaceAll(data, nil), 0o644)
	})
	require.NoError(t, err)
	require.NotEmpty(t, stripped, "fixture 里应当有锚点文件可剥")
	return stripped
}

// legacy 服务(无锚点、接线在):锚点文件必须报 blocked,任何策略都不写,接线不动。
//
// 这是 upgrade 对真实 ecommerce 服务最危险的路径。之前的实现对这类文件是普通
// modified,--write 一把盖上去,NewCartUseCase / mux.Handle(...) 全没了,
// go build 照样绿。
func TestUpgradeBlocksLegacyAnchorFiles(t *testing.T) {
	serviceRoot, src := generateFixture(t, upgradeFixtureFeatures)
	stripped := stripAnchors(t, serviceRoot)
	assertWiringIntact(t, serviceRoot)

	_, m := templateSource(t)
	up, err := PlanUpgrade(context.Background(), src, m, UpgradeOptions{ServiceDir: serviceRoot})
	require.NoError(t, err)
	defer func() { _ = up.Close() }()

	got := map[string]Change{}
	for _, c := range up.Changes {
		got[c.Path] = c
	}
	for _, rel := range stripped {
		c, ok := got[rel]
		require.True(t, ok, "%s 剥掉锚点后必须被识别为差异", rel)
		assert.Equal(t, ChangeModified, c.Kind)
		assert.NotEmpty(t, c.Blocked, "%s 是 legacy 锚点文件,必须 blocked", rel)
	}
	assert.Len(t, up.Changes, len(stripped), "除了剥掉锚点的文件不该有别的差异")

	// 三种策略都不能碰 blocked:默认、--write-modified、--only 点名
	for _, policy := range []WritePolicy{
		{},
		{Modified: true},
		{Only: stripped},
	} {
		sel, err := up.Select(policy)
		require.NoError(t, err)
		assert.Empty(t, sel.Write, "policy %+v 不该选中任何 blocked 文件", policy)
		assert.Len(t, sel.Blocked, len(stripped))

		n, err := up.Apply(silentReporter{}, sel)
		require.NoError(t, err)
		assert.Zero(t, n)
	}
	assertWiringIntact(t, serviceRoot)
	for _, rel := range stripped {
		data, err := os.ReadFile(filepath.Join(serviceRoot, filepath.FromSlash(rel)))
		require.NoError(t, err)
		assert.False(t, anchorLine.Match(data), "%s 不该被写回(写回会带上锚点)", rel)
	}
}

// 默认策略只写 added;modified 要显式要求。
func TestSelectWritePolicy(t *testing.T) {
	up := &Upgrade{Changes: []Change{
		{Path: "Makefile", Kind: ChangeAdded},
		{Path: "Dockerfile", Kind: ChangeModified},
		{Path: "internal/biz/biz.go", Kind: ChangeModified, Blocked: "legacy"},
	}}
	paths := func(cs []Change) []string {
		var out []string
		for _, c := range cs {
			out = append(out, c.Path)
		}
		return out
	}

	sel, err := up.Select(WritePolicy{})
	require.NoError(t, err)
	assert.Equal(t, []string{"Makefile"}, paths(sel.Write), "默认只写 added")
	assert.Equal(t, []string{"Dockerfile"}, paths(sel.Skipped))
	assert.Equal(t, []string{"internal/biz/biz.go"}, paths(sel.Blocked))

	sel, err = up.Select(WritePolicy{Modified: true})
	require.NoError(t, err)
	assert.Equal(t, []string{"Makefile", "Dockerfile"}, paths(sel.Write))
	assert.Empty(t, sel.Skipped)
	assert.Equal(t, []string{"internal/biz/biz.go"}, paths(sel.Blocked), "Modified 不解锁 blocked")

	sel, err = up.Select(WritePolicy{Only: []string{"./Dockerfile"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"Dockerfile"}, paths(sel.Write), "--only 点名的 modified 视为已同意")
	assert.Equal(t, []string{"Makefile"}, paths(sel.Skipped), "--only 之外的 added 也不写")

	sel, err = up.Select(WritePolicy{Only: []string{"internal/biz/biz.go"}})
	require.NoError(t, err)
	assert.Empty(t, sel.Write, "--only 点名 blocked 也不写")

	_, err = up.Select(WritePolicy{Only: []string{"nope.go"}})
	require.Error(t, err, "点名不存在的路径必须报错,不能静默忽略")
	assert.Contains(t, err.Error(), "nope.go")
}

// 写入要么全成功要么全不写:第 N 个失败时前 N-1 个必须回滚。
func TestUpgradeApplyRollsBackOnFailure(t *testing.T) {
	root := t.TempDir()
	fresh := t.TempDir()

	// 现有文件 a.txt、b.txt;c/ 是个目录,而模板要在 c 这个路径写文件 → rename 失败
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.txt"), []byte("old a\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "b.txt"), []byte("old b\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "c", "inside"), 0o755))

	up := &Upgrade{
		ServiceRoot: root,
		freshRoot:   fresh,
		Changes: []Change{
			{Path: "a.txt", Kind: ChangeModified},
			{Path: "b.txt", Kind: ChangeModified},
			{Path: "c", Kind: ChangeAdded},
			{Path: "d.txt", Kind: ChangeAdded},
		},
		next: map[string][]byte{
			"a.txt": []byte("new a\n"),
			"b.txt": []byte("new b\n"),
			"c":     []byte("new c\n"),
			"d.txt": []byte("new d\n"),
		},
	}
	sel, err := up.Select(WritePolicy{Modified: true})
	require.NoError(t, err)
	require.Len(t, sel.Write, 4)

	n, err := up.Apply(silentReporter{}, sel)
	require.Error(t, err)
	assert.Zero(t, n)
	assert.Contains(t, err.Error(), "rolled back")

	for name, want := range map[string]string{"a.txt": "old a\n", "b.txt": "old b\n"} {
		data, rerr := os.ReadFile(filepath.Join(root, name))
		require.NoError(t, rerr)
		assert.Equal(t, want, string(data), "%s 必须回滚到原内容", name)
	}
	assert.NoFileExists(t, filepath.Join(root, "d.txt"), "失败后新增文件不能留下")
	assert.DirExists(t, filepath.Join(root, "c", "inside"), "冲突的目录原样保留")

	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	for _, e := range entries {
		assert.False(t, strings.HasPrefix(e.Name(), ".co-upgrade-"), "暂存目录必须清掉: %s", e.Name())
	}
}

// 正常路径:写完之后暂存目录不留,内容与权限正确。
func TestUpgradeApplyStagesAndRenames(t *testing.T) {
	root := t.TempDir()
	fresh := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(fresh, "run.sh"), []byte("#!/bin/sh\n"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "old.txt"), []byte("old\n"), 0o644))

	up := &Upgrade{
		ServiceRoot: root,
		freshRoot:   fresh,
		Changes: []Change{
			{Path: "run.sh", Kind: ChangeAdded},
			{Path: "old.txt", Kind: ChangeModified},
			{Path: "deep/dir/new.txt", Kind: ChangeAdded},
		},
		next: map[string][]byte{
			"run.sh":           []byte("#!/bin/sh\necho hi\n"),
			"old.txt":          []byte("new\n"),
			"deep/dir/new.txt": []byte("x\n"),
		},
	}
	sel, err := up.Select(WritePolicy{Modified: true})
	require.NoError(t, err)
	n, err := up.Apply(silentReporter{}, sel)
	require.NoError(t, err)
	assert.Equal(t, 3, n)

	st, err := os.Stat(filepath.Join(root, "run.sh"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), st.Mode().Perm(), "权限跟模板走")
	data, _ := os.ReadFile(filepath.Join(root, "old.txt"))
	assert.Equal(t, "new\n", string(data))
	assert.FileExists(t, filepath.Join(root, "deep", "dir", "new.txt"))

	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	for _, e := range entries {
		assert.False(t, strings.HasPrefix(e.Name(), ".co-upgrade-"), "暂存目录必须清掉: %s", e.Name())
	}
}

// 本地元数据(.DS_Store 等)不进生成物,也不参与比对。
func TestPathSkipperDropsLocalMetadata(t *testing.T) {
	skip := PathSkipper(".git")
	for _, p := range []string{".DS_Store", "internal/.DS_Store", "a/b/Thumbs.db", "x.go~", ".main.go.swp"} {
		assert.True(t, skip(filepath.FromSlash(p)), "%s 是本地元数据", p)
	}
	for _, p := range []string{"Makefile", "internal/biz/biz.go", ".gitignore", ".env.example"} {
		assert.False(t, skip(filepath.FromSlash(p)), "%s 是模板内容", p)
	}
}

// 同包耦合:added 的 Go 文件跟着同目录里最坏的那个 Go 文件走。
//
// 真实 ecommerce cart 上的表现:模板把 data.go 拆成 data.go + cache_redis.go +
// db_postgres.go;legacy data.go 是 blocked,只写 added 会得到
// "NewRedisClient redeclared in this block"。
func TestSelectCouplesAddedGoFilesWithPackageSiblings(t *testing.T) {
	up := &Upgrade{Changes: []Change{
		{Path: "internal/data/cache_redis.go", Kind: ChangeAdded},
		{Path: "internal/data/cache_redis_test.go", Kind: ChangeAdded},
		{Path: "internal/data/data.go", Kind: ChangeModified, Blocked: "legacy"},
		{Path: "internal/data/seeds/00001.sql", Kind: ChangeAdded}, // 非 Go,不受影响
		{Path: "internal/pkg/log/README.md", Kind: ChangeAdded},
		{Path: "internal/pkg/log/log.go", Kind: ChangeModified},
		{Path: "internal/pkg/log/log_test.go", Kind: ChangeAdded},
		{Path: "internal/pkg/otel/otel_test.go", Kind: ChangeAdded}, // 同包无其他差异,照写
		{Path: "internal/server/health.go", Kind: ChangeModified},   // 新版 health.go 配的是新版 server.go
		{Path: "internal/server/server.go", Kind: ChangeModified, Blocked: "legacy"},
	}}
	paths := func(cs []Change) []string {
		var out []string
		for _, c := range cs {
			out = append(out, c.Path)
		}
		return out
	}

	sel, err := up.Select(WritePolicy{})
	require.NoError(t, err)
	assert.Equal(t, []string{
		"internal/data/seeds/00001.sql",
		"internal/pkg/log/README.md",
		"internal/pkg/otel/otel_test.go",
	}, paths(sel.Write))
	assert.Equal(t, []string{
		"internal/data/cache_redis.go",
		"internal/data/cache_redis_test.go",
		"internal/data/data.go",
		"internal/server/server.go",
	}, paths(sel.Blocked), "同包有 blocked 的 added .go 跟着 blocked")
	assert.Equal(t, []string{
		"internal/pkg/log/log.go",
		"internal/pkg/log/log_test.go",
		"internal/server/health.go",
	}, paths(sel.Skipped), "同包有未选中 modified 的 added .go 跟着跳过")
	for _, c := range sel.Skipped {
		if c.Path == "internal/pkg/log/log_test.go" {
			assert.Contains(t, c.Blocked, "log.go")
		}
	}

	// 把 modified 一起选上,log 包就整体可写;data 包仍然 blocked
	sel, err = up.Select(WritePolicy{Modified: true})
	require.NoError(t, err)
	assert.Contains(t, paths(sel.Write), "internal/pkg/log/log.go")
	assert.Contains(t, paths(sel.Write), "internal/pkg/log/log_test.go")
	assert.Equal(t, []string{
		"internal/data/cache_redis.go",
		"internal/data/cache_redis_test.go",
		"internal/data/data.go",
		"internal/server/health.go",
		"internal/server/server.go",
	}, paths(sel.Blocked), "选中的 modified 同包有 blocked 也跟着 blocked")

	// --only 点名 modified 而没点同包 added 的 _test.go:照写,少个测试文件不影响编译
	sel, err = up.Select(WritePolicy{Only: []string{"internal/pkg/log/log.go"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"internal/pkg/log/log.go"}, paths(sel.Write))

	// --only 点名 added 却没点同包的 modified,同样跳过 —— 部分写入就是会炸的
	sel, err = up.Select(WritePolicy{Only: []string{"internal/pkg/log/log_test.go"}})
	require.NoError(t, err)
	assert.Empty(t, sel.Write)
}
