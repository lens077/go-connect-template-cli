# 改造记录与待办

本轮目标:把 `co-cli` 从「正则改写克隆下来的源码」重写成「按 manifest 做结构化裁剪」的声明式引擎。
模板侧的改动见 `../go-connect-template/TODO.md`。

核心思路是**只做减法**:模板本体保留所有 feature 源码并能编译,CLI 只删文件、
删标记行、删 `go.mod` require,不改写代码。删除的正确性由模板自己的 CI 兜住 ——
模板编译得过,裁剪后剩下的部分就编译得过。

---

## 已完成(按执行顺序)

### 1. 清掉旧实现

- [x] 删除 `pkg/template/template.go`(单文件 1769 行)。其中 `createEnvPackage` / `createTestFiles` /
      `createConstantsPackage` 把几百行 Go 源码以字符串字面量内嵌在 CLI 里,与模板里同名文件重复且必然漂移
- [x] 删除重复的 `cmd/co/main.go` 与 `cmd/co/commands/`(`main.go` 与它完全重复)
- [x] module 名改成 `github.com/lens077/co-cli`(原来叫 `co-cli`,无法 `go install`)

修掉的旧 bug:

- [x] `--database mysql|sqlite` 生成的代码引用 `conf.Data.Database.Mysql`,但 `conf.proto` 里根本没有这个
      message —— **生成物编译不过**。改为只做减法后这类问题在源头就不成立
- [x] `cmd/co/commands/proto.go` 的 `init()` 从未 `rootCmd.AddCommand(protoCmd)`,`co proto` 实际不可用
- [x] `updateDataLayer` 用正则把 `pgxpool.Pool` 替换成 `*sql.DB` 再追加一段 MySQL 字符串 —— 模板一改格式就静默失效

### 2. 新架构

```
internal/
├── cli/        cobra 命令,只做参数绑定,无业务逻辑
├── ui/         huh 表单 + lipgloss 输出
├── manifest/   解析 .co/manifest.yaml、feature 选择与校验
├── protogen/   proto 解析(go-protoparser)、骨架与 handler 生成
├── resource/   命名推导 + 从 .co/scaffold/resource 渲染资源
└── scaffold/   引擎:fetch / plan / apply / marker / gomod / detect / rewrite
```

- [x] 「算清单」(`plan.go`)与「执行清单」(`apply.go`)分开:生成结果不对时,先看 `--dry-run`
      就能判断是选型算错了还是执行环节出了问题
- [x] 换库:`promptui`(已停更)→ `charmbracelet/huh`;`exec.Command("git")` → `go-git/v5` 浅克隆;
      字符串 `strings.Replace` 改 go.mod → `x/mod/modfile`;手写正则解析 proto → `go-protoparser/v4`
- [x] 弃用的 `strings.Title` 一并清掉

### 3. 裁剪引擎

- [x] `marker.go`:行标记 `// +co:a,b`(逗号是「与」,必须是行尾最后一个 token)、块标记
      `// +co:begin x` … `// +co:end`(可嵌套)、锚点 `// +co:anchor name`(裁剪后保留)
- [x] 注释前缀按扩展名派发:`.go` → `//`,`.yaml/.yml/.yaml.example/.yml.example/.toml` → `#`,`.sql` → `--`,
      `Makefile`/`Dockerfile`/`.gitignore` 等 → `#`;其余(含 `.proto`、`.md`、`.ts`)返回空串 = 整个文件跳过
- [x] `gomod.go`:用 `modfile` 精确 `DropRequire`,而不是文本替换 —— 后者会把 require 块里
      恰好同前缀的依赖一起改掉。生成后 `go mod tidy` 仍可能因共享 kit 的模块图把同一模块补成
      `// indirect`；门禁要求的是未选 feature 不再直接导入或直接 require
- [x] `apply.go` 的步骤顺序有强耦合,注释里写清楚了:删除 → 改名 → 裁剪 → 锚点插入 → 搬 proto → hook
- [x] `deleteAll` 删完文件后向上收空目录 —— 否则 `api/search/v1/` 会剩个空壳被原样搬到
      `backend/api/search`,看着像漏删了示例资源
- [x] `detect.go`:`co resource add` 时从已生成的服务反推启用了哪些 feature(服务里没有 manifest,
      因为 `.co/` 是模板的输入不是产物)

### 4. 布局

- [x] `standalone` / `monorepo` 两种布局,差异全部由 manifest 的 `layouts.*` 描述
- [x] `layouts.<name>.features`:布局强制启用的 feature,与用户选择取并集。monorepo 用它强制打开
      `config-configcenter` —— 否则 config adapter 里的 `controlsource.NewKitSource` 接线会被裁掉，
      而 monorepo 的生产路径正是 `CONFIG_SOURCE_FILE`
