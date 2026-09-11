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

// 三方合并的四行真值表 + 无 base 回退。
//
// 布置:base = 模板仓库 HEAD(FetchAt 从本地 git 导出);服务 = 从工作树生成并写 origin;
// fresh = 模板的一份修改过的拷贝(不是 git 仓库,Commit 为空)。
// 前提:模板工作树相对 HEAD 是干净的,否则 base ≠ 服务生成时的样子,断言会失真 ——
// 脏的时候直接跳过,而不是让人对着一个假失败排查。

func templateGitRoot(t *testing.T) (string, string) {
	t.Helper()
	src, _ := templateSource(t)
	if src.Commit == "" {
		t.Skip("模板目录不是 git 仓库,无法做三方合并测试")
	}
	if src.Dirty {
		t.Skip("模板工作树有未提交改动,base 与生成物对不上;提交后再跑")
	}
	return src.Root, src.Commit
}

// evolveTemplate 把模板拷到临时目录并应用一处修改,返回作为 fresh 的 Source。
func evolveTemplate(t *testing.T, root string, edit func(dir string)) Source {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "template")
	require.NoError(t, copyTree(root, dir, PathSkipper(".git")))
	edit(dir)
	src, err := Fetch(context.Background(), FetchOptions{Dir: dir})
	require.NoError(t, err)
	require.Empty(t, src.Commit, "拷贝出来的模板不该是 git 仓库")
	return src
}

func replaceInFile(t *testing.T, path, old, new string) {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), old, path)
	require.NoError(t, os.WriteFile(path, []byte(strings.Replace(string(data), old, new, 1)), 0o644))
}

// generateWithOrigin 生成服务并写 origin(模拟 co new)。
func generateWithOrigin(t *testing.T, root, commit string) string {
	t.Helper()
	serviceRoot, _ := generateFixture(t, upgradeFixtureFeatures)
	require.NoError(t, WriteOrigin(serviceRoot, Origin{Template: root, Commit: commit, Co: "test"}))
	return serviceRoot
}

const healthComment = "// 应用本身的健康检查"

