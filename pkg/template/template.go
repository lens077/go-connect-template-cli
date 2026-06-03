package template

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

type NewOptions struct {
	Path      string
	RepoURL   string
	NoMod     bool
	BuildTool string // "makefile" or "taskfile"
	Database  string // "mysql", "postgres", "sqlite", "none"
	Cache     string // "redis", "none"
	Search    string // "es", "none"
	IAM       string // "casdoor", "none"
}

type ProtoAddOptions struct {
	ProtoPath string
}

type ProtoServerOptions struct {
	ProtoPath string
	TargetDir string
}

// ServiceMethod 表示服务方法定义
type ServiceMethod struct {
	Name         string
	RequestType  string
	ResponseType string
}

// ServiceInfo 表示从proto文件解析出的服务信息
type ServiceInfo struct {
	ServiceName string
	Methods     []ServiceMethod
}

func CreateNewProject(opts NewOptions) error {
	parts := strings.Split(opts.Path, "/")
	appName := parts[len(parts)-1]
	templateURL := opts.RepoURL
	targetPath := filepath.Join(".", opts.Path)

	if err := gitClone(templateURL, targetPath); err != nil {
		return fmt.Errorf("failed to clone template: %w", err)
	}

	if opts.NoMod {
		if err := handleMonorepoMode(targetPath, opts.Path, appName, opts.BuildTool, opts); err != nil {
			return fmt.Errorf("failed to handle monorepo mode: %w", err)
		}
	} else {
		goModPath := filepath.Join(targetPath, "go.mod")
		if err := updateGoMod(goModPath, "github.com/lens077/go-connect-template", opts.Path); err != nil {
			return fmt.Errorf("failed to update go.mod: %w", err)
		}

		if err := updateAllGoFiles(targetPath, "github.com/lens077/go-connect-template", opts.Path); err != nil {
			return fmt.Errorf("failed to update go files: %w", err)
		}

		if err := updateProtoFiles(targetPath, "github.com/lens077/go-connect-template", opts.Path); err != nil {
			return fmt.Errorf("failed to update proto files: %w", err)
		}

		mainFilePath := filepath.Join(targetPath, "cmd", "server", "main.go")
		if err := ensureMainImports(mainFilePath, appName); err != nil {
			return fmt.Errorf("failed to update main.go imports: %w", err)
		}

		if err := renameSearchFiles(targetPath, appName); err != nil {
			return fmt.Errorf("failed to rename search.go files: %w", err)
		}

		if err := createTestFiles(targetPath, opts.Path); err != nil {
			return fmt.Errorf("failed to create test files: %w", err)
		}

		if err := createBuildFile(targetPath, opts.BuildTool, false); err != nil {
			return fmt.Errorf("failed to create build file: %w", err)
		}
	}

	// Apply configuration options
	if err := updateConfigFiles(targetPath, opts); err != nil {
		return fmt.Errorf("failed to update config files: %w", err)
	}

	if err := updateDataLayer(targetPath, appName, opts); err != nil {
		return fmt.Errorf("failed to update data layer: %w", err)
	}

	fmt.Printf("services %s created successfully at %s\n", appName, targetPath)
	return nil
}

func AddProto(opts ProtoAddOptions) error {
	if err := os.MkdirAll(filepath.Dir(opts.ProtoPath), 0755); err != nil {
		return err
	}

	protoContent := generateProtoContent(opts.ProtoPath)
	return os.WriteFile(opts.ProtoPath, []byte(protoContent), 0644)
}

func GenerateProtoServer(opts ProtoServerOptions) error {
	if err := os.MkdirAll(opts.TargetDir, 0755); err != nil {
		return err
	}

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	absProtoPath, err := filepath.Abs(opts.ProtoPath)
	if err != nil {
		return err
	}

	protoContent, err := os.ReadFile(absProtoPath)
	if err != nil {
		return err
	}

	serviceInfo := parseProtoService(string(protoContent))
	if serviceInfo.ServiceName == "" {
		return fmt.Errorf("cannot find service definition in proto file")
	}

	fileServiceName := strings.TrimSuffix(filepath.Base(absProtoPath), ".proto")
	rootModuleName, rootDir := findGoMod(cwd)
	if rootModuleName == "" {
		return fmt.Errorf("cannot find go.mod file")
	}

	protoRelPath, err := filepath.Rel(rootDir, absProtoPath)
	if err != nil {
		return err
	}

	protoDirPath := filepath.Dir(protoRelPath)
	appModule := rootModuleName
	if cwd != rootDir {
		relPath, err := filepath.Rel(rootDir, cwd)
		if err == nil && relPath != "." {
			if strings.Contains(relPath, "services") {
				pathParts := strings.Split(relPath, "/")
				appIndex := -1
				for i, part := range pathParts {
					if part == "services" {
						appIndex = i
						break
					}
				}
				if appIndex != -1 {
					relPath = strings.Join(pathParts[appIndex:], "/")
				}
			}
			appModule = rootModuleName + "/" + relPath
		}
	}

	serverCode := generateServerCode(serviceInfo.ServiceName, strings.ToLower(serviceInfo.ServiceName), appModule, rootModuleName, protoDirPath, serviceInfo.Methods)
	targetFile := filepath.Join(opts.TargetDir, strings.ToLower(fileServiceName)+"_service.go")
	return os.WriteFile(targetFile, []byte(serverCode), 0644)
}

func gitClone(url, path string) error {
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		return fmt.Errorf("target directory %s already exists", path)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}

	cmd := exec.Command("git", "clone", url, path)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func updateGoMod(path, oldModule, newModule string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	newData := strings.Replace(string(data), "module "+oldModule, "module "+newModule, 1)
	return os.WriteFile(path, []byte(newData), 0644)
}