- [x] `layouts.<name>.shared_proto`:声明整个仓库共用的 proto 子树。搬 proto 时目标已存在就保留
      仓库里那份(它可能比模板新),而不是报冲突 —— 同一个 monorepo 里生成第二个服务时必然如此
- [x] overlay 目录结构即目标路径,渲染后去掉 `.tmpl` 后缀写进 `service_dir`

### 5. 资源生成

- [x] 改为「删除示例资源 + 按模板生成一套新的」,而不是重命名。`search` 是检索目录示例,
      不能可靠改名成任意 CRUD 资源；底层实现现已隔离在 `SearchCatalog` adapter 后面
- [x] `--keep-example` 可保留示例资源
- [x] 资源模板里 proto 的 `go_package` 统一拼 `{{.Module}}/{{.APIDir}}`

### 6. 端到端验证(生成的项目真的跑起来)

**单体** —— `postgres` + `redis` 容器:`go build` / `go vet` 干净;`/healthz` 返回
`{"healthy":true,"details":{"postgres":"ok","redis":"ok"}}`;CRUD 全套走通;
protovalidate 对空字段返回 `invalid_argument` 带 `buf.validate.Violations`;
不存在的 id 返回 `not_found` → HTTP 404;DB 里确认是软删除。

**微服务** —— `cart` + `order` 两个服务:第二个服务生成时打印
`→ keeping existing backend/api/config (shared)`,共用 proto 没被覆盖也没报冲突;
`go mod tidy` / `go build` / `go vet` 全部 exit 0;cart 起在 :30100,`/healthz` 绿。

### 7. E2E 查出来的真实 bug:monorepo 导入路径

- [x] 症状:monorepo 下 `go build ./...` 报
      `no required module provides package <Module>/services/cart/api/config/v1`
- [x] 根因:`applyRenames` 把模板 module 一律替换成 `ServiceModule`,但 `api/` 那棵树会被
      `relocateProto` 搬到仓库根,导入前缀必须跟着仓库根走(`<Module>/api`)。服务自己生成的
      proto 没事(资源模板直接拼 `{{.Module}}/{{.APIDir}}`),但模板自带的 `--keep-example`
      下的 `api/search` 就对不上了
- [x] 修法(`internal/scaffold/apply.go`):改成**有序**两条替换规则,第一条先吃掉 `api/` 前缀

      ```go
      reps := []Replacement{
          {Old: p.Manifest.Module + "/api", New: p.Opts.Module + "/api"},
          {Old: p.Manifest.Module, New: p.ServiceModule},
      }
      ```

      `ReplaceInTree` 是顺序应用的,所以第一条命中后第二条碰不到它们。
      standalone 下 `Module == ServiceModule`,第一条等价于第二条,无副作用

### 8. 补上让这个 bug 漏过去的测试缺口

- [x] 原因:`TestGenerateMatrix` 只编译 standalone 输出(那里 `Module == ServiceModule`,恰好命中不了),
      `TestGenerateMonorepo` 只 grep 字符串和路径、从不编译
- [x] 新增 `buildMonorepo` helper:把生成结果补成一个能编译的仓库(从模板取 `go.mod`/`buf.*`/`third_party`
      到仓库根、改 module 行、在根上按 `--path` 跑 buf)再 `go build ./...`
- [x] 反向验证过:把上面的修复撤掉,这个测试以完全相同的报错挂掉

### 9. schema 文件名补 `000NN_` 前缀

- [x] 问题:生成器写出的是 `internal/data/schema/demos.sql`,而文件自己的头注释写着
      「文件名的 `000NN_` 前缀决定读取顺序」,模板里的示例也确实叫 `00001_products.sql` —— 生成物不带前缀
- [x] 影响:sqlc 按文件名排序读整个 schema 目录,序号就是建表顺序。不带前缀的文件在字典序里
      排到所有 `000NN_` 之后,后续资源引用它就建不起外键
- [x] 新增 `internal/resource/schema.go`:`NextSchemaSeq(dir)` / `NextSchemaSeqFrom(names)`,
      取已有最大值 +1,补零到 4 位(补零是为了让字典序等于数值序,`9` 与 `10` 不补零会排反)
- [x] 无前缀的旧文件不参与计数:它在字典序里本来就排在所有 `000NN_` 之后,没有可依赖的位置,
      算进来只会凭空跳号
- [x] 两个调用点算法不同:
      - `co resource add` 扫目标服务已有的 `schema/`,接着往下排
      - `co new` 扫模板目录但要**减掉 `p.Deletes`** —— 不带 `--keep-example` 时模板那份
        `00001_products.sql` 会被删掉,新资源就该占 `0001`;照模板原样数会跳到 `0002`,
        序号里空一个洞,看着像丢了一次迁移
