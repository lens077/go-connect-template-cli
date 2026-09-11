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
- [x] 聚焦生成验收检查无本地 `env` / `meta` / `dbutil` / `healthcheck`、kit semver、无 `replace`、Docker ldflags，并真实执行 `go build ./...` 与 `go test -count=1 ./...`
- [x] 无 IAM 生成验收检查配置文件不再保留 `auth`，避免可选能力裁剪后只编译绿、生成物测试红
- [x] 发布前可显式设置 `CO_USE_LOCAL_MODULES=1`，仅在测试临时生成物中接入并排 checkout；默认路径仍验证已发布版本
- [x] kit `v0.3.0` 与 control-tower `v0.1.4` 已发布；不带本地开关重跑完整生成矩阵

### 15. CI 与首个发布

- [x] 新增 `.github/workflows/ci.yml`：并排 checkout 模板并显式设置 `CO_TEMPLATE_DIR`，安装 buf/sqlc 后跑完整生成矩阵与 `go vet`
- [x] 打首个发布 tag `v0.1.0`；发布级验收用 `--template-ref v0.1.0` 从远端模板生成双 adapter 项目
- [x] module path 改为 `github.com/lens077/go-connect-template-cli`：仓库归并时只改了远端名，module 仍指向已不存在的 `co-cli`，`go install ...@v0.1.0` 拉不到。改完打 `v0.1.1`，`go install ...@v0.1.1` 作为验收
- [ ] `v0.1.0` 的 module path 已损坏，不可 `go install`；不删 tag（已推送，删了会破坏 proxy 缓存一致性），文档只推荐 `v0.1.1+`

### 16. Markdown 裁剪与生成物测试

- [x] `+co:` 标记支持 Markdown：`.md` 用 `<!-- +co:x -->`，必须以 `-->` 闭合，未闭合不当标记；隔离检查扩到 `.md`
- [x] 生成矩阵每格在 `go build` 后追加 `go test -count=1 ./...`：生成物自带的 fx 依赖图校验和 adapter 契约测试必须真跑。`go build` 看不见 fx 图是否闭合，曾有一次所有生成服务起不来而矩阵全绿
- [x] `assertFeatureTestFiles`：按 manifest 数据驱动核对 `_test.go` 去留——选中 feature 的必须在，未选的必须不在；以后给 adapter 补测试只需在 manifest 登记
- [x] manifest v3：顶层 `exclude` 列表，模板自身元数据（`TODO.md`）无条件进删除清单并压过 `example.keep`。精确路径、无 glob；与 feature files / keep 重叠视为矛盾，加载即报错。`KnownFields` 打开着，新字段对旧 CLI 是错误而非忽略，所以必须走版本号——v1/v2 里出现 `exclude` 也报「version >= 3」
- [x] 矩阵加 `assertExcluded`（生成物里不得有）与 plan 断言（必在 `Deletes` 里），都从 manifest 读，数据驱动
- [x] 模板已升到 manifest v3，CLI 发 `v0.2.0` 后模板 push 并打 `v0.2.0`
- [x] `v0.1.1` 拉 v3 模板报的是 `field exclude not found in type manifest.Manifest`，不是「请升级 co」——严格解码抢在版本校验前面。已发布的 v0.1.1 改不了；main 上 `Load` 先宽松读版本号，超出支持范围直接给「upgrade co」，下个版本起生效

### 17. `co upgrade`

