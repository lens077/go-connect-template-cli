# co

从 [`go-connect-template`](https://github.com/lens077/go-connect-template) 生成 ConnectRPC 微服务骨架的脚手架。

## 它是怎么工作的

模板本身是一份**所有能力都打开、能直接 `go build ./...` 通过**的参考实现。
`co` 不改写模板,只做减法:

1. `git clone` 模板(浅克隆,带本地缓存)
2. 按选择删掉未启用 feature 的文件、`+co:` 标记行、`go.mod` 里的直接依赖
3. 生成一套以资源名命名的资源代码(proto + SQL + biz/data/service)
4. 把新 provider 插进各 `fx.Module` 的锚点
5. 跑 `sqlc generate` / `buf generate` / `go mod tidy`

减法比改写可靠:模板编译得过,裁剪后的结果就编译得过。模板会被它自己的 CI 编译,
所以「生成的代码引用了不存在的 conf 字段」这类问题在源头就不成立。

模板与 CLI 之间唯一的契约是模板仓库里的 `.co/manifest.yaml`。
`co` 里不写死任何文件路径 —— 模板挪一个文件,改 manifest 即可,不必跟着发一版 CLI。

### `+co:` 标记

标记是各语言里合法的注释,所以模板照常编译。三种形式:

```go
var Module = fx.Provide(
    NewData,
    NewRedisClient,   // +co:redis
    NewSearchCatalog, // +co:elasticsearch|meilisearch
    // +co:anchor data-providers
)

// +co:begin minio
func NewMinioClient(...) { ... }
// +co:end
```

- **行标记** `// +co:<feature-expression>` —— feature 表达式成立时只摘掉标记本身，
  否则整行删掉。逗号是「与」，竖线是「或」；例如 `elasticsearch|meilisearch` 表示任一项启用。
  标记必须是行尾最后一个 token，前面的说明文字会保留。
- **块标记** `// +co:begin <feature-expression>` … `// +co:end` —— 可嵌套，`begin`/`end` 两行都会消失。
- **锚点** `// +co:anchor <name>` —— 插入点,裁剪后**保留**,留给后续 `co resource add`。

YAML（含 `.yaml.example` / `.yml.example`）/ Makefile / Dockerfile / `.gitignore` 用 `#`，SQL 用 `--`，语义完全相同。
`.proto` 刻意不参与裁剪:protoc 会把注释搬进生成的 Go 文件,标记的配对关系在那儿会断掉。

## 安装

```shell
go install github.com/lens077/co-cli@latest
```

版本号可以用 ldflags 注入,不注入时退回 `debug.ReadBuildInfo()`:

```shell
go build -ldflags "-X github.com/lens077/co-cli/internal/cli.Version=v1.2.3" .
```

## 命令

| 命令 | 作用 |
|---|---|
| `co new <name>` | 生成一个新服务 |
| `co resource add <name>` | 在已有服务里再加一套资源 |
| `co proto add <path>` | 按约定布局新建一个 `.proto` |
| `co proto gen <path> -t <dir>` | 按已有 proto 生成 service/biz/data 三层示例 |
| `co proto server <path>` | 由已有 service 生成 Handler 骨架 |
| `co doctor` | 检查 git / buf / sqlc / protoc 插件 |
| `co version` | 打印版本 |

### `co new`

```shell
# 交互式:未指定的选项会弹表单
co new cart

# 全命令行
co new cart --module github.com/acme/shop --yes

# 先看清楚会发生什么,再真跑
co new cart --cache none --search none --dry-run

# monorepo:服务落在 backend/services/cart,proto 抽到 backend/api/cart
co new cart --layout monorepo --dir ~/src/ecommerce --module github.com/acme/ecommerce/backend
```

可选能力按组暴露成 flag,每组都可以传 `none`:

| flag | 可选值 |
|---|---|
| `--database` | `postgres`(必选一个) |
| `--cache` | `redis` |
| `--search` | `elasticsearch`、`meilisearch`（二选一） |
| `--iam` | `casdoor` |
| `--store` | `minio` |
| `--discovery` | `consul` |
| `--config-source` | `config-file`、`config-configcenter`(可逗号分隔多选) |

> mysql / sqlite 暂未提供。补充方式见模板 `.co/manifest.yaml` 里 `groups.database` 的注释。

检索 adapter 在**生成期**选择，不是运行时开关。`--search elasticsearch` 会删除 Meilisearch 的
adapter 文件、本地 compose 和 Go 依赖；`--search meilisearch` 反向删除 Elasticsearch 的对应内容。
生成物只保留一个实现，但业务仓储统一依赖项目自有的 `SearchCatalog` interface，不接触厂商 SDK 类型。

其它常用 flag:

- `--keep-example` 保留模板自带的示例资源(默认整套删掉)
- `--no-resource` 只出骨架,不生成资源代码
- `--template-dir <path>` 用本地模板目录,完全跳过 clone —— 开发模板本身时用它
- `--template-ref <branch|tag>` 钉住模板版本
- `--no-cache` 忽略本地缓存重新 clone

### `co resource add`

在一个已经生成好的服务里再加一套资源。服务里没有 manifest(`.co/` 是模板的输入不是产物),
所以「这个服务启用了哪些 feature」由 `co` 从文件与 `go.mod` 反推;判断不准时用 `--feature` 覆盖。

```shell
cd cart
co resource add order
buf generate            # 独立仓库
# make api && make conf # monorepo:buf 必须在仓库根跑
go build ./...
```

生成的 Goose migration 落在 `internal/data/migrations/000NN_<表名>.sql`，序号接着服务里已有的
migration 往下排（`00001_carts.sql` → `00002_orders.sql`）。sqlc 按文件名排序读整个目录，
序号同时决定迁移与建表顺序。新表引用老表时，排在前面会让外键建不起来。不带前缀的旧文件
不参与计数。`queries/` 不加前缀：sqlc 读查询没有顺序语义。

### `co proto`

`proto add` 按 `<dir>/<name>/<version>/<name>.proto` 的布局新建文件,
`package` 与 `go_package` 由路径和最近的 `go.mod` 推出。

`proto gen` 解析已有 service 与同文件 message,在 `-t` 指定的服务目录写出三层示例:

- `internal/service/<name>.go` — Connect handler,把 proto 译成 biz 再调 UseCase
- `internal/biz/<name>.go` — 领域结构体、Repo 接口、UseCase(方法与 rpc 一一对应)
- `internal/data/<name>.go` — Repo 占位实现,返回 `not implemented`,留给 sqlc

形状对齐 `ecommerce/backend/services/cart`:service 只做翻译,biz 不认 protobuf。
流式 rpc 只在 service 层生成 `Unimplemented`,不进 biz/data。普通 `oneof`、导入的业务
message 与尚未映射的 WKT 会快速失败,不会生成一套看似完成但编译不过的代码。
默认把 `NewXxx` 插进各 `fx.Module` 的 `+co:anchor`(与 `co new` 产物一致);
目标目录没有锚点时跳过接线并警告,三个文件照样写。

`proto server` 只生成 Handler 骨架,每个方法返回 `CodeUnimplemented`
(不是 `panic` —— panic 会带走整个服务进程)。一元 / 客户端流 / 服务端流 / 双向流
四种签名都会正确生成。

```shell
co proto add api/invoice/v1/invoice.proto
buf generate --path api/invoice
co proto gen api/invoice/v1/invoice.proto -t .
# 或只出 handler:
co proto server api/invoice/v1/invoice.proto
```

monorepo 下 `-t` 指向服务目录:

```shell
co proto gen api/merchant/v1/merchant.proto -t services/merchant/
```

## 布局

| | `standalone` | `monorepo` |
|---|---|---|
| 服务目录 | 目标目录本身 | `backend/services/<name>/` |
| proto | `api/<name>/v1/` | `backend/api/<name>/v1/` |
| `go.mod` | 生成 | 不生成,用根仓库的 |
| buf | `co` 代跑 | 需自己在仓库根 `make api && make conf` |
| 配置数据源 | 按 `--config-source` | 同左,且强制带上 `config-configcenter` |
| `internal/` 的导入前缀 | `<Module>` | `<Module>/services/<name>` |
| `api/` 的导入前缀 | `<Module>/api` | `<Module>/api`(**不是** `<ServiceModule>/api`) |

最后一行是 monorepo 唯一容易搞错的地方:proto 被搬出了服务目录,导入前缀就得跟着仓库根走。
资源模板里的 `go_package` 也是这么拼的(`{{.Module}}/{{.APIDir}}`),两边必须一致 ——
不一致的症状是模板自带的 proto(`--keep-example` 下的 `api/search`)导入
`<ServiceModule>/api/...`,而文件实际在 `<Module>/api/...`,`go build` 报
`no required module provides package`。`TestGenerateMonorepo` 会真编译一次来守住它。

`config-configcenter` 源经 `github.com/lens077/control-tower` SDK 连接配置中心。
monorepo 布局把该 feature 强制打开（与用户的 `--config-source` 取并集）。独立仓库默认也带上，
本地仍可用 `CONFIG_SOURCE=file`；生产挂 `CONFIG_SOURCE_FILE` 指向 `type: config_center` 的 selector。

契约 proto 由 SDK 携带,生成物不再复制 `api/config/`。

## 生成出来的服务长什么样

```
cart/
├── api/cart/v1/            # proto 与 buf 生成物
├── cmd/server/             # 入口
├── configs/                # dev.yml / pre.yml
├── internal/
│   ├── biz/                # 领域逻辑
│   ├── conf/               # 配置结构(由 conf.proto 生成)
│   ├── data/               # 仓储实现 + migrations/ + queries/ + models/(sqlc)
│   ├── pkg/                # kit 的 config / log / otel / registry adapter，以及 money / minio
│   ├── server/             # HTTP + Connect handler 注册
│   └── service/            # Connect handler
├── constants/
├── deploy/
├── Makefile
└── go.mod
```

`make dev` 用 `CONFIG_SOURCE=file` 起服务,不依赖 Consul 等外部组件。

## 目录结构

```
co-cli/
├── main.go                 # 只调 cli.Execute()
└── internal/
    ├── cli/                # cobra 命令,只做参数绑定,无业务逻辑
    ├── ui/                 # huh 表单 + lipgloss 输出
    ├── manifest/           # 解析 .co/manifest.yaml、feature 选择与校验
    ├── protogen/           # proto 解析(go-protoparser)、骨架、handler、三层示例生成
    ├── resource/           # 命名推导 + 从 .co/scaffold/resource 渲染资源
    └── scaffold/           # 引擎
        ├── fetch.go        # go-git 浅克隆 + 缓存
        ├── plan.go         # feature 集合 -> 有序操作清单(可 --dry-run 打印)
        ├── apply.go        # 执行:拷贝 / 删除 / overlay / 裁剪 / 锚点插入 / hook
        ├── addresource.go  # co resource add 的清单与执行
        ├── marker.go       # +co: 行与块的裁剪
        ├── gomod.go        # x/mod/modfile 改 module 与 DropRequire
        ├── detect.go       # 从已生成的服务反推 feature
        └── rewrite.go      # 模块路径替换
```

「算清单」与「执行清单」是分开的:生成结果不对时,先看 `--dry-run` 就能判断
是选型算错了还是执行环节出了问题。

## 开发

```shell
# 单元测试:不联网、不落盘
go test -short ./...

# 完整测试：生成矩阵会真的生成项目并跑 go build ./...
# go test 会把包目录作为工作目录，因此这里传绝对路径。
CO_TEMPLATE_DIR="$(cd ../go-connect-template && pwd)" go test ./...
```

`CO_TEMPLATE_DIR` 不设时默认按并排 checkout 猜(`../../../go-connect-template`),
找不到就跳过相关测试。

两种布局各有一格真生成 + 真编译的测试:

- `TestGenerateMatrix` —— standalone，把 feature 组合矩阵跑一遍；Elasticsearch、Meilisearch 与无检索三种产物都会检查 adapter 隔离并执行 `go build ./...`
- `TestGenerateMonorepo` —— 生成 `cart` + `order` 两个服务,补出仓库根(`go.mod` / `buf.*` /
  `third_party`)后在根上跑 buf 再整体编译。**monorepo 必须真编译**,因为它和 standalone
  唯一实质不同的地方是导入路径,而路径错只有编译才看得出来

改完模板不必先 push:

```shell
co new demo --template-dir ../go-connect-template --module github.com/acme/demo --yes
```

改造记录、已知问题与未做项见 [`TODO.md`](TODO.md)。

## 用到的库

| 用途 | 库 |
|---|---|
| 命令 | `spf13/cobra` |
| 交互表单 | `charmbracelet/huh` |
| 终端输出 | `charmbracelet/lipgloss` |
| clone | `go-git/go-git/v5` |
| `go.mod` | `golang.org/x/mod/modfile` |
| manifest | `gopkg.in/yaml.v3` |
| proto 解析 | `yoheimuta/go-protoparser/v4` |
| 复数推导 | `gertd/go-pluralize` |
| 测试 | `stretchr/testify` |
