# PostgreSQL 声明图核验与统计默认值修复 — 2026-11-02

延续 [目录身份与表达式核验](postgres-graph-identity-2026-11-02.md)，把稳定身份扩展为独立目标声明图，并在真实 CREATE/ALTER 验收的提交前比较。新增表达式/目录图核验属于实现 I；正式通用 Apply 和 PG 表单仍未开放。

## 实施结果

`ProjectPostgreSQLDeclarationGraph` 选择目标表、行/数组类型、活跃字段、约束、索引/序列及其属性、用户/内部触发器、继承/分区记录和按目录类关联的依赖边。它以原完整 JSON 记录为基线，只规范化已知 typed OID、字段引用、属性向量和表达式树；未知字段、精确数字和边的未知属性仍参与声明比较。不按相同数字或文本猜测 OID/字段关联。

新增显式原资产引用映射：探针可以引用原表、原行类型或原资产，原对象只获得引用身份，不成为探针的目标记录。原 catalog/schema/版本/字节序必须一致，重复或冲突映射明确拒绝。regclass/regtype 常量及依赖边使用同一映射。

投影保留表字段的实际顺序，索引/序列字段按独立物理位置绑定，外部属性按原目录类/关系 OID/属性编号绑定。删除字段的 attnum 空洞不会伪装成不同活跃字段；原关系的字段记录数量和最大编号仍必须完整。默认标记必须有对应的原表达式记录；缺少属性、默认、分区键或依赖证据不能返回部分图。

原生 `pg_depend` 没有唯一边约束，合法目录中可以有重复边。本轮先按“重复边一定无效”拒绝，真实服务发现该假设错误后修正为保留全部边及其重数，再排序比较；没有静默去重。跨目录使用相同 OID 的对象仍分别处理，非目标上下文不会因数字重号进入目标依赖图。

权限/owner、RLS、注释、原索引标记及内部 RI 触发器启用状态存入 `Opaque`；物理文件/缓存/缺失值状态存入 `Physical`，原数据不被丢弃。派生 SQL 由底层表达式、类型/OID、字段和原始属性核验。这些分离字段仍需要完整原目录保留及数据核验，不能由声明一致性授予删除或覆盖权限。

`ComparePostgreSQLDeclarationGraphs` 比较完整目标记录集合和 JSON 内容，保持嵌套未知属性及精确数值、字段顺序、依赖重数，拒绝错误 scope、重复或缺失记录和无效证据。报错只包含记录类别和字段名。修正表达式整数解析错误，使 malformed 字段也不回显表达式原文。`Clone` 深复制图和所有 JSON 缓冲，原请求/目录输入保持不变。

验收 `pgNativeFixtureApply` 在已有独立形状和二进制保留值核验之外，对 CREATE/ALTER 实际结果执行目标图比较。该函数仍是自有 fixture harness；没有把它当作生产 Apply，PG `SchemaDesign` 的 Apply/CanApply 继续为空。

## 真实核验发现的统计默认值问题

PostgreSQL 17 的 `pg_attribute.attstattarget` 用 NULL 表示未显式设置；0 明确禁止统计收集。旧私有快照直接读到 Go int，把 NULL 变成了 0，导致独立 CREATE 将默认统计改为 0。字段级归一化和原形状比较都未识别这一差异，本轮完整声明图比较首先拒绝了它。

现在以可空私有字段解析统计目标，工作区属性继续用 -1 表示默认，0 保持显式关闭。原完整目录仍保留 NULL。私有 metadata 验证按版本处理 `SET STATISTICS -1` 的结果：PG 17+ 应写为 NULL，旧版本仍为 -1。没有数据库迁移、凭据格式或公开连接接口变化。

纯回归覆盖 NULL/-1/0/73 和 PG 16/17 的默认重置格式。真实 PG 17 fixture 分别执行 0、73、-1，核对私有回执和数据库原字段，确认恢复默认后为 NULL。旧版本结果来自纯回归，不声称真实 PG 16 通过。

