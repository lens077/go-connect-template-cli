package protogen

import (
	"bytes"
	"fmt"
)

func renderBiz(spec LayerSpec) ([]byte, error) {
	var b bytes.Buffer
	b.WriteString("package biz\n\n")

	std := []string{}
	if len(unaryMethods(spec.Service)) > 0 {
		std = append(std, "context")
	}
	if needsJSON(spec) {
		std = append(std, "encoding/json")
	}
	if needsTime(spec) {
		std = append(std, "time")
	}
	writeImports(&b, std, nil)

	fmt.Fprintf(&b, "// %s 的领域对象由 proto 消息生成,不含 protobuf 类型。\n", spec.Domain)
	fmt.Fprintf(&b, "// data 层实现 %sRepo,service 层把这些结构体翻译成 connect 的形状。\n\n", spec.Domain)

	for _, e := range spec.Enums {
		fmt.Fprintf(&b, "type %s int32\n\n", e.Name)
		if len(e.Values) == 0 {
			continue
		}
		b.WriteString("const (\n")
		for _, v := range e.Values {
			fmt.Fprintf(&b, "\t%s %s = %s\n", v.GoName, e.Name, v.Number)
		}
		b.WriteString(")\n\n")
	}

	for _, m := range spec.Messages {
		fmt.Fprintf(&b, "type %s struct {\n", m.Name)
		for _, field := range m.Fields {
			mt := mapField(spec.File, field)
			fmt.Fprintf(&b, "\t%s %s\n", field.GoName, mt.bizType())
		}
		b.WriteString("}\n\n")
	}

	fmt.Fprintf(&b, "// %sRepo 由 internal/data 实现。方法与 proto rpc 一一对应。\n", spec.Domain)
	fmt.Fprintf(&b, "type %sRepo interface {\n", spec.Domain)
	for _, m := range unaryMethods(spec.Service) {
		req, resp := bizParams(m)
		fmt.Fprintf(&b, "\t%s(ctx context.Context%s) %s\n", m.Name, req, resp)
	}
	b.WriteString("}\n\n")

	fmt.Fprintf(&b, "type %sUseCase struct {\n\trepo %sRepo\n}\n\n", spec.Domain, spec.Domain)
	fmt.Fprintf(&b, "func New%sUseCase(repo %sRepo) *%sUseCase {\n\treturn &%sUseCase{repo: repo}\n}\n",
		spec.Domain, spec.Domain, spec.Domain, spec.Domain)

	for _, m := range unaryMethods(spec.Service) {
		b.WriteByte('\n')
		req, resp := bizParams(m)
		fmt.Fprintf(&b, "func (uc *%sUseCase) %s(ctx context.Context%s) %s {\n",
			spec.Domain, m.Name, req, resp)
		if isEmpty(m.RequestType, m.Request) {
			fmt.Fprintf(&b, "\treturn uc.repo.%s(ctx)\n", m.Name)
		} else {
			fmt.Fprintf(&b, "\treturn uc.repo.%s(ctx, req)\n", m.Name)
		}
		b.WriteString("}\n")
	}
	return formatGo(b.Bytes())
}

func renderData(spec LayerSpec) ([]byte, error) {
	var b bytes.Buffer
	b.WriteString("package data\n\n")
	std := []string{}
	third := []string{spec.ServiceModule + "/internal/biz", "go.uber.org/zap"}
	if len(unaryMethods(spec.Service)) > 0 {
		std = append(std, "context", "fmt")
	}
	writeImports(&b, std, third)

	fmt.Fprintf(&b, "var _ biz.%sRepo = (*%sRepo)(nil)\n\n", spec.Domain, spec.Camel)
	fmt.Fprintf(&b, "type %sRepo struct {\n\tdata *Data\n\tlog  *zap.Logger\n}\n\n", spec.Camel)
	fmt.Fprintf(&b, "func New%sRepo(data *Data, logger *zap.Logger) biz.%sRepo {\n", spec.Domain, spec.Domain)
	fmt.Fprintf(&b, "\treturn &%sRepo{data: data, log: logger}\n}\n", spec.Camel)

	for _, m := range unaryMethods(spec.Service) {
		b.WriteByte('\n')
		req, resp := dataParams(m)
		fmt.Fprintf(&b, "func (r *%sRepo) %s(ctx context.Context%s) %s {\n",
			spec.Camel, m.Name, req, resp)
		b.WriteString("\t// TODO: 用 sqlc 实现。查询写在 internal/data/queries/,跑 sqlc generate 后调用 models。\n")
		if isEmpty(m.ResponseType, m.Response) {
			fmt.Fprintf(&b, "\treturn fmt.Errorf(%q)\n", spec.Domain+"."+m.Name+": not implemented")
		} else {
			fmt.Fprintf(&b, "\treturn nil, fmt.Errorf(%q)\n", spec.Domain+"."+m.Name+": not implemented")
		}
		b.WriteString("}\n")
	}
	return formatGo(b.Bytes())
}

func dataParams(m Method) (req, resp string) {
	if !isEmpty(m.RequestType, m.Request) {
		req = ", req biz." + m.Request
	}
	if isEmpty(m.ResponseType, m.Response) {
		resp = "error"
	} else {
		resp = "(*biz." + m.Response + ", error)"
	}
	return req, resp
}
