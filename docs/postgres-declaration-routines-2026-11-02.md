# PostgreSQL 声明表达式探针与序列默认值 — 2026-11-02

本阶段补齐服务内部 CREATE/ALTER 执行器的表达式预检查，证据为独立实现 **I** 与自有 PostgreSQL 运行结果。正式 `SchemaDesign()` 仍只公开 Snapshot；本阶段不发布通用 Apply，不新增原生窗口或 Navicat O/S/V。

## 原问题与修复

真实 PG 17 基线分别增加 `DEFAULT schema.effect()`、存储生成列和 `CHECK(schema.effect()>0)`。fixture 的用户函数虽然声明 IMMUTABLE，内部仍调用 nextval。三项 DDL 最后回滚，但序列的 `is_called` 已变为 true；回滚无法恢复该副作用。基线 exit 1，31.948 秒，证据为 `build/verify/postgres-SeBFJQ/` 和 `postgres-declaration-calls-before.log`，保留失败结果。

正式预检查从只检查索引扩展为默认值、生成列、CHECK/排斥约束、索引和触发器 WHEN；触发器 `EXECUTE FUNCTION` 声明本身不作为执行回调。中立词法收集保留原 clause ID 和范围，区分注释/字符串、限定/引号名称、CAST 的类型精度、typed literal、时间精度、OPERATOR 及索引操作符类选项。不会将表达式送给数据库进行规划后才判断安全性。

检查按照请求创建私有缓存，逐项读取全部同名 pg_catalog 重载。用户命名空间、用户重载、非支持语言、可执行配置和不合适的 volatility 不获取信任；IMMUTABLE 声明不能单独通过。当前支持常用数值/字符串/编码/JSON 及限定日期、UUID/随机/时间默认值；生成列和索引继续要求 immutable，稳定函数的默认值许可不会通过缓存泄漏给生成列。

内建 SQL 函数同样核对真实 `prosqlbody`。共享的严格单 RETURN/SELECT 查询解析器继续约束原运算符转发，新增函数转发只允许有界的 FUNCEXPR、绑定外部参数及 native int4 零常量。递归收集每个终端的参数/返回类型并核对真实 pg_proc；仅支持经检查的 core C/internal 终端，拒绝嵌套 SQL、任意强制转换/关系/子查询/未知属性及用户 OID。真实保留 `round(numeric)`、`trunc(numeric)` 与嵌套的 `substring(text,text,text)`，不将任意 SQL 体解释成安全转发。

## nextval 与 serial

- 空 CREATE 探针声明 core nextval 默认值不调用序列。已有字段 SET DEFAULT 也不填充旧行，继续支持。
- 在原表 ACCESS EXCLUSIVE 锁及原修订复核之后检查新增字段。若 nextval 默认值将填充现有行，在任何 probe/ALTER 前明确拒绝；空表和新表的声明继续支持。失败保留请求、目录和序列值。
- 真实回归另发现已有 serial 所有权没有出现在独立 CREATE 中，导致严格声明图计数拒绝。新增原序列声明保留：从原自动 ownership 边推导序列，只为保留的非 identity owner 字段追加原始序列记录。所有权、完整声明、未知属性、目录及当前序列值继续接受原有严格比较，不通过字符串猜测归属或忽略缺失记录。
- 回归实际保存含 serial 的表并核对序列值不变；另对已读取图注入未知序列属性，确认独立比较拒绝。该注入属于证据测试，不是服务端 DDL。

## 验证

最终检查按以下范围记录，真实服务通过与无窗口测试分别列出。所有最终检查 exit 0；完整失败过程保留在独立日志中。