- [x] `co upgrade [dir]`：用同样身份（name/module/layout）+ 反推的 feature 生成参考副本到临时目录，逐文件比对；默认只报告，`--diff` 打 diff，`--write` 写回并要求 git 干净（`--allow-dirty` 跳过）。只增改不删；hook 产物（`go.mod`/`go.sum`/`*.pb.go`/`*.connect.go`/`models/`）不参与；`.go` 两侧先 gofmt 再比
- [x] **根因修复：锚点接线被盖掉**。参考副本是 `NoResource` 的，锚点上方是空的；真实服务那里堆着 `co new` / `resource add` / `proto gen` 插进去的 `NewCartUseCase,` / `mux.Handle(...)`。原实现刚 `co new` 完立刻 `upgrade` 就报 biz/data/server/service 四处假差异，`--write` 把接线抹掉且 `go build` 照样绿（无人引用的构造函数不报错）——服务不再注册 handler，静默失败。单测没抓到是因为 fixture 也用了 `NoResource: true`，与实现盲区完全重合
- [x] 修法 `upgrade_anchor.go`：约定「锚点上方那段属于服务」。按锚点分段，对段做 LCS 对齐，取 current 里紧贴锚点、未对上模板的尾部连续行搬进副本（用 LCS 而非「这一行模板里出现过」，否则 `)` 这类到处都有的行会把 `--keep-example` 的多行块拦腰截断）；只取尾部连续段，段中间的未匹配行是模板改了，搬过去会复制旧模板行。分不清时选择保留（结果多一行，diff 可见）而不是丢弃。锚点行本身缩进以服务为准：gofmt 给「只剩一条注释的 `fx.Provide(`」少排一级，之后再 gofmt 也不改回来。模板删掉锚点 → `Warnings`，CLI 先于差异列表打印
- [x] 比对、`--diff`、`--write` 三处用同一份合并后的内容（`Upgrade.next`），不再回头读副本——否则「看到的」和「写进去的」不是一份
- [x] 第二个同类根因：monorepo overlay `Makefile` 由 `DockerRegistry/DockerNamespace/ConsulAddr` 渲染，`PlanUpgrade` 没传就渲染成空串，永远 modified 且 `--write` 会把 `REGISTER`/`CONSUL_ADDR` 抹空。抽出 `renderFlags`（含 `--service-name`）由 `new`/`upgrade` 共用同一份默认值；生成时传过非默认值的 upgrade 要再传一遍（产物不记录），不传则差异可见
- [x] 测试：fixture 改为真生成资源（含接线）；刚生成零差异（standalone + monorepo）；模板演进 + 手写 provider 同时保留并收敛；多行块不截断；仅尾部段；锚点缩进；锚点缺失告警；渲染参数不一致时 Makefile 可见。e2e：`co new` → `upgrade` 零差异 → 改模板 → `--write` → 接线在、`go build`/`go test` 绿 → 再 `upgrade` 零差异
- [x] `--keep-example` 真实 e2e：顺带发现 `+co:example` 标记的接线在 `--keep-example` 下也被裁掉（`example` 从未进 FeatureSet，文件留了、接线没了、`go build` 照样绿）。修在 `NewPlan`：`set[manifest.ExampleMarker] = true`。upgrade 侧 `detectKeepExample` 反推后参考副本同样保留示例
- [ ] 模板改了紧贴锚点的那一行（例如 data.go 锚点上方最后一个 provider 改名）时，结果里新旧两行都在（fx 启动时报 duplicate provide，不是编译错）。这是「宁多不少」的刻意取舍，靠 `--diff` 看见；要根治得记录生成时的模板 ref 做三方合并，与「产物不存状态」的原则冲突，暂不做
- [ ] monorepo 下 `PlanUpgrade` 要求仓库根有 `go.mod`（`InspectTarget` 反推 module），`co new` 不生成它；没有时报错提示，不猜

### 18. `co upgrade` 对 legacy 服务的安全边界

真实 ecommerce cart（锚点机制之前生成）上验证：原实现报 49 处差异，其中 biz/data/server/service
四个接线文件是普通 modified，`--write` 一把盖上去接线全没、`go build` 绿。「git 干净」只保证能撤销，
保证不了「文件完全属于模板」。

