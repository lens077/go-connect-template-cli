package protogen

import (
	"bytes"
	"fmt"
)

func renderService(spec LayerSpec) ([]byte, error) {
	var b bytes.Buffer
	b.WriteString("package service\n\n")

	std := []string{}
	third := []string{}
	nUnary := len(unaryMethods(spec.Service))
	nStream := 0
	for _, m := range spec.Service.Methods {
		if m.Stream() {
			nStream++
		}
	}
	if nUnary+nStream > 0 {
		std = append(std, "context")
	}
	if nStream > 0 {
		std = append(std, "errors")
	}
	if needsJSON(spec) {
		std = append(std, "encoding/json")
	}
	if nUnary+nStream > 0 {
		third = append(third, "connectrpc.com/connect")
	}
	third = append(third, spec.ConnectImport)
	if usesPB(spec) {
		third = append(third, spec.PBImport)
	}
	third = append(third, spec.ServiceModule+"/internal/biz")
	third = append(third, "go.uber.org/zap")
	if usesWKT(spec, "Timestamp") {
		third = append(third, "google.golang.org/protobuf/types/known/timestamppb")
	}
	if usesWKT(spec, "Duration") {
		third = append(third, "google.golang.org/protobuf/types/known/durationpb")
	}
	if usesWKT(spec, "Struct") || usesWKT(spec, "Value") {
		third = append(third, "google.golang.org/protobuf/types/known/structpb")
	}
	if usesWrapper(spec) {
		third = append(third, "google.golang.org/protobuf/types/known/wrapperspb")
	}
	if usesEmpty(spec) {
		third = append(third, "google.golang.org/protobuf/types/known/emptypb")
	}
	writeServiceImports(&b, std, spec, third)

	fmt.Fprintf(&b, "var _ %s.%sHandler = (*%s)(nil)\n\n", spec.ConnectPkg, spec.Service.Name, spec.Service.Name)
	fmt.Fprintf(&b, "type %s struct {\n\tuc  *biz.%sUseCase\n\tlog *zap.Logger\n}\n\n", spec.Service.Name, spec.Domain)
	fmt.Fprintf(&b, "func New%s(uc *biz.%sUseCase, log *zap.Logger) %s.%sHandler {\n",
		spec.Service.Name, spec.Domain, spec.ConnectPkg, spec.Service.Name)
	fmt.Fprintf(&b, "\treturn &%s{uc: uc, log: log}\n}\n", spec.Service.Name)

	recv := "s"
	for _, m := range spec.Service.Methods {
		b.WriteByte('\n')
		if m.Stream() {
			writeMethod(&b, recv, spec.Service.Name, m)
			continue
		}
		writeServiceUnary(&b, spec, recv, m)
	}

	if nUnary > 0 {
		for _, msg := range spec.Messages {
			b.WriteByte('\n')
			writeToBiz(&b, spec, msg)
			b.WriteByte('\n')
			writeToPB(&b, spec, msg)
		}
	}

	return formatGo(b.Bytes())
}

func writeServiceImports(b *bytes.Buffer, std []string, spec LayerSpec, third []string) {
	std = unique(std)
	var rest []string
	seen := map[string]bool{}
	hasPB := false
	for _, p := range unique(third) {
		if p == spec.PBImport {
			hasPB = true
			continue
		}
		if !seen[p] {
			rest = append(rest, p)
			seen[p] = true
		}
	}
	if len(std)+len(rest) == 0 && !hasPB {
		return
	}
	b.WriteString("import (\n")
	for _, p := range std {
		fmt.Fprintf(b, "\t%q\n", p)
	}
	if len(std) > 0 && (hasPB || len(rest) > 0) {
		b.WriteByte('\n')
	}
	if hasPB {
		fmt.Fprintf(b, "\tv1 %q\n", spec.PBImport)
	}
	for _, p := range rest {
		fmt.Fprintf(b, "\t%q\n", p)
	}
	b.WriteString(")\n\n")
}

func writeServiceUnary(b *bytes.Buffer, spec LayerSpec, recv string, m Method) {
	reqT := pbType(m.RequestType, m.Request)
	respT := pbType(m.ResponseType, m.Response)
	fmt.Fprintf(b, "func (%s *%s) %s(ctx context.Context, c *connect.Request[%s]) (*connect.Response[%s], error) {\n",
		recv, spec.Service.Name, m.Name, reqT, respT)

	emptyReq := isEmpty(m.RequestType, m.Request)
	emptyResp := isEmpty(m.ResponseType, m.Response)

	switch {
	case emptyReq && emptyResp:
		fmt.Fprintf(b, "\tif err := %s.uc.%s(ctx); err != nil {\n\t\treturn nil, err\n\t}\n", recv, m.Name)
		b.WriteString("\treturn connect.NewResponse(&emptypb.Empty{}), nil\n")
	case emptyReq:
		fmt.Fprintf(b, "\tout, err := %s.uc.%s(ctx)\n", recv, m.Name)
		b.WriteString("\tif err != nil {\n\t\treturn nil, err\n\t}\n")
		fmt.Fprintf(b, "\treturn connect.NewResponse(to%sPB(out)), nil\n", m.Response)
	case emptyResp:
		fmt.Fprintf(b, "\tif err := %s.uc.%s(ctx, to%s(c.Msg)); err != nil {\n\t\treturn nil, err\n\t}\n",
			recv, m.Name, m.Request)
		b.WriteString("\treturn connect.NewResponse(&emptypb.Empty{}), nil\n")
	default:
		fmt.Fprintf(b, "\tout, err := %s.uc.%s(ctx, to%s(c.Msg))\n", recv, m.Name, m.Request)
		b.WriteString("\tif err != nil {\n\t\treturn nil, err\n\t}\n")
		fmt.Fprintf(b, "\treturn connect.NewResponse(to%sPB(out)), nil\n", m.Response)
	}
	b.WriteString("}\n")
}

