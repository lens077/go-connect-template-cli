package scaffold

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/mod/modfile"

	"github.com/lens077/go-connect-template-cli/internal/manifest"
)

// co resource add 是在一个已经生成好的服务里干活,而那个服务里没有 manifest
// (.co/ 是模板的输入,不是产物)。所以「这个服务启用了哪些 feature」只能反推。
//
// 反推的依据是 manifest 自己声明的 files/requires:文件还在 = feature 还在。
// 这比让用户每次 resource add 都重新报一遍选型可靠 —— 报错了会生成一份
// 引用不存在的包的代码,而文件在不在是客观事实。

// DetectFeatures 从已生成的服务目录反推启用了哪些 feature。
func DetectFeatures(serviceRoot string, m *manifest.Manifest) (manifest.FeatureSet, error) {
	requires, err := readRequires(filepath.Join(serviceRoot, "go.mod"))
	if err != nil {
		return nil, err
	}

	fileOwners, requireOwners := evidenceOwners(m)
	set := manifest.FeatureSet{}
	for _, f := range m.SortedFeatures() {
		if f.Always {
			set[f.Name] = true
			continue
		}

		// 只用本 feature 独占的证据。多个 adapter 可以共享接口文件;
		// 若把共享文件也算上,ES 项目会同时被误判成启用了 Meilisearch。
		observable := false
		for _, rel := range f.Files {
			if fileOwners[rel] != 1 {
				continue
			}
			observable = true
			if _, serr := os.Stat(filepath.Join(serviceRoot, filepath.FromSlash(rel))); serr == nil {
				set[f.Name] = true
				break
			}
		}
		if !set[f.Name] && requires != nil {
			for _, req := range f.Requires {
				if requireOwners[req] != 1 {
					continue
				}
				observable = true
				if requires[req] {
					set[f.Name] = true
					break
				}
			}
		}
		if !observable {
			// 没有独占证据时无法可靠反推。宁可多留一段用不上的代码,
			// 也不要少生成一段导致编译失败。
			set[f.Name] = true
		}
	}
	// 没探测到的 feature 也要显式置 false,模板才敢用 {{if .Features.x}}
	m.Complete(set)
	return set, nil
}

func evidenceOwners(m *manifest.Manifest) (map[string]int, map[string]int) {
	files := map[string]int{}
	requires := map[string]int{}
	for _, f := range m.SortedFeatures() {
		for _, file := range f.Files {
			files[file]++
		}
		for _, req := range f.Requires {
			requires[req]++
		}
	}
	return files, requires
}

// readRequires 读 go.mod 的直接依赖。monorepo 下服务没有 go.mod,返回 nil。
func readRequires(path string) (map[string]bool, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	f, err := modfile.Parse(filepath.Base(path), data, nil)
	if err != nil {
		return nil, fmt.Errorf("parse go.mod: %w", err)
	}

	out := map[string]bool{}
	for _, r := range f.Require {
		if !r.Indirect {
			out[r.Mod.Path] = true
		}
	}
	return out, nil
}

// ServiceInfo 是从一个已存在的服务目录里读出来的身份信息。
type ServiceInfo struct {
	// Root 服务目录
	Root string
	// Module 拥有该服务的 module 路径(monorepo 下是根 module)
	Module string
	// ServiceModule 服务包的导入前缀
	ServiceModule string
	// ModuleRoot 拥有该服务的 go.mod 所在目录
	ModuleRoot string
	// APIRoot api/ 树的实际位置(绝对路径)
	APIRoot string
}

// InspectTarget 从服务目录反推 module 路径,不要求 api/ 存在。
// co proto gen 只需要知道 internal/ 的导入前缀,proto 文件本身已经在手边。
func InspectTarget(dir string) (ServiceInfo, error) {
	root, err := filepath.Abs(dir)
	if err != nil {
		return ServiceInfo{}, err
	}

	modRoot, err := findModuleRoot(root)
	if err != nil {
		return ServiceInfo{}, err
	}
	mod, err := ReadModulePath(filepath.Join(modRoot, "go.mod"))
	if err != nil {
		return ServiceInfo{}, err
	}

	rel, err := filepath.Rel(modRoot, root)
	if err != nil {
		return ServiceInfo{}, err
	}

	serviceModule := mod
	if rel != "." {
		serviceModule = mod + "/" + ToSlash(rel)
	}

	apiRoot := filepath.Join(root, "api")
	if _, serr := os.Stat(apiRoot); serr != nil {
		alt := filepath.Join(modRoot, "api")
		if _, aerr := os.Stat(alt); aerr == nil {
			apiRoot = alt
		} else {
			apiRoot = ""
		}
	}

	return ServiceInfo{
		Root:          root,
		Module:        mod,
		ServiceModule: serviceModule,
		ModuleRoot:    modRoot,
		APIRoot:       apiRoot,
	}, nil
}

// InspectService 从服务目录反推 module 与 api/ 的位置。
//
// 往上找 go.mod 而不是要求用户传 --module:standalone 下 go.mod 就在服务目录里,
// monorepo 下在仓库根,两种情况用同一套逻辑就能覆盖,用户什么都不用记。
func InspectService(dir string) (ServiceInfo, error) {
	info, err := InspectTarget(dir)
	if err != nil {
		return ServiceInfo{}, err
	}
	if info.APIRoot == "" {
		return ServiceInfo{}, fmt.Errorf("cannot locate api/ under %s or %s", info.Root, info.ModuleRoot)
	}
	return info, nil
}

func findModuleRoot(start string) (string, error) {
	dir := start
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no go.mod found at or above %s; run this inside a generated service", start)
		}
		dir = parent
	}
}

// RelAPIDir 把资源的 APIDir(api/<name>/v1)换算成相对服务目录的路径,
// 使得写文件时统一以服务目录为基准。monorepo 下会得到 ../../api/<name>/v1。
func RelAPIDir(info ServiceInfo, apiDir string) (string, error) {
	sub := strings.TrimPrefix(apiDir, "api/")
	abs := filepath.Join(info.APIRoot, filepath.FromSlash(sub))
	return filepath.Rel(info.Root, abs)
}
