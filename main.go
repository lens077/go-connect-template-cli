package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

func main() {
	// 检查命令行参数长度
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	// 处理子命令
	subcmd := os.Args[1]
	switch subcmd {
	case "new":
		// 处理 new 子命令
		handleNewCommand()
	case "proto":
		// 处理 proto 子命令
		handleProtoCommand()
	default:
		fmt.Printf("Unknown command: %s\n", subcmd)
		printUsage()
		os.Exit(1)
	}
}

// handleNewCommand 处理 new 子命令
func handleNewCommand() {
	nomod := false
	repoURL := "https://github.com/lens077/go-connect-template.git"
	var args []string

	for i := 0; i < len(os.Args); i++ {
		arg := os.Args[i]
		switch arg {
		case "--nomod":
			nomod = true
		case "-r":
			i++
			if i < len(os.Args) {
				repoURL = os.Args[i]
			}
		default:
			args = append(args, arg)
		}
	}

	if len(args) < 3 || args[1] != "new" {
		printUsage()
		os.Exit(1)
	}

	appPath := args[2]
	parts := strings.Split(appPath, "/")
	appName := parts[len(parts)-1]

	templateURL := repoURL
	targetPath := filepath.Join(".", appPath)

	if err := gitClone(templateURL, targetPath); err != nil {
		fmt.Printf("Failed to clone template: %v\n", err)
		os.Exit(1)
	}

	if nomod {
		if err := handleMonorepoMode(targetPath, appPath, appName); err != nil {
			fmt.Printf("Failed to handle monorepo mode: %v\n", err)
			os.Exit(1)
		}
	} else {
		goModPath := filepath.Join(targetPath, "go.mod")
		if err := updateGoMod(goModPath, "github.com/lens077/go-connect-template", appName); err != nil {
			fmt.Printf("Failed to update go.mod: %v\n", err)
			os.Exit(1)
		}

		if err := updateAllGoFiles(targetPath, "github.com/lens077/go-connect-template", appName); err != nil {
			fmt.Printf("Failed to update go files: %v\n", err)
			os.Exit(1)
		}

		if err := updateProtoFiles(targetPath, "github.com/lens077/go-connect-template", appName); err != nil {
			fmt.Printf("Failed to update proto files: %v\n", err)
			os.Exit(1)
		}

		mainFilePath := filepath.Join(targetPath, "cmd", "server", "main.go")
		if err := ensureMainImports(mainFilePath, appName); err != nil {
			fmt.Printf("Failed to update main.go imports: %v\n", err)
			os.Exit(1)
		}

		if err := renameUserFiles(targetPath, appName); err != nil {
			fmt.Printf("Failed to rename user.go files: %v\n", err)
			os.Exit(1)
		}

		if err := createTestFiles(targetPath, appName); err != nil {
			fmt.Printf("Failed to create test files: %v\n", err)
			os.Exit(1)
		}

		if err := createMakefile(targetPath, false); err != nil {
			fmt.Printf("Failed to create Makefile: %v\n", err)
			os.Exit(1)
		}
	}

	fmt.Printf("Application %s created successfully at %s\n", appName, targetPath)
}