func TestThreeWayMerge(t *testing.T) {
	root, commit := templateGitRoot(t)
	serverRel := filepath.Join("internal", "server", "server.go")
	fetch := FetchOptions{Dir: root}

	t.Run("template changed, service untouched → take template", func(t *testing.T) {
		serviceRoot := generateWithOrigin(t, root, commit)
		fresh := evolveTemplate(t, root, func(dir string) {
			replaceInFile(t, filepath.Join(dir, serverRel), healthComment, healthComment+"(新版)")
		})
		_, m := templateSource(t)

		up, err := PlanUpgrade(context.Background(), fresh, m, UpgradeOptions{ServiceDir: serviceRoot, Fetch: fetch})
		require.NoError(t, err)
		defer func() { _ = up.Close() }()
		assert.Equal(t, commit, up.Base)
		require.Len(t, up.Changes, 1, "%+v", up.Changes)
		assert.Equal(t, ChangeModified, up.Changes[0].Kind)
		next, _, err := up.Diff("internal/server/server.go")
		require.NoError(t, err)
		assert.Contains(t, string(next), healthComment+"(新版)")
		assert.Contains(t, string(next), "cartService cartv1connect.CartServiceHandler", "接线不靠锚点也要在")
	})

	t.Run("service changed, template untouched → keep service, no diff", func(t *testing.T) {
		serviceRoot := generateWithOrigin(t, root, commit)
		// 用户在锚点段之外改了一行 —— 两方比对下这会被报成 modified 并盖掉
		replaceInFile(t, filepath.Join(serviceRoot, serverRel), healthComment, "// 我们自己的注释")
		fresh := evolveTemplate(t, root, func(string) {})
		_, m := templateSource(t)

		up, err := PlanUpgrade(context.Background(), fresh, m, UpgradeOptions{ServiceDir: serviceRoot, Fetch: fetch})
		require.NoError(t, err)
		defer func() { _ = up.Close() }()
		assert.Empty(t, up.Changes, "用户改动不是模板差异")
	})

	t.Run("both changed different lines → merge keeps both", func(t *testing.T) {
		serviceRoot := generateWithOrigin(t, root, commit)
		replaceInFile(t, filepath.Join(serviceRoot, serverRel), "// 构建处理器链", "// 构建处理器链(我们改的)")
		fresh := evolveTemplate(t, root, func(dir string) {
			replaceInFile(t, filepath.Join(dir, serverRel), healthComment, healthComment+"(新版)")
		})
		_, m := templateSource(t)

		up, err := PlanUpgrade(context.Background(), fresh, m, UpgradeOptions{ServiceDir: serviceRoot, Fetch: fetch})
		require.NoError(t, err)
		defer func() { _ = up.Close() }()
		require.Len(t, up.Changes, 1, "%+v", up.Changes)
		assert.Equal(t, ChangeModified, up.Changes[0].Kind)
		next, _, err := up.Diff("internal/server/server.go")
		require.NoError(t, err)
		assert.Contains(t, string(next), healthComment+"(新版)", "模板的改动")
		assert.Contains(t, string(next), "// 构建处理器链(我们改的)", "用户的改动")

		sel, err := up.Select(WritePolicy{Modified: true})
		require.NoError(t, err)
		_, err = up.Apply(silentReporter{}, sel)
		require.NoError(t, err)
		assertWiringIntact(t, serviceRoot)
	})

	t.Run("both changed the same line → conflict, never written", func(t *testing.T) {
		serviceRoot := generateWithOrigin(t, root, commit)
		replaceInFile(t, filepath.Join(serviceRoot, serverRel), healthComment, "// 我们改的")
		fresh := evolveTemplate(t, root, func(dir string) {
			replaceInFile(t, filepath.Join(dir, serverRel), healthComment, "// 模板改的")
		})
		_, m := templateSource(t)

		up, err := PlanUpgrade(context.Background(), fresh, m, UpgradeOptions{ServiceDir: serviceRoot, Fetch: fetch})
		require.NoError(t, err)
		defer func() { _ = up.Close() }()
		require.Len(t, up.Changes, 1, "%+v", up.Changes)
		c := up.Changes[0]
		assert.Equal(t, ChangeConflict, c.Kind)
		assert.Contains(t, c.Blocked, "conflict")

		next, _, err := up.Diff(c.Path)
		require.NoError(t, err)
		assert.Contains(t, string(next), "<<<<<<< current (this service)")
		assert.Contains(t, string(next), ">>>>>>> template (new)")

		for _, policy := range []WritePolicy{{}, {Modified: true}, {Only: []string{c.Path}}} {
			sel, err := up.Select(policy)
			require.NoError(t, err)
			assert.Empty(t, sel.Write, "%+v", policy)
			n, err := up.Apply(silentReporter{}, sel)
			require.NoError(t, err)
			assert.Zero(t, n)
		}
		data, _ := os.ReadFile(filepath.Join(serviceRoot, serverRel))
		assert.NotContains(t, string(data), "<<<<<<<", "冲突标记不能落盘")
	})

	t.Run("legacy file with a base is merged, not blocked", func(t *testing.T) {
		serviceRoot := generateWithOrigin(t, root, commit)
		stripped := stripAnchors(t, serviceRoot)
		fresh := evolveTemplate(t, root, func(dir string) {
			replaceInFile(t, filepath.Join(dir, serverRel), healthComment, healthComment+"(新版)")
		})
		_, m := templateSource(t)

		up, err := PlanUpgrade(context.Background(), fresh, m, UpgradeOptions{ServiceDir: serviceRoot, Fetch: fetch})
		require.NoError(t, err)
		defer func() { _ = up.Close() }()
		for _, c := range up.Changes {
			assert.NotEqual(t, ChangeConflict, c.Kind, "%s", c.Path)
			assert.Empty(t, c.Blocked, "有 base 就不该 blocked: %s", c.Path)
		}
		// 剥掉锚点的文件相对 base 是「用户删了几行注释」,合并结果保留这个删除;
		// 只有模板真正改动的 server.go 报 modified
		got := map[string]bool{}
		for _, c := range up.Changes {
			got[c.Path] = true
		}
		assert.True(t, got["internal/server/server.go"])
		assert.Len(t, up.Changes, 1, "%+v", up.Changes)

		sel, err := up.Select(WritePolicy{Modified: true})
		require.NoError(t, err)
		_, err = up.Apply(silentReporter{}, sel)
		require.NoError(t, err)
		assertWiringIntact(t, serviceRoot)
		data, _ := os.ReadFile(filepath.Join(serviceRoot, serverRel))
		assert.NotContains(t, string(data), "+co:anchor", "用户删掉的锚点不该被合并加回来")
		_ = stripped
	})

	t.Run("--no-base falls back to two-way", func(t *testing.T) {
		serviceRoot := generateWithOrigin(t, root, commit)
		replaceInFile(t, filepath.Join(serviceRoot, serverRel), healthComment, "// 我们自己的注释")
		fresh := evolveTemplate(t, root, func(string) {})
		_, m := templateSource(t)

		up, err := PlanUpgrade(context.Background(), fresh, m, UpgradeOptions{ServiceDir: serviceRoot, Fetch: fetch, NoBase: true})
		require.NoError(t, err)
		defer func() { _ = up.Close() }()
		assert.Empty(t, up.Base)
		require.Len(t, up.Changes, 1, "两方比对分不清用户改动,报 modified")
	})

	t.Run("origin pointing at an unfetchable revision is an error, not a silent fallback", func(t *testing.T) {
		serviceRoot := generateWithOrigin(t, root, "0000000000000000000000000000000000000000")
		fresh := evolveTemplate(t, root, func(string) {})
		_, m := templateSource(t)

		_, err := PlanUpgrade(context.Background(), fresh, m, UpgradeOptions{ServiceDir: serviceRoot, Fetch: fetch})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--no-base")
	})
}