func updateAllGoFiles(root, oldModule, newModule string) error {
	serviceName := extractServiceName(newModule)
	serviceNameTitle := strings.Title(serviceName)
	serviceNameLower := strings.ToLower(serviceName)

	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if !info.IsDir() && filepath.Ext(path) == ".go" {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}

			content := string(data)

			// 1. 替换模块路径
			content = strings.ReplaceAll(content, "\""+oldModule+"/", "\""+newModule+"/")
			content = strings.ReplaceAll(content, oldModule+".", newModule+".")

			// 2. 替换 API 路径中的 search 为服务名称
			content = strings.ReplaceAll(content, "/api/search/v1", "/api/"+serviceNameLower+"/v1")
			content = strings.ReplaceAll(content, "api/search/v1", "api/"+serviceNameLower+"/v1")
			content = regexp.MustCompile(`api/search/`).ReplaceAllString(content, "api/"+serviceNameLower+"/")

			// 3. 替换 connect 相关的导入和变量名
			content = strings.ReplaceAll(content, "searchv1connect", serviceNameLower+"v1connect")
			content = strings.ReplaceAll(content, "searchv1Service", serviceNameLower+"v1Service")
			content = strings.ReplaceAll(content, "searchv1connectPath", serviceNameLower+"v1connectPath")
			content = strings.ReplaceAll(content, "searchv1connectHandler", serviceNameLower+"v1connectHandler")

			// 4. 替换服务处理器类型和构造函数
			content = strings.ReplaceAll(content, "SearchServiceHandler", serviceNameTitle+"ServiceHandler")
			content = strings.ReplaceAll(content, "NewSearchServiceHandler", "New"+serviceNameTitle+"ServiceHandler")

			// 5. 替换业务层相关的类型和函数
			cleanServiceName := strings.ReplaceAll(serviceName, "-", "_")
			parts := strings.Split(cleanServiceName, "_")
			for i, part := range parts {
				parts[i] = strings.Title(part)
			}
			appName := strings.Join(parts, "")
			content = strings.ReplaceAll(content, "SearchService", appName+"Service")
			content = strings.ReplaceAll(content, "SearchUseCase", appName+"UseCase")
			content = strings.ReplaceAll(content, "SearchRepo", appName+"Repo")
			content = strings.ReplaceAll(content, "searchRepo", strings.ToLower(appName[:1])+appName[1:]+"Repo")
			content = strings.ReplaceAll(content, "NewSearchService", "New"+appName+"Service")
			content = strings.ReplaceAll(content, "NewSearchUseCase", "New"+appName+"UseCase")
			content = strings.ReplaceAll(content, "NewSearchRepo", "New"+appName+"Repo")
			content = strings.ReplaceAll(content, "SearchRequest", appName+"Request")
			content = strings.ReplaceAll(content, "SearchResponse", appName+"Response")

			// 6. 替换方法名 Search（需要精确匹配，避免误替换）
			// 替换方法定义: func (s *XXXService) Search(
			content = regexp.MustCompile(`func \(s \*\w+Service\) Search\(`).ReplaceAllString(content, `func (s *`+appName+`Service) `+appName+`(`)
			// 替换方法调用: s.uc.Search(ctx,
			content = regexp.MustCompile(`\.Search\(ctx,`).ReplaceAllString(content, `.`+appName+`(ctx,`)
			// 替换接口方法定义: Search(ctx context.Context,
			content = regexp.MustCompile(`\tSearch\(ctx context\.Context,`).ReplaceAllString(content, "\t"+appName+"(ctx context.Context,")
			// 替换 UseCase 方法定义: func (uc *XXXUseCase) Search(
			content = regexp.MustCompile(`func \(uc \*\w+UseCase\) Search\(`).ReplaceAllString(content, `func (uc *`+appName+`UseCase) `+appName+`(`)

			if err := os.WriteFile(path, []byte(content), 0644); err != nil {
				return err
			}
		}

		return nil
	})
}

func extractServiceName(modulePath string) string {
	parts := strings.Split(modulePath, "/")
	if len(parts) < 1 {
		return ""
	}
	return parts[len(parts)-1]
}

func updateProtoFiles(root, oldModule, newModule string) error {
	serviceName := extractServiceName(newModule)
	serviceNameTitle := strings.Title(serviceName)
	serviceNameLower := strings.ToLower(serviceName)
	cleanServiceName := strings.ReplaceAll(serviceName, "-", "_")

	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if !info.IsDir() && filepath.Ext(path) == ".proto" {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}

			content := string(data)
			content = strings.ReplaceAll(content, oldModule, newModule)
			content = strings.ReplaceAll(content, ";searchv1\"", ";"+serviceNameLower+"v1\"")
			packageRegex := regexp.MustCompile(`package\s+search\.(v\d+);`)
			content = packageRegex.ReplaceAllString(content, "package "+cleanServiceName+".$1;")
			content = strings.ReplaceAll(content, "service SearchService", "service "+serviceNameTitle+"Service")
			content = strings.ReplaceAll(content, "SearchRequest", serviceNameTitle+"Request")
			content = strings.ReplaceAll(content, "SearchResponse", serviceNameTitle+"Response")
			content = strings.ReplaceAll(content, "api/search/v1", "api/"+serviceNameLower+"/v1")

			if err := os.WriteFile(path, []byte(content), 0644); err != nil {
				return err
			}
		}

		return nil
	})
}

func handleMonorepoMode(targetPath, appPath, appName, buildTool string, opts NewOptions) error {
	rootDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get root directory: %w", err)
	}

	rootGoModPath := filepath.Join(rootDir, "go.mod")
	rootGoModData, err := os.ReadFile(rootGoModPath)
	if err != nil {
		return fmt.Errorf("failed to read root go.mod: %w", err)
	}

	rootModuleName := extractModuleName(string(rootGoModData))
	fullImportPath := fmt.Sprintf("%s/%s", rootModuleName, appPath)

	goModPath := filepath.Join(targetPath, "go.mod")
	if err := os.Remove(goModPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove go.mod: %w", err)
	}

	goSumPath := filepath.Join(targetPath, "go.sum")
	if err := os.Remove(goSumPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove go.sum: %w", err)
	}

	apiPath := filepath.Join(targetPath, "api")
	if err := os.RemoveAll(apiPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove api directory: %w", err)
	}

	if err := updateGoFilesForMonorepo(targetPath, "github.com/lens077/go-connect-template", fullImportPath); err != nil {
		return fmt.Errorf("failed to update go files: %w", err)
	}

	mainFilePath := filepath.Join(targetPath, "cmd", "server", "main.go")
	if err := ensureMainImports(mainFilePath, appName); err != nil {
		return fmt.Errorf("failed to update main.go imports: %w", err)
	}

	makefilePath := filepath.Join(targetPath, "Makefile")
	if _, err := os.Stat(makefilePath); err == nil {
		makefileContent, err := os.ReadFile(makefilePath)
		if err != nil {
			return fmt.Errorf("failed to read Makefile: %w", err)
		}

		content := string(makefileContent)
		content = regexp.MustCompile(`\.PHONY: api\napi:\n[\s\S]*?\n\.PHONY:`).ReplaceAllString(content, ".PHONY: api\napi:\n\tcd ../../ && buf generate --template buf.gen.yaml --path api\n\tcd ../../ && buf generate --template buf.gen.ts.yaml --path api\n\n.PHONY:")
		content = regexp.MustCompile(`\.PHONY: generate\ngenerate:\n[\s\S]*?\n\.PHONY:`).ReplaceAllString(content, ".PHONY: generate\ngenerate:\n\tcd ../../ && buf generate --template buf.gen.yaml --path api\n\tcd ../../ && buf generate --template buf.gen.ts.yaml --path api\n\n.PHONY:")
		content = regexp.MustCompile(`\.PHONY: conf\nconf:\n[\s\S]*?\n\n`).ReplaceAllString(content, ".PHONY: conf\nconf:\n\tcd ../../ && buf generate --template buf.gen.yaml --path api\n\n")
	}

	if err := renameSearchFiles(targetPath, appName); err != nil {
		return fmt.Errorf("failed to rename search.go files: %w", err)
	}

	if err := createTestFiles(targetPath, fullImportPath); err != nil {
		return fmt.Errorf("failed to create test files: %w", err)
	}

	if err := createBuildFile(targetPath, buildTool, true); err != nil {
		return fmt.Errorf("failed to create build file: %w", err)
	}

	return nil
}

func extractModuleName(goModContent string) string {
	moduleRegex := regexp.MustCompile(`module\s+([^\s]+)\s*`)
	matches := moduleRegex.FindStringSubmatch(goModContent)
	if len(matches) > 1 {
		return matches[1]
	}
	return ""
}

