package protogen

import (
	"bytes"
	"fmt"
	"go/format"
	"path/filepath"
	"sort"
	"strings"
)

// LayerSpec 是渲染 service/biz/data 三层示例所需的全部信息。
type LayerSpec struct {
	Service       Service
	Domain        string // MerchantService -> Merchant
	Camel         string
	Stem          string // proto 文件名去掉 .proto,用来给三个 .go 起名
	PBImport      string
	ConnectImport string
	ConnectPkg    string
	ServiceModule string
	File          File
	Messages      []Message
	Enums         []Enum
}

// NewLayerSpec 由 proto 文件和一个 service 推出三层示例的输入。
func NewLayerSpec(f File, svc Service, serviceModule string) (LayerSpec, error) {
	ss, err := NewServerSpec(f, svc, "service")
	if err != nil {
		return LayerSpec{}, err
	}
	domain := domainName(svc.Name)
	msgs, enums, err := reachable(f, svc)
	if err != nil {
		return LayerSpec{}, err
	}
	return LayerSpec{
		Service:       svc,
		Domain:        domain,
		Camel:         camelIdent(domain),
		Stem:          fileStem(f.Path),
		PBImport:      ss.PBImport,
		ConnectImport: ss.ConnectImport,
		ConnectPkg:    ss.ConnectPkg,
		ServiceModule: serviceModule,
		File:          f,
		Messages:      msgs,
		Enums:         enums,
	}, nil
}

// LayerFile 是一层的落点与内容。
type LayerFile struct {
	Rel     string
	Content []byte
}

// RenderLayers 一次渲染三层。任一层 format 失败都不返回,避免落半套。
func RenderLayers(spec LayerSpec) ([]LayerFile, error) {
	biz, err := renderBiz(spec)
	if err != nil {
		return nil, fmt.Errorf("biz: %w", err)
	}
	data, err := renderData(spec)
	if err != nil {
		return nil, fmt.Errorf("data: %w", err)
	}
	svc, err := renderService(spec)
	if err != nil {
		return nil, fmt.Errorf("service: %w", err)
	}
	stem := spec.Stem
	if stem == "" {
		stem = strings.ToLower(spec.Camel)
	}
	return []LayerFile{
		{Rel: filepath.Join("internal", "biz", stem+".go"), Content: biz},
		{Rel: filepath.Join("internal", "data", stem+".go"), Content: data},
		{Rel: filepath.Join("internal", "service", stem+".go"), Content: svc},
	}, nil
}

// WireItem 是往 fx.Module 锚点插的一行。
type WireItem struct {
	File   string
	Anchor string
	Text   string
}

// defaultAnchors 与模板 .co/manifest.yaml 的 anchors 对齐。
// proto gen 不 clone 模板,所以把这张表写在 CLI 里:锚点名和相对路径
// 是 co new 产物的稳定契约,挪了等于换模板大版本。
var defaultAnchors = map[string]string{
	"biz-providers":           "internal/biz/biz.go",
	"data-providers":          "internal/data/data.go",
	"service-providers":       "internal/service/service.go",
	"server-imports":          "internal/server/server.go",
	"server-handler-params":   "internal/server/server.go",
	"server-handler-register": "internal/server/server.go",
}

// WireItems 返回要把新 provider 插到哪些锚点。
func (s LayerSpec) WireItems() []WireItem {
	param := s.Camel + "Service"
	return []WireItem{
		{File: defaultAnchors["biz-providers"], Anchor: "biz-providers", Text: fmt.Sprintf("New%sUseCase,", s.Domain)},
		{File: defaultAnchors["data-providers"], Anchor: "data-providers", Text: fmt.Sprintf("New%sRepo,", s.Domain)},
		{File: defaultAnchors["service-providers"], Anchor: "service-providers", Text: fmt.Sprintf("New%s,", s.Service.Name)},
		{File: defaultAnchors["server-imports"], Anchor: "server-imports", Text: fmt.Sprintf("%q", s.ConnectImport)},
		{File: defaultAnchors["server-handler-params"], Anchor: "server-handler-params",
			Text: fmt.Sprintf("%s %s.%sHandler,", param, s.ConnectPkg, s.Service.Name)},
		{File: defaultAnchors["server-handler-register"], Anchor: "server-handler-register",
			Text: fmt.Sprintf("mux.Handle(%s.New%sHandler(%s, handlerOptions(connectOptions)...))",
				s.ConnectPkg, s.Service.Name, param)},
	}
}