func pbType(raw, name string) string {
	if isEmpty(raw, name) {
		return "emptypb.Empty"
	}
	return "v1." + name
}

func writeToBiz(b *bytes.Buffer, spec LayerSpec, msg Message) {
	fmt.Fprintf(b, "func to%s(p *v1.%s) biz.%s {\n", msg.Name, msg.Name, msg.Name)
	fmt.Fprintf(b, "\tif p == nil {\n\t\treturn biz.%s{}\n\t}\n", msg.Name)
	fmt.Fprintf(b, "\tm := biz.%s{\n", msg.Name)
	var after []string
	for _, field := range msg.Fields {
		mt := mapField(spec.File, field)
		line, deferred := protoToBizField(field, mt)
		if deferred != "" {
			after = append(after, deferred)
			continue
		}
		if line != "" {
			fmt.Fprintf(b, "\t\t%s: %s,\n", field.GoName, line)
		}
	}
	b.WriteString("\t}\n")
	for _, s := range after {
		b.WriteString(s)
	}
	b.WriteString("\treturn m\n}\n")
}

func protoToBizField(field Field, mt mapped) (inline, deferred string) {
	get := "p.Get" + field.GoName + "()"
	switch {
	case mt.slice && mt.kind == kindMessage:
		return "", fmt.Sprintf(
			"\tif xs := %s; len(xs) > 0 {\n\t\tm.%s = make([]*biz.%s, 0, len(xs))\n\t\tfor _, x := range xs {\n\t\t\titem := to%s(x)\n\t\t\tm.%s = append(m.%s, &item)\n\t\t}\n\t}\n",
			get, field.GoName, mt.biz, mt.biz, field.GoName, field.GoName)
	case mt.slice && mt.kind == kindEnum:
		return "", fmt.Sprintf(
			"\tif xs := %s; len(xs) > 0 {\n\t\tm.%s = make([]biz.%s, 0, len(xs))\n\t\tfor _, x := range xs {\n\t\t\tm.%s = append(m.%s, biz.%s(x))\n\t\t}\n\t}\n",
			get, field.GoName, mt.biz, field.GoName, field.GoName, mt.biz)
	case mt.key != nil && mt.elem != nil && mt.elem.kind == kindMessage:
		return "", fmt.Sprintf(
			"\tif xs := %s; len(xs) > 0 {\n\t\tm.%s = make(map[%s]*biz.%s, len(xs))\n\t\tfor k, x := range xs {\n\t\t\titem := to%s(x)\n\t\t\tm.%s[k] = &item\n\t\t}\n\t}\n",
			get, field.GoName, mt.key.biz, mt.elem.biz, mt.elem.biz, field.GoName)
	case mt.key != nil && mt.elem != nil && mt.elem.kind == kindEnum:
		return "", fmt.Sprintf(
			"\tif xs := %s; len(xs) > 0 {\n\t\tm.%s = make(map[%s]biz.%s, len(xs))\n\t\tfor k, x := range xs {\n\t\t\tm.%s[k] = biz.%s(x)\n\t\t}\n\t}\n",
			get, field.GoName, mt.key.biz, mt.elem.biz, field.GoName, mt.elem.biz)
	case mt.kind == kindTime:
		return "", fmt.Sprintf("\tif t := %s; t != nil {\n\t\tm.%s = t.AsTime()\n\t}\n", get, field.GoName)
	case mt.kind == kindDuration:
		return "", fmt.Sprintf("\tif d := %s; d != nil {\n\t\tm.%s = d.AsDuration()\n\t}\n", get, field.GoName)
	case mt.kind == kindStruct:
		return "", fmt.Sprintf("\tif s := %s; s != nil {\n\t\tm.%s, _ = json.Marshal(s.AsMap())\n\t}\n", get, field.GoName)
	case mt.kind == kindValue:
		return "", fmt.Sprintf("\tif v := %s; v != nil {\n\t\tm.%s, _ = json.Marshal(v.AsInterface())\n\t}\n", get, field.GoName)
	case mt.kind == kindWrapper:
		return "", fmt.Sprintf("\tif w := %s; w != nil {\n\t\tm.%s = w.GetValue()\n\t}\n", get, field.GoName)
	case mt.kind == kindMessage && !mt.slice:
		return "", fmt.Sprintf(
			"\tif x := %s; x != nil {\n\t\titem := to%s(x)\n\t\tm.%s = &item\n\t}\n",
			get, mt.biz, field.GoName)
	case mt.kind == kindEnum && !mt.slice:
		return fmt.Sprintf("biz.%s(%s)", mt.biz, get), ""
	case mt.pointer && mt.kind == kindScalar:
		return "p." + field.GoName, ""
	case mt.kind == kindEmpty:
		return "", ""
	default:
		return get, ""
	}
}