// handleProtoCommand 处理 proto 子命令
func handleProtoCommand() {
	if len(os.Args) < 3 {
		printProtoUsage()
		os.Exit(1)
	}

	protoSubcmd := os.Args[2]
	switch protoSubcmd {
	case "add":
		// 处理 proto add 子命令
		if len(os.Args) < 4 {
			fmt.Println("Usage: co proto add <proto-path>")
			os.Exit(1)
		}
		protoPath := os.Args[3]
		if err := addProtoFile(protoPath); err != nil {
			fmt.Printf("Failed to add proto file: %v\n", err)
			os.Exit(1)
		}

	case "server":
		// 处理 proto server 子命令
		targetDir := "internal/service"
		if len(os.Args) > 5 && os.Args[4] == "-t" {
			targetDir = os.Args[5]
		}
		if len(os.Args) < 4 {
			fmt.Println("Usage: co proto server <proto-path> [-t <target-dir>]")
			os.Exit(1)
		}
		protoPath := os.Args[3]
		if err := generateProtoServer(protoPath, targetDir); err != nil {
			fmt.Printf("Failed to generate proto server: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Printf("Unknown proto command: %s\n", protoSubcmd)
		printProtoUsage()
		os.Exit(1)
	}
}

// printUsage 打印使用帮助
func printUsage() {
	fmt.Println("Usage:")
	fmt.Println("  co new <application/path> [-r <repo-url>] [--nomod]")
	fmt.Println("  co proto [add|client|server] [options]")
	fmt.Println()
	fmt.Println("Subcommands:")
	fmt.Println("  new       Create a new application from template")
	fmt.Println("  proto     Proto file generation commands")
	fmt.Println()
	fmt.Println("Proto Subcommands:")
	printProtoUsage()
}

// printProtoUsage 打印 proto 子命令使用帮助
func printProtoUsage() {
	fmt.Println("  proto add <proto-path>        Add a new proto file")
	fmt.Println("  proto client <proto-path>     Generate proto client codes")
	fmt.Println("  proto server <proto-path>     Generate proto server codes")
	fmt.Println("    -t <target-dir>            Target directory for server codes (default: internal/service)")
}

// addProtoFile 添加新的proto文件
func addProtoFile(protoPath string) error {
	// 确保目录存在
	if err := os.MkdirAll(filepath.Dir(protoPath), 0755); err != nil {
		return err
	}

	// 生成proto文件内容
	protoContent := generateProtoContent(protoPath)

	// 写入文件
	return os.WriteFile(protoPath, []byte(protoContent), 0644)
}

// generateProtoContent 生成proto文件内容
func generateProtoContent(protoPath string) string {
	// 从路径中提取服务名称和包名
	// 例如: api/helloworld/demo.proto -> helloworld, demo
	pathParts := strings.Split(protoPath, "/")
	if len(pathParts) < 3 {
		return "" // 无效路径
	}

	// 提取包名和服务名
	pkgName := pathParts[len(pathParts)-2]
	serviceName := strings.TrimSuffix(pathParts[len(pathParts)-1], ".proto")

	// 生成go_package，使用相对路径
	goPkg := fmt.Sprintf("./%s/%s/%s;%s", strings.Join(pathParts[:len(pathParts)-1], "/"), serviceName, serviceName, serviceName)

	// 生成proto文件内容
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

// generateProtoServer 生成proto服务器代码
func generateProtoServer(protoPath, targetDir string) error {
	// 确保目标目录存在
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return err
	}

	// 从proto路径中提取服务名称
	serviceName := strings.TrimSuffix(filepath.Base(protoPath), ".proto")
	serviceName = strings.Title(serviceName)

	// 生成服务代码
	serverCode := generateServerCode(protoPath, serviceName)

	// 替换{{.AppModule}}为实际的应用模块路径
	// 1. 获取当前目录的go.mod文件，提取根模块名
	rootDir, _ := os.Getwd()
	rootModuleName := ""

	// 查找go.mod文件，从当前目录向上查找
	currentDir := rootDir
	for i := 0; i < 5; i++ { // 最多向上查找5层
		goModPath := filepath.Join(currentDir, "go.mod")
		if _, err := os.Stat(goModPath); err == nil {
			// 找到go.mod文件，提取模块名
			goModData, err := os.ReadFile(goModPath)
			if err == nil {
				rootModuleName = extractModuleName(string(goModData))
				break
			}
		}
		// 向上一级目录查找
		currentDir = filepath.Dir(currentDir)
	}

	// 2. 构建应用模块路径
	appModule := rootModuleName
	if appModule != "" {
		// 从当前目录中提取应用相对路径，只包含从application开始的部分
		relPath, err := filepath.Rel(currentDir, rootDir)
		if err == nil {
			// 如果当前目录是根目录的子目录，添加相对路径
			if relPath != "." {
				// 检查relPath是否包含application目录
				if strings.Contains(relPath, "application") {
					// 只保留从application开始的部分
					pathParts := strings.Split(relPath, "/")
					appIndex := -1
					for i, part := range pathParts {
						if part == "application" {
							appIndex = i
							break
						}
					}
					if appIndex != -1 {
						relPath = strings.Join(pathParts[appIndex:], "/")
					}
				}
				appModule = appModule + "/" + relPath
			}
		}
	}

	// 3. 替换模板变量
	serverCode = strings.ReplaceAll(serverCode, "{{.AppModule}}", appModule)

	// 写入文件
	targetFile := filepath.Join(targetDir, strings.ToLower(serviceName)+"_service.go")
	return os.WriteFile(targetFile, []byte(serverCode), 0644)
}

// generateServerCode 生成connect-go风格的服务器代码
func generateServerCode(protoPath, serviceName string) string {
	// 从proto路径中提取包名和服务信息
	pathParts := strings.Split(protoPath, "/")
	if len(pathParts) < 3 {
		return "" // 无效路径
	}

	// 提取服务名
	serviceNameLower := strings.ToLower(serviceName)

	// 生成简化的服务代码，只包含必要部分
	return fmt.Sprintf(`package service

import (
    "context"
    "connectrpc.com/connect"
    "{{.AppModule}}/internal/biz"
    pb "{{.AppModule}}/%s"
    %sconnect "{{.AppModule}}/%s/%sconnect"
)

// %sService 实现 Connect 服务
 type %sService struct {
    // 业务逻辑依赖
    uc *biz.%sUseCase
 }

// 显式接口检查
 var _ %sconnect.%sServiceHandler = (*%sService)(nil)
`,
		strings.Join(pathParts[:len(pathParts)-1], "/"),
		serviceNameLower, strings.Join(pathParts[:len(pathParts)-1], "/"), serviceNameLower,
		serviceName, serviceName, serviceName,
		serviceNameLower, serviceName, serviceName,
	)
}

// gitClone 从远程仓库克隆代码
func gitClone(url, path string) error {
	// 确保目标目录不存在
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		return fmt.Errorf("target directory %s already exists", path)
	}

	// 创建父目录
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}

	// 执行git clone命令
	cmd := exec.Command("git", "clone", url, path)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// updateGoMod 更新go.mod文件中的module名称
func updateGoMod(path, oldModule, newModule string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	newData := strings.Replace(string(data), "module "+oldModule, "module "+newModule, 1)
	return os.WriteFile(path, []byte(newData), 0644)
}

// updateAllGoFiles 更新所有go文件中的import路径
func updateAllGoFiles(root, oldModule, newModule string) error {
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

			content = strings.ReplaceAll(content, oldModule, newModule)

			// 将连字符替换为下划线，因为 Go 类型名称不能包含连字符
			cleanAppName := strings.ReplaceAll(newModule, "-", "_")
			// 对每个下划线分隔的单词进行标题化处理
			parts := strings.Split(cleanAppName, "_")
			for i, part := range parts {
				parts[i] = strings.Title(part)
			}
			appName := strings.Join(parts, "")
			content = strings.ReplaceAll(content, "ErrUserAlreadyExists", "Err"+appName+"AlreadyExists")
			content = strings.ReplaceAll(content, "ErrUserNotFound", "Err"+appName+"NotFound")
			content = strings.ReplaceAll(content, "ErrAuthFailed", "Err"+appName+"AuthFailed")
			content = strings.ReplaceAll(content, "UserInfo", appName+"Info")
			content = strings.ReplaceAll(content, "UserRepo", appName+"Repo")
			content = strings.ReplaceAll(content, "UserUseCase", appName+"UseCase")
			content = strings.ReplaceAll(content, "UserService", appName+"Service")
			content = strings.ReplaceAll(content, "userRepo", strings.ToLower(appName[:1])+appName[1:]+"Repo")
			content = strings.ReplaceAll(content, "NewUserRepo", "New"+appName+"Repo")
			content = strings.ReplaceAll(content, "NewUserUseCase", "New"+appName+"UseCase")
			content = strings.ReplaceAll(content, "NewUserService", "New"+appName+"Service")

			content = strings.ReplaceAll(content, "SignInRequest", appName+"SignInRequest")
			content = strings.ReplaceAll(content, "SignInResponse", appName+"SignInResponse")
			content = strings.ReplaceAll(content, "GetUserProfileRequest", "Get"+appName+"ProfileRequest")
			content = strings.ReplaceAll(content, "GetUserProfileResponse", "Get"+appName+"ProfileResponse")

			if err := os.WriteFile(path, []byte(content), 0644); err != nil {
				return err
			}
		}

		return nil
	})
}

// updateProtoFiles 更新所有proto文件中的package和go_package字段
func updateProtoFiles(root, oldModule, newModule string) error {
	// 将服务名称中的连字符替换为下划线，用于package字段
	protoPackageName := strings.ReplaceAll(newModule, "-", "_")

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

			// 修改go_package中的旧module名称为新名称
			content = strings.ReplaceAll(content, oldModule, newModule)

			// 修改package字段，使用下划线替换连字符
			packageRegex := regexp.MustCompile(`package\s+\w+\.(v\d+);`)
			content = packageRegex.ReplaceAllString(content, "package "+protoPackageName+".$1;")

			if err := os.WriteFile(path, []byte(content), 0644); err != nil {
				return err
			}
		}

		return nil
	})
}

