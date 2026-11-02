# PostgreSQL CREATE 探针的显式索引函数检查 — 2026-11-02

基于 `ff7cb5f` 继续正式 Apply 的前置工作。本步已完成自动检查、完整 PostgreSQL 17 真实服务 race 和资源清理复核，修复保留在工作树，未 commit/push。PG 正式 `SchemaDesign` 仍只读，不将本检查称为完整探针副作用证明或 COMMIT 授权。

## 发现及修复范围

真实 PostgreSQL 17.10 自有 fixture 证明：索引表达式调用一个错误声明 IMMUTABLE 的 PL/pgSQL 函数时，独立 CREATE 空表/索引探针仍会调用它，改变自有序列的 `is_called`；SAVEPOINT 回滚删除 probe 对象但不能恢复该序列状态。

最初 DEFAULT 和 CHECK 两个样例没有观察到序列消费，只发现探针接受了该声明，不能将它们记成实际副作用。加入常量表达式索引后的 `postgres-CJy23X` 才明确观察到非事务性变化及被接受的基线，退出 1；三个早期日志分别为 `postgres-probe-effect-{initial,check,index}.log`。

修复只覆盖显式索引函数调用：

- `PostgreSQLIndexProbeCalls` 先通过已有单语句/原表绑定检查，纯词法收集索引表达式和 WHERE 中的嵌套调用；不让数据库解析/规划表达式。保留限定命名空间和引用大小写，忽略字符串/dollar 字符串/嵌套注释、访问方法和已支持的语法构造。带命名空间或引用形式的语法同名函数仍需检查，三段身份及显式 OPERATOR 分派拒绝。
- `postgresCheckIndexProbeRoutines` 在 SAVEPOINT/CREATE 之前只允许限定的常用 core 数值/文本函数，且限定为 pg_catalog；对每个名称读取全部当前 overload。每个必须为正常 core OID（小于 FirstNormalObjectId 16384）、function、IMMUTABLE 和 internal/c/sql 核心语言。新增用户/扩展 overload 即使同名、即使 IMMUTABLE 也没有许可。
- 读取使用参数化 pg_catalog 查询和原上下文，不调用函数。不输出用户函数 SQL、值或名称，拒绝不产生基线、不修改 caller 设置/角色和原数据。原元数据/改名执行仍使用原流程，不新增此读取要求。

这不是通用安全证明。类型/显式与隐式 cast、域 CHECK、运算符/opclass、内建委托回调、其它表达式范围、并发定义变化和新字段值语义仍未验证；未知或尚未支持的形式没有完整编辑许可。不要仅凭函数名称、IMMUTABLE、空表或回滚就放开正式 Apply。

## 已完成的定向验证

- 纯回归覆盖名称去重、嵌套/限定/引用调用、语法同名的限定函数、注释/字符串、WHERE/INCLUDE/方法、未绑定身份、多个语句和显式运算符。`postgres-probe-calls-unit.log` 通过。
- 首次修复 `postgres-FYtbRo` 中副作用样例和已有 definition 样例通过，但原图回归因 WHERE 被误判为函数退出 1。修正语法识别后的 `postgres-TRNT3a` exit 0，15.099 秒，已有独立定义/目录图和副作用样例均通过；日志 `postgres-probe-effect-direct-fix-where.log`。
- `postgres-PTXqMF` 真实 race exit 0，4.628 秒。两项回归分别拒绝非 pg_catalog 用户函数和自有 fixture 创建的 `pg_catalog.lower(integer)` 假 core overload；原序列仍未调用，probe 对象不存在。假 overload 在本测试 cleanup 中删除，容器只用于本轮 fixture。
- 相同两项真实 race 在 PostgreSQL 14.24 (`postgres-o385OV`, 4.137 秒) 和 18.6 (`postgres-5ZUNcx`, 4.317 秒) 通过；这两个专项不替代 PG 18 独立 NOT NULL 约束/完整设计器的未适配范围。
- `go test ./... -timeout=20m` 通过（Shell 106.418 秒、dataworkspace 51.953 秒）；相关 schemaddl/engines/dbruntime/objectworkspace race 通过（8.693/7.624/1.105/29.143 秒）。`go vet ./...`、MyGo vet、生成和 i18n 检查通过。日志前缀 `build/verify/postgres-probe-calls-*`。
- MyGo 正式打包通过，32.1 秒，1456 条翻译；Linux 二进制及 deb/tar 包生成。正式二进制 SHA-256 `55c5d0ffd68d5ed51b1d4f4f24e6576aee99e2ea2737a8b684d5827b8a2032b1`。本步没有运行该窗口，不以本哈希替代以前窗口证据。

- 完整真实 PostgreSQL 17.10 服务 race `postgres-nnobtx` exit 0，903.711 秒，34 个顶层 fixture 全部通过；连续原生 DDL 264.60 秒，最后的保留属性验收 34.59 秒。新函数副作用与假 core overload 两项分别为 1.50/1.45 秒。日志 `build/verify/postgres-probe-calls-full-service-race.log` 和 `build/verify/postgres-nnobtx/tests.log`。
- 最终资源回执：run `nyadb-pg-it-780-485758-1793610810`，容器 ID `29b684e3db4cc9461c521bf8e2271ca84940a82b426ae3fb54a92ac5a55b0a33`，readiness=true、test_exit=0、owned_container_removed=true。独立 `docker inspect` 明确返回不存在，`docker ps -a --no-trunc` 亦不包含该 ID；本轮 9 个通过/失败运行的容器均确认移除。审计 `build/verify/postgres-probe-calls-cleanup-audit.json` 已替换运行期间的旧状态；没有清理其它容器。

可重复真实服务验收：

```bash
GOWORK=off GOCACHE=/data/.cache/golang PATH=/usr/local/go/bin:$PATH \
  bash tools/verify_postgres_fixture.sh --race
```

仅重复函数拒绝回归时追加 `--run 'TestPostgreSQL17DesignProbe(FunctionSideEffect|CoreOverload)Fixture'`；`--postgres-version 14` / `18` 用于对应版本专项，不代表该版本的完整设计器验收。

本阶段未修改 Python/UI、未运行 Navicat 或生产服务，未新增窗口截图或 O/S/V。全量服务的新测试名为 `TestPostgreSQL17DesignProbeFunctionSideEffectFixture` / `TestPostgreSQL17DesignProbeCoreOverloadFixture`，普通 Go 测试中的显式跳过不计真实服务验证。Linux 正式程序仅完成打包，本修复没有新增原生窗口结果；Windows/macOS 原生验收仍未验证。

本步完成后仍继续并发/其它探针副作用/新字段语义、正式事务 Apply/原生表单及 [全部剩余目标](reconstruction-remaining-2026-11-02.md)。
