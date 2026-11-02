# PostgreSQL 保留资产属性核验 — 2026-11-02

继续 [声明图核验](postgres-declaration-graph-2026-11-02.md)，把原目标资产的权限、注释、安全标签和未知属性加入真实原生改表验收的提交前检查。此阶段属于实现 I；PG 正式 `SchemaDesign` 仍只公开 Snapshot，Apply/CanApply 未开放。

## 实施与实际发现

新增 `VerifyPostgreSQLRetainedCatalog`。它复核原请求规划、完整修订、实际模型与目录的一致性及原范围，先比较实际目标与独立目标声明图，再按原始稳定字段/约束 ID 和定义身份比较保留资产。根表、行/数组类型、保留字段位置及保留序列必须保持原生身份，不能通过给新 OID/attnum 分配相同显示名称通过检查。

独立 CREATE 的默认权限、注释及内部状态不能替代原目录。已声明字段的语义依赖独立目标图检查；未知及未开放编辑的字段另外与原记录比较，保留精确 JSON 数字、嵌套对象、缺失/null 的区别。序列嵌套未知属性、依赖边的未知属性与其 typed 原边关联，原始依赖容器的未知属性随每个保留目录类/对象身份核验，不按数值 OID 重号串接。

Opaque 核验只允许原请求明确修改的表/字段/独立索引/用户触发器注释，以及表 RLS 与复制标识。原 ACL/owner、字段选项、行类型属性、约束注释、索引聚簇标记、内部 RI 触发器状态和其他未知属性继续要求保留。显式从 USING INDEX 切换到其他复制标识时，允许撤销原索引的对应标记，原标记不能覆盖这次请求。

安全标签按目录类、原生 OID、稳定资产身份和字段位置关联；保留约束重建后可以关联新的约束 OID，但标签必须保留。验证错误只报告类别/字段，不输出 SQL、注释、标签或数值内容。输入目录与草稿不被修改。

真实 PostgreSQL 回归在替换 CHECK 约束时发现原注释丢失，新增核验在 COMMIT 前拒绝操作。修复原生规划器，使保留约束的原注释在创建之后重新应用；独立保留索引重新应用原 CLUSTER ON 标记及仍被请求保留的 REPLICA IDENTITY USING INDEX。实际执行由 fixture 的原事务承担，失败仍回滚；不引入存储迁移或新的生产 Apply binding。

修复后真实操作覆盖：带注释的 CHECK 替换、唯一索引替换、聚簇标记、使用索引的复制标识、显式切换 FULL、带 ACL/字段选项/行类型注释/RLS policy 的类型变更。原生 SQL 操作结果经过提交前核验，并从服务查询约束注释及索引标记复核。

## 回归范围

纯回归覆盖：原请求授权范围、目录类/OID 与字段位置、用户索引/触发器注释、原约束/内部触发器/行类型属性丢失、标签移动/删除、未知精确数值、未知依赖元数据与原边绑定、依赖容器重建映射、序列嵌套未知字段、删除资产不继承原元数据，以及复制标识的显式覆盖。不会用新声明图相符给原属性丢失授予权限。

真实目录注入回归有 7 个子项：未知表/字段/行类型属性变化、表/字段权限丢失、约束注释丢失和聚簇标记丢失。它们用真实服务读取的完整快照，给实际与“新目标”注入相同属性，确认原目录核验仍拒绝；这些是证据变异测试，不声称执行了相应 SQL。

安全标签由 typed 注入记录验证。没有安装 PostgreSQL 安全标签 provider，也没有把该结果写成真实标签操作通过。已有 native CREATE/ALTER harness 对所有原表 alter 加入本核验；CREATE 和改名继续使用各自已有证明。

## 检查与证据

Linux amd64，Go 1.27.3 / MyGo v0.7.3-nyadb.2，HEAD `5960d26`，`GOWORK=off GOCACHE=/data/.cache/golang`；现有未提交成果保留，没有 commit/push。