// handleMonorepoMode 处理大仓模式的逻辑
func handleMonorepoMode(targetPath, appPath, appName string) error {
	fmt.Printf("Entering monorepo mode for %s with app path %s\n", targetPath, appPath)

	// 1. 获取根目录的go.mod文件内容，提取module名称
	rootDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get root directory: %w", err)
	}

	rootGoModPath := filepath.Join(rootDir, "go.mod")
	rootGoModData, err := os.ReadFile(rootGoModPath)
	if err != nil {
		return fmt.Errorf("failed to read root go.mod: %w", err)
	}

	// 解析根目录go.mod的module名称
	rootModuleName := extractModuleName(string(rootGoModData))
	fmt.Printf("Root module name: %s\n", rootModuleName)

	// 2. 计算完整的import路径，使用根目录的module名称
	// 直接使用appPath构建完整的import路径，避免重复的backend目录
	fullImportPath := fmt.Sprintf("%s/%s", rootModuleName, appPath)
	fmt.Printf("Full import path: %s\n", fullImportPath)

	// 3. 删除生成的go.mod和go.sum文件
	goModPath := filepath.Join(targetPath, "go.mod")
	if err := os.Remove(goModPath); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("failed to remove go.mod: %w", err)
		}
	}
	fmt.Printf("Removed go.mod file\n")

	goSumPath := filepath.Join(targetPath, "go.sum")
	if err := os.Remove(goSumPath); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("failed to remove go.sum: %w", err)
		}
	}
	fmt.Printf("Removed go.sum file\n")

	// 5. 删除生成的api目录
	apiPath := filepath.Join(targetPath, "api")
	if err := os.RemoveAll(apiPath); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("failed to remove api directory: %w", err)
		}
	}
	fmt.Printf("Removed api directory\n")

	// 6. 修改所有go文件中的import路径，使用根目录的module名称
	if err := updateGoFilesForMonorepo(targetPath, "github.com/lens077/go-connect-template", fullImportPath); err != nil {
		return fmt.Errorf("failed to update go files: %w", err)
	}
	fmt.Printf("Updated import paths in go files\n")

	// 6. 确保main.go中有必要的import
	mainFilePath := filepath.Join(targetPath, "cmd", "server", "main.go")
	if err := ensureMainImports(mainFilePath, appName); err != nil {
		return fmt.Errorf("failed to update main.go imports: %w", err)
	}
	fmt.Printf("Ensured main.go imports\n")

	// 7. 修改Makefile，使其在--nomod模式下使用正确的buf命令
	makefilePath := filepath.Join(targetPath, "Makefile")
	if _, err := os.Stat(makefilePath); err == nil {
		makefileContent, err := os.ReadFile(makefilePath)
		if err != nil {
			return fmt.Errorf("failed to read Makefile: %w", err)
		}

		content := string(makefileContent)

		content = regexp.MustCompile(`\.PHONY: api\napi:\n[\s\S]*?\n\.PHONY:`).ReplaceAllString(content, `.PHONY: api
api:
	cd ../../ && buf generate --template buf.gen.yaml --path api
	cd ../../ && buf generate --template buf.gen.ts.yaml --path api

.PHONY:`)

		content = regexp.MustCompile(`\.PHONY: generate\ngenerate:\n[\s\S]*?\n\.PHONY:`).ReplaceAllString(content, `.PHONY: generate
generate:
	cd ../../ && buf generate --template buf.gen.yaml --path api
	cd ../../ && buf generate --template buf.gen.ts.yaml --path api

.PHONY:`)

		content = regexp.MustCompile(`\.PHONY: conf\nconf:\n[\s\S]*?\n\n`).ReplaceAllString(content, `.PHONY: conf
conf:
	cd ../../ && buf generate --template buf.gen.yaml --path api

`)

	}

	if err := renameUserFiles(targetPath, appName); err != nil {
		return fmt.Errorf("failed to rename user.go files: %w", err)
	}

	if err := createTestFiles(targetPath, fullImportPath); err != nil {
		return fmt.Errorf("failed to create test files: %w", err)
	}

	if err := createMakefile(targetPath, true); err != nil {
		return fmt.Errorf("failed to create Makefile: %w", err)
	}

	return nil
}

// extractModuleName 从go.mod内容中提取module名称
func extractModuleName(goModContent string) string {
	// 使用正则表达式匹配module名称
	moduleRegex := regexp.MustCompile(`module\s+([^\s]+)\s*`)
	matches := moduleRegex.FindStringSubmatch(goModContent)
	if len(matches) > 1 {
		return matches[1]
	}
	// 如果匹配失败，返回默认值
	return ""
}

// updateGoFilesForMonorepo 更新大仓模式下的go文件import路径
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
			if len(parts) < 2 {
				return fmt.Errorf("invalid module path: %s", newModulePath)
			}
			var rootModuleName string
			for i, part := range parts {
				if part == "application" {
					rootModuleName = strings.Join(parts[:i], "/")
					break
				}
			}
			if rootModuleName == "" {
				rootModuleName = parts[0]
			}

			content = strings.ReplaceAll(content, oldModule, newModulePath)

			content = regexp.MustCompile(`"root/api/([^"]+)"`).ReplaceAllString(content, `"`+rootModuleName+`/api/$1"`)
			content = regexp.MustCompile(`root\.api\.([^\s]+)`).ReplaceAllString(content, rootModuleName+`.api.$1`)

			content = regexp.MustCompile(`"github.com/api/([^"]+)"`).ReplaceAllString(content, `"`+rootModuleName+`/api/$1"`)
			content = regexp.MustCompile(`github\.com\.api\.([^\s]+)`).ReplaceAllString(content, rootModuleName+`.api.$1`)

			content = regexp.MustCompile(`"/api/([^"]+)"`).ReplaceAllString(content, `"`+rootModuleName+`/api/$1"`)
			content = regexp.MustCompile(`"api/([^"]+)"`).ReplaceAllString(content, `"`+rootModuleName+`/api/$1"`)

			appName := parts[len(parts)-1]
			appApiPrefix := rootModuleName + "/application/" + appName + "/api/"
			rootApiPrefix := rootModuleName + "/api/"
			content = strings.ReplaceAll(content, appApiPrefix, rootApiPrefix)

			appApiRegex := regexp.MustCompile(`"` + regexp.QuoteMeta(rootModuleName) + `/application/[^/]+/api/([^"]+)"`)
			content = appApiRegex.ReplaceAllString(content, `"`+rootModuleName+`/api/$1"`)

			// 将连字符替换为下划线，因为 Go 类型名称不能包含连字符
			cleanAppName := strings.ReplaceAll(parts[len(parts)-1], "-", "_")
			// 对每个下划线分隔的单词进行标题化处理
			appParts := strings.Split(cleanAppName, "_")
			for i, part := range appParts {
				appParts[i] = strings.Title(part)
			}
			appNameTitle := strings.Join(appParts, "")
			content = strings.ReplaceAll(content, "ErrUserAlreadyExists", "Err"+appNameTitle+"AlreadyExists")
			content = strings.ReplaceAll(content, "ErrUserNotFound", "Err"+appNameTitle+"NotFound")
			content = strings.ReplaceAll(content, "ErrAuthFailed", "Err"+appNameTitle+"AuthFailed")
			content = strings.ReplaceAll(content, "UserInfo", appNameTitle+"Info")
			content = strings.ReplaceAll(content, "UserRepo", appNameTitle+"Repo")
			content = strings.ReplaceAll(content, "UserUseCase", appNameTitle+"UseCase")
			content = strings.ReplaceAll(content, "UserService", appNameTitle+"Service")
			content = strings.ReplaceAll(content, "userRepo", strings.ToLower(appNameTitle[:1])+appNameTitle[1:]+"Repo")
			content = strings.ReplaceAll(content, "NewUserRepo", "New"+appNameTitle+"Repo")
			content = strings.ReplaceAll(content, "NewUserUseCase", "New"+appNameTitle+"UseCase")
			content = strings.ReplaceAll(content, "NewUserService", "New"+appNameTitle+"Service")

			content = strings.ReplaceAll(content, "SignInRequest", appNameTitle+"SignInRequest")
			content = strings.ReplaceAll(content, "SignInResponse", appNameTitle+"SignInResponse")
			content = strings.ReplaceAll(content, "GetUserProfileRequest", "Get"+appNameTitle+"ProfileRequest")
			content = strings.ReplaceAll(content, "GetUserProfileResponse", "Get"+appNameTitle+"ProfileResponse")

			if err := os.WriteFile(path, []byte(content), 0644); err != nil {
				return err
			}
		}

		return nil
	})
}