func updateGoFilesForMonorepo(root, oldModule, newModulePath string) error {
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if !info.IsDir() && filepath.Ext(path) == ".go" {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}

			content := string(data)

			parts := strings.Split(newModulePath, "/")
			if len(parts) < 1 {
				return fmt.Errorf("invalid module path: %s", newModulePath)
			}

			// 提取服务名称
			appName := parts[len(parts)-1]
			serviceNameLower := strings.ToLower(appName)
			cleanAppName := strings.ReplaceAll(appName, "-", "_")
			appParts := strings.Split(cleanAppName, "_")
			for i, part := range appParts {
				appParts[i] = strings.Title(part)
			}
			appNameTitle := strings.Join(appParts, "")

			// 1. 替换模块路径
			content = strings.ReplaceAll(content, "\""+oldModule+"/", "\""+newModulePath+"/")
			content = strings.ReplaceAll(content, oldModule+".", newModulePath+".")

			// 2. 替换 API 路径中的 search 为服务名称
			content = strings.ReplaceAll(content, "/api/search/v1", "/api/"+serviceNameLower+"/v1")
			content = strings.ReplaceAll(content, "api/search/v1", "api/"+serviceNameLower+"/v1")
			content = regexp.MustCompile(`api/search/`).ReplaceAllString(content, "api/"+serviceNameLower+"/")

			// 3. 替换 connect 相关的导入和变量名
			content = strings.ReplaceAll(content, "searchv1connect", serviceNameLower+"v1connect")
			content = strings.ReplaceAll(content, "searchv1Service", serviceNameLower+"v1Service")
			content = strings.ReplaceAll(content, "searchv1connectPath", serviceNameLower+"v1connectPath")
			content = strings.ReplaceAll(content, "searchv1connectHandler", serviceNameLower+"v1connectHandler")

			// 4. 替换服务处理器类型和构造函数
			content = strings.ReplaceAll(content, "SearchServiceHandler", appNameTitle+"ServiceHandler")
			content = strings.ReplaceAll(content, "NewSearchServiceHandler", "New"+appNameTitle+"ServiceHandler")

			// 5. 替换业务层相关的类型和函数
			content = strings.ReplaceAll(content, "SearchService", appNameTitle+"Service")
			content = strings.ReplaceAll(content, "SearchUseCase", appNameTitle+"UseCase")
			content = strings.ReplaceAll(content, "SearchRepo", appNameTitle+"Repo")
			content = strings.ReplaceAll(content, "searchRepo", strings.ToLower(appNameTitle[:1])+appNameTitle[1:]+"Repo")
			content = strings.ReplaceAll(content, "NewSearchService", "New"+appNameTitle+"Service")
			content = strings.ReplaceAll(content, "NewSearchUseCase", "New"+appNameTitle+"UseCase")
			content = strings.ReplaceAll(content, "NewSearchRepo", "New"+appNameTitle+"Repo")
			content = strings.ReplaceAll(content, "SearchRequest", appNameTitle+"Request")
			content = strings.ReplaceAll(content, "SearchResponse", appNameTitle+"Response")

			// 6. 替换方法名 Search（需要精确匹配，避免误替换）
			// 替换方法定义: func (s *XXXService) Search(
			content = regexp.MustCompile(`func \(s \*\w+Service\) Search\(`).ReplaceAllString(content, `func (s *`+appNameTitle+`Service) `+appNameTitle+`(`)
			// 替换方法调用: s.uc.Search(ctx,
			content = regexp.MustCompile(`\.Search\(ctx,`).ReplaceAllString(content, `.`+appNameTitle+`(ctx,`)
			// 替换接口方法定义: Search(ctx context.Context,
			content = regexp.MustCompile(`\tSearch\(ctx context\.Context,`).ReplaceAllString(content, "\t"+appNameTitle+"(ctx context.Context,")
			// 替换 UseCase 方法定义: func (uc *XXXUseCase) Search(
			content = regexp.MustCompile(`func \(uc \*\w+UseCase\) Search\(`).ReplaceAllString(content, `func (uc *`+appNameTitle+`UseCase) `+appNameTitle+`(`)
			// 替换 repo 方法调用: uc.repo.Search(ctx,
			content = regexp.MustCompile(`\.repo\.Search\(ctx,`).ReplaceAllString(content, `.repo.`+appNameTitle+`(ctx,`)
			// 替换接口方法定义: Search(
			content = regexp.MustCompile(`Search\(ctx context\.Context, req`).ReplaceAllString(content, appNameTitle+"(ctx context.Context, req")

			if err := os.WriteFile(path, []byte(content), 0644); err != nil {
				return err
			}
		}

		return nil
	})
}

func ensureMainImports(path, appName string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	content := string(data)

	if !strings.Contains(content, `"flag"`) || !strings.Contains(content, `"os"`) {
		importRegex := regexp.MustCompile(`import \(([^)]+)\)`)
		matches := importRegex.FindStringSubmatch(content)
		if len(matches) < 2 {
			return fmt.Errorf("could not find import block in main.go")
		}

		importBlock := matches[1]
		newImportBlock := importBlock

		if !strings.Contains(importBlock, `"flag"`) {
			newImportBlock += "\n\t\"flag\""
		}
		if !strings.Contains(importBlock, `"os"`) {
			newImportBlock += "\n\t\"os\""
		}

		content = importRegex.ReplaceAllString(content, "import ("+newImportBlock+")")
	}

	return os.WriteFile(path, []byte(content), 0644)
}

func renameSearchFiles(root, appName string) error {
	fileName := strings.ReplaceAll(appName, "-", "_")
	serviceName := extractServiceName(appName)
	serviceNameLower := strings.ToLower(serviceName)

	var renameDirs []struct{ old, new string }
	var renameFiles []struct{ old, new string }

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() {
			if filepath.Base(path) == "search" && strings.HasSuffix(filepath.Dir(path), "api") {
				dir := filepath.Dir(path)
				newPath := filepath.Join(dir, serviceNameLower)
				renameDirs = append(renameDirs, struct{ old, new string }{path, newPath})
			} else if filepath.Base(path) == "searchv1connect" {
				dir := filepath.Dir(path)
				newPath := filepath.Join(dir, serviceNameLower+"v1connect")
				renameDirs = append(renameDirs, struct{ old, new string }{path, newPath})
			}
		} else {
			if filepath.Base(path) == "search.go" {
				dir := filepath.Dir(path)
				newPath := filepath.Join(dir, fileName+".go")
				renameFiles = append(renameFiles, struct{ old, new string }{path, newPath})
			} else if strings.HasPrefix(filepath.Base(path), "search") {
				dir := filepath.Dir(path)
				newBaseName := strings.Replace(filepath.Base(path), "search", serviceNameLower, 1)
				newPath := filepath.Join(dir, newBaseName)
				renameFiles = append(renameFiles, struct{ old, new string }{path, newPath})
			}
		}

		return nil
	})

	if err != nil {
		return err
	}

	for _, item := range renameFiles {
		if err := os.Rename(item.old, item.new); err != nil {
			return fmt.Errorf("failed to rename %s to %s: %w", item.old, item.new, err)
		}
	}

	for i := len(renameDirs) - 1; i >= 0; i-- {
		item := renameDirs[i]
		if err := os.Rename(item.old, item.new); err != nil {
			return fmt.Errorf("failed to rename directory %s to %s: %w", item.old, item.new, err)
		}
	}

	return nil
}

