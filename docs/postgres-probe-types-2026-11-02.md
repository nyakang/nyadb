# PostgreSQL CREATE 探针的类型与转换检查 — 2026-11-02

继续 [显式索引函数检查](postgres-index-probe-routines-2026-11-02.md)，处理独立 CREATE 的解析/规范化阶段可能调用的类型输入、域及转换回调。该前置检查属于实现 **I**；正式 PG `SchemaDesign` 仍只提供 Snapshot，完整图形保存、事务 Apply 及 [其它完整目标](reconstruction-remaining-2026-11-02.md) 保持进行中。

## 实现与真实问题

`schemaddl.PostgreSQLProbeTypes` 纯词法读取字段声明、DEFAULT、生成表达式、约束、索引和触发器 WHEN 中的显式 `::` / `CAST` / 类型字面量。保留引用大小写和 namespace；按实际 SQL 语法规范化常用未引用类型别名、精度、时间区间及数组维数。嵌套字符串、E/dollar 字符串和注释保持不可分；CAST 继续检查其内层操作数。类型修饰只接受数值参数，不把用户提供的类型片段交给服务解析。

数值常量包括 int4/int8/numeric 候选，字符串和布尔常量亦进入候选类型集合。B/X/N 无空白前缀按字面量处理；有空白或引用形式仍属于类型身份。候选不等于通用表达式类型推导。

`postgresCheckProbeTypes` 在 SAVEPOINT/CREATE 前，按参数化 namespace/name 查询真实 OID，递归读取数组元素、域基类型与 CHECK/继承 DEFAULT 依赖、复合类型字段以及范围 subtype/canonical/difference/opclass。检查输入/输出/收发/typmod/analyze/subscript 的回调身份。只有 pg_catalog 的 core OID、function、internal/c 和 IMMUTABLE/STABLE 回调被接受；错误声明 IMMUTABLE 的用户函数及同名 internal 别名均不能获得许可。结构化用户枚举、域、复合、范围和数组可以使用对应核心 I/O；任意自定义 base representation 没有编辑许可。

范围 opclass 的实际 family support routines/operators 也逐项读取。遇到的类型 OID 集合用于检查相互之间的 pg_cast 候选；不因为无关扩展定义了到 text 的转换就禁用普通文本声明。各请求独立持有私有检查缓存，没有跨连接/事务许可复用。查询只读取目录，不执行用户表达式、类型 parser、PREPARE、EXPLAIN 或输入/转换函数；失败不返回可用独立定义，不改变原记录/请求。

真实 PostgreSQL 17.10 的修复前回归：

- `postgres-aHDKzm` 中显式 enum→integer 自定义 IMMUTABLE cast 被接受，且实际改变自有序列 `is_called`；SAVEPOINT 回滚未恢复。域 CHECK 样例仅观察到不受信任声明被接受，**没有观察到序列消费**，不得将其写成已发生副作用。日志 `build/verify/postgres-probe-types-before.log`，exit 1。
- 初次类型检查补入之后，`postgres-F3bBi6` 的 `CAST(1.25 AS uuid)` 仍通过；未显式写出 numeric 源类型的常量调用真实 numeric→uuid 用户转换，序列被消费。新增常量候选后拒绝此路径。日志 `build/verify/postgres-probe-types-literal-before.log`，exit 1。

失败运行作为问题证据保留，不计验收通过。新增回归还覆盖数组/复合中的危险域、继承的 DEFAULT、range subtype_diff、自定义 internal 输入别名、同一物理连接上下一请求的定义变化、取消和无关转换隔离。

## 验证与交付

基线 Go 1.27.3 / MyGo v0.7.3-nyadb.2 / Linux amd64，`GOWORK=off`，Go SDK `/usr/local/go`，缓存 `/data/.cache/golang`。本步从 `5294f95` 继续；按用户继续目标中的要求，验收后单独本地提交，不 push 或改写历史。