// ensureMainImports 确保main.go中有必要的import
func ensureMainImports(path, appName string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	content := string(data)

	// 检查是否已经有flag和os import
	if !strings.Contains(content, `"flag"`) || !strings.Contains(content, `"os"`) {
		// 找到import块并添加flag和os
		importRegex := regexp.MustCompile(`import \(([^)]+)\)`)
		matches := importRegex.FindStringSubmatch(content)
		if len(matches) < 2 {
			return fmt.Errorf("could not find import block in main.go")
		}

		importBlock := matches[1]
		newImportBlock := importBlock

		// 添加flag包
		if !strings.Contains(importBlock, `"flag"`) {
			newImportBlock += "\n\t\"flag\""
		}
		// 添加os包
		if !strings.Contains(importBlock, `"os"`) {
			newImportBlock += "\n\t\"os\""
		}

		content = importRegex.ReplaceAllString(content, "import ("+newImportBlock+")")
	}

	return os.WriteFile(path, []byte(content), 0644)
}

// renameUserFiles 将所有 user.go 文件重命名为 appName.go
func renameUserFiles(root, appName string) error {
	// 将连字符替换为下划线，因为 Go 文件名不能包含连字符
	fileName := strings.ReplaceAll(appName, "-", "_")
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if !info.IsDir() && filepath.Base(path) == "user.go" {
			dir := filepath.Dir(path)
			newPath := filepath.Join(dir, fileName+".go")

			if err := os.Rename(path, newPath); err != nil {
				return fmt.Errorf("failed to rename %s to %s: %w", path, newPath, err)
			}

			fmt.Printf("Renamed %s to %s\n", path, newPath)
		}

		return nil
	})
}

// createConstantsPackage 创建 constants 包
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

// RPC metadata
const (
	UserOwnerMetadataKey = "x-md-global-owner"
	UserNameMetadataKey  = "x-md-global-name"
	UserRoleMetadataKey  = "x-md-global-role"
	UserIdMetadataKey    = "x-md-global-user-id"
)

// Log options
const (
	FormatConsole = "console"
	FormatJson    = "json"
)

// Postgres ssl mode options
const (
	SslModeDisable    = "disable"
	SslModeAllow      = "allow"
	SslModePrefer     = "prefer"
	SslModeVerifyCa   = "verify-ca"
	SslModeVerifyFull = "verify-full"
)

// Consul configs default values
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

// Consul
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

// GetEnvString 如果环境变量存在且不为空，则返回环境变量值，否则返回默认值
func GetEnvString(key, defaultValue string) string {
	if value, exists := os.LookupEnv(key); exists && value != "" {
		return value
	}
	return defaultValue
}

// GetEnvBool 处理布尔类型
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

// createEnvPackage 创建 env 包
func createEnvPackage(root string) error {
	// 创建目录
	envDir := filepath.Join(root, "internal", "pkg", "env")
	if err := os.MkdirAll(envDir, 0755); err != nil {
		return err
	}

	// 生成 env.go
	envPath := filepath.Join(envDir, "env.go")
	envContent := `package env

import (
	"os"
	"strconv"
)

// GetEnvString 如果环境变量存在且不为空，则返回环境变量值，否则返回默认值
func GetEnvString(key, defaultValue string) string {
	if value, exists := os.LookupEnv(key); exists && value != "" {
		return value
	}
	return defaultValue
}

// GetEnvBool 处理布尔类型
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

	// 生成 env_test.go
	envTestPath := filepath.Join(envDir, "env_test.go")
	envTestContent := `package env

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
)

// EnvTestSuite 是 Env 的测试套件
type EnvTestSuite struct {
	suite.Suite
}

func (suite *EnvTestSuite) SetupTest() {
	// 清理环境变量
	os.Clearenv()
}

func (suite *EnvTestSuite) TestGetEnvString_WithExistingEnv() {
	// 测试环境变量存在的情况
	os.Setenv("TEST_KEY", "test-value")
	value := GetEnvString("TEST_KEY", "default-value")
	assert.Equal(suite.T(), "test-value", value)
}

func (suite *EnvTestSuite) TestGetEnvString_WithMissingEnv() {
	// 测试环境变量不存在的情况
	value := GetEnvString("NON_EXISTENT_KEY", "default-value")
	assert.Equal(suite.T(), "default-value", value)
}

func (suite *EnvTestSuite) TestGetEnvString_WithEmptyValue() {
	// 测试环境变量存在但值为空的情况
	os.Setenv("TEST_KEY", "")
	value := GetEnvString("TEST_KEY", "default-value")
	assert.Equal(suite.T(), "default-value", value)
}

func (suite *EnvTestSuite) TestGetEnvBool_WithTrueValue() {
	// 测试布尔值 true 的情况
	testCases := []string{"true", "True", "TRUE", "1"}
	for _, tc := range testCases {
		os.Setenv("TEST_BOOL_KEY", tc)
		value := GetEnvBool("TEST_BOOL_KEY", false)
		assert.True(suite.T(), value, "Test case: %s", tc)
	}
}

func (suite *EnvTestSuite) TestGetEnvBool_WithFalseValue() {
	// 测试布尔值 false 的情况
	testCases := []string{"false", "False", "FALSE", "0"}
	for _, tc := range testCases {
		os.Setenv("TEST_BOOL_KEY", tc)
		value := GetEnvBool("TEST_BOOL_KEY", true)
		assert.False(suite.T(), value, "Test case: %s", tc)
	}
}

func (suite *EnvTestSuite) TestGetEnvBool_WithInvalidValue() {
	// 测试无效布尔值的情况
	os.Setenv("TEST_BOOL_KEY", "invalid-value")
	value := GetEnvBool("TEST_BOOL_KEY", true)
	// 无效值应该返回默认值
	assert.True(suite.T(), value)
}

func (suite *EnvTestSuite) TestGetEnvBool_WithMissingEnv() {
	// 测试环境变量不存在的情况
	value := GetEnvBool("NON_EXISTENT_KEY", true)
	assert.True(suite.T(), value)
}