func createConstantsPackage(root string) error {
	constantsDir := filepath.Join(root, "internal", "constants")
	if err := os.MkdirAll(constantsDir, 0755); err != nil {
		return err
	}

	constantPath := filepath.Join(constantsDir, "constant.go")
	constantContent := `package constants

const (
	Host = "localhost"
	Port = "8080"
)

const (
	UserOwnerMetadataKey = "x-md-global-owner"
	UserNameMetadataKey  = "x-md-global-name"
	UserRoleMetadataKey  = "x-md-global-role"
	UserIdMetadataKey    = "x-md-global-user-id"
)

const (
	FormatConsole = "console"
	FormatJson    = "json"
)

const (
	SslModeDisable    = "disable"
	SslModeAllow      = "allow"
	SslModePrefer     = "prefer"
	SslModeVerifyCa   = "verify-ca"
	SslModeVerifyFull = "verify-full"
)

const (
	ConsulAddr               = "127.0.0.1:8500"
	ConsulPath               = "/consul/"
	ConsulFileFormat         = "yaml"
	ConsulScheme             = "http"
	ConsulTlsScheme          = "https"
	ConsulInsecureSkipVerify = false
	ConsulToken              = ""
)
`
	if err := os.WriteFile(constantPath, []byte(constantContent), 0644); err != nil {
		return err
	}

	envPath := filepath.Join(constantsDir, "env.go")
	envContent := `package constants

import (
	"os"
	"strconv"
)

const (
	EnvServiceName    = "SERVICE_NAME"
	EnvServiceVersion = "SERVICE_VERSION"
	EnvDeploymentMode = "DEPLOYMENT_MODE"
)

const (
	EnvConsulEnabled            = "CONSUL_ENABLED"
	EnvConsulAddr               = "CONSUL_ADDR"
	EnvConsulPath               = "CONSUL_PATH"
	EnvConsulScheme             = "CONSUL_SCHEME"
	EnvConsulToken              = "CONSUL_TOKEN"
	EnvConsulInsecureSkipVerify = "CONSUL_INSECURE_SKIP_VERIFY"
	EnvConsulCaFile             = "CONSUL_CA_FILE"
	EnvConsulCertFile           = "CONSUL_CERT_FILE"
	EnvConsulKeyFile            = "CONSUL_KEY_FILE"
)

func GetEnvString(key, defaultValue string) string {
	if value, exists := os.LookupEnv(key); exists && value != "" {
		return value
	}
	return defaultValue
}

func GetEnvBool(key string, defaultValue bool) bool {
	s, exists := os.LookupEnv(key)
	if !exists || s == "" {
		return defaultValue
	}
	v, err := strconv.ParseBool(s)
	if err != nil {
		return defaultValue
	}
	return v
}
`
	if err := os.WriteFile(envPath, []byte(envContent), 0644); err != nil {
		return err
	}

	return nil
}

func createEnvPackage(root string) error {
	envDir := filepath.Join(root, "internal", "pkg", "env")
	if err := os.MkdirAll(envDir, 0755); err != nil {
		return err
	}

	envPath := filepath.Join(envDir, "env.go")
	envContent := `package env

import (
	"os"
	"strconv"
)

func GetEnvString(key, defaultValue string) string {
	if value, exists := os.LookupEnv(key); exists && value != "" {
		return value
	}
	return defaultValue
}

func GetEnvBool(key string, defaultValue bool) bool {
	s, exists := os.LookupEnv(key)
	if !exists || s == "" {
		return defaultValue
	}
	v, err := strconv.ParseBool(s)
	if err != nil {
		return defaultValue
	}
	return v
}
`
	if err := os.WriteFile(envPath, []byte(envContent), 0644); err != nil {
		return err
	}

	envTestPath := filepath.Join(envDir, "env_test.go")
	envTestContent := `package env

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
)

type EnvTestSuite struct {
	suite.Suite
}

func (suite *EnvTestSuite) SetupTest() {
	os.Clearenv()
}

func (suite *EnvTestSuite) TestGetEnvString_WithExistingEnv() {
	os.Setenv("TEST_KEY", "test-value")
	value := GetEnvString("TEST_KEY", "default-value")
	assert.Equal(suite.T(), "test-value", value)
}

func (suite *EnvTestSuite) TestGetEnvString_WithMissingEnv() {
	value := GetEnvString("NON_EXISTENT_KEY", "default-value")
	assert.Equal(suite.T(), "default-value", value)
}

func (suite *EnvTestSuite) TestGetEnvString_WithEmptyValue() {
	os.Setenv("TEST_KEY", "")
	value := GetEnvString("TEST_KEY", "default-value")
	assert.Equal(suite.T(), "default-value", value)
}

func (suite *EnvTestSuite) TestGetEnvBool_WithTrueValue() {
	testCases := []string{"true", "True", "TRUE", "1"}
	for _, tc := range testCases {
		os.Setenv("TEST_BOOL_KEY", tc)
		value := GetEnvBool("TEST_BOOL_KEY", false)
		assert.True(suite.T(), value, "Test case: %s", tc)
	}
}

func (suite *EnvTestSuite) TestGetEnvBool_WithFalseValue() {
	testCases := []string{"false", "False", "FALSE", "0"}
	for _, tc := range testCases {
		os.Setenv("TEST_BOOL_KEY", tc)
		value := GetEnvBool("TEST_BOOL_KEY", true)
		assert.False(suite.T(), value, "Test case: %s", tc)
	}
}

func (suite *EnvTestSuite) TestGetEnvBool_WithInvalidValue() {
	os.Setenv("TEST_BOOL_KEY", "invalid-value")
	value := GetEnvBool("TEST_BOOL_KEY", true)
	assert.True(suite.T(), value)
}

func (suite *EnvTestSuite) TestGetEnvBool_WithMissingEnv() {
	value := GetEnvBool("NON_EXISTENT_KEY", true)
	assert.True(suite.T(), value)
}

func (suite *EnvTestSuite) TestGetEnvBool_WithEmptyValue() {
	os.Setenv("TEST_BOOL_KEY", "")
	value := GetEnvBool("TEST_BOOL_KEY", true)
	assert.True(suite.T(), value)
}

func (suite *EnvTestSuite) TestGetEnvString_WithMultipleCalls() {
	os.Setenv("TEST_KEY", "value1")
	value1 := GetEnvString("TEST_KEY", "default")
	assert.Equal(suite.T(), "value1", value1)

	os.Setenv("TEST_KEY", "value2")
	value2 := GetEnvString("TEST_KEY", "default")
	assert.Equal(suite.T(), "value2", value2)
}

func TestEnvTestSuite(t *testing.T) {
	suite.Run(t, new(EnvTestSuite))
}

func TestGetEnvString_ConcurrentAccess(t *testing.T) {
	assert.NotPanics(t, func() {
		for i := 0; i < 100; i++ {
			GetEnvString("TEST_KEY", "default")
		}
	})
}

func TestGetEnvBool_ConcurrentAccess(t *testing.T) {
	assert.NotPanics(t, func() {
		for i := 0; i < 100; i++ {
			GetEnvBool("TEST_KEY", true)
		}
	})
}
`
	if err := os.WriteFile(envTestPath, []byte(envTestContent), 0644); err != nil {
		return err
	}

	return nil
}