func reachable(f File, svc Service) ([]Message, []Enum, error) {
	needMsg := map[string]bool{}
	needEnum := map[string]bool{}
	var walkType func(raw string) error
	walkType = func(raw string) error {
		if isWKT(raw) {
			if name, _ := wktName(raw); name != "" {
				if _, ok := wkt[name]; !ok {
					return fmt.Errorf("google.protobuf.%s is not supported by proto gen", name)
				}
			}
			return nil
		}
		name := baseType(raw)
		if _, ok := scalars[name]; ok && !strings.Contains(raw, ".") {
			return nil
		}
		qualified := strings.TrimPrefix(raw, ".")
		if strings.Contains(qualified, ".") {
			prefix := strings.TrimSuffix(qualified, "."+name)
			if prefix != f.Package {
				return fmt.Errorf("type %q is declared outside %s; imported or nested application messages are not supported by proto gen", raw, f.Path)
			}
		}
		if _, ok := f.Enum(name); ok {
			needEnum[name] = true
			return nil
		}
		m, ok := f.Message(name)
		if !ok {
			return fmt.Errorf("type %q is not a top-level message in %s; imported or nested application messages are not supported by proto gen", raw, f.Path)
		}
		if needMsg[name] {
			return nil
		}
		needMsg[name] = true
		for _, field := range m.Fields {
			if field.Oneof != "" {
				return fmt.Errorf("message %s contains oneof %q; oneof conversion is not supported by proto gen", m.Name, field.Oneof)
			}
			if field.IsMap {
				if err := walkType(field.MapKey); err != nil {
					return err
				}
				if err := walkType(field.MapVal); err != nil {
					return err
				}
				if err := validateGeneratedField(f, m, field); err != nil {
					return err
				}
				continue
			}
			if err := walkType(field.Type); err != nil {
				return err
			}
			if err := validateGeneratedField(f, m, field); err != nil {
				return err
			}
		}
		return nil
	}
	for _, m := range svc.Methods {
		if m.Stream() {
			if err := validateStreamType(f, m.RequestType); err != nil {
				return nil, nil, fmt.Errorf("rpc %s request: %w", m.Name, err)
			}
			if err := validateStreamType(f, m.ResponseType); err != nil {
				return nil, nil, fmt.Errorf("rpc %s response: %w", m.Name, err)
			}
			continue
		}
		if isWKT(m.RequestType) && !isEmpty(m.RequestType, m.Request) {
			return nil, nil, fmt.Errorf("rpc %s request: top-level WKT %q is not supported by proto gen", m.Name, m.RequestType)
		}
		if isWKT(m.ResponseType) && !isEmpty(m.ResponseType, m.Response) {
			return nil, nil, fmt.Errorf("rpc %s response: top-level WKT %q is not supported by proto gen", m.Name, m.ResponseType)
		}
		if err := walkType(m.RequestType); err != nil {
			return nil, nil, fmt.Errorf("rpc %s request: %w", m.Name, err)
		}
		if err := walkType(m.ResponseType); err != nil {
			return nil, nil, fmt.Errorf("rpc %s response: %w", m.Name, err)
		}
	}
	msgs := make([]Message, 0, len(needMsg))
	for _, m := range f.Messages {
		if needMsg[m.Name] {
			msgs = append(msgs, m)
		}
	}
	enums := make([]Enum, 0, len(needEnum))
	for _, e := range f.Enums {
		if needEnum[e.Name] {
			enums = append(enums, e)
		}
	}
	return msgs, enums, nil
}

func validateStreamType(f File, raw string) error {
	if isWKT(raw) {
		return fmt.Errorf("WKT %q is not supported for streaming RPCs", raw)
	}
	name := baseType(raw)
	qualified := strings.TrimPrefix(raw, ".")
	if strings.Contains(qualified, ".") {
		prefix := strings.TrimSuffix(qualified, "."+name)
		if prefix != f.Package {
			return fmt.Errorf("type %q is not a top-level message in %s", raw, f.Path)
		}
	}
	if _, ok := f.Message(name); !ok {
		return fmt.Errorf("type %q is not a top-level message in %s", raw, f.Path)
	}
	return nil
}