- [x] `queries/` 不加前缀:sqlc 读 queries 没有顺序语义,只有建表有

### 10. 测试

- [x] `manifest`:解析、未知字段拒绝、各项校验、feature 解析(needs / always / 互斥 / required)、
      依赖与文件的删除计算
- [x] `scaffold`:plan 矩阵(不落盘,永远跑)、生成矩阵(真生成 + `go build`,`-short` 跳过)、
      monorepo 单独一格(真编译)、标记裁剪残留检查、`resource add`
- [x] `resource`:命名推导、schema 序号推导
- [x] `protogen`:proto 方法提取(一元 / 客户端流 / 服务端流 / 双向流四种签名)

### 11. `co proto gen`

- [x] 已有 proto 时不再只能出 Unimplemented handler:`co proto gen <proto> -t <service-dir>`
      写出 service/biz/data 三层示例,形状对齐 cart(service 只翻译、biz 不认 protobuf、
      data 接 `*Data` + logger,方法体留 `not implemented` 给 sqlc)
- [x] 解析补上 message / field / enum / map / oneof / proto3 optional,以及
      Timestamp / Duration / Struct / wrappers / Empty 的类型映射
- [x] 默认往 `+co:anchor` 插 NewXxx;没有锚点时跳过并警告,不让整条命令失败
- [x] 流式 rpc 只在 service 层生成 Unimplemented,不进 biz/data
- [x] 普通 oneof、导入的业务 message、未映射 WKT 快速失败;写文件前先检查三层冲突,
      避免留下半套生成物
- [x] 模板无需改动:锚点与三层目录是 `co new` 产物的既有契约

```
gofmt ✓   go build ✓   go vet ✓   go test ./... ✓(完整,非 -short)
```

---

## 待办

- [ ] **mysql / sqlite 支持**:按要求延后。加进来要同时补模板侧的驱动文件与 `manifest.yaml`;
      `.co/scaffold/resource/data.go.tmpl` 需按驱动分三份(pgx / database-sql 的 `DBTX`、
      null 类型、`pgtype` 差异较大)。这是剩下工作量最大的一块
- [ ] **`co doctor` 没有覆盖 `protoc-gen-connect-go` 之外的插件版本校验**,只查了存在性
- [ ] **monorepo 下 buf 仍需用户自己在仓库根跑** `make api && make conf`。hook 不能代跑是因为
      `--path` 要写成相对仓库根的路径,在生成的服务目录里跑不了
- [ ] **`CO_TEMPLATE_DIR` 不设时按并排 checkout 猜路径**,猜不到就跳过测试。CI 里要显式设置

### 12. 仓库归并

- [x] 确认旧路径 `co-cli` 与本仓指向同一远端、同一提交且跟踪文件一致
- [x] 删除本机重复 checkout `/Users/sumery/lens077/co-cli`；本仓是 CLI 唯一维护入口
- [x] README 同步 Goose migration 路径与 `control-tower` SDK，避免文档继续指向旧标准

### 13. 检索 adapter 生成期互斥

- [x] manifest v2 新增 `example.needs_any`，搜索示例可依赖任一互斥 adapter
- [x] `+co:` 表达式新增竖线「或」语义，同时保留逗号「与」语义
- [x] `groups.search` 同时提供 `elasticsearch` 与 `meilisearch`，解析层拒绝两者同时选择
- [x] feature 反推只使用独占文件或依赖，避免共享 `search_catalog.go` 让两个 adapter 同时被误判为启用
- [x] 生成矩阵覆盖两个 adapter，并检查未选择的源码、compose 与 Go 依赖没有进入产物

### 14. go-connect-kit 生成契约

- [x] 删除对模板 `source_sdk.go` 和基础设施实现副本的断言；生成物必须依赖 `go-connect-kit`
- [x] monorepo 断言 Config Center 经 `controlsource.NewKitSource` 接入 `kitconfig.FromEnvironment`
- [x] 生成矩阵先执行 `go mod tidy` 再编译，依赖版本不存在时必须失败，不能只记录日志后放行
- [x] 聚焦生成验收检查无本地 `env` / `meta` / `dbutil` / `healthcheck`、kit semver、无 `replace`、Docker ldflags，并真实执行 `go build ./...`
- [x] 发布前可显式设置 `CO_USE_LOCAL_MODULES=1`，仅在测试临时生成物中接入并排 checkout；默认路径仍验证已发布版本
- [x] kit `v0.3.0` 与 control-tower `v0.1.4` 已发布；不带本地开关重跑完整生成矩阵