func createTestFiles(root, appName string) error {
	metaDir := filepath.Join(root, "internal", "pkg", "meta")
	metaTestPath := filepath.Join(metaDir, "meta_test.go")
	metaTestContent := `package meta

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
)

type MetaTestSuite struct {
	suite.Suite
}

func (suite *MetaTestSuite) TestAppInfoStruct() {
	appInfo := AppInfo{
		ID:          "test-id",
		Name:        "test-service",
		Host:        "localhost",
		Environment: "dev",
	}

	assert.Equal(suite.T(), "test-id", appInfo.ID)
	assert.Equal(suite.T(), "test-service", appInfo.Name)
	assert.Equal(suite.T(), "localhost", appInfo.Host)
	assert.Equal(suite.T(), "dev", appInfo.Environment)
}

func (suite *MetaTestSuite) TestGetOutboundIP() {
	ip, err := GetOutboundIP()
	if err == nil {
		assert.NotEmpty(suite.T(), ip)
		assert.NotContains(suite.T(), ip, ":")
	}
}

func (suite *MetaTestSuite) TestGetOutboundIP_PanicRecovery() {
	assert.NotPanics(suite.T(), func() {
		_, _ = GetOutboundIP()
	})
}

func TestMetaTestSuite(t *testing.T) {
	suite.Run(t, new(MetaTestSuite))
}

func TestAppInfoZeroValue(t *testing.T) {
	var appInfo AppInfo
	assert.Empty(t, appInfo.ID)
	assert.Empty(t, appInfo.Name)
	assert.Empty(t, appInfo.Host)
	assert.Empty(t, appInfo.Environment)
}

func TestAppInfoInitialization(t *testing.T) {
	testCases := []struct {
		name        string
		input       AppInfo
		expectedID  string
		expectedName string
	}{
		{
			name: "Full info",
			input: AppInfo{
				ID:          "service-1",
				Name:        "user-service",
				Host:        "192.168.1.1",
				Environment: "production",
			},
			expectedID:   "service-1",
			expectedName: "user-service",
		},
		{
			name: "Partial info",
			input: AppInfo{
				ID:   "service-2",
				Name: "payment-service",
			},
			expectedID:   "service-2",
			expectedName: "payment-service",
		},
		{
			name:        "Empty info",
			input:       AppInfo{},
			expectedID:  "",
			expectedName: "",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expectedID, tc.input.ID)
			assert.Equal(t, tc.expectedName, tc.input.Name)
		})
	}
}
`
	if err := os.WriteFile(metaTestPath, []byte(metaTestContent), 0644); err != nil {
		return err
	}

	registryDir := filepath.Join(root, "internal", "pkg", "registry")
	registryTestPath := filepath.Join(registryDir, "registry_test.go")
	registryTestContent := `package registry

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
	"go.uber.org/zap"
)

type RegistryTestSuite struct {
	suite.Suite
	testLogger *zap.Logger
}

func (suite *RegistryTestSuite) SetupTest() {
	var err error
	suite.testLogger, err = zap.NewDevelopment()
	assert.NoError(suite.T(), err)
	os.Clearenv()
}

func (suite *RegistryTestSuite) TestNewConsulRegistry_WithValidAddr() {
	reg, err := NewConsulRegistry("localhost:8500", "test-id", "test-service", WithLogger(suite.testLogger))
	assert.NoError(suite.T(), err)
	assert.NotNil(suite.T(), reg)
	assert.Equal(suite.T(), "test-id", reg.ID)
	assert.Equal(suite.T(), "test-service", reg.Name)
	assert.Equal(suite.T(), "localhost:8500", reg.Addr)
}

func (suite *RegistryTestSuite) TestNewConsulRegistry_WithInvalidAddr() {
	reg, err := NewConsulRegistry("invalid-addr", "test-id", "test-service", WithLogger(suite.testLogger))
	assert.NoError(suite.T(), err)
	assert.NotNil(suite.T(), reg)
}

func (suite *RegistryTestSuite) TestNewConsulRegistry_WithTLS() {
	reg, err := NewConsulRegistry("localhost:8500", "test-id", "test-service", WithLogger(suite.testLogger), WithTLS(true, ""))
	assert.NoError(suite.T(), err)
	assert.NotNil(suite.T(), reg)
}

func (suite *RegistryTestSuite) TestWithLogger() {
	opt := WithLogger(suite.testLogger)
	o := &options{}
	opt(o)
	assert.Equal(suite.T(), suite.testLogger, o.logger)
}

func (suite *RegistryTestSuite) TestWithTLS() {
	opt := WithTLS(true, "test-ca-pem")
	o := &options{}
	opt(o)
	assert.NotNil(suite.T(), o.tlsConf)
	assert.True(suite.T(), o.tlsConf.InsecureSkipVerify)
}

func (suite *RegistryTestSuite) TestModuleCreation() {
	module := Module
	assert.NotNil(suite.T(), module)
	assert.Contains(suite.T(), module.String(), "registry")
}

func (suite *RegistryTestSuite) TestParseToTCPAddr_ValidHTTPUrl() {
	addr, err := ParseToTCPAddr("http://localhost:8080")
	assert.NoError(suite.T(), err)
	assert.NotNil(suite.T(), addr)
	assert.True(suite.T(), addr.IP.String() == "localhost" || addr.IP.String() == "127.0.0.1")
	assert.Equal(suite.T(), 8080, addr.Port)
}

func (suite *RegistryTestSuite) TestParseToTCPAddr_ValidHTTPSUrl() {
	addr, err := ParseToTCPAddr("https://localhost:8443")
	assert.NoError(suite.T(), err)
	assert.NotNil(suite.T(), addr)
	assert.True(suite.T(), addr.IP.String() == "localhost" || addr.IP.String() == "127.0.0.1")
	assert.Equal(suite.T(), 8443, addr.Port)
}

func (suite *RegistryTestSuite) TestParseToTCPAddr_WithoutPort() {
	addr, err := ParseToTCPAddr("http://example.com")
	assert.NoError(suite.T(), err)
	assert.NotNil(suite.T(), addr)
	assert.Equal(suite.T(), 80, addr.Port)
}

func (suite *RegistryTestSuite) TestParseToTCPAddr_HTTPSWithoutPort() {
	addr, err := ParseToTCPAddr("https://example.com")
	assert.NoError(suite.T(), err)
	assert.NotNil(suite.T(), addr)
	assert.Equal(suite.T(), 443, addr.Port)
}

func (suite *RegistryTestSuite) TestParseToTCPAddr_InvalidUrl() {
	addr, err := ParseToTCPAddr("invalid-url")
	assert.Error(suite.T(), err)
	assert.Nil(suite.T(), addr)
}

func (suite *RegistryTestSuite) TestParseToTCPAddr_EmptyHost() {
	addr, err := ParseToTCPAddr("http://")
	assert.Error(suite.T(), err)
	assert.Nil(suite.T(), addr)
}

func TestRegistryTestSuite(t *testing.T) {
	suite.Run(t, new(RegistryTestSuite))
}

func TestConstants(t *testing.T) {
	assert.Equal(t, "30s", TtlDuration)
	assert.Equal(t, 10*time.Second, TtlPingInterval)
}

func TestNewConsulRegistry_PanicRecovery(t *testing.T) {
	assert.NotPanics(t, func() {
		_, _ = NewConsulRegistry("localhost:8500", "test-id", "test-name")
	})
}
`
	if err := os.WriteFile(registryTestPath, []byte(registryTestContent), 0644); err != nil {
		return err
	}

	configDir := filepath.Join(root, "internal", "pkg", "config")
	configTestPath := filepath.Join(configDir, "config_test.go")
	configTestContent := `package config

import (
	"context"
	"os"
	"testing"

	confv1 "` + appName + `/internal/conf/v1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
)

type ConfigTestSuite struct {
	suite.Suite
}

func (suite *ConfigTestSuite) SetupTest() {
	os.Clearenv()
}

func (suite *ConfigTestSuite) TestDecodeConfig() {
	testConfig := map[string]interface{}{
		"server": map[string]interface{}{
			"http": map[string]interface{}{
				"addr": ":8080",
			},
		},
		"data": map[string]interface{}{
			"database": map[string]interface{}{
				"host":     "localhost",
				"port":     5432,
				"user":     "test",
				"password": "password",
				"db_name":  "test_db",
			},
		},
	}

	target := &confv1.Bootstrap{}
	err := decodeConfig(testConfig, target)

	assert.NoError(suite.T(), err)
	assert.NotNil(suite.T(), target.Server)
	assert.NotNil(suite.T(), target.Server.Http)
	assert.Equal(suite.T(), ":8080", target.Server.Http.Addr)
}

func (suite *ConfigTestSuite) TestUpdateConfig() {
	testConfig := map[string]interface{}{
		"server": map[string]interface{}{
			"http": map[string]interface{}{
				"addr": ":9090",
			},
		},
	}

	updateConfig(testConfig)

	currentConf := GetConfig()
	assert.NotNil(suite.T(), currentConf.Server)
	assert.Equal(suite.T(), ":9090", currentConf.Server.Http.Addr)
}

func (suite *ConfigTestSuite) TestGetConfig() {
	currentConf := GetConfig()
	assert.NotNil(suite.T(), currentConf)
}

func (suite *ConfigTestSuite) TestValidateConfig_Valid() {
	validConfig := &confv1.Bootstrap{
		Server: &confv1.Server{
			Http: &confv1.Server_HTTP{
				Addr: ":8080",
			},
		},
		Data: &confv1.Data{
			Database: &confv1.Data_Database{},
		},
		Auth: &confv1.Auth{
			Endpoint:         "http://localhost:9000",
			ClientId:         "test-client-id",
			ClientSecret:     "test-client-secret",
			OrganizationName: "test-org",
			ApplicationName:  "test-app",
			Certificate:      "test-cert",
		},
		Observability: &confv1.Observability{
			Trace: &confv1.Observability_Trace{
				Endpoint: "http://localhost:4317",
				Tls: &confv1.Observability_Tls{
					Enable: false,
				},
			},
			Metric: &confv1.Observability_Metric{
				Endpoint: "http://localhost:4318",
				Tls: &confv1.Observability_Tls{
					Enable: false,
				},
			},
			Log: &confv1.Observability_Logging{
				Endpoint: "http://localhost:4319",
				Tls: &confv1.Observability_Tls{
					Enable: false,
				},
			},
		},
		Discovery: &confv1.Discovery{
			Consul: &confv1.Discovery_Consul{
				Addr:        "http://localhost:8500",
				Scheme:      "http",
				HealthCheck: true,
			},
		},
	}

	err := ValidateConfig(validConfig)
	assert.NoError(suite.T(), err)
}

func (suite *ConfigTestSuite) TestValidateConfig_NilConfig() {
	err := ValidateConfig(nil)
	assert.Error(suite.T(), err)
	assert.Equal(suite.T(), "configuration is nil", err.Error())
}

func (suite *ConfigTestSuite) TestValidateConfig_MissingServer() {
	invalidConfig := &confv1.Bootstrap{
		Data: &confv1.Data{
			Database: &confv1.Data_Database{},
		},
	}

	err := ValidateConfig(invalidConfig)
	assert.Error(suite.T(), err)
	assert.Equal(suite.T(), "server configuration is required", err.Error())
}

func (suite *ConfigTestSuite) TestValidateConfig_MissingDatabase() {
	invalidConfig := &confv1.Bootstrap{
		Server: &confv1.Server{
			Http: &confv1.Server_HTTP{
				Addr: ":8080",
			},
		},
	}

	err := ValidateConfig(invalidConfig)
	assert.Error(suite.T(), err)
	assert.Equal(suite.T(), "database configuration is required", err.Error())
}

func (suite *ConfigTestSuite) TestModuleCreation() {
	module := Module
	assert.NotNil(suite.T(), module)
	assert.Contains(suite.T(), module.String(), "config")
}

func (suite *ConfigTestSuite) TestInit_MissingConsulPath() {
	ctx := context.Background()
	conf, err := Init(ctx)
	assert.Error(suite.T(), err)
	assert.Nil(suite.T(), conf)
}

func (suite *ConfigTestSuite) TestInit_InvalidConsulAddr() {
	ctx := context.Background()
	os.Setenv("CONSUL_PATH", "test/path")
	os.Setenv("CONSUL_ADDR", "invalid-addr:8500")
	conf, err := Init(ctx)
	assert.Error(suite.T(), err)
	assert.Nil(suite.T(), conf)
}

func TestConfigTestSuite(t *testing.T) {
	suite.Run(t, new(ConfigTestSuite))
}

func TestGetConfig_ConcurrentAccess(t *testing.T) {
	assert.NotPanics(t, func() {
		for i := 0; i < 100; i++ {
			GetConfig()
		}
	})
}
`
	if err := os.WriteFile(configTestPath, []byte(configTestContent), 0644); err != nil {
		return err
	}

	logDir := filepath.Join(root, "internal", "pkg", "log")
	logTestPath := filepath.Join(logDir, "log_test.go")
	logTestContent := `package log

import (
	"testing"

	"` + appName + `/internal/constants"
	"` + appName + `/internal/pkg/meta"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type LogTestSuite struct {
	suite.Suite
	testAppInfo meta.AppInfo
}

func (suite *LogTestSuite) SetupTest() {
	suite.testAppInfo = meta.AppInfo{
		ID:          "test-service-id",
		Name:        "test-service",
		Host:        "localhost",
		Environment: "dev",
	}
}

func (suite *LogTestSuite) TestNewLogger_DebugLevel() {
	logger := NewLogger("debug", constants.FormatJson, suite.testAppInfo)
	assert.NotNil(suite.T(), logger)
	assert.True(suite.T(), logger.Core().Enabled(zapcore.DebugLevel))
}

func (suite *LogTestSuite) TestNewLogger_InfoLevel() {
	logger := NewLogger("info", constants.FormatJson, suite.testAppInfo)
	assert.NotNil(suite.T(), logger)
	assert.True(suite.T(), logger.Core().Enabled(zapcore.InfoLevel))
	assert.False(suite.T(), logger.Core().Enabled(zapcore.DebugLevel))
}

func (suite *LogTestSuite) TestNewLogger_WarnLevel() {
	logger := NewLogger("warn", constants.FormatJson, suite.testAppInfo)
	assert.NotNil(suite.T(), logger)
	assert.True(suite.T(), logger.Core().Enabled(zapcore.WarnLevel))
	assert.False(suite.T(), logger.Core().Enabled(zapcore.InfoLevel))
}

func (suite *LogTestSuite) TestNewLogger_ErrorLevel() {
	logger := NewLogger("error", constants.FormatJson, suite.testAppInfo)
	assert.NotNil(suite.T(), logger)
	assert.True(suite.T(), logger.Core().Enabled(zapcore.ErrorLevel))
	assert.False(suite.T(), logger.Core().Enabled(zapcore.WarnLevel))
}

func (suite *LogTestSuite) TestNewLogger_InvalidLevel() {
	logger := NewLogger("invalid-level", constants.FormatJson, suite.testAppInfo)
	assert.NotNil(suite.T(), logger)
	assert.True(suite.T(), logger.Core().Enabled(zapcore.InfoLevel))
}
`
	if err := os.WriteFile(logTestPath, []byte(logTestContent), 0644); err != nil {
		return err
	}

	return nil
}