// base 与模板当前 commit 相同时不必再生成一份,但必须仍按三方语义(用户改动不算差异)。
func TestThreeWayMergeBaseEqualsFresh(t *testing.T) {
	root, commit := templateGitRoot(t)
	serviceRoot := generateWithOrigin(t, root, commit)
	replaceInFile(t, filepath.Join(serviceRoot, "internal", "server", "server.go"), healthComment, "// 我们自己的注释")

	src, m := templateSource(t)
	require.Equal(t, commit, src.Commit)
	up, err := PlanUpgrade(context.Background(), src, m, UpgradeOptions{ServiceDir: serviceRoot, Fetch: FetchOptions{Dir: root}})
	require.NoError(t, err)
	defer func() { _ = up.Close() }()
	assert.Equal(t, commit, up.Base)
	assert.Empty(t, up.Changes)
}

func TestOriginRoundTrip(t *testing.T) {
	dir := t.TempDir()
	o, err := ReadOrigin(dir)
	require.NoError(t, err)
	assert.Nil(t, o, "没有文件是合法状态")

	want := Origin{Template: "https://example.com/t", Commit: strings.Repeat("a", 40), Dirty: true, Co: "v9",
		Render: RenderParams{DockerRegistry: "reg.example.com"}}
	require.NoError(t, WriteOrigin(dir, want))
	got, err := ReadOrigin(dir)
	require.NoError(t, err)
	assert.Equal(t, want, *got)

	assert.Error(t, WriteOrigin(dir, Origin{}), "没有 commit 的 origin 没有意义")
	assert.True(t, skipCompare(OriginFile), "origin 不参与比对")
}

func TestFetchAtExportsSnapshot(t *testing.T) {
	root, commit := templateGitRoot(t)
	cache := t.TempDir()
	src, err := FetchAt(context.Background(), FetchOptions{Dir: root, CacheDir: cache}, commit)
	require.NoError(t, err)
	assert.Equal(t, commit, src.Commit)
	assert.FileExists(t, filepath.Join(src.Root, ".co", "manifest.yaml"))
	assert.NoDirExists(t, filepath.Join(src.Root, ".git"))

	again, err := FetchAt(context.Background(), FetchOptions{Dir: root, CacheDir: cache}, commit)
	require.NoError(t, err)
	assert.True(t, again.FromCache)

	_, err = FetchAt(context.Background(), FetchOptions{Dir: root, CacheDir: cache}, "no-such-rev")
	require.Error(t, err)
}

// 模板改了紧贴锚点的那一行(接线插入点的正上方)。两方比对分不清那是模板改的还是
// 用户加的,结果里新旧两行都在(fx 启动报 duplicate provide)。有 base 后不允许出现
// 「静默留两行」:要么干净合并,要么显式冲突。
func TestThreeWayMergeAdjacentToAnchor(t *testing.T) {
	root, commit := templateGitRoot(t)
	dataRel := filepath.Join("internal", "data", "data.go")
	const old = "NewSearchCatalog, // +co:elasticsearch|meilisearch"

	serviceRoot := generateWithOrigin(t, root, commit)
	fresh := evolveTemplate(t, root, func(dir string) {
		replaceInFile(t, filepath.Join(dir, dataRel), old, "NewCatalog, // +co:elasticsearch|meilisearch")
	})
	_, m := templateSource(t)

	up, err := PlanUpgrade(context.Background(), fresh, m, UpgradeOptions{ServiceDir: serviceRoot, Fetch: FetchOptions{Dir: root}})
	require.NoError(t, err)
	defer func() { _ = up.Close() }()

	var c *Change
	for i := range up.Changes {
		if up.Changes[i].Path == "internal/data/data.go" {
			c = &up.Changes[i]
		}
	}
	require.NotNil(t, c, "%+v", up.Changes)
	next, _, err := up.Diff(c.Path)
	require.NoError(t, err)

	switch c.Kind {
	case ChangeModified:
		assert.Contains(t, string(next), "NewCatalog,", "模板的改名")
		assert.NotContains(t, string(next), "NewSearchCatalog,", "旧行不能留下")
		assert.Contains(t, string(next), "NewCartRepo,", "接线")
	case ChangeConflict:
		assert.Contains(t, string(next), "<<<<<<<")
	default:
		t.Fatalf("unexpected kind %s", c.Kind)
	}
	t.Logf("adjacent-to-anchor edit resolved as %s", c.Kind)
}
