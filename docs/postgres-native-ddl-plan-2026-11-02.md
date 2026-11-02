# PostgreSQL 字段与原生 DDL 规划 — 2026-11-02

本阶段继续 [PG 目录与事务元数据后端](postgres-schema-design-2026-11-02.md)，实现 PostgreSQL 字段解析、创建和原生 ALTER 规划，并通过真实 PG 17 fixture 执行预览 SQL 核对语法及实际结果。**通用生产 Apply 和 PG 图形表单仍未接通**；现有 PG SchemaDesign binding 继续只公开 Snapshot。完整功能目标没有缩小，[剩余工作清单](workbench-completion-2026-11-02.md) 继续有效。

## 已实现 I

`schemaddl.PostgreSQLField` 区分类型、排序规则、默认表达式、非空、身份模式/完整序列选项、生成表达式及 STORED/VIRTUAL。解析不把嵌套 CASE、数组、E/dollar 字符串里的关键词或分号当成字段属性。身份列保留隐式 NOT NULL；未知内联约束保持只读，不能误作 ALTER TYPE。渲染后重解析，验证表单没有改变语义。单片段拒绝额外语句、多个字段、非法引用和不平衡括号。

共享 lexer 保持 SQLite 原行为，新增 PostgreSQL 方言处理，将数组方括号与 SQLite 引用标识符分开。SQL 边界继续保留字节位置，值和触发器函数正文不做字符串替换。

`PlanPostgreSQL` 从完整原 catalog 校验原模型/修订，再生成事务内原生语句：

- 创建普通表，包含身份序列选项、生成字段、命名约束、索引/触发器及属性；创建选项支持持久化、access method、tablespace、reloptions、注释、RLS、replica identity 和显式 owner。
- 已有表增删字段、类型/排序规则变更、默认值及非空、身份添加/模式变化/移除、生成表达式移除及 PG 17 的 SET EXPRESSION。映射保留原字段 ID；移除字段要求显式数据丢失确认。类型变更生成显式 USING cast，仍须在未来生产执行器中核验保留值，不能凭 SQL 成功确认无损。
- 命名约束添加/删除/改名，索引/触发器完整定义更新、注释及启用状态；字段 storage/compression/statistics 和表持久化/access method/tablespace/reloptions/RLS/replica identity 的原生更新。
- 表和字段改名单独执行，避免旧定义文本同时改写；实际 PG 负责按 catalog 依赖改写视图、生成定义和索引。
- DROP COLUMN/CONSTRAINT/INDEX/TRIGGER 使用 RESTRICT；不增加 DROP TABLE、CASCADE 或影子重建。外键自身声明的 `ON DELETE CASCADE` 是显式外键动作，不受这一 DDL 删除规则影响。

索引与触发器 CREATE 名称按 PG 语法保持不带 schema，目标表绑定原 schema/table；拒绝 CONCURRENTLY、IF NOT EXISTS、OR REPLACE、名称不匹配及跨范围目标。外键的未限定引用表自动限定到原 schema；显式外部 schema 保留，三段跨 catalog 引用拒绝。普通函数/自定义类型表达式仍需要自身正确限定，规划器不会猜测未知名字。

真实测试发现 PG 会拒绝更改被触发器字段列表使用的列类型。规划器因此在类型/排序规则变更前移除该表的用户触发器，并在事务后段用完整定义重建，重新应用启用状态与注释。SQL 预览包含这些动作。通用执行器仍须核验重建对象/内部依赖的身份变化和保留属性；这不是已经完成的依赖保真证明。

## 验证

Go 1.27.3 / MyGo v0.7.3-nyadb.2，Linux amd64，`GOWORK=off GOCACHE=/data/.cache/golang`。HEAD `5960d26`，未 commit/push。