规则经 PostgreSQL 17.10 [公开 `pg_attribute.h`](https://github.com/postgres/postgres/blob/REL_17_10/src/include/catalog/pg_attribute.h) 核对。依赖重数及序列归属分别参照公开 [pg_depend.h](https://github.com/postgres/postgres/blob/REL_17_10/src/include/catalog/pg_depend.h) 和 [sequence.c](https://github.com/postgres/postgres/blob/REL_17_10/src/backend/commands/sequence.c)：OWNED BY 要求序列与表同 owner/schema。它们是 PostgreSQL 依赖依据，不是 Navicat O/S/V。

## 检查与证据

Go 1.27.3 / MyGo v0.7.3-nyadb.2，Linux amd64，`GOWORK=off GOCACHE=/data/.cache/golang`，HEAD `5960d26`。现有未提交成果全部保留。

纯回归验证 typed OID 的目录类区分、属性向量与普通 flags/字面值隔离、未知精确值和 JSON 格式变化、错误范围/身份、无效数据、依赖重数和深复制。真实 fixture 额外拒绝未知属性变化、缺失默认/属性/依赖、非空状态变化、默认标记不一致；物理/权限差异可与声明分离，但仍在结果中完整保留。

失败及中间验证原始记录保留：

- `build/verify/postgres-MV7lzb`：误拒绝合法重复依赖边；已按原生规则保留重数。
- `build/verify/postgres-SkhFse`：声明图发现 `attstattarget` 的真实默认值差异；修复可空解析及重置核验。
- `build/verify/postgres-xce98d`：修复后的首项真实图核验通过。
- `build/verify/postgres-bTDL29`：扩大后 5 个真实 race 场景通过，122.868 秒；原生 CREATE/ALTER 84.19 秒，统计默认值 11.71 秒，原引用 5.07 秒。这是中间验收，最终以完整全包运行记录为准。

最终全量检查、二进制身份和独立清理审计将在最终验收记录中列出，失败运行不计通过。

## 最终验收记录

| 检查 | 实际结果 |
|---|---|
| `go test ./... -timeout=20m` | 通过；Shell 75.268 秒、dataworkspace 48.968 秒、objectworkspace 3.605 秒、engines 0.402 秒。默认跳过的集成测试不计真实服务通过。 |
| contract/schemaddl/engines/dbruntime/objectworkspace race，20 分钟超时 | 通过；schemaddl 2.309 秒、engines 3.541 秒、objectworkspace 18.090 秒。没有重跑未修改的 Shell/dataworkspace 全包 race。 |
| `go vet ./...` / `go tool mygo vet` | 通过。 |
| 翻译生成 / `go tool mygo i18n check` | 通过，1338 条 zh-Hans/en 消息，生成目录已是最新。 |
| `go tool mygo build` | 通过，26.6 秒；正式 Linux amd64 程序及 deb/tar。 |
| 最终表达式 fuzz，15 秒，4 workers | 通过，34123 次执行，总工具时间 16.036 秒。 |
| 完整自有 PostgreSQL 17.10 fixture `--race` | 16 个主场景全部通过，engines 248.629 秒；另有 7 项图变异子回归。native plan 84.87 秒、graph 10.45 秒、原引用 5.12 秒、统计默认值 10.99 秒。 |
| `git diff --check` | 通过。 |

最终正式二进制 SHA-256：`54b0590177f4476768a8a2b8188cb418e8ffdfcda20cf964883c4852b7fb6fe8`。这些时间是本次环境下的服务/工具验证耗时，不是 UI 响应或性能基准。

最终服务证据：`build/verify/postgres-0R7PH3`，包含 `tests.log`、`versions.txt`、`resource.txt`、`validation.json` 和 `cleanup-audit.json`。`test_exit=0`、`owned_container_removed=true`；独立 `docker inspect --type=container` 确认原容器 `cdd34b251289729552845b891aa1fca0ecf4f94b8b4ad7f478cd99257fd5b405` 不存在。上述四个失败/中间运行的容器也分别保存了独立清理审计。

完整重放：

```bash
GOWORK=off GOCACHE=/data/.cache/golang PATH=/usr/local/go/bin:$PATH \
  tools/verify_postgres_fixture.sh --race
```

本轮 5 项定向场景可用 `--race --run 'TestPostgreSQL17Design(Graph.*|StatisticsDefault|NativePlan|NativeRetainedProperties)Fixture'`，不能代替完整服务包验收。

## 继续实施的工作

必须把原完整 catalog 的所有记录与重建 OID/字段映射结合，证明保留/新增/重建对象、未知字段、原权限/RLS/标签、入站依赖、序列设置和当前值。原声明图的 `Opaque`/`Physical` 记录只保留证据，并不证明它们已经恢复或可以忽略。

独立探针仍可能调用用户类型输入函数或折叠用户 IMMUTABLE 表达式，非事务性序列副作用不能靠 SAVEPOINT 回滚，还要求原 schema/tablespace 的 CREATE 权限。这些生产问题没有解决；不能用本轮图/值核验开放完整 Apply。需要原表达式复用与完整变更策略，随后接入锁后修订复核、完整证明、提交确认和 PG 引擎表单。

继承/分区记录已有投影，但本轮真实图场景为普通表及其外键/触发器/索引，不宣称完成分区/继承的全套设计或原生表单验收。真实大端、PG 14/18、多平台原生窗口和网络故障没有新增结果。本轮没有 UI/Python 改动、原生截图或 Navicat O/V，未启动原版、连接生产、提交或推送。

PG/SQLite 对象/导入/备份/隧道、跨库传输/同步、调度、ClickHouse、可配置 OpenAI 兼容 AI SQL、模型/逆向、BI、监控、字典、生成、脱敏及 CLI 仍属于 [完整目标](workbench-completion-2026-11-02.md)，不因本次后端阶段通过而缩小范围。