func generateProtoContent(protoPath string) string {
	pathParts := strings.Split(protoPath, "/")
	if len(pathParts) < 3 {
		return ""
	}

	pkgName := pathParts[len(pathParts)-2]
	serviceName := strings.TrimSuffix(pathParts[len(pathParts)-1], ".proto")
	goPkg := fmt.Sprintf("./%s/%s/%s;%s", strings.Join(pathParts[:len(pathParts)-1], "/"), serviceName, serviceName, serviceName)

	return fmt.Sprintf(`syntax = "proto3";

package %s;

option go_package = "%s";
option java_multiple_files = true;
option java_package = "%s";

service %s {
	rpc Create%s (Create%sRequest) returns (Create%sReply);
	rpc Update%s (Update%sRequest) returns (Update%sReply);
	rpc Delete%s (Delete%sRequest) returns (Delete%sReply);
	rpc Get%s (Get%sRequest) returns (Get%sReply);
	rpc List%s (List%sRequest) returns (List%sReply);
}

message Create%sRequest {}
message Create%sReply {}

message Update%sRequest {}
message Update%sReply {}

message Delete%sRequest {}
message Delete%sReply {}

message Get%sRequest {}
message Get%sReply {}

message List%sRequest {}
message List%sReply {}
`,
		pkgName,
		goPkg,
		pkgName,
		strings.Title(serviceName),
		strings.Title(serviceName), strings.Title(serviceName), strings.Title(serviceName),
		strings.Title(serviceName), strings.Title(serviceName), strings.Title(serviceName),
		strings.Title(serviceName), strings.Title(serviceName), strings.Title(serviceName),
		strings.Title(serviceName), strings.Title(serviceName), strings.Title(serviceName),
		strings.Title(serviceName), strings.Title(serviceName), strings.Title(serviceName),
		strings.Title(serviceName), strings.Title(serviceName),
		strings.Title(serviceName), strings.Title(serviceName),
		strings.Title(serviceName), strings.Title(serviceName),
		strings.Title(serviceName), strings.Title(serviceName),
		strings.Title(serviceName), strings.Title(serviceName),
	)
}