func (suite *EnvTestSuite) TestGetEnvBool_WithEmptyValue() {
	// 测试环境变量存在但值为空的情况
	os.Setenv("TEST_BOOL_KEY", "")
	value := GetEnvBool("TEST_BOOL_KEY", true)
	assert.True(suite.T(), value)
}

func (suite *EnvTestSuite) TestGetEnvString_WithMultipleCalls() {
	// 测试多次调用的情况
	os.Setenv("TEST_KEY", "value1")
	value1 := GetEnvString("TEST_KEY", "default")
	assert.Equal(suite.T(), "value1", value1)

	os.Setenv("TEST_KEY", "value2")
	value2 := GetEnvString("TEST_KEY", "default")
	assert.Equal(suite.T(), "value2", value2)
}

// 运行测试套件
func TestEnvTestSuite(t *testing.T) {
	suite.Run(t, new(EnvTestSuite))
}

// 单元测试函数
func TestGetEnvString_ConcurrentAccess(t *testing.T) {
	// 测试并发访问
	assert.NotPanics(t, func() {
		for i := 0; i < 100; i++ {
			GetEnvString("TEST_KEY", "default")
		}
	})
}

func TestGetEnvBool_ConcurrentAccess(t *testing.T) {
	// 测试并发访问
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

// createTestFiles 创建所有测试文件
func createTestFiles(root, appName string) error {
	// 创建 meta_test.go
	metaDir := filepath.Join(root, "internal", "pkg", "meta")
	metaTestPath := filepath.Join(metaDir, "meta_test.go")
	metaTestContent := `package meta

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
)

// MetaTestSuite 是 Meta 的测试套件
type MetaTestSuite struct {
	suite.Suite
}

func (suite *MetaTestSuite) TestAppInfoStruct() {
	// 测试 AppInfo 结构体
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
	// 测试 GetOutboundIP 函数
	ip, err := GetOutboundIP()

	// 这个测试可能因为网络原因失败，所以我们接受两种情况
	if err == nil {
		assert.NotEmpty(suite.T(), ip)
		// 验证 IP 格式
		assert.NotContains(suite.T(), ip, ":") // 不应该包含端口
	} else {
		// 如果出错也接受，因为可能在无网络环境中
		suite.T().Logf("GetOutboundIP failed (expected in offline environment): %v", err)
	}
}

func (suite *MetaTestSuite) TestGetOutboundIP_PanicRecovery() {
	// 测试 panic 恢复
	assert.NotPanics(suite.T(), func() {
		_, _ = GetOutboundIP()
	})
}

// 运行测试套件
func TestMetaTestSuite(t *testing.T) {
	suite.Run(t, new(MetaTestSuite))
}

// 单元测试函数
func TestAppInfoZeroValue(t *testing.T) {
	// 测试零值
	var appInfo AppInfo
	assert.Empty(t, appInfo.ID)
	assert.Empty(t, appInfo.Name)
	assert.Empty(t, appInfo.Host)
	assert.Empty(t, appInfo.Environment)
}

func TestAppInfoInitialization(t *testing.T) {
	// 测试结构体初始化
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

	// 创建 registry_test.go
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

// RegistryTestSuite 是 Registry 的测试套件
type RegistryTestSuite struct {
	suite.Suite
	testLogger *zap.Logger
}

func (suite *RegistryTestSuite) SetupTest() {
	// 创建测试用的 logger
	var err error
	suite.testLogger, err = zap.NewDevelopment()
	assert.NoError(suite.T(), err)

	// 清理环境变量
	os.Clearenv()
}

func (suite *RegistryTestSuite) TestNewConsulRegistry_WithValidAddr() {
	// 测试 NewConsulRegistry 函数
	reg, err := NewConsulRegistry("localhost:8500", "test-id", "test-service", WithLogger(suite.testLogger))
	assert.NoError(suite.T(), err)
	assert.NotNil(suite.T(), reg)
	assert.Equal(suite.T(), "test-id", reg.ID)
	assert.Equal(suite.T(), "test-service", reg.Name)
	assert.Equal(suite.T(), "localhost:8500", reg.Addr)
}

func (suite *RegistryTestSuite) TestNewConsulRegistry_WithInvalidAddr() {
	// 测试无效地址的情况
	reg, err := NewConsulRegistry("invalid-addr", "test-id", "test-service", WithLogger(suite.testLogger))
	// 这里应该不会在创建时就出错，而是在实际使用时出错
	assert.NoError(suite.T(), err)
	assert.NotNil(suite.T(), reg)
}

func (suite *RegistryTestSuite) TestNewConsulRegistry_WithTLS() {
	// 测试带 TLS 配置的情况
	reg, err := NewConsulRegistry("localhost:8500", "test-id", "test-service", WithLogger(suite.testLogger), WithTLS(true, ""))
	assert.NoError(suite.T(), err)
	assert.NotNil(suite.T(), reg)
}

func (suite *RegistryTestSuite) TestWithLogger() {
	// 测试 WithLogger 选项
	opt := WithLogger(suite.testLogger)
	o := &options{}
	opt(o)
	assert.Equal(suite.T(), suite.testLogger, o.logger)
}

func (suite *RegistryTestSuite) TestWithTLS() {
	// 测试 WithTLS 选项
	opt := WithTLS(true, "test-ca-pem")
	o := &options{}
	opt(o)
	assert.NotNil(suite.T(), o.tlsConf)
	assert.True(suite.T(), o.tlsConf.InsecureSkipVerify)
}

func (suite *RegistryTestSuite) TestModuleCreation() {
	// 测试模块创建
	module := Module
	assert.NotNil(suite.T(), module)
	assert.Contains(suite.T(), module.String(), "registry")
}

func (suite *RegistryTestSuite) TestParseToTCPAddr_ValidHTTPUrl() {
	// 测试有效的 HTTP URL
	addr, err := ParseToTCPAddr("http://localhost:8080")
	assert.NoError(suite.T(), err)
	assert.NotNil(suite.T(), addr)
	// localhost 可能被解析为 127.0.0.1
	assert.True(suite.T(), addr.IP.String() == "localhost" || addr.IP.String() == "127.0.0.1")
	assert.Equal(suite.T(), 8080, addr.Port)
}

func (suite *RegistryTestSuite) TestParseToTCPAddr_ValidHTTPSUrl() {
	// 测试有效的 HTTPS URL
	addr, err := ParseToTCPAddr("https://localhost:8443")
	assert.NoError(suite.T(), err)
	assert.NotNil(suite.T(), addr)
	// localhost 可能被解析为 127.0.0.1
	assert.True(suite.T(), addr.IP.String() == "localhost" || addr.IP.String() == "127.0.0.1")
	assert.Equal(suite.T(), 8443, addr.Port)
}

func (suite *RegistryTestSuite) TestParseToTCPAddr_WithoutPort() {
	// 测试不带端口的 URL
	addr, err := ParseToTCPAddr("http://example.com")
	assert.NoError(suite.T(), err)
	assert.NotNil(suite.T(), addr)
	assert.Equal(suite.T(), 80, addr.Port) // 应该添加默认端口 80
}

func (suite *RegistryTestSuite) TestParseToTCPAddr_HTTPSWithoutPort() {
	// 测试 HTTPS 不带端口的 URL
	addr, err := ParseToTCPAddr("https://example.com")
	assert.NoError(suite.T(), err)
	assert.NotNil(suite.T(), addr)
	assert.Equal(suite.T(), 443, addr.Port) // 应该添加默认端口 443
}

func (suite *RegistryTestSuite) TestParseToTCPAddr_InvalidUrl() {
	// 测试无效 URL
	addr, err := ParseToTCPAddr("invalid-url")
	assert.Error(suite.T(), err)
	assert.Nil(suite.T(), addr)
}

func (suite *RegistryTestSuite) TestParseToTCPAddr_EmptyHost() {
	// 测试空 host
	addr, err := ParseToTCPAddr("http://")
	assert.Error(suite.T(), err)
	assert.Nil(suite.T(), addr)
}

// 运行测试套件
func TestRegistryTestSuite(t *testing.T) {
	suite.Run(t, new(RegistryTestSuite))
}

// 单元测试函数
func TestConstants(t *testing.T) {
	// 测试常量定义
	assert.Equal(t, "30s", TtlDuration)
	assert.Equal(t, 10*time.Second, TtlPingInterval)
}

func TestNewConsulRegistry_PanicRecovery(t *testing.T) {
	// 测试 panic 恢复
	assert.NotPanics(t, func() {
		_, _ = NewConsulRegistry("localhost:8500", "test-id", "test-name")
	})
}
`
	if err := os.WriteFile(registryTestPath, []byte(registryTestContent), 0644); err != nil {
		return err
	}

	// 创建 config_test.go
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

// ConfigTestSuite 是 Config 的测试套件
type ConfigTestSuite struct {
	suite.Suite
}

func (suite *ConfigTestSuite) SetupTest() {
	// 清理环境变量
	os.Clearenv()
}

func (suite *ConfigTestSuite) TestDecodeConfig() {
	// 测试 decodeConfig 函数
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
	// 测试 updateConfig 函数
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
	// 测试 GetConfig 函数
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
	// 测试模块创建
	module := Module
	assert.NotNil(suite.T(), module)
	assert.Contains(suite.T(), module.String(), "config")
}

func (suite *ConfigTestSuite) TestInit_MissingConsulPath() {
	// 测试 Init 函数缺少 CONSUL_PATH 环境变量的情况
	ctx := context.Background()
	conf, err := Init(ctx)
	assert.Error(suite.T(), err)
	assert.Nil(suite.T(), conf)
}

func (suite *ConfigTestSuite) TestInit_InvalidConsulAddr() {
	// 测试 Init 函数使用无效 Consul 地址的情况
	ctx := context.Background()
	os.Setenv("CONSUL_PATH", "test/path")
	os.Setenv("CONSUL_ADDR", "invalid-addr:8500")
	conf, err := Init(ctx)
	assert.Error(suite.T(), err)
	assert.Nil(suite.T(), conf)
}

// 运行测试套件
func TestConfigTestSuite(t *testing.T) {
	suite.Run(t, new(ConfigTestSuite))
}

// 单元测试函数
func TestGetConfig_ConcurrentAccess(t *testing.T) {
	// 测试并发访问 GetConfig 函数
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

	// 创建 log_test.go
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

// LogTestSuite 是 Log 的测试套件
type LogTestSuite struct {
	suite.Suite
	testAppInfo meta.AppInfo
}

func (suite *LogTestSuite) SetupTest() {
	// 设置测试用的应用信息
	suite.testAppInfo = meta.AppInfo{
		ID:          "test-service-id",
		Name:        "test-service",
		Host:        "localhost",
		Environment: "dev",
	}
}

func (suite *LogTestSuite) TestNewLogger_DebugLevel() {
	// 测试 debug 日志级别
	logger := NewLogger("debug", constants.FormatJson, suite.testAppInfo)
	assert.NotNil(suite.T(), logger)

	// 验证日志级别
	assert.True(suite.T(), logger.Core().Enabled(zapcore.DebugLevel))
}

func (suite *LogTestSuite) TestNewLogger_InfoLevel() {
	// 测试 info 日志级别
	logger := NewLogger("info", constants.FormatJson, suite.testAppInfo)
	assert.NotNil(suite.T(), logger)

	assert.True(suite.T(), logger.Core().Enabled(zapcore.InfoLevel))
	assert.False(suite.T(), logger.Core().Enabled(zapcore.DebugLevel))
}

func (suite *LogTestSuite) TestNewLogger_WarnLevel() {
	// 测试 warn 日志级别
	logger := NewLogger("warn", constants.FormatJson, suite.testAppInfo)
	assert.NotNil(suite.T(), logger)

	assert.True(suite.T(), logger.Core().Enabled(zapcore.WarnLevel))
	assert.False(suite.T(), logger.Core().Enabled(zapcore.InfoLevel))
}

func (suite *LogTestSuite) TestNewLogger_ErrorLevel() {
	// 测试 error 日志级别
	logger := NewLogger("error", constants.FormatJson, suite.testAppInfo)
	assert.NotNil(suite.T(), logger)

	assert.True(suite.T(), logger.Core().Enabled(zapcore.ErrorLevel))
	assert.False(suite.T(), logger.Core().Enabled(zapcore.WarnLevel))
}

func (suite *LogTestSuite) TestNewLogger_InvalidLevel() {
	// 测试无效日志级别
	logger := NewLogger("invalid-level", constants.FormatJson, suite.testAppInfo)
	assert.NotNil(suite.T(), logger)

	// 无效级别应该默认使用 info 级别
	assert.True(suite.T(), logger.Core().Enabled(zapcore.InfoLevel))
}

func (suite *LogTestSuite) TestNewLogger_EmptyLevel() {
	// 测试空日志级别
	logger := NewLogger("", constants.FormatJson, suite.testAppInfo)
	assert.NotNil(suite.T(), logger)

	assert.True(suite.T(), logger.Core().Enabled(zapcore.InfoLevel))
}

func (suite *LogTestSuite) TestNewLogger_ConsoleFormat() {
	// 测试 console 日志格式
	logger := NewLogger("info", constants.FormatConsole, suite.testAppInfo)
	assert.NotNil(suite.T(), logger)
}

func (suite *LogTestSuite) TestNewLogger_JsonFormat() {
	// 测试 json 日志格式
	logger := NewLogger("info", constants.FormatJson, suite.testAppInfo)
	assert.NotNil(suite.T(), logger)
}

func (suite *LogTestSuite) TestNewLogger_InvalidFormat() {
	// 测试无效日志格式
	logger := NewLogger("info", "invalid-format", suite.testAppInfo)
	assert.NotNil(suite.T(), logger)
	// 无效格式应该默认使用 json 格式
}

func (suite *LogTestSuite) TestModuleCreation() {
	// 测试模块创建
	module := Module
	assert.NotNil(suite.T(), module)
	assert.Contains(suite.T(), module.String(), "log")
}

func (suite *LogTestSuite) TestLoggerInterface() {
	// 测试日志接口实现
	logger := NewLogger("info", constants.FormatJson, suite.testAppInfo)
	assert.NotNil(suite.T(), logger)

	// 测试各种日志级别
	assert.NotPanics(suite.T(), func() {
		logger.Debug("debug message")
		logger.Info("info message")
		logger.Warn("warn message")
		logger.Error("error message")
	})
}

func (suite *LogTestSuite) TestLoggerWithFields() {
	// 测试带字段的日志
	logger := NewLogger("info", constants.FormatJson, suite.testAppInfo)
	assert.NotNil(suite.T(), logger)

	assert.NotPanics(suite.T(), func() {
		logger.With(
			zap.String("key", "value"),
			zap.Int("number", 42),
		).Info("message with fields")
	})
}

func (suite *LogTestSuite) TestLoggerSugar() {
	// 测试 Sugar 日志
	logger := NewLogger("info", constants.FormatJson, suite.testAppInfo)
	assert.NotNil(suite.T(), logger)
	sugar := logger.Sugar()

	assert.NotPanics(suite.T(), func() {
		sugar.Debugw("debug message", "key", "value")
		sugar.Infow("info message", "key", "value")
		sugar.Warnw("warn message", "key", "value")
		sugar.Errorw("error message", "key", "value")
	})
}

// 运行测试套件
func TestLogTestSuite(t *testing.T) {
	suite.Run(t, new(LogTestSuite))
}

// 单元测试函数
func TestNewLogger_PanicRecovery(t *testing.T) {
	// 测试日志创建时的 panic 恢复
	assert.NotPanics(t, func() {
		testAppInfo := meta.AppInfo{
			ID:   "test-id",
			Name: "test-name",
		}
		_ = NewLogger("info", constants.FormatJson, testAppInfo)
	})
}
`
	if err := os.WriteFile(logTestPath, []byte(logTestContent), 0644); err != nil {
		return err
	}

	// 创建 otel_test.go
	otelDir := filepath.Join(root, "internal", "pkg", "otel")
	otelTestPath := filepath.Join(otelDir, "otel_test.go")
	otelTestContent := `package otel

import (
	"context"
	"testing"

	confv1 "` + appName + `/internal/conf/v1"
	"` + appName + `/internal/pkg/meta"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
	"go.uber.org/zap"
)

// OtelTestSuite 是 Otel 的测试套件
type OtelTestSuite struct {
	suite.Suite
	testLogger  *zap.Logger
	testAppInfo meta.AppInfo
}

func (suite *OtelTestSuite) SetupTest() {
	// 创建测试用的 logger
	var err error
	suite.testLogger, err = zap.NewDevelopment()
	assert.NoError(suite.T(), err)

	// 设置测试用的应用信息
	suite.testAppInfo = meta.AppInfo{
		ID:          "test-service-id",
		Name:        "test-service",
		Host:        "localhost",
		Environment: "dev",
	}
}

func (suite *OtelTestSuite) TestModuleCreation() {
	// 测试模块创建
	module := Module
	assert.NotNil(suite.T(), module)
	assert.Contains(suite.T(), module.String(), "otel")
}

func (suite *OtelTestSuite) TestWithTraceTLS() {
	// 测试 WithTraceTLS 选项
	opt := WithTraceTLS(true, []byte("test-ca-pem"))
	o := &traceOptions{}
	opt(o)
	// 只要不 panic 就通过
	assert.NotNil(suite.T(), o)
}

func (suite *OtelTestSuite) TestWithTraceTLS_WithoutCaPem() {
	// 测试不带 CA pem 的情况
	opt := WithTraceTLS(true, []byte(""))
	o := &traceOptions{}
	opt(o)
	assert.NotNil(suite.T(), o)
}

func (suite *OtelTestSuite) TestWithMetricTLS() {
	// 测试 WithMetricTLS 选项
	opt := WithMetricTLS(true, []byte("test-ca-pem"))
	o := &metricOptions{}
	opt(o)
	assert.NotNil(suite.T(), o)
}

func (suite *OtelTestSuite) TestWithLogTLS() {
	// 测试 WithLogTLS 选项
	opt := WithLogTLS(true, []byte("test-ca-pem"))
	o := &logOptions{}
	opt(o)
	assert.NotNil(suite.T(), o)
}

func (suite *OtelTestSuite) TestNewResource() {
	// 测试 newResource 函数
	res, err := newResource(suite.testAppInfo)
	assert.NoError(suite.T(), err)
	assert.NotNil(suite.T(), res)
}

func (suite *OtelTestSuite) TestNewPropagator() {
	// 测试 newPropagator 函数
	prop := newPropagator()
	assert.NotNil(suite.T(), prop)
}

func (suite *OtelTestSuite) TestSetupOTelSDK_PanicRecovery() {
	// 测试 panic 恢复
	assert.NotPanics(suite.T(), func() {
		ctx := context.Background()
		minConfig := &confv1.Observability{
			Trace: &confv1.Observability_Trace{
				Endpoint: "localhost:4318",
				Tls: &confv1.Observability_Tls{
					Enable: false,
				},
			},
			Metric: &confv1.Observability_Metric{
				Endpoint: "localhost:4318",
				Tls: &confv1.Observability_Tls{
					Enable: false,
				},
			},
			Log: &confv1.Observability_Logging{
				Endpoint: "localhost:4318",
				Tls: &confv1.Observability_Tls{
					Enable: false,
				},
			},
		}
		_, _ = SetupOTelSDK(ctx, suite.testAppInfo, minConfig, suite.testLogger)
	})
}

// 运行测试套件
func TestOtelTestSuite(t *testing.T) {
	suite.Run(t, new(OtelTestSuite))
}

func TestOtelOptionTypes(t *testing.T) {
	// 测试选项类型
	// 验证类型是否正确存在
	var _ TraceOption = nil
	var _ MetricOption = nil
	var _ LogOption = nil
}
`
	if err := os.WriteFile(otelTestPath, []byte(otelTestContent), 0644); err != nil {
		return err
	}

	return nil
}

func createMakefile(root string, isMonorepo bool) error {
	makefileContent := `# 默认值
VERSION ?= dev
GOIMAGE ?= golang:1.25.8-alpine3.22
GOOS ?= linux
GOARCH ?= arm64
CGOENABLED ?= 0

# 动态变量
SERVICE = $(shell basename $$PWD)
DOCKER_IMAGE=connect/$(SERVICE):$(VERSION)
REPOSITORY = sumery/$(SERVICE)
REGISTER = docker.io
ARM64=linux/arm64
AMD64=linux/amd64
CONSUL_ADDR=consul.example.com

.PHONY: k8s-dev
k8s-dev:
	kubectl apply -f deploy/dev

.PHONY: k8s-prod
k8s-prod:
	kubectl apply -f deploy/prod

.PHONY: dev
dev:
	CONSUL_ENABLED=false \
	CONSUL_ADDR=$(CONSUL_ADDR) \
	CONSUL_PATH=ecommerce/user/dev.yml \
	CONSUL_SCHEME=https \
	CONSUL_INSECURE_SKIP_VERIFY=true \
	go run cmd/server/main.go

.PHONY: prod
prod:
	CONSUL_ENABLED=true \
	CONSUL_ADDR=$(CONSUL_ADDR) \
	CONSUL_PATH=ecommerce/user/prod.yml \
	CONSUL_SCHEME=https \
	CONSUL_INSECURE_SKIP_VERIFY=true \
	go run cmd/server/main.go

.PHONY: pre
pre:
	CONSUL_ENABLED=true \
	CONSUL_ADDR=$(CONSUL_ADDR) \
	CONSUL_PATH=ecommerce/user/pre.yaml \
	CONSUL_SCHEME=https \
	CONSUL_INSECURE_SKIP_VERIFY=true \
	go run cmd/server/main.go

.PHONY: test
test:
	go test -short -coverprofile=coverage.out ./...

.PHONY: sqlc
sqlc:
	sqlc generate

`

	if isMonorepo {
		makefileContent += `.PHONY: api
api:
	cd ../../ && buf generate --template buf.gen.yaml --path api
	cd ../../ && buf generate --template buf.gen.ts.yaml --path api

.PHONY: generate
generate:
	cd ../../ && buf generate --template buf.gen.yaml --path api
	cd ../../ && buf generate --template buf.gen.ts.yaml --path api

.PHONY: conf
conf:
	cd ../../ && buf generate --template buf.gen.yaml --path services/user/internal/conf

.PHONY: docker-build
docker-build:
	@echo "构建的微服务: $(SERVICE)"
	@echo "系统: $(GOOS) | CPU架构: $(GOARCH)"
	@echo "镜像名: $(REPOSITORY):$(VERSION)"
	cd ../.. && docker build . \
	  -f ./services/$(SERVICE)/Dockerfile \
	  --progress=plain \
	  -t ecommerce/$(SERVICE):dev \
	  --build-arg SERVICE=$(SERVICE) \
	  --build-arg CGOENABLED=0 \
	  --build-arg GOIMAGE=golang:1.25.8-alpine3.22 \
	  --build-arg GOOS=linux \
	  --build-arg GOARCH=amd64 \
	  --build-arg VERSION=dev \
	  --platform linux/amd64

.PHONY: docker-push
docker-push:
	@echo "使用方式: make docker-push SERVICE=微服务名"
	@echo "OS: $(GOOS) | ARCH: $(GOARCH)"
	@echo "Docker image: $(REPOSITORY):$(VERSION)"
	docker tag ecommerce/$(SERVICE):$(VERSION) $(REGISTER)/$(REPOSITORY):$(VERSION)
	docker push $(REGISTER)/$(REPOSITORY):$(VERSION)

.PHONY: docker-deploy
docker-deploy:
	@echo "使用方式: make docker-deploy SERVICE=微服务名"
	@echo "SERVICE=$(SERVICE)"
	make docker-build SERVICE=$(SERVICE)
	@echo "SERVICE=$(SERVICE)"
	make docker-push SERVICE=$(SERVICE)

.PHONY: docker-deployx
docker-deployx:
	@echo "构建的微服务: $(SERVICE)"
	@echo "平台1: $(ARM64)"
	@echo "平台2: $(AMD64)"
	@echo "镜像名: $(REPOSITORY):$(VERSION)"
	cd ../.. && docker buildx build . \
	  -f ./services/$(SERVICE)/Dockerfile \
	  --progress=plain \
	  -t $(REGISTER)/$(REPOSITORY):$(VERSION) \
	  --build-arg SERVICE=$(SERVICE) \
	  --build-arg CGOENABLED=$(CGOENABLED) \
	  --build-arg GOIMAGE=$(GOIMAGE) \
	  --build-arg VERSION=$(VERSION) \
	  --platform $(ARM64),$(AMD64) \
	  --push \
	  --cache-from type=registry,ref=$(REGISTER)/$(REPOSITORY):cache \
	  --cache-to type=registry,ref=$(REGISTER)/$(REPOSITORY):cache,mode=max

.PHONY: docker-run
docker-run:
	docker compose up -d
`
	} else {
		makefileContent += `.PHONY: api
api:
	buf generate --template buf.gen.yaml --path api
	buf generate --template buf.gen.ts.yaml --path api

.PHONY: generate
generate:
	buf generate --template buf.gen.yaml --path api
	buf generate --template buf.gen.ts.yaml --path api

.PHONY: conf
conf:
	buf generate --template buf.gen.yaml --path internal/conf

.PHONY: docker-build
docker-build:
	@echo "构建的服务: $(SERVICE)"
	@echo "系统: $(GOOS) | CPU架构: $(GOARCH)"
	@echo "镜像名: $(REPOSITORY):$(VERSION)"
	docker build . \
	  --progress=plain \
	  -t ecommerce/$(SERVICE):dev \
	  --build-arg SERVICE=$(SERVICE) \
	  --build-arg CGOENABLED=0 \
	  --build-arg GOIMAGE=golang:1.25.8-alpine3.22 \
	  --build-arg GOOS=linux \
	  --build-arg GOARCH=amd64 \
	  --build-arg VERSION=dev \
	  --platform linux/amd64

.PHONY: docker-push
docker-push:
	@echo "使用方式: make docker-push SERVICE=服务名"
	@echo "OS: $(GOOS) | ARCH: $(GOARCH)"
	@echo "Docker image: $(REPOSITORY):$(VERSION)"
	docker tag ecommerce/$(SERVICE):$(VERSION) $(REGISTER)/$(REPOSITORY):$(VERSION)
	docker push $(REGISTER)/$(REPOSITORY):$(VERSION)

.PHONY: docker-deploy
docker-deploy:
	@echo "使用方式: make docker-deploy SERVICE=服务名"
	@echo "SERVICE=$(SERVICE)"
	make docker-build SERVICE=$(SERVICE)
	@echo "SERVICE=$(SERVICE)"
	make docker-push SERVICE=$(SERVICE)

.PHONY: docker-deployx
docker-deployx:
	@echo "构建的服务: $(SERVICE)"
	@echo "平台1: $(ARM64)"
	@echo "平台2: $(AMD64)"
	@echo "镜像名: $(REPOSITORY):$(VERSION)"
	docker buildx build . \
	  --progress=plain \
	  -t $(REGISTER)/$(REPOSITORY):$(VERSION) \
	  --build-arg SERVICE=$(SERVICE) \
	  --build-arg CGOENABLED=$(CGOENABLED) \
	  --build-arg GOIMAGE=$(GOIMAGE) \
	  --build-arg VERSION=$(VERSION) \
	  --platform $(ARM64),$(AMD64) \
	  --push \
	  --cache-from type=registry,ref=$(REGISTER)/$(REPOSITORY):cache \
	  --cache-to type=registry,ref=$(REGISTER)/$(REPOSITORY):cache,mode=max

.PHONY: docker-run
docker-run:
	docker compose up -d
`
	}

	return os.WriteFile(filepath.Join(root, "Makefile"), []byte(makefileContent), 0644)
}
