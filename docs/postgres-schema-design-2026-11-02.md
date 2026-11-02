# PostgreSQL 结构目录与事务元数据变更 — 2026-11-02

本阶段建立 PG 表设计的完整目录基线和首批事务元数据变更后端。**PG 图形设计尚未完成**：`Session.SchemaDesign()` 只公开 Snapshot，Apply/CanApply 仍为空；不会把 PostgreSQL 路由进 SQLite 表单或重建流程。[完整工作清单](workbench-completion-2026-11-02.md) 继续有效。

## 实现 I

新增中立 PostgreSQL catalog/property 模型、结构声明和确定性修订；engines 读取原 profile/catalog/schema/table/generation 的公开目录，dbruntime 负责绑定和 generation 翻译。没有数据库迁移、明文密钥回退或新公开生产入口。

- 完整记录保留在 catalog JSON，包含表、字段、默认值、约束、索引、内部/用户触发器及启用状态、身份序列设置、继承/分区、依赖视图/外键、RLS/policy/rule、例程/聚合、类型/domain/enum/range/composite、cast、collation、operator/access method/operator family/class、publication、default ACL、security label、extension 和 dependency。全局 DDL event trigger 及其函数依赖也进入基线。
- 字段按 attnum、约束按 OID 保留稳定 ID。身份列保留序列名称、起始/边界/步长/cache/cycle；约束所属索引和内部触发器不进入可编辑定义，用户 constraint trigger 保留。
- 独立物理连接使用 `REPEATABLE READ READ ONLY`；局部设置 search_path/字符串语义并在 commit/rollback 后恢复。最多 10000 条记录、每条 1 MiB、整体 16 MiB；失败返回空快照，禁止截断基线。修订保留精确 JSON 数值，忽略行顺序/空白、表行数据、序列当前值及优化器统计。
- `PostgreSQLStructuralDDL` 仅是结构声明，**不是完整备份或无损恢复脚本**。ACL grantor、security label、publication 等完整元数据保存在 catalog，不能用声明文本代替这些记录。

首批纯 `PlanPostgreSQLMetadata` 和私有 `postgresDesignMetadataApply` 支持已有普通表的表/字段注释、字段 storage、compression、statistics。更名、创建、删除、改类型/默认值/约束/索引/触发器/权限及未知属性变化明确拒绝；分区和继承子表也拒绝。注释去除使用 `IS NULL`；引用标识符和文本独立转义。

执行前深复制请求，重新从完整 catalog 验证模型和修订；取得原表 ACCESS EXCLUSIVE 锁后才建立 catalog 读取快照。所有语句在同一事务内执行，之后比较完整目录，仅允许请求的属性变化。未知键、精确数值、外部依赖、ACL、索引和策略不获得隐式重写权限。已有启用的 event trigger 会被拒绝，禁用 hook 保留。

这个后端不读取或重写表行数据。真实 fixture 从数据库侧额外核对精确数值、NULL、bytea、ctid 和身份序列当前状态，但该 fixture 结果不等于通用类型转换核验。完整转换、保留值/依赖核验和图形编辑仍待实施。

失败保留原草稿；未发送 COMMIT 且确认 rollback 时报告 rolled_back。COMMIT 已发送后若回执丢失，即使随后 PostgreSQL 接受 ROLLBACK，也只能报告 uncertain、清除 Actual/Verified 并废弃物理连接，禁止重放。取消或会话关闭后驱动可能关闭连接；不能确认 rollback 时同样报告 uncertain。

修复真实 fixture 发现的空数组复制问题：原先克隆把 `[]` 变成 `nil`，完整 schema signature 因而变化。模型改用保留空数组语义的 `slices.Clone`，新增修订兼容和深复制回归。

## 验证

Go 1.27.3 / MyGo v0.7.3-nyadb.2，Linux amd64，`GOWORK=off GOCACHE=/data/.cache/golang`；HEAD `5960d26`，变更未提交。

| 检查 | 实际结果 |
|---|---|
| `go test ./... -timeout 20m` | 最终通过；Shell 74.595 秒、dataworkspace 53.902 秒、objectworkspace 3.419 秒。默认跳过的服务集成测试不计为真实服务通过。 |
| contract/schemaddl/engines/dbruntime/objectworkspace race，20 分钟超时 | 最终通过；engines 4.263 秒、objectworkspace 18.144 秒。本次没有重跑 Shell/dataworkspace 全包 race。 |
| `go vet ./...` / `go tool mygo vet` | 通过。 |
| 翻译生成 / i18n check | 通过，1338 条 zh-Hans/en 消息。 |
| `go tool mygo build` | 通过，27.3 秒；Linux amd64 正式程序及 deb/tar。 |
| `git diff --check` / fixture 脚本 `bash -n` | 通过。 |
| 真实 PostgreSQL 17.10 disposable fixture `--race` | 通过；目录读取、事务元数据、丢失 COMMIT 回执及既有运行/配置测试均实际执行。资源和最终日志见下方证据记录。 |