func parseProtoService(protoContent string) ServiceInfo {
	result := ServiceInfo{}

	serviceRegex := regexp.MustCompile(`service\s+(\w+)\s*\{([^}]+)\}`)
	serviceMatches := serviceRegex.FindStringSubmatch(protoContent)

	if len(serviceMatches) >= 3 {
		result.ServiceName = serviceMatches[1]
		serviceBody := serviceMatches[2]

		rpcRegex := regexp.MustCompile(`rpc\s+(\w+)\s*\(\s*(\w+)\s*\)\s*returns\s*\(\s*(\w+)\s*\)`)
		rpcMatches := rpcRegex.FindAllStringSubmatch(serviceBody, -1)

		for _, match := range rpcMatches {
			if len(match) >= 4 {
				result.Methods = append(result.Methods, ServiceMethod{
					Name:         match[1],
					RequestType:  match[2],
					ResponseType: match[3],
				})
			}
		}
	}

	return result
}

func findGoMod(startDir string) (string, string) {
	currentDir := startDir
	for i := 0; i < 10; i++ {
		goModPath := filepath.Join(currentDir, "go.mod")
		if _, err := os.Stat(goModPath); err == nil {
			goModData, err := os.ReadFile(goModPath)
			if err == nil {
				return extractModuleName(string(goModData)), currentDir
			}
		}
		parentDir := filepath.Dir(currentDir)
		if parentDir == currentDir {
			break
		}
		currentDir = parentDir
	}
	return "", ""
}

func generateServerCode(serviceNameTitle, serviceNameLower, appModule, rootModuleName, protoDirPath string, methods []ServiceMethod) string {
	pbImportPath := rootModuleName + "/" + protoDirPath
	connectImportPath := pbImportPath + "/" + serviceNameLower + "connect"

	useCaseName := serviceNameTitle
	if strings.HasSuffix(useCaseName, "Service") {
		useCaseName = strings.TrimSuffix(useCaseName, "Service")
	}

	var methodCode string
	for _, method := range methods {
		methodCode += fmt.Sprintf(`
func (s *%s) %s(ctx context.Context, c *connect.Request[v1.%s]) (*connect.Response[v1.%s], error) {
	panic("implement me")
}
`, serviceNameTitle, method.Name, method.RequestType, method.ResponseType)
	}

	return fmt.Sprintf(`package service

import (
	"context"

	"connectrpc.com/connect"
	v1 "%s"
	%sconnect "%s"
	"%s/internal/biz"
)

type %s struct {
	uc *biz.%sUseCase
}

var _ %sconnect.%sHandler = (*%s)(nil)

func New%s(uc *biz.%sUseCase) %sconnect.%sHandler {
	return &%s{uc: uc}
}
%s
`,
		pbImportPath,
		serviceNameLower, connectImportPath,
		appModule,
		serviceNameTitle, useCaseName,
		serviceNameLower, serviceNameTitle, serviceNameTitle,
		serviceNameTitle, useCaseName, serviceNameLower, serviceNameTitle,
		serviceNameTitle,
		methodCode,
	)
}

func createBuildFile(targetPath, buildTool string, isMonorepo bool) error {
	switch strings.ToLower(buildTool) {
	case "makefile":
		return createMakefile(targetPath, isMonorepo)
	case "taskfile":
		return createTaskfile(targetPath, isMonorepo)
	default:
		return fmt.Errorf("unsupported build tool: %s", buildTool)
	}
}

func createMakefile(targetPath string, isMonorepo bool) error {
	makefileContent := `.PHONY: all
all: build

.PHONY: build
build:
	go build -o bin/server ./cmd/server

.PHONY: run
run:
	go run ./cmd/server

.PHONY: test
test:
	go test -v ./...

.PHONY: clean
clean:
	rm -rf bin
`
	return os.WriteFile(filepath.Join(targetPath, "Makefile"), []byte(makefileContent), 0644)
}

func createTaskfile(targetPath string, isMonorepo bool) error {
	taskfileContent := `version: '3'

tasks:
  default:
    cmds:
      - task: build

  build:
    desc: Build the project
    cmds:
      - go build -o bin/server ./cmd/server

  run:
    desc: Run the server
    cmds:
      - go run ./cmd/server

  test:
    desc: Run tests
    cmds:
      - go test -v ./...

  clean:
    desc: Clean build artifacts
    cmds:
      - rm -rf bin
`
	return os.WriteFile(filepath.Join(targetPath, "Taskfile.yml"), []byte(taskfileContent), 0644)
}

func updateConfigFiles(targetPath string, opts NewOptions) error {
	devYmlPath := filepath.Join(targetPath, "configs", "dev.yml")
	data, err := os.ReadFile(devYmlPath)
	if err != nil {
		return fmt.Errorf("failed to read dev.yml: %w", err)
	}

	content := string(data)

	content = regexp.MustCompile(`data:\s*\n\s*database:\s*\n\s*postgres:`).ReplaceAllString(content, "data:\n  database:\n    "+opts.Database+":")
	content = regexp.MustCompile(`data:\s*\n\s*database:\s*\n\s*mysql:`).ReplaceAllString(content, "data:\n  database:\n    "+opts.Database+":")
	content = regexp.MustCompile(`data:\s*\n\s*database:\s*\n\s*sqlite:`).ReplaceAllString(content, "data:\n  database:\n    "+opts.Database+":")

	if opts.Cache != "none" {
		if !strings.Contains(content, "redis:") {
			content = strings.Replace(content, "database:", "database:\n  cache:\n    redis:", 1)
		}
	}

	if opts.Search != "none" {
		if !strings.Contains(content, "search:") {
			content = strings.Replace(content, "data:", "data:\n  search:\n    elasticsearch:", 1)
		}
	}

	if opts.IAM != "none" {
		if !strings.Contains(content, "auth:") {
			content = strings.Replace(content, "data:", "data:\n  auth:\n    casdoor:", 1)
		}
	}

	return os.WriteFile(devYmlPath, []byte(content), 0644)
}

