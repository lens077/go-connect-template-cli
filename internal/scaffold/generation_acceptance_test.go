package scaffold

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/mod/modfile"
	"golang.org/x/mod/semver"
)

func TestGeneratedServiceUsesPublishedSharedKit(t *testing.T) {
	requireTools(t, "go")

	src, manifest := templateSource(t)
	plan, err := NewPlan(src, manifest, Options{
		Name:       "cart",
		Module:     "github.com/acme/cart",
		Dest:       t.TempDir(),
		Layout:     "standalone",
		Features:   []string{"postgres"},
		NoResource: true,
	})
	require.NoError(t, err)
	require.NoError(t, Apply(context.Background(), plan, nil))
	root := filepath.Join(plan.Dest, plan.ServiceDir)

	for _, rel := range []string{
		"internal/pkg/env",
		"internal/pkg/meta",
		"internal/pkg/dbutil",
		"internal/pkg/healthcheck",
	} {
		_, statErr := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
		assert.True(t, os.IsNotExist(statErr), "生成物不应包含本地共享实现 %s", rel)
	}

	goModPath := filepath.Join(root, "go.mod")
	goMod, err := os.ReadFile(goModPath)
	require.NoError(t, err)
	parsed, err := modfile.Parse(goModPath, goMod, nil)
	require.NoError(t, err)

	const kitModule = "github.com/lens077/go-connect-kit"
	var kitVersion string
	for _, dependency := range parsed.Require {
		if dependency.Mod.Path == kitModule {
			kitVersion = dependency.Mod.Version
			assert.False(t, dependency.Indirect, "go-connect-kit 必须是直接依赖")
			break
		}
	}
	require.NotEmpty(t, kitVersion, "生成物必须固定 go-connect-kit 版本")
	require.True(t, semver.IsValid(kitVersion), "go-connect-kit 版本不是有效 semver: %q", kitVersion)
	for _, replacement := range parsed.Replace {
		assert.NotEqual(t, kitModule, replacement.Old.Path, "生成物不能用 replace 伪造 go-connect-kit 发布")
	}

	dockerfile, err := os.ReadFile(filepath.Join(root, "Dockerfile"))
	require.NoError(t, err)
	assert.Contains(t, string(dockerfile), "-X github.com/lens077/go-connect-kit/meta.Version=$VERSION")
	assert.NotContains(t, string(dockerfile), "-X main.Version=$VERSION")

	cmd := exec.Command("go", "build", "./...")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOWORK=off", "CO_USE_LOCAL_MODULES=")
	output, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "生成物无法使用已发布的 %s@%s 编译:\n%s", kitModule, kitVersion, output)
}
