package protogen

import (
	"strings"
	"unicode"
)

// wkt 是 google.protobuf 里我们认识、会映射成 Go 原生类型的那些。
// 不在这张表里的 WKT(Any / FieldMask / ListValue …)由 NewLayerSpec 快速拒绝;
// 猜一种转换方式只会生成表面完整、实际丢语义的代码。
var wkt = map[string]mapped{
	"Timestamp":   {biz: "time.Time", kind: kindTime},
	"Duration":    {biz: "time.Duration", kind: kindDuration},
	"Empty":       {biz: "", kind: kindEmpty},
	"Struct":      {biz: "json.RawMessage", kind: kindStruct},
	"Value":       {biz: "json.RawMessage", kind: kindValue},
	"BoolValue":   {biz: "bool", kind: kindWrapper, wrapper: "Bool"},
	"StringValue": {biz: "string", kind: kindWrapper, wrapper: "String"},
	"BytesValue":  {biz: "[]byte", kind: kindWrapper, wrapper: "Bytes"},
	"Int32Value":  {biz: "int32", kind: kindWrapper, wrapper: "Int32"},
	"Int64Value":  {biz: "int64", kind: kindWrapper, wrapper: "Int64"},
	"UInt32Value": {biz: "uint32", kind: kindWrapper, wrapper: "UInt32"},
	"UInt64Value": {biz: "uint64", kind: kindWrapper, wrapper: "UInt64"},
	"FloatValue":  {biz: "float32", kind: kindWrapper, wrapper: "Float"},
	"DoubleValue": {biz: "float64", kind: kindWrapper, wrapper: "Double"},
}

var scalars = map[string]string{
	"double":   "float64",
	"float":    "float32",
	"int32":    "int32",
	"int64":    "int64",
	"uint32":   "uint32",
	"uint64":   "uint64",
	"sint32":   "int32",
	"sint64":   "int64",
	"fixed32":  "uint32",
	"fixed64":  "uint64",
	"sfixed32": "int32",
	"sfixed64": "int64",
	"bool":     "bool",
	"string":   "string",
	"bytes":    "[]byte",
}

type kind int

const (
	kindScalar kind = iota
	kindTime
	kindDuration
	kindStruct
	kindValue
	kindEnum
	kindMessage
	kindEmpty
	kindWrapper
)

// mapped 是一个 proto 类型在 biz 层的样子。
type mapped struct {
	biz     string
	kind    kind
	wrapper string // wrapperspb 构造函数名,如 Bool
	elem    *mapped
	key     *mapped
	slice   bool
	pointer bool
}

func (m mapped) bizType() string {
	if m.elem != nil {
		t := m.elem.bizType()
		if m.key != nil {
			return "map[" + m.key.bizType() + "]" + t
		}
		if m.slice {
			return "[]" + t
		}
		if m.pointer {
			return "*" + t
		}
		return t
	}
	t := m.biz
	if m.pointer {
		t = "*" + t
	}
	if m.slice {
		t = "[]" + t
	}
	return t
}

func mapField(f File, field Field) mapped {
	if field.IsMap {
		key := mapNamed(f, field.MapKey)
		val := mapNamed(f, field.MapVal)
		if val.kind == kindMessage {
			val.pointer = true
		}
		return mapped{kind: kindScalar, key: &key, elem: &val}
	}
	m := mapNamed(f, field.Type)
	if field.Repeated {
		if m.kind == kindMessage {
			m.pointer = true
		}
		m.slice = true
		return m
	}
	if field.Optional && m.kind == kindScalar {
		// protoc keeps optional bytes as []byte; other optional scalars are pointers.
		m.pointer = m.biz != "[]byte"
	}
	if m.kind == kindMessage {
		m.pointer = true
	}
	return m
}

func mapNamed(f File, raw string) mapped {
	name := baseType(raw)
	if goType, ok := scalars[name]; ok && !strings.Contains(raw, ".") {
		return mapped{biz: goType, kind: kindScalar}
	}
	// 标量写成 .int32 这种不会出现;带点的一律当 message/enum/WKT
	if wktName, ok := wktName(raw); ok {
		if m, found := wkt[wktName]; found {
			return m
		}
	}
	if _, ok := f.Enum(name); ok {
		return mapped{biz: name, kind: kindEnum}
	}
	if _, ok := f.Message(name); ok {
		return mapped{biz: name, kind: kindMessage}
	}
	// 本文件里没有的类型:当成同名 message,转换函数会引用它,
	// 用户要么补定义要么自己改。猜成 string 会让字段对不上。
	if goType, ok := scalars[name]; ok {
		return mapped{biz: goType, kind: kindScalar}
	}
	return mapped{biz: name, kind: kindMessage}
}

func wktName(raw string) (string, bool) {
	t := strings.TrimPrefix(raw, ".")
	const prefix = "google.protobuf."
	if strings.HasPrefix(t, prefix) {
		return strings.TrimPrefix(t, prefix), true
	}
	return "", false
}

func isWKT(raw string) bool {
	_, ok := wktName(raw)
	return ok
}

func isEmpty(raw, name string) bool {
	wktN, ok := wktName(raw)
	return ok && wktN == "Empty"
}

// exportedName 把 proto 字段名变成导出的 Go 名:merchant_id -> MerchantId。
// 与 protoc-gen-go 的 GoCamelCase 在「按 _ 切段、首字母大写、不做缩写」
// 这一点上一致,生成的 p.GetXxx() 才能对得上。
func exportedName(s string) string {
	return pascalIdent(s)
}

func pascalIdent(s string) string {
	var b strings.Builder
	for _, part := range strings.Split(s, "_") {
		if part == "" {
			continue
		}
		b.WriteString(strings.ToUpper(part[:1]))
		b.WriteString(part[1:])
	}
	return b.String()
}

func camelIdent(s string) string {
	p := pascalIdent(s)
	if p == "" {
		return p
	}
	return strings.ToLower(p[:1]) + p[1:]
}

// enumValueGoName 把 CART_STATUS_ACTIVE + CartStatus 收成 CartStatusActive。
// 先剥掉枚举名的 SCREAMING_SNAKE 前缀,剩下的段转 Pascal 再拼回去;
// 剥不掉就整串转 Pascal,保证生成的一定是合法标识符。
func enumValueGoName(enumName, ident string) string {
	prefix := screamingSnake(enumName) + "_"
	rest := ident
	if strings.HasPrefix(ident, prefix) {
		rest = strings.TrimPrefix(ident, prefix)
	}
	return enumName + pascalIdent(strings.ToLower(rest))
}

func screamingSnake(s string) string {
	var b strings.Builder
	for i, r := range s {
		if i > 0 && unicode.IsUpper(r) {
			b.WriteByte('_')
		}
		b.WriteRune(unicode.ToUpper(r))
	}
	return b.String()
}

// domainName 从 service 名去掉 Service 后缀:MerchantService -> Merchant。
// 没有这个后缀就原样用,不强行截断,截错了生成的类型名会对不上 handler 接口。
func domainName(svc string) string {
	const suffix = "Service"
	if strings.HasSuffix(svc, suffix) && len(svc) > len(suffix) {
		return strings.TrimSuffix(svc, suffix)
	}
	return svc
}

func fileStem(path string) string {
	base := path
	if i := strings.LastIndexAny(path, `/\`); i >= 0 {
		base = path[i+1:]
	}
	return strings.TrimSuffix(base, ".proto")
}
