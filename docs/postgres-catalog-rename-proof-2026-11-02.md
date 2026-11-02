# PostgreSQL 改名目录保真与事务执行 — 2026-11-02

继续 [保留值与独立结构核验](postgres-value-shape-proof-2026-11-02.md)，补齐原生表/字段改名的完整原目录证明，并在私有事务执行中使用。PG `SchemaDesign` 仍只公开 Snapshot；完整通用 Apply、重建对象 OID 映射及 PG 原生表单尚未完成。完整 [功能清单](workbench-completion-2026-11-02.md) 继续有效。

## 已实现 I

`VerifyPostgreSQLRename` 先用原规划器确认草稿只有一次原生表或字段改名，再验证实际表 OID、字段/约束 ID、对象集合、所有公开属性和完整 catalog scope。它逐条比较完整原 JSON 记录，保留精确数值、未知字段、OID、表达式树、ACL、RLS、约束/索引/内部触发器状态、序列关联和依赖边。不能增加或删去原目录记录，也不能把另一个 catalog/schema 的相同数值 OID 当成原对象。

只允许明确请求的名称变化及 PostgreSQL 自身关联的行/数组类型名称变化。字段改名通过已捕获的继承边定位子表，不按名称猜测其他表。普通索引的物理属性名称保持原值；服务器生成的索引 SQL 会使用新的表字段名称。

视图/约束/索引/触发器/默认值/SQL 函数/策略的服务器派生 SQL 可随名称改变，完整底层表达式树及其他原始字段仍必须一致；没有捕获到对应 rewrite/default/inheritance 或必要底层字段时不接受这些变化。错误只报告目录记录和字段名，不打印 SQL 正文或未知字段值。该证明针对当前事务的完整目录快照，不声称封锁全数据库的独立写入。