func updateDataLayer(targetPath, appName string, opts NewOptions) error {
	dataDir := filepath.Join(targetPath, "internal", "data")

	dataGoPath := filepath.Join(dataDir, "data.go")
	data, err := os.ReadFile(dataGoPath)
	if err != nil {
		return fmt.Errorf("failed to read data.go: %w", err)
	}

	content := string(data)

	cleanAppName := strings.ReplaceAll(appName, "-", "_")
	appParts := strings.Split(cleanAppName, "_")
	for i, part := range appParts {
		appParts[i] = strings.Title(part)
	}
	appNameTitle := strings.Join(appParts, "")

	if opts.Database == "none" {
		content = regexp.MustCompile(`NewPostgresPool,\s*`).ReplaceAllString(content, "")
		content = regexp.MustCompile(`pgxpool\.Pool,\s*`).ReplaceAllString(content, "")
		content = regexp.MustCompile(`pg *pgxpool\.Pool,?`).ReplaceAllString(content, "")
	} else if opts.Database == "mysql" {
		content = strings.ReplaceAll(content, "NewPostgresPool", "NewMySQLPool")
		content = strings.ReplaceAll(content, "pgxpool.Pool", "*sql.DB")
		content = strings.ReplaceAll(content, "pg pgxpool.Pool", "db *sql.DB")

		mysqlCode := `func NewMySQLPool(lc fx.Lifecycle, conf *conf.Bootstrap, logger *zap.Logger) (*sql.DB, error) {
	cfg := conf.Data.Database.Mysql
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=utf8mb4&parseTime=True&loc=Local",
		cfg.Username, cfg.Password, cfg.Host, cfg.Port, cfg.Database)

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		logger.Error("failed to open mysql connection", zap.Error(err))
		return nil, err
	}

	db.SetMaxOpenConns(int(cfg.MaxOpenConns))
	db.SetMaxIdleConns(int(cfg.MaxIdleConns))
	db.SetConnMaxLifetime(cfg.ConnMaxLifetime.AsDuration())

	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			if err := db.PingContext(ctx); err != nil {
				logger.Error("mysql ping failed", zap.Error(err))
				return err
			}
			logger.Info("mysql connection established")
			return nil
		},
		OnStop: func(ctx context.Context) error {
			logger.Info("closing mysql connection")
			return db.Close()
		},
	})

	return db, nil
}`
		if !strings.Contains(content, "NewMySQLPool") {
			content = content + "\n\n" + mysqlCode
		}
	} else if opts.Database == "sqlite" {
		content = strings.ReplaceAll(content, "NewPostgresPool", "NewSQLitePool")
		content = strings.ReplaceAll(content, "pgxpool.Pool", "*sql.DB")
		content = strings.ReplaceAll(content, "pg pgxpool.Pool", "db *sql.DB")

		sqliteCode := `func NewSQLitePool(lc fx.Lifecycle, conf *conf.Bootstrap, logger *zap.Logger) (*sql.DB, error) {
	cfg := conf.Data.Database.Sqlite

	db, err := sql.Open("sqlite3", cfg.Path)
	if err != nil {
		logger.Error("failed to open sqlite connection", zap.Error(err))
		return nil, err
	}

	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			if err := db.PingContext(ctx); err != nil {
				logger.Error("sqlite ping failed", zap.Error(err))
				return err
			}
			logger.Info("sqlite connection established")
			return nil
		},
		OnStop: func(ctx context.Context) error {
			logger.Info("closing sqlite connection")
			return db.Close()
		},
	})

	return db, nil
}`
		if !strings.Contains(content, "NewSQLitePool") {
			content = content + "\n\n" + sqliteCode
		}
	}

	if opts.Cache == "redis" {
		if !strings.Contains(content, "NewRedisClient") {
			redisCode := `func NewRedisClient(lc fx.Lifecycle, conf *conf.Bootstrap, logger *zap.Logger) (*redis.Client, error) {
	cfg := conf.Data.Cache.Redis

	rdb := redis.NewClient(&redis.Options{
		Addr:     fmt.Sprintf("%s:%d", cfg.Host, cfg.Port),
		Password: cfg.Password,
		DB:       int(cfg.Db),
	})

	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			if err := rdb.Ping(ctx).Err(); err != nil {
				logger.Error("redis ping failed", zap.Error(err))
				return err
			}
			logger.Info("redis connection established")
			return nil
		},
		OnStop: func(ctx context.Context) error {
			logger.Info("closing redis connection")
			return rdb.Close()
		},
	})

	return rdb, nil
}`
			content = content + "\n\n" + redisCode
			content = strings.Replace(content, "fx.Provide(", "fx.Provide(\n\tNewRedisClient,", 1)
			content = strings.Replace(content, "type Data struct {", "type Data struct {\n\trdb *redis.Client", 1)
			content = strings.Replace(content, "func NewData(", "func NewData(rdb *redis.Client, ", 1)
		}
	}

	if opts.Search == "es" {
		if !strings.Contains(content, "NewElasticSearchClient") {
			esCode := `func NewElasticSearchClient(lc fx.Lifecycle, conf *conf.Bootstrap, logger *zap.Logger) (*elasticsearch.TypedClient, error) {
	cfg := conf.Data.Search.Elasticsearch
	transport := http.DefaultTransport.(*http.Transport).Clone()

	if cfg.Tls.Enable {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: cfg.Tls.InsecureSkipVerify}
		if cfg.Tls.CaPem != "" {
			pool := x509.NewCertPool()
			if pool.AppendCertsFromPEM([]byte(cfg.Tls.CaPem)) {
				transport.TLSClientConfig.RootCAs = pool
			}
		}
	}

	esCfg := elasticsearch.Config{
		Addresses: cfg.Addresses,
		Username:  cfg.Username,
		Password:  cfg.Password,
		Transport: transport,
	}

	es, err := elasticsearch.NewTypedClient(esCfg)
	if err != nil {
		logger.Error("failed to initialize elasticsearch client", zap.Error(err))
		return nil, err
	}

	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			if _, err := es.Ping().Do(ctx); err != nil {
				logger.Error("elasticsearch ping failed", zap.Error(err))
				return err
			}
			logger.Info("elasticsearch client initialized", zap.Strings("addresses", cfg.Addresses))
			return nil
		},
	})

	return es, nil
}`
			content = content + "\n\n" + esCode
			content = strings.Replace(content, "fx.Provide(", "fx.Provide(\n\tNewElasticSearchClient,", 1)
			content = strings.Replace(content, "type Data struct {", "type Data struct {\n\tes *elasticsearch.TypedClient", 1)
			content = strings.Replace(content, "func NewData(", "func NewData(es *elasticsearch.TypedClient, ", 1)
		}
	}

	if opts.IAM == "casdoor" {
		if !strings.Contains(content, "NewCasdoorAuthClient") {
			casdoorCode := `func NewCasdoorAuthClient(conf *conf.Bootstrap, logger *zap.Logger) (*casdoor.Client, error) {
	cfg := conf.Data.Auth.Casdoor

	client := casdoor.NewClient(
		cfg.Endpoint,
		cfg.ClientId,
		cfg.ClientSecret,
		cfg.Certificate,
		cfg.Organization,
		cfg.Application,
	)

	logger.Info("casdoor auth client initialized")
	return client, nil
}`
			content = content + "\n\n" + casdoorCode
			content = strings.Replace(content, "fx.Provide(", "fx.Provide(\n\tNewCasdoorAuthClient,", 1)
			content = strings.Replace(content, "type Data struct {", "type Data struct {\n\tcasdoor *casdoor.Client", 1)
			content = strings.Replace(content, "func NewData(", "func NewData(casdoor *casdoor.Client, ", 1)
		}
	}

	content = strings.ReplaceAll(content, "NewSearchRepo", "New"+appNameTitle+"Repo")

	return os.WriteFile(dataGoPath, []byte(content), 0644)
}