func writeToPB(b *bytes.Buffer, spec LayerSpec, msg Message) {
	fmt.Fprintf(b, "func to%sPB(m *biz.%s) *v1.%s {\n", msg.Name, msg.Name, msg.Name)
	b.WriteString("\tif m == nil {\n\t\treturn nil\n\t}\n")
	fmt.Fprintf(b, "\tp := &v1.%s{\n", msg.Name)
	var after []string
	for _, field := range msg.Fields {
		mt := mapField(spec.File, field)
		line, deferred := bizToProtoField(field, mt)
		if deferred != "" {
			after = append(after, deferred)
			continue
		}
		if line != "" {
			fmt.Fprintf(b, "\t\t%s: %s,\n", field.GoName, line)
		}
	}
	b.WriteString("\t}\n")
	for _, s := range after {
		b.WriteString(s)
	}
	b.WriteString("\treturn p\n}\n")
}

func bizToProtoField(field Field, mt mapped) (inline, deferred string) {
	src := "m." + field.GoName
	switch {
	case mt.slice && mt.kind == kindMessage:
		return "", fmt.Sprintf(
			"\tif len(%s) > 0 {\n\t\tp.%s = make([]*v1.%s, 0, len(%s))\n\t\tfor _, x := range %s {\n\t\t\tp.%s = append(p.%s, to%sPB(x))\n\t\t}\n\t}\n",
			src, field.GoName, mt.biz, src, src, field.GoName, field.GoName, mt.biz)
	case mt.slice && mt.kind == kindEnum:
		return "", fmt.Sprintf(
			"\tif len(%s) > 0 {\n\t\tp.%s = make([]v1.%s, 0, len(%s))\n\t\tfor _, x := range %s {\n\t\t\tp.%s = append(p.%s, v1.%s(x))\n\t\t}\n\t}\n",
			src, field.GoName, mt.biz, src, src, field.GoName, field.GoName, mt.biz)
	case mt.key != nil && mt.elem != nil && mt.elem.kind == kindMessage:
		return "", fmt.Sprintf(
			"\tif len(%s) > 0 {\n\t\tp.%s = make(map[%s]*v1.%s, len(%s))\n\t\tfor k, x := range %s {\n\t\t\tp.%s[k] = to%sPB(x)\n\t\t}\n\t}\n",
			src, field.GoName, mt.key.biz, mt.elem.biz, src, src, field.GoName, mt.elem.biz)
	case mt.key != nil && mt.elem != nil && mt.elem.kind == kindEnum:
		return "", fmt.Sprintf(
			"\tif len(%s) > 0 {\n\t\tp.%s = make(map[%s]v1.%s, len(%s))\n\t\tfor k, x := range %s {\n\t\t\tp.%s[k] = v1.%s(x)\n\t\t}\n\t}\n",
			src, field.GoName, mt.key.biz, mt.elem.biz, src, src, field.GoName, mt.elem.biz)
	case mt.kind == kindTime:
		return fmt.Sprintf("timestamppb.New(%s)", src), ""
	case mt.kind == kindDuration:
		return fmt.Sprintf("durationpb.New(%s)", src), ""
	case mt.kind == kindStruct:
		return "", fmt.Sprintf(
			"\tif len(%s) > 0 {\n\t\tvar st *structpb.Struct\n\t\t_ = json.Unmarshal(%s, &st)\n\t\tp.%s = st\n\t}\n",
			src, src, field.GoName)
	case mt.kind == kindValue:
		return "", fmt.Sprintf(
			"\tif len(%s) > 0 {\n\t\tvar value any\n\t\tif json.Unmarshal(%s, &value) == nil {\n\t\t\tp.%s, _ = structpb.NewValue(value)\n\t\t}\n\t}\n",
			src, src, field.GoName)
	case mt.kind == kindWrapper:
		return fmt.Sprintf("wrapperspb.%s(%s)", mt.wrapper, src), ""
	case mt.kind == kindMessage && !mt.slice:
		return fmt.Sprintf("to%sPB(%s)", mt.biz, src), ""
	case mt.kind == kindEnum && !mt.slice:
		return fmt.Sprintf("v1.%s(%s)", mt.biz, src), ""
	case mt.kind == kindEmpty:
		return "", ""
	default:
		return src, ""
	}
}
