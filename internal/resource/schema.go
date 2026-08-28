package resource

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// SchemaDir 是 migration 文件的落点,相对服务目录。
// 它由模板的 sqlc.yaml 里的 schema: 锁死,不是可配置项。
const SchemaDir = "internal/data/migrations"

// seqPrefix 匹配 00001_products.sql 这类带序号前缀的文件名。
var seqPrefix = regexp.MustCompile(`^(\d+)_`)

// NextSchemaSeq 返回 dir 下 migration 文件的下一个序号前缀,如 "00002"。
// 目录不存在、为空、或里面一个带前缀的文件都没有时返回 "00001"。
func NextSchemaSeq(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return NextSchemaSeqFrom(nil)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return NextSchemaSeqFrom(names)
}

// NextSchemaSeqFrom 从一组文件名算下一个序号前缀。
//
// sqlc 按文件名排序依次读 migrations 目录,所以序号就是建表顺序 —— 后建的表
// 若引用先建的表,文件名的先后决定外键能不能建起来。
//
// 不带前缀的文件(旧版生成的 products.sql)不参与计数:它们在字典序里排在
// 所有 000NN_ 之后,本来就没有可依赖的位置,把它们算进来只会凭空跳号。
func NextSchemaSeqFrom(names []string) string {
	max := 0
	for _, n := range names {
		if !strings.HasSuffix(n, ".sql") {
			continue
		}
		m := seqPrefix.FindStringSubmatch(n)
		if m == nil {
			continue
		}
		if v, err := strconv.Atoi(m[1]); err == nil && v > max {
			max = v
		}
	}
	// 宽度固定 4 位:补零是为了让字典序等于数值序,9 与 10 不补零会排反
	return fmt.Sprintf("%05d", max+1)
}
