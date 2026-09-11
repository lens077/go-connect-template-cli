package scaffold

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// OriginFile 记录一个服务是从模板的哪个 commit 生成的。
//
// 这是产物里唯一一份 co 写的元数据,而且刻意只记**历史事实**:模板仓库、commit、
// 当时的 co 版本、当时传的渲染参数。不记 feature 列表、不记「哪些文件是模板的」——
// 那些描述的是代码现状,用户一改代码就成了假的;而「从哪个 commit 生成」怎么改
// 代码都不会变。
//
// 它的用途是给 co upgrade 一个 base:有了「生成时模板长什么样」,比对就从两方
// (模板新版 vs 现状)变成三方,能分清「模板改了」和「用户改了」。没有这个文件
// (锚点机制之前生成的服务)upgrade 退回两方比对 + 锚点搬运。
const OriginFile = ".co-origin.yaml"

// Origin 是 OriginFile 的内容。
type Origin struct {
	// Template 模板来源:URL 或本地目录
	Template string `yaml:"template"`
	// Commit 生成时模板的 commit(40 位)。用 commit 而不是 tag 名:tag 可以被挪
	Commit string `yaml:"commit"`
	// Dirty 生成时模板工作树有未提交改动(--template-dir 开发模板时),
	// 生成物和 Commit 对不上,base 只是近似,合并结果要多看一眼
	Dirty bool `yaml:"dirty,omitempty"`
	// Co 生成时的 co 版本,排查用
	Co string `yaml:"co"`
	// Render 生成时传的渲染参数。upgrade 要用同样的值渲染参考副本,
	// 没记录的话用户得每次再传一遍
	Render RenderParams `yaml:"render,omitempty"`
}

// RenderParams 是填进模板、产物里别处不再出现的渲染参数。
type RenderParams struct {
	ServiceName     string `yaml:"service_name,omitempty"`
	DockerRegistry  string `yaml:"docker_registry,omitempty"`
	DockerNamespace string `yaml:"docker_namespace,omitempty"`
	ConsulAddr      string `yaml:"consul_addr,omitempty"`
}

const originHeader = `# co 生成记录:这个服务是从模板的哪个 commit 生成的。
# co upgrade 用它做三方合并的 base,分清「模板改了」和「你改了」。
# 只记历史事实,不记代码现状;别手改 commit,除非你确定服务对应哪个模板版本。
`

// WriteOrigin 写 OriginFile。
func WriteOrigin(serviceRoot string, o Origin) error {
	if o.Commit == "" {
		// 没有 commit 的 origin 没有用途,写一个只会让 upgrade 以为有 base
		return errors.New("origin: commit is empty")
	}
	data, err := yaml.Marshal(o)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(serviceRoot, OriginFile), append([]byte(originHeader), data...), 0o644)
}

// ReadOrigin 读 OriginFile;文件不存在返回 (nil, nil) —— 那是合法状态,不是错误。
func ReadOrigin(serviceRoot string) (*Origin, error) {
	data, err := os.ReadFile(filepath.Join(serviceRoot, OriginFile))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var o Origin
	if err := yaml.Unmarshal(data, &o); err != nil {
		return nil, fmt.Errorf("parse %s: %w", OriginFile, err)
	}
	if o.Commit == "" {
		return nil, fmt.Errorf("%s: commit is empty", OriginFile)
	}
	return &o, nil
}