| 检查 | 结果 |
|---|---|
| schemaddl / engines 默认回归 | 通过；0.144 / 0.444 秒。默认跳过的集成测试不计真实服务通过。 |
| contract/schemaddl/engines/dbruntime/objectworkspace race，20 分钟超时 | 通过；schemaddl 2.009 秒、engines 3.476 秒、dbruntime 1.067 秒、objectworkspace 17.227 秒。 |
| `go vet ./...` / `go tool mygo vet` | 通过。 |
| 翻译生成 / i18n check | 通过，1338 条 zh-Hans/en 消息，生成目录已是最新。 |
| `go test ./... -timeout=20m` | 通过；Shell 75.958 秒、objectworkspace 4.266 秒、schemaddl 0.172 秒、engines 0.372 秒，其余未变化包按 Go 的正常缓存机制复用。 |
| 完整自有 PostgreSQL 17.10 fixture `--race` | 18 个主场景及 14 个子回归通过，engines 332.208 秒；native plan 111.36 秒、新真实保留属性 41.48 秒、7 项属性变异 13.41 秒。 |
| `go tool mygo build` / 正式二进制身份 | 通过，27.0 秒；Linux amd64 正式程序及 deb/tar 已生成，哈希见下文。 |

中间运行全部保留，失败运行不计通过：

- `build/verify/postgres-k0qrm7`：加入新核验后已有 native plan / retained properties 通过，121.663 秒。
- `build/verify/postgres-M0Z6fZ`：新真实场景发现 CHECK 原注释丢失，5.898 秒，退出 1。
- `build/verify/postgres-hw7s33`：修复后首次原注释/聚簇/类型变更通过，12.883 秒。
- `build/verify/postgres-9EOmPy`：扩展真实复制标识及 7 项注入回归通过，34.241 秒。这是中间验收，最终以完整服务包记录为准。

正式二进制 `build/linux-amd64/nyadb` SHA-256：`4ea208eebbda5eb184c80c935331f9319b83b51eba3b13db3d8d4a344f5b8170`。构建通过不计原生窗口执行通过。本步没有 UI/Python 改动，因此没有重跑无关截图工具或窗口场景。

四个失败/中间运行均记录 `owned_container_removed=true`；另行 `docker inspect --type=container` 确认原容器不存在，各证据目录保存 `cleanup-audit.json`。失败运行仍记录退出 1，不计通过。

最终服务证据：`build/verify/postgres-qYWrM8`，含 `tests.log`、`versions.txt`、`resource.txt`、`validation.json` 及 `cleanup-audit.json`。`test_exit=0`，`owned_container_removed=true`；独立 `docker inspect --type=container` 确认原容器 `2f12ea5c3e0c01bea136152b75f3daba100e7990cdb6824a17f077b36dba3b72` 不存在。14 个子回归由已有 7 项声明图变异与新增 7 项保留属性变异组成。上述时间是该环境下的工具/服务验收耗时，不是 UI 性能基准。最终 `git diff --check` 通过。

完整重放：

```bash
GOWORK=off GOCACHE=/data/.cache/golang PATH=/usr/local/go/bin:$PATH \
  tools/verify_postgres_fixture.sh --race
```

新定向场景可加 `--run 'TestPostgreSQL17DesignRetainedCatalog.*Fixture'`；不能代替完整服务包验收。

## 明确未完成的证明与下一步

本核验只覆盖原目标的**保留资产属性**，不等于完整原 catalog 保留证明。Physical 中的表/TOAST 文件、缓存、缺失值和重写状态仍需明确的变更许可与值核验；掉落字段残留记录、新资产初始属性、非目标/入站依赖、policy/rewrite/publication/外部类型及对象的完整保留仍需独立检查。原 catalog 范围随类型/依赖闭包变化时还需按原捕获身份补读，不能把从新快照闭包消失视为实际删除或授权删除。

约束拥有的索引属性、内部 RI 触发器状态及真实安全标签如未被原生操作保留，本检查会拒绝；本阶段没有给所有这类对象提供完整恢复执行器。序列的设置通过声明与原未知属性检查；当前值及非事务性副作用仍需额外策略。

独立 CREATE 探针可能调用用户类型输入函数、折叠 IMMUTABLE 表达式并产生不能由 SAVEPOINT 回滚的序列副作用，还要求原 schema/tablespace CREATE 权限。这些生产问题、完整依赖锁定/并发冲突策略、创建/分区/继承迁移、锁后正式重规划与完整提交确认没有解决。须先完成这些证明与执行协议，再接入 PG 原生表单及正式 Apply。

本步没有 UI/Python 改动、原生窗口截图、Windows/macOS 或其他 PG 版本/大端运行结果，没有新增 Navicat O/S/V。

完整目标仍包含 PG/SQLite 对象 CRUD/导入/备份/隧道、跨库传输及数据/结构同步、调度、ClickHouse、可配置 OpenAI 兼容 AI SQL、模型/逆向、BI、监控、字典、生成、脱敏、CLI，以及编辑器/设计器细节和原生/原版验收。具体清单见 [工作台剩余功能](workbench-completion-2026-11-02.md)，不因本后端阶段通过而标记整体完成。