目录 fixture 覆盖精确大数、身份设置、生成/删除字段、延迟/NOT VALID 约束、表达式/包含/部分索引、用户/内部/constraint trigger、外部类型/复合属性/cast/operator family、RLS/ACL/publication、外部视图/外键/继承/分区。每类结构变更改变修订，DML/ANALYZE/VACUUM 不改变；并发提交的 COMMENT 不混入同一 MVCC 快照；超限记录明确失败并可重读。

元数据 fixture 覆盖注释引号/反斜线/中文/删除、storage/compression/statistics、完整目录属性保留、依赖视图和数据/ctid/序列不变、第二条语句错误的整批回滚、旧修订拒绝、锁等待期间另一事务提交后的冲突、请求复制、取消/会话关闭/占满连接池、物理设置恢复、启用/禁用 DDL hook 和无效原范围。独立回执测试在真实 PG 已提交之后由测试 connector 丢弃 COMMIT 回执；这是**真实服务加注入回执故障**，没有声称实际网络断线测试。

早期 `postgres-Go9zyu` 因空数组复制引发修订冲突失败，修复后通过；`postgres-HKXWdz` 的取消测试错误地要求总能确认回滚，实际驱动已关闭连接，后按协议核对 rolled_back/uncertain 和禁止替换草稿。保留失败日志，不将其计为通过。一次从 `/tmp` 使用相对脚本路径退出 127，随后用绝对路径实际运行；脚本也已自行定位仓库根目录。

本阶段没有修改 Python 采集工具，没有新增 PG 原生窗口截图，也没有启动 Navicat 或连接生产。现有只读 binding 的 runtime/Tester 回归通过，不能据此声称 PG 图形编辑通过。正式构建 SHA-256 为 `f142d011d3370bbe9ca321016199f5ef053ff9c93853dc42b8a09dde0df9a7f5`；之前 SQLite 截图仍使用其报告中记录的旧二进制哈希。

## 可重复工具与证据

```bash
GOWORK=off GOCACHE=/data/.cache/golang PATH=/usr/local/go/bin:$PATH \
  bash /data/kang/nyadb/tools/verify_postgres_fixture.sh --race
```

脚本只创建自己的唯一命名、唯一 ownership label 的 PG 17 容器，端口随机绑定 localhost。保存返回的 container ID，readiness/port/版本/清理均使用该 ID；删除前检查原 label。EXIT/INT/TERM 清理只针对本次资源，readiness 超时明确失败；未知参数拒绝。配置、版本、退出码和日志写入忽略的 `build/verify/postgres-*`。一次成功清理不是强杀或 Docker daemon 故障的清理验收。

代码最终真实 race 证据 `build/verify/postgres-TKELQa`：全部测试通过，包耗时 29.316 秒；原容器 ID `6e0bd2f6858a7450c58802aea9528a3fff0179bf369e6f355b05c103c5466b3f`，退出 0、记录已清理，独立 inspect 确认不存在。

脚本最终版从 `/tmp` 成功重放，证据 `build/verify/postgres-gko2Nn`：目录 11.78 秒、元数据 14.41 秒、注入的 COMMIT 回执丢失 0.58 秒，包耗时 28.346 秒、退出 0、readiness=true。原容器 ID `e607d7a012ca3a8ca75a3f86ee330233d94f87cc2f514179314c26306a658979`；清理记录 true，独立 inspect 确认不存在。全部运行只使用自有临时服务。

## 剩余工作与证据边界

PG 14 的 catalog 代码按 publication 目录差异做版本分流，但没有该版本的实际运行结果；真实服务只验证 PG 17.10。其他版本、只读账户完整目录访问、故障网络以及 Windows/macOS 原生行为仍未验收。这个私有 metadata 后端没有通用名称/结构转换、完整依赖锁集合或并发管理员启用全局 DDL hook 的防护，不能据此开放完整图形设计。

后续继续：PG 原生 ALTER 规划、锁下冲突检查、类型化保留值与依赖核验、完整回滚/不确定协议、PG 表单和原生验收；PG/SQLite 对象 CRUD、导入/备份及隧道；跨库传输/数据及结构同步；任务调度；用户优先选择的 ClickHouse；可配置 OpenAI 兼容 AI SQL；模型/逆向、BI、监控、字典、生成、脱敏、命令行。证据是实现 **I** 和 NyaDB 测试，新增 Navicat **O/V** 仍 **Unknown**。