| 检查 | 最终结果 |
|---|---|
| 全量 Go | `go test ./... -timeout=20m` exit 0。最终源代码检查包含 Shell 92.827 秒、dataworkspace 46.066 秒；最终 fixture 修正后全量重跑通过，未修改的包使用有效缓存。 |
| 生命周期 race | schemaddl/engines/dbruntime/objectworkspace exit 0（10.250/6.985/1.093/25.902 秒）；最终 fixture 修正后 engines race 重跑 exit 0，6.591 秒。 |
| PG 17 真实 race | 最终十二项顶层 fixture exit 0，276.566 秒，`build/verify/postgres-anZhlr/`；含三项 DDL 事务/生命周期/丢失回执、五项声明及四项既有 core 运算符/索引回归。 |
| PG 14 真实服务 | 最终五项声明回归 exit 0，91.141 秒，`build/verify/postgres-azowNU/`；含完整 serial 保存、默认值与原状态核验。 |
| PG 18 限定真实服务 | 表达式/内建函数/替换 core SQL 体/范围缓存四项 exit 0，58.959 秒，`build/verify/postgres-gvjlgZ/`；不包括 PG 18 serial/独立 NOT NULL 约束适配。 |
| 静态、翻译、打包 | `go vet ./...`、`go tool mygo vet`、翻译生成/检查（1456 条）和 `go tool mygo build` exit 0。 |

实际服务版本为 PostgreSQL 14.24、17.10、18.6。使用 Linux amd64、Go 1.27.3 / MyGo v0.7.3-nyadb.2、`GOWORK=off`、`GOCACHE=/data/.cache/golang`。race 超时 20 分钟。所有真实服务通过既有 `tools/verify_postgres_fixture.sh` 创建唯一自有容器、随机 localhost 端口，开启显式 `NYADB_POSTGRES_IT`；没有生产数据库或 mock 替代服务结果。

可重复命令：

```bash
GOWORK=off go test ./... -timeout=20m
GOWORK=off go test -race ./internal/db/schemaddl ./internal/services/engines ./internal/services/dbruntime ./internal/ui/objectworkspace -timeout=20m
GOWORK=off bash tools/verify_postgres_fixture.sh --race --run 'TestPostgreSQL17Design(Declaration|ProbeCore|ProbeChangedCore|DDLTransaction).*Fixture'
GOWORK=off bash tools/verify_postgres_fixture.sh --postgres-version 14 --run 'TestPostgreSQL17DesignDeclaration.*Fixture'
GOWORK=off bash tools/verify_postgres_fixture.sh --postgres-version 18 --run 'TestPostgreSQL17DesignDeclaration(FunctionEffects|CoreRoutines|ChangedCoreBody|RoutineScope)Fixture'
GOWORK=off go vet ./...
GOWORK=off go tool mygo vet
GOWORK=off go tool mygo generate
GOWORK=off go tool mygo i18n check
GOWORK=off go tool mygo build
```

中间失败全部保留：触发器 fixture 在原快照之后创建函数引起原修订冲突；SQL fixture 的原 BEGIN ATOMIC 声明语法未完成；独立图复核曾使用保存后的新字段 ID，而不是原草稿绑定。分别修正 fixture 后重跑。已有 serial 声明图缺失是真实实现缺陷，单独修复并增加原状态保留和属性变化拒绝回归。不能把这些失败日志标为整轮通过；最终通过日志与失败日志分开。

提交前正式程序 `build/linux-amd64/nyadb` SHA-256：

```text
dc7c441e494fa184fddbbc6b38caa1b985a4bff69dedd11108af1119f57509d3
```

所有十三个成功/失败 fixture 容器和一个 core 目录诊断容器，共十四个自有容器，均通过 Docker daemon 可用、inspect 明确不存在及全量容器 ID 列表独立核对。最终审计为 `build/verify/postgres-declaration-cleanup-audit.json`；未清理其它资源。运行身份/版本/退出码记录在 `postgres-declaration-runs.json`，最终检查和二进制身份汇总于 `postgres-declaration-delivery.json`。本阶段按用户逐项要求本地提交，不 push 或改写历史。

没有 UI 变更或实际窗口执行，本阶段不生成新截图，也不将打包成功算作 GUI 验证。Python 工具未修改。完整待办仍见 [剩余清单](reconstruction-remaining-2026-11-02.md)：PG 并发/依赖与其它隐式回调、新字段值语义、正式 binding/原生表单/实际保存验收，以及 PG/SQLite 文件/对象/大数据工具、传输/同步、调度和全部高级阶段继续有效。