| 检查 | 实际结果 |
|---|---|
| `go test ./... -timeout=20m` | 最终通过，含补充 TIME 字面量纯回归；合法缓存结果仍标为 cached。此前数值候选修复后的完整非缓存 Shell/dataworkspace 为 93.948/45.739 秒。日志 `postgres-probe-types-all-tests-complete.log`。 |
| schemaddl/engines/dbruntime/objectworkspace 生命周期 race，20 分钟超时 | 最终通过；schemaddl 8.069 秒、engines 7.715 秒，其余有效缓存。没有重跑 Shell/dataworkspace 全包 race；本步未改变其生命周期。 |
| Go vet / MyGo vet | 最终通过，日志后缀 `vet-complete.log` / `mygo-vet-complete.log`。 |
| 翻译生成 / i18n check | 通过，1456 条消息；目录未变化。 |
| MyGo 正式构建 | 最终通过，28.4 秒，Linux 程序/deb/tar。 |
| PostgreSQL 17.10 全量真实 fixture | 最终 `postgres-fqxRj0` 41 个顶层 fixture 全部通过，522.658 秒；连续原生 DDL 111.48 秒，最后保留属性 19.31 秒。早一轮 `postgres-Bx3tO6` 40 项通过、518.348 秒；该早一轮不包含数值候选修正。 |
| PostgreSQL 17.10 类型专项真实 race | `postgres-99wT6K` 7 个 fixture 通过，62.291 秒；覆盖 15 类合法类型及危险回调。 |
| TIME 字面量补充真实 race | `postgres-quS2oh` 通过，3.003 秒；同一 fixture 分别拒绝 numeric→uuid 和 time→uuid 转换，原序列未调用。后续全量 Go/race/静态检查与正式构建均已通过。前述全量真实 fixture/14/18 专项早于这一 TIME 补充，不把其范围写成包含 TIME 路径。 |
| PostgreSQL 14.24 / 18.6 类型专项 | 7 个 fixture 各自通过，45.858/55.162 秒；覆盖数值字面量转换和同样 15 类类型，不替代版本完整设计器验收。 |
| `git diff --check` / gofmt | 通过。 |

最终二进制 SHA-256：`5e3cb9b897fe2d36ecd40446d9e081918d36434cac3cf96b28211677c4776121`。机器回执/源码哈希在 `build/verify/postgres-probe-types-checks.json`；各命令日志在同一忽略目录，前缀 `postgres-probe-types-`。没有将成功打包写成实际窗口验证。

最终真实服务 run `nyadb-pg-it-20604-524297-1793612672`，容器 ID `14959455df4e32b31cb46a14174857542ef3089360ee5b3410e21ce452eb1a61`，readiness=true、test_exit=0、owned_container_removed=true。本步 10 个通过/失败运行的容器均由独立 `docker inspect` 明确确认不存在，且不在 `docker ps -a --no-trunc` 列表；清理审计 `build/verify/postgres-probe-types-cleanup-audit.json`。没有操作其它资源。

可重复完整真实服务验证：

```bash
GOWORK=off GOCACHE=/data/.cache/golang PATH=/usr/local/go/bin:$PATH \
  bash tools/verify_postgres_fixture.sh
```

生命周期 race 专项：

```bash
GOWORK=off GOCACHE=/data/.cache/golang PATH=/usr/local/go/bin:$PATH \
  bash tools/verify_postgres_fixture.sh --race \
  --run 'TestPostgreSQL17DesignProbe(DomainEffect|CastEffect|ImplicitLiteralCast|StructuralTypes|NestedTypeCallbacks|TypeCallbacksRefreshAndCancel|UnrelatedCast)Fixture'
```

普通 `go test` 明确跳过的集成测试不计真实服务。环境为自有唯一容器、随机 localhost 端口和测试 namespace；不读取或清理无关容器。未修改 UI、翻译目录或 Python；正式 Linux 程序只执行构建，本步没有新增窗口截图。Windows/macOS 原生结果、Navicat **O/S/V** 及生产服务验证未新增。

## 仍需完成

这不是完整探针安全或 COMMIT 证明。完整表达式类型解析、函数返回的中间类型/隐式分派、一般运算符/opclass/访问方法、普通 DEFAULT/CHECK/触发器表达式的显式函数、其它委托回调与并发定义变化仍需要独立证据及检查。合法 volatile 新字段语义、锁下重规划/完整事务 Apply、PG 专属表单、原生保存及 PG 18 NOT NULL 约束适配继续待实施。已有元数据/改名执行不新增本检查或权限要求。