自动数组名称按 PG 规则使用 `_名称`、`_名称_1` 等数字后缀，按 63 UTF-8 字节上限裁剪。已有非数组类型不能被替换；允许服务器移开与其基础类型明确关联的自动数组，保留其 OID/基础类型及其他属性，并为改名后的行类型选择唯一数组名。也覆盖表名取自身数组名时的单次数组移动。规则由真实 fixture 和 PostgreSQL 17.10 [公开 `pg_type.c`](https://github.com/postgres/postgres/blob/REL_17_10/src/backend/catalog/pg_type.c) 中 `makeArrayTypeName` / `moveArrayTypeName` / `RenameTypeInternal` 核对；这是 PostgreSQL 依赖行为证据，不是 Navicat S/O/V。

目录读取新增用户 schema 类型及其派生数组/domain 的闭包，捕获使用这些类型的外部表，以及直接引用本 schema 表/类型的外部 SQL 函数和操作符。它没有把内建共享类型的所有入站依赖展开成全数据库扫描；既有 10000 记录、单记录 1 MiB、整体 16 MiB 和取消边界继续适用。

`postgresDesignRenameApply` 与既有 metadata 执行共用事务生命周期：不可变请求、原连接/代次验证、独立物理连接、局部设置、原表 ACCESS EXCLUSIVE 锁、锁后建立 repeatable-read 快照并复核完整原修订、执行单条原生改名、提交前目录核验、最后检查代次、COMMIT 确认。字段/表名更新只出现在核验后的原范围回执中。存在启用的 DDL event trigger 时明确拒绝；失败回滚，清理或提交确认不足时丢弃连接并返回 uncertain，不能替换草稿或获得重放权限。

## 验证

环境为 Go 1.27.3 / MyGo v0.7.3-nyadb.2、Linux amd64、`GOWORK=off GOCACHE=/data/.cache/golang`，HEAD `5960d26`，现有未提交工作全部保留。

纯回归覆盖完整目录/稳定身份、未知精确值、权限和外部记录变化拒绝、额外对象、错 catalog、缺少底层表达式、UTF-8 裁剪，以及只能移动关联自动数组而不能修改其基础类型。

真实 PG fixture 验证：

- 表/字段改名保留身份序列与当前值、精确 numeric、bytea、CTID、生成列及读取结果。
- 外部视图输出名、解析 SQL 函数及外部行/数组类型表保持可用；自引用/外部外键、普通/表达式/包含索引、注释、权限、RLS/策略、用户触发器启用状态与参数、继承子表参与完整证明。
- 普通数组名称冲突、移开另一类型的自动数组、表名取自身数组名。
- 目标占用回滚、外部函数修订冲突、锁等待期间的已提交修改、取消及驱动确认、旧 generation、捕获请求后修改调用方草稿/原 JSON 不影响执行。
- 真实 COMMIT 已成功但注入丢失回执时，返回 uncertain、无可接受 actual/verified；从独立数据库连接确认已提交结果。没有自动重放。

新增 `pgNativeFixtureApply` 的改名分支在原阶段二进制保留值比较外执行完整目录证明。metadata 原有锁等待、设置恢复、取消、关闭和提交回执丢失回归沿用同一事务生命周期，最终完整服务包会再次验证。

| 最终检查 | 实际结果 |
|---|---|
| `go test ./... -timeout 20m` | 通过；Shell 73.045 秒、dataworkspace 49.520 秒、objectworkspace 3.537 秒、engines 0.390 秒。默认跳过的集成测试不算真实服务证据。 |
| contract/schemaddl/engines/dbruntime/objectworkspace race，20 分钟超时 | 通过；schemaddl 2.171 秒、engines 4.082 秒、objectworkspace 18.251 秒。本轮没有重跑 Shell/dataworkspace 全包 race。 |
| `go vet ./...` / `go tool mygo vet` | 通过。 |
| 翻译生成 / `go tool mygo i18n check` | 通过，1338 条 zh-Hans/en 消息。 |
| `go tool mygo build` | 通过，27.1 秒；正式 Linux amd64 程序与 deb/tar。 |
| `git diff --check` | 通过。 |
| 完整自有 PostgreSQL 17.10 fixture `--race` | 13 个场景全部通过，engines 206.946 秒；rename catalog 22.06 秒、rename lifecycle 11.28 秒、rename lost commit 3.51 秒、native plan 71.63 秒。 |

最终正式二进制 SHA-256：`70a09a02ab9efb5da808122fbf040cf6ab8641eafdf44900e954d7634cd670b0`。扩大依赖读取范围增加了本轮 fixture 的快照成本；这些耗时是本次环境下的服务测试结果，不是 UI 响应时间或性能基准。

最终证据：`build/verify/postgres-Rf8YUS`，原始 `tests.log`、`versions.txt` 和 `resource.txt` 保存场景/版本/退出码/资源身份。服务 `test_exit=0`、`owned_container_removed=true`；独立 `docker inspect --type=container` 确认原容器不存在，保存 `cleanup-audit.json`。

重复验证使用：

```bash
GOWORK=off GOCACHE=/data/.cache/golang PATH=/usr/local/go/bin:$PATH \
  tools/verify_postgres_fixture.sh --race
```

`--race --run 'TestPostgreSQL17DesignRename.*Fixture'` 只重放改名场景，不能替代完整服务包验收。

## 失败记录与限制

初始 fixture 的 SQL-standard BEGIN ATOMIC 正文被现有脚本拆句流程截断，产生语法错误，记录在 `postgres-O4oDpn`；fixture 改用单语句 SQL RETURN 正文，不以此声称通用 BEGIN ATOMIC 文件执行已支持。`postgres-vS06kR` / `postgres-Q8txvm` 发现数组名称冲突预测错误；已保留日志并修正。`postgres-baInzt` 为改名证明中行类型检查顺序错误的运行，已修正。`postgres-YmAFCp` 发现普通索引物理属性名应保留原值，修正后 `postgres-qk2aXU` 三项改名场景通过。补充关联数组移动及自身数组名场景后，最终 `postgres-Rf8YUS` 全部通过。上述运行及初期 native plan 验证 `postgres-NE4GXm` 的自有容器均已独立确认不存在，各目录保存清理审计。失败运行不计通过。

PG 通用 CREATE/ALTER 仍需独立目标 catalog 的映射、新增对象证明、类型/约束/索引/内部触发器重建 OID 的完整保留规则、并发创建冲突及原生表单。不能将本次无重建的原生改名证明套用到类型变化或数据迁移。PG 14/18、网络故障、多平台原生窗口没有新增验证结果。本轮没有 UI/Python 改动、原生截图或 Navicat O/V；未运行 Navicat、连接生产、提交或推送。

PG/SQLite 对象 CRUD、导入/备份和隧道、跨库传输/同步、自动运行、ClickHouse、可配置 OpenAI 兼容 AI SQL、模型/逆向、BI、监控、字典、生成、脱敏和 CLI 仍属于完整目标。