| 检查 | 实际结果 |
|---|---|
| `go test ./... -timeout 20m` | 最终通过，Shell 79.893 秒、dataworkspace 49.533 秒、objectworkspace 3.791 秒。默认跳过的服务集成测试不计真实服务证据。 |
| contract/schemaddl/engines/dbruntime/objectworkspace race，20 分钟超时 | 最终通过；schemaddl 1.520 秒、engines 3.528 秒、objectworkspace 19.090 秒。未重跑 Shell/dataworkspace 全包 race。 |
| `go vet ./...` / `go tool mygo vet` | 通过。 |
| 翻译生成 / i18n check | 通过，1338 条 zh-Hans/en 消息。 |
| `go tool mygo build` | 通过，27.0 秒；正式 Linux amd64 程序、deb/tar。 |
| `git diff --check` | 通过。 |
| 自有 PostgreSQL 17.10 fixture `--race` | 最终通过，原生规划场景 10.18 秒，完整 engines 服务测试包 38.600 秒。 |

新增纯测试覆盖字段重解析、嵌套 CASE/数组/引用/字面量、稳定 ID、原生数据映射、删除确认、字段顺序拒绝、改名隔离、CREATE 完整定义、菜单目标对应的定义范围、事务形式拒绝、外键限定和触发器重建顺序/启用/注释。既有 SQLite 与 PG 只读 binding 回归同时通过。

`TestPostgreSQL17DesignNativePlanFixture` 使用真实 engines.Session 和自己的 schema，逐项执行规划 SQL。测试侧使用独立事务，执行前复核原修订并绑定原表锁；执行后读取真实 catalog 并提交到 disposable 服务。这是**验收 harness**，没有用它替代正式 Apply。

实际核对：身份列精确起点 9007199254740993、numeric(40,20)→numeric(50,20)、NULL/bytea/数组、字段默认和非空、新字段默认值、expression/include/partial index、触发器字段列表与启用状态、原 schema 外键、显式/默认 collation、身份添加/模式切换/移除、存储生成表达式替换、显式移除字段/约束、约束改名，以及表/字段改名后的视图读取。fixture 在提交后的数据库查询中比较代表性保留值；**正式执行器内全量类型化核验仍未实现**，不能把代表性 fixture 当成通用安全提交证明。

初次 `postgres-prQk4p` 因触发器字段列表阻止类型变更失败，已保留日志并补规划及回归，随后 `postgres-UJiP5w` 通过。最终补充外键原范围限定及恢复默认 collation 后，`postgres-8ADpQW` 全部通过。没有将失败运行计为通过。

最终真实证据：`build/verify/postgres-8ADpQW`，run `nyadb-pg-it-12720-3913797-1793575739`，原容器 ID `4449125b2f594dfb58969a722c69570849508f50313dfee0de94ba4fa63a174a`。readiness=true、test_exit=0、owned_container_removed=true；独立 inspect 确认不存在。可重复命令沿用：

```bash
GOWORK=off GOCACHE=/data/.cache/golang PATH=/usr/local/go/bin:$PATH \
  bash /data/kang/nyadb/tools/verify_postgres_fixture.sh --race
```

最终正式二进制 SHA-256：`62205bfd566b6279ff9574c5d4349011c538a815237748a6da7d4f37bce7c1d3`。本阶段没有新增原生窗口截图或 Python 采集代码；旧截图仍以各自报告的二进制哈希为准。没有运行 Navicat、连接生产或新增 O/V。

## 尚未完成

下一步仍是锁下重规划的正式通用执行、全量保留值/行关联/序列/目录依赖核验、创建与对象变更并发冲突、回滚/不确定回执，以及 PG 原生表单和实际窗口验收。现有私有 metadata Apply 继续只处理原阶段的属性变化。

分区/继承设计、物理字段重排、身份序列完整选项修改、生成存储模式迁移及未知属性编辑仍没有通用规划支持；PG 14 的 SQL 分支及 PG 18 VIRTUAL 仅有代码条件，真实服务只验证 PG 17.10。只读账户、多版本、网络故障及 Windows/macOS 原生验收没有新增结果。

完成 PG 设计后继续 PG/SQLite 对象 CRUD、导入/备份及隧道；跨库映射/传输/数据和结构同步；调度；用户选择的 ClickHouse 和可配置 OpenAI 兼容 AI SQL；模型/逆向、BI、监控、字典、生成、脱敏及命令行。完整目标仍处于实施中。