- [x] **写回分层**：`--write` 只写 added；`--write-modified` 连 modified；`--only <path>` 点名（点名的 modified 视为已同意，点名不存在的路径报错不静默）。`--allow-dirty` 只跳 git 检查，不解锁任何一层
- [x] **blocked**：模板这份带 `+co:anchor` 而服务那份一个都没有（`legacyAnchorFile`）→ 任何策略都不写，`--only` 点名也不写。出路是手工补锚点再重跑；e2e 验证补完锚点后接线全部搬运成功
- [x] **同包耦合**（e2e 才发现）：只写 added 的 `cache_redis.go` 也炸——legacy `data.go`（blocked）里还有 `NewRedisClient`。规则：选中的 Go 文件同包有 blocked → blocked；added 的 Go 文件同包有未选中的 modified → 跳过；modified 同包有未选中的照写（`--only` 的正常用法）。列表里逐文件标原因。之后 legacy cart 默认 `--write` 后 `go build` 绿
- [x] **原子写入**：先落到服务目录下 `.co-upgrade-*/new`（同文件系统，rename 才原子），旧文件挪到 `old/`，逐个 rename；任一步失败按记录回滚，暂存目录清掉。目标是目录时拒绝（不替用户删树）
- [x] 纯插入 hunk 在锚点段任何位置都搬（gofmt 会把 `cartv1connect` import 排到 `searchv1connect` 前面，不再紧贴锚点）；替换 hunk 只在紧贴锚点时保留
- [x] Go 文件里锚点不在 import 块内时不搬 import 行：legacy `data.go` 多出的 `crypto/tls`、`pgx` 被当纯插入搬来，用它们的代码在锚点下方已被模板替换，结果 5 个 "imported and not used"
- [x] `.DS_Store` / `Thumbs.db` / `*.swp` / `*~` 在拷贝与比对时任何层级都跳过（`PathSkipper`）
- [x] manifest v3 `layouts.*.root_packages`：monorepo 下 `constants` 由仓库根提供，删副本 + 导入改写 `<Module>/constants`。只写 `drop` 不够（import 仍指向 `services/<name>/constants`）。ecommerce cart 从 48 → 46 处差异，不再把已清掉的影子副本带回来
- [x] 「下一步」按写入内容变化：写了 `.proto` 提示 `buf generate` / `make api && make conf`，写了 `.sql` 提示 `sqlc generate`
- [x] 发布链闭合：control-tower `v0.1.6`（HEAD 仅升 kit v0.4.3，工作树 WIP 不带）→ CLI `v0.2.0` → template `v0.2.0`（kit v0.4.3 / control-tower v0.1.6 / manifest v3）→ ecommerce 升 control-tower v0.1.6 并补根 `constants`。验收全部用已发布版本：`go install ...@v0.2.0`、`co new --template-ref v0.2.0`（standalone build/vet/test 绿；monorepo 无 constants 副本、import 指向根）、`co upgrade --template-ref v0.2.0` 零差异、drift 后 `--write` 收敛；control-tower/template 按 tag 浅克隆 `GOWORK=off` build/vet/test 绿
- [ ] legacy cart 补锚点 + `--write-modified` 后剩两处真实分歧，工具不该替人决定：~~ecommerce 根 `constants` 缺 `DefaultDBPingTimeout` / `DefaultHealthCheckTimeout`~~（已在 ecommerce 侧补）；`cart.go` 业务代码用旧的 `*LiveRedis`（业务适配，留给 ecommerce）
- [ ] 锚点段之外的用户定制（legacy cart 的自定义 health handler、`info meta.AppInfo` 参数）在 `--write-modified` 时按模板版本覆盖，diff 可见。这是 modified 层的定义，不是 bug；把锚点放在自定义块之后可以把它们纳入搬运范围

### 19. `co upgrade` 三方合并

第 17/18 条剩下的三个「敞着」其实是同一个根因:没有 base,两方比对分不清「模板改了」和「用户改了」。

- [x] `.co-origin.yaml`:`co new` 写,只记历史事实(模板仓库、commit、co 版本、渲染参数),不记 feature 等现状。「从哪个 commit 生成」不随用户改代码而变,所以不违反「产物不存状态」;渲染参数顺带解决了「要再传一遍」
- [x] `Source` 记 `Repo/Commit/Dirty`;`FetchAt(opts, rev)` 导出模板在任意 revision 的快照(本地目录从所属 git 仓库导出;远端维护一份完整 bare clone 再导出,按 commit 缓存)
- [x] `PlanUpgrade`:origin 或 `--base-ref` → 生成 base 副本 → 逐文件 `git merge-file`(不自己写 diff3)。base == 模板当前 commit 时 base 直接指向 fresh(不能因此退回两方——两方会把用户改动报成 modified 并盖掉)。base 里没有的文件、找不到 git、`--no-base` 退回两方 + 锚点搬运
- [x] `ChangeConflict`:冲突文件永远不写,`--diff` 打带标记的合并结果;同包 Go 文件耦合跟着不写。legacy blocked 只在无 base 时成立
- [x] 全部写完(无跳过/拒绝)推进 origin 到模板新 commit;`--base-ref` 首次使用即记进 origin
- [x] 第 17 条「紧贴锚点的替换 hunk 新旧两行都留」:有 base 后 git 判为相邻 hunk 冲突,显式而非静默(`TestThreeWayMergeAdjacentToAnchor`)。无 base 维持原行为
- [x] 第 18 条「锚点段之外的定制被覆盖」:三方合并的 A/B/A 行,保留且不报差异
- [x] 真实 ecommerce cart `--base-ref v0.1.0`(近似 base):46 → 19 处差异,biz/data/server/service 的 legacy 分歧全部消失(模板自 v0.1.0 没改过它们),2 处 conflict(`main.go`、`data.go`)显式标出;`--write-modified` 后 build/vet 绿
- [ ] 存量 10 个服务的精确 base 不存在(旧正则 CLI 生成),`v0.1.0` 是最早的 manifest 期快照。近似 base 的代价:模板在 v0.1.0 之前就改过、而服务没跟上的行,会被当成「用户改的」保留——需要人对着 `--diff` 决定。这是数据问题,不是工具问题
