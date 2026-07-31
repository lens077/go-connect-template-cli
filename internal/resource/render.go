package resource

import (
	"bytes"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"text/template"

	"github.com/lens077/co-cli/internal/manifest"
)

// Spec 是渲染一套资源需要的全部输入。
type Spec struct {
	Names
	// Module 目标 module 路径。proto 的 go_package 与 api 导入用它。
	Module string
	// ServiceModule 服务包的导入前缀。internal/... 的导入用它。
	// standalone 下与 Module 相同;monorepo 下服务没有自己的 go.mod,
	// 它是根 module 再接上 services/<name>。
	ServiceModule string
	// ServiceName 服务注册名,如 cart-service
	ServiceName string
	// SchemaSeq schema 文件名的 000N_ 前缀,决定 sqlc 读取建表语句的顺序。
	// 由 NextSchemaSeq 从目标服务已有的 schema 目录算出。
	SchemaSeq string
	// Features 已启用的 feature,模板里 {{if .Features.minio}} 用
	Features manifest.FeatureSet
}

// File 是渲染结果:相对服务目录的路径 + 内容。
type File struct {
	Path    string
	Content []byte
}

// tmplTargets 把模板文件映射到输出路径。
//
// 这张表刻意写死在 CLI 里而不是放进 manifest:输出路径不是配置项,
// 它由模板本身的 import 路径与 sqlc.yaml 的 schema/queries 目录锁死,
// 改一处就得同步改三处,做成可配置只会让人以为它能随便改。
var tmplTargets = []struct {
	tmpl string // 相对 .co/scaffold/resource
	dest string // 相对服务目录,可含 {{...}}
}{
	{"proto.tmpl", "{{.APIDir}}/{{.Name}}.proto"},
	{"schema.sql.tmpl", SchemaDir + "/{{.SchemaSeq}}_{{.Table}}.sql"},
	{"queries.sql.tmpl", "internal/data/queries/{{.Table}}.sql"},
	{"biz.go.tmpl", "internal/biz/{{.Name}}.go"},
	{"data.go.tmpl", "internal/data/{{.Name}}.go"},
	{"service.go.tmpl", "internal/service/{{.Name}}.go"},
}

// Anchors 是新资源要往各 Module 里插的 provider,按锚点名分组。
//
// 与 tmplTargets 同理,这些代码片段的形状由模板里 fx.Module 的写法决定,
// 不是用户可调的东西。
func (s Spec) Anchors() []manifest.Insert {
	return []manifest.Insert{
		{Anchor: "biz-providers", Text: fmt.Sprintf("New%sUseCase,", s.Pascal)},
		{Anchor: "data-providers", Text: fmt.Sprintf("New%sRepo,", s.Pascal)},
		{Anchor: "service-providers", Text: fmt.Sprintf("New%sService,", s.Pascal)},
		{Anchor: "server-imports", Text: fmt.Sprintf("%q", path.Join(s.Module, s.APIDir, s.ConnectPkg))},
		{Anchor: "server-handler-params", Text: fmt.Sprintf("%sService %s.%sServiceHandler,", s.Camel, s.ConnectPkg, s.Pascal)},
		// handlerOptions 定义在 server.go 里,把 validate 拦截器追加到全局
		// Connect 选项上。用它而不是自己 append,是为了让每个 handler 拿到
		// 独立的一份选项切片(见 server.go 里那段注释)。
		{Anchor: "server-handler-register", Text: fmt.Sprintf(
			"mux.Handle(%s.New%sServiceHandler(%sService, handlerOptions(connectOptions)...))",
			s.ConnectPkg, s.Pascal, s.Camel)},
	}
}

// Targets 只算出这套资源会写哪些文件,不做渲染。--dry-run 用它。
func Targets(spec Spec) []string {
	out := make([]string, 0, len(tmplTargets))
	for _, t := range tmplTargets {
		dest, err := renderText("dest:"+t.dest, t.dest, spec)
		if err != nil {
			// dest 里只有 Names 的字段,渲染不出错;真出错了也只影响预览
			continue
		}
		out = append(out, filepath.FromSlash(string(dest)))
	}
	return out
}

// Render 渲染整套资源。scaffoldDir 是模板仓库里的 .co/scaffold 绝对路径。
//
// 一次性全部渲染完再返回,不边渲染边落盘:任何一个模板出错都不该在磁盘上
// 留下半套代码 —— 那种状态既编译不过,也看不出是生成到一半失败的。
func Render(scaffoldDir string, spec Spec) ([]File, error) {
	out := make([]File, 0, len(tmplTargets))

	for _, t := range tmplTargets {
		src := filepath.Join(scaffoldDir, "resource", t.tmpl)
		body, err := renderFile(src, spec)
		if err != nil {
			return nil, err
		}

		dest, err := renderText("dest:"+t.dest, t.dest, spec)
		if err != nil {
			return nil, err
		}

		out = append(out, File{Path: filepath.FromSlash(string(dest)), Content: body})
	}
	return out, nil
}

func renderFile(src string, data any) ([]byte, error) {
	raw, err := os.ReadFile(src)
	if err != nil {
		return nil, fmt.Errorf("read template: %w", err)
	}
	return renderText(filepath.Base(src), string(raw), data)
}

func renderText(name, text string, data any) ([]byte, error) {
	// Option missingkey=error:模板里写错一个变量名(.Pascl),默认会渲染成
	// <no value> 静默塞进源码,要等 go build 才发现,而且报错指向生成物不指向模板。
	tmpl, err := template.New(name).Option("missingkey=error").Parse(text)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", name, err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("render %s: %w", name, err)
	}
	return buf.Bytes(), nil
}

// RenderOverlay 渲染布局覆盖目录里的一个模板文件。
func RenderOverlay(src string, data any) ([]byte, error) { return renderFile(src, data) }