func validateGeneratedField(f File, message Message, field Field) error {
	m := mapField(f, field)
	if m.kind == kindEmpty {
		return fmt.Errorf("message %s field %s: google.protobuf.Empty is not supported as a field", message.Name, field.Name)
	}
	if field.Optional && m.kind == kindEnum {
		return fmt.Errorf("message %s field %s: optional enum conversion is not supported by proto gen", message.Name, field.Name)
	}
	if field.Repeated {
		switch m.kind {
		case kindTime, kindDuration, kindStruct, kindValue, kindWrapper:
			return fmt.Errorf("message %s field %s: repeated WKT conversion is not supported by proto gen", message.Name, field.Name)
		}
	}
	if field.IsMap && m.elem != nil {
		switch m.elem.kind {
		case kindTime, kindDuration, kindStruct, kindValue, kindWrapper, kindEmpty:
			return fmt.Errorf("message %s field %s: WKT map values are not supported by proto gen", message.Name, field.Name)
		}
	}
	return nil
}

func unaryMethods(svc Service) []Method {
	var out []Method
	for _, m := range svc.Methods {
		if !m.Stream() {
			out = append(out, m)
		}
	}
	return out
}

func formatGo(src []byte) ([]byte, error) {
	out, err := format.Source(src)
	if err != nil {
		return nil, fmt.Errorf("generated code is not valid Go: %w\n%s", err, src)
	}
	return out, nil
}

func unique(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func writeImports(b *bytes.Buffer, std, third []string) {
	std = unique(std)
	third = unique(third)
	if len(std)+len(third) == 0 {
		return
	}
	b.WriteString("import (\n")
	for _, p := range std {
		fmt.Fprintf(b, "\t%q\n", p)
	}
	if len(std) > 0 && len(third) > 0 {
		b.WriteByte('\n')
	}
	for _, p := range third {
		fmt.Fprintf(b, "\t%q\n", p)
	}
	b.WriteString(")\n\n")
}

func bizParams(m Method) (req, resp string) {
	if !isEmpty(m.RequestType, m.Request) {
		req = ", req " + m.Request
	}
	if isEmpty(m.ResponseType, m.Response) {
		resp = "error"
	} else {
		resp = "(*" + m.Response + ", error)"
	}
	return req, resp
}

func needsJSON(spec LayerSpec) bool {
	return usesWKT(spec, "Struct") || usesWKT(spec, "Value")
}

func needsTime(spec LayerSpec) bool {
	return usesWKT(spec, "Timestamp") || usesWKT(spec, "Duration")
}

func usesWrapper(spec LayerSpec) bool {
	for _, name := range []string{
		"BoolValue", "StringValue", "BytesValue",
		"Int32Value", "Int64Value", "UInt32Value", "UInt64Value",
		"FloatValue", "DoubleValue",
	} {
		if usesWKT(spec, name) {
			return true
		}
	}
	return false
}

func usesEmpty(spec LayerSpec) bool {
	for _, m := range spec.Service.Methods {
		if isEmpty(m.RequestType, m.Request) || isEmpty(m.ResponseType, m.Response) {
			return true
		}
	}
	return false
}

func usesPB(spec LayerSpec) bool {
	for _, m := range spec.Service.Methods {
		if !isEmpty(m.RequestType, m.Request) || !isEmpty(m.ResponseType, m.Response) {
			return true
		}
	}
	return len(spec.Messages) > 0
}

func usesWKT(spec LayerSpec, name string) bool {
	want := "google.protobuf." + name
	check := func(raw string) bool {
		t := strings.TrimPrefix(raw, ".")
		return t == want
	}
	for _, m := range spec.Service.Methods {
		if check(m.RequestType) || check(m.ResponseType) {
			return true
		}
	}
	for _, msg := range spec.Messages {
		for _, field := range msg.Fields {
			if field.IsMap {
				if check(field.MapVal) {
					return true
				}
				continue
			}
			if check(field.Type) {
				return true
			}
		}
	}
	return false
}
