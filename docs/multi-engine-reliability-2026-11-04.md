# 多引擎可靠性与维护交付记录（2026-11-04）

## 本轮行为

产品仍支持 MySQL、PostgreSQL、SQLite、ClickHouse 四种引擎。本轮按连接可靠性、公共边界、导入、备份恢复、对象维护五个阶段实施；没有增加数据库类型或 PostgreSQL 账户、集群备份、跨库同步及隧道功能。

| 引擎 | SQL 与后台资源 | 导入 | 备份恢复 | 对象与维护 |
| --- | --- | --- | --- | --- |
| MySQL | 保留原有会话和传输行为 | 保留原有向导与执行器 | 保留 `.nyabackup` 格式及恢复行为 | 保留管理接口，维护提供中立适配 |
| PostgreSQL | SQL 与后台独立池；保留 SQL 会话上下文 | 共享向导、六种模式、新建或已有普通表 | 官方 custom-format `.dump`，新库或明确确认的覆盖恢复 | PG 14–18 普通表复制/删除、普通视图管理及表维护 |
| SQLite | SQL 与后台独立池；每条连接初始化附加库和 PRAGMA | 共享向导、六种模式、新建或已有表 | 在线备份/恢复，每次一个实际文件 | 复用表/视图管理，新增维护与诊断 |
| ClickHouse | 保留现有适配器 | 保持原能力范围 | 保持原能力范围 | 保持查询、元数据及导出等原能力范围 |

### 1. SQL 会话和连接预算

- `QueryLease` 的公开调用方式不变。适配器记录应用最后成功设置的 schema，相同上下文不再插入 `SET search_path`；用户执行的 `SET` 保留到明确切换 schema 或替换物理租约。
- 失败事务里的独立 `ROLLBACK`、`ROLLBACK TO SAVEPOINT` 不受待设置 schema 阻塞。恢复语句不会隐式重连或自动回滚；混合 SQL 批次不使用这项豁免。
- PG 原 `MaxConnections` 仍是后台上限，新增 SQL 上限；SQLite 两项分别配置。新增默认值均为 8，范围 1–64，旧公开配置补默认值，不迁移配置数据库表。
- 预连接和首次执行非阻塞申请 SQL 配额。超额返回 `ErrSQLSessionLimit`，释放物理资源后归还配额；关闭、替换、断开可取消等待中的拨号。SQL 标签可关闭其他标签后直接重试，不被误标为连接丢失。
- 代码：`internal/services/engines/session.go`、`adapter.go`、`internal/db/sqltext/rollback.go`、`internal/ui/sqlworkspace/execution.go`。

### 2. 公共边界与能力

- `dbruntime` 合并产品声明、实际 provider、公开 generation 和对象能力。表设计、对象管理、视图、维护、账户、导入、备份、恢复各自判断，不再让 `DesignTables` 控制整套管理入口。
- `contract` 新增维护及备份恢复请求、计划、进度、结果与可用性能力；运行时负责公开/内部 generation 转换。UI 使用已发布状态，版本、权限、对象限制在 worker 预检查。
- 备份计划包含原目标、归档摘要与封印；SQLite 覆盖由独立维护操作持有任务所有权。主动排空 SQL 会话不会把自己的恢复任务一并取消；取消、退出、目标替换仍能撤销它。
- 提取公共 SSH/HTTP 选项和独立传输配置，旧 MySQL 名称使用兼容别名/转发。MySQL JSON、凭据键、布局键及 bootstrap 入口保持兼容；PG 未实现的隧道继续明确拒绝。
- 代码：`internal/db/contract/{maintenance,backups}.go`、`internal/services/dbruntime/{capabilities,maintenance,backups}.go`、`internal/services/connections/transport.go`。

### 3. 共享导入

- 复用原文件读取器、映射、预览、实际建表 DDL、临时流文件和逐行结果工作区。中立 `importspec` 提供目标引擎类型/校验策略，MySQL 专属规则保留在 `mysqlspec`。
- 支持追加、更新、追加或更新、仅插入未匹配行、删除匹配行、清空后导入。匹配模式要求完整、可靠的唯一键；生成列禁止写入，PG 身份列由引擎规则处理。
- 每次导入一个独立后台连接和一个事务。逐行 savepoint 隔离“遇错继续”；清空使用可回滚 `DELETE`，不关闭外键检查。源数据和逐行日志流式处理，内存结果摘要最多 1,000 行。
- 保留 NULL、二进制、类型化值和精确数值。普通 CSV 无 Kind 的值按原文传入。SQLite 自动推断优先使用 TEXT 保存精确小数；REAL/FLOAT/DOUBLE 对无法精确表示的整数或精确小数明确拒绝，避免 SQLite affinity 静默舍入。
- 取消等待执行器回执。提交回执丢失或回滚无法确认时返回不确定状态，保留原数据/结果供核实，禁止自动重放。
- 代码：`internal/services/engines/import.go`、`internal/db/importspec/`、`internal/ui/dataworkspace/toolbar_transfer*.go`。

### 4. 备份恢复

**PostgreSQL**：工具目录优先于 PATH；预检查服务端与 `pg_dump`/`pg_restore` 主版本，首轮只接受 14–18 的同主版本组合，归档主版本也需匹配。进程使用结构化参数、0600 私有 pgpass 文件及原操作 context，密码不进入命令行、日志或历史。支持整库、schema、选定表；部分备份展示外部依赖，不包含集群角色或物理灾备信息。

恢复前读取归档目录并绑定文件摘要、原连接、目标数据库身份。默认省略 owner/ACL，可显式保留并预检查角色。新库恢复处理模板自带的 public schema；已有目标使用过滤后的目录、`--clean --if-exists --single-transaction --exit-on-error`，保留已存在的 schema 和归档外对象，不使用 CASCADE。无成功回执不宣称完整回滚。新库创建成功而后续恢复失败时可能留下目标库，结果会保留该目标供检查，不自动删除。

**SQLite**：调用 modernc 在线备份/恢复 API，每批 128 页报告进度；main 和配置中的附加库均可选择，一次只操作一个文件。新输出先写临时文件，完整性检查成功后无覆盖发布。覆盖前经过草稿/事务离开守卫与明确确认，暂停所有引用同一物理文件的应用会话，阻止重新打开，使用在线恢复更新原文件，避免直接替换 WAL 或打开中的文件。外部锁明确失败。完成后按原 profile/generation 刷新缓存，保留 SQL 正文并要求显式恢复会话。

两引擎共用 `objectworkspace` 的备份恢复面板、预览、进度、取消与结果。覆盖输入与预览不一致时不能执行；异步离开守卫取消后恢复面板并撤销旧 continuation。

### 5. 对象与维护

- PG 普通表删除使用 RESTRICT；同 schema 复制可包含数据，处理受支持的约束、索引、注释、自有序列和自引用选项，触发器在数据复制后安装。执行前锁定并检查原修订；不能完整表达的定义（如扩展统计、复制不支持的 rewrite rules/security labels）明确拒绝。
- PG 普通视图支持创建、修改、改名、删除。修改使用 `CREATE OR REPLACE VIEW` 保留依赖，不兼容的列形状变化拒绝；物化视图保持只读。
- PG：VACUUM、ANALYZE、VACUUM ANALYZE、REINDEX TABLE。SQLite：VACUUM、ANALYZE、integrity_check、foreign_key_check。PG 维护预检查版本、普通表边界及实际权限，避免 PostgreSQL 仅发 warning 却被界面当成成功。
- 后台独立连接执行维护，计划说明是否可事务执行；结果区分完成、已回滚、失败及不确定。完成事件携带原 Profile/Catalog/Schema/Generation，晚到或停放回执不会刷新新的连接。

## 验证证据

所有最终命令使用 Go 1.27.3 和 `GOWORK=off`，确保使用 `go.mod` 固定的 MyGo `v0.7.3-nyadb.2`，不使用本地研究 worktree 的替换模块。下列 `build/` 内容是本次本地证据，不纳入版本控制。

| 检查 | 证据 |
| --- | --- |
| 全量 Go 测试：通过 | `build/multi_engine_go_test_final.log` |
| 生命周期包完整 race：通过 | `build/multi_engine_race_final.log`；shell 包 1,067.542 秒，无 race 报告 |
| 最新恢复守卫、配额关闭、SQLite 精度及能力撤销 race 回归 | `build/multi_engine_race_guard.log`、`multi_engine_quota_close.log`、`multi_engine_sqlite_precision.log`、`multi_engine_capabilities_final.log` |
| 最终临时资源清理修复后的 engines/dbruntime 完整 race | `build/multi_engine_backup_cleanup_race.log` |
| Go vet / MyGo vet | `build/multi_engine_vet_final.log`、`multi_engine_mygo_vet_release.log` |
| 中英文翻译检查 | `build/multi_engine_i18n_release.log`，1,536 条 |
| Python 工具测试 | `build/multi_engine_python_final.log`，64 项 |
| Linux amd64 / Windows amd64 / macOS arm64 无 CGO 构建 | `build/multi_engine_build_{linux,windows,darwin}.log`；产物在 `build/cross-platform/` |
| MySQL 真实备份恢复、对象、SQL、表设计回归 | `build/multi_engine_mysql_core.log` |
| MySQL 真实 SSH/HTTP 物理会话、事务、撤销回归（race） | `build/multi_engine_mysql_tunnels.log` |
| ClickHouse 真实 adapter/shell 回归 | `build/multi_engine_clickhouse.log`、`build/verify/clickhouse-dvDpbg/` |

上述检查均已通过，机器可读索引与构建摘要见 `build/multi-engine-delivery.json`。生命周期完整 race 包：`dbruntime`、`engines`、`sessionbrowser`、`sqlworkspace`、`dataworkspace`、`objectworkspace`、`shell`。固定依赖下以 `-race -timeout 30m -p 2` 执行；最后新增的精度和菜单撤销保护另有对应 race 日志。早期默认十分钟超时的旧 race 日志不作为通过证据。

### PostgreSQL 14–18 实测矩阵

| 服务端版本 | 主要功能证据目录 | 最终 SQL 回滚/schema/拨号取消证据目录 |
| --- | --- | --- |
| 14.24 | `build/verify/postgres-5nPkwC/` | `build/verify/postgres-vAfoB7/` |
| 15.19 | `build/verify/postgres-z6zTBf/` | `build/verify/postgres-fV3I4w/` |
| 16.15 | `build/verify/postgres-WxaJB8/` | `build/verify/postgres-rTcjat/` |
| 17.10 | `build/verify/postgres-ZXiB1Q/` | `build/verify/postgres-OY0780/` |
| 18.6 | `build/verify/postgres-ytCtdy/` | `build/verify/postgres-WsuTzr/` |

各目录含 `tests.log`、`versions.txt`、`resource.txt`，主要功能和最终 SQL 回归均 `test_exit=0`，自建容器 `owned_container_removed=true`。测试函数名沿用 `TestPostgreSQL17...Fixture`，实际版本由隔离容器选择；不能把名称中的 17 当成仅验证 PG 17。

覆盖六种导入模式、精确值、生成/身份字段、完整唯一键、FK 与 DELETE 回滚、逐行 savepoint、11,000 行流式处理、源取消/日志失败/提交回执丢失；覆盖官方归档新库/覆盖、归档外对象保留、外部依赖冲突、损坏与变更归档、工具缺失与主版本不匹配；覆盖完整表复制、独立序列、自引用、视图形状与依赖限制以及维护。

复现主要功能矩阵（每个版本执行一次，工具目录须包含同主版本官方客户端）：

```sh
GOWORK=off NYADB_POSTGRES_TOOLS=/path/to/postgresql/18/bin \
  tools/verify_postgres_fixture.sh --postgres-version 18 \
  --run 'TestPostgreSQL17(BackupRestore|BackupPrechecks|ImportsAndMaintenance|ImportLostCommit|ImportStream|ObjectOperations|Options|QueryLease)Fixture'
```

本机 14–17 工具位于 `temp/pg-tools/<major>/usr/lib/postgresql/<major>/bin`，18 位于 `/usr/lib/postgresql/18/bin`。`NYADB_POSTGRES_WRONG_TOOLS` 可提供其他主版本以运行不匹配检查。SQL 补充矩阵使用 `--run 'TestPostgreSQL17Query(Lease|DialCancellation)Fixture'`。

恢复临时目录清理修复后另用 PG 18 官方工具复测备份恢复：`build/multi_engine_pg18_cleanup_final.log`、`build/verify/postgres-qZ97Db/`，通过且已清理自建容器。

### 真实 Linux 窗口

固定 MyGo 构建，在 GTK/Xvfb 原生窗口执行工作区真实 action，分别采集 1280×800 和 840×560。包含导入读取/预览/提交、维护预览/执行、备份、恢复新目标和覆盖恢复；脚本检查实际提交行数、精确值及恢复后的数据。此处是原生工作区验收，未将其描述为鼠标从 shell 导航起点操作的完整端到端测试。

- SQLite：`build/multi-engine-native/run-cpklkuqc/`。
- PostgreSQL 18：`build/multi-engine-native/run-younok_9/`；服务端资源记录 `build/verify/postgres-xAY6Wr/resource.txt`。
- 每个目录有 `results.json`、`import-verified.json`、`maintenance-verified.json`、fixture 日志和 `screenshots/`。结果包含两个工作区退出码 0、`display_stopped=true`；PG `native_exit=0` 且已清理自建容器。
- 精确导入核对值包含 `9007199254740993.123456789` 和 `0.01`，提交 2 行、失败 0 行；恢复分别核对新目标和覆盖目标。
- 已检查紧凑尺寸的覆盖确认、会话暂停提示及导入结果，主操作按钮在窗口范围内。额外 SQLite REAL 整数精度修复由真实文件 race 回归覆盖，不影响这里使用 TEXT/NUMERIC 的验收路径。

代表截图：

- [SQLite 覆盖恢复 840×560](../build/multi-engine-native/run-cpklkuqc/screenshots/overwrite-plan-840.png)
- [PostgreSQL 覆盖恢复 840×560](../build/multi-engine-native/run-younok_9/screenshots/overwrite-plan-840.png)
- [PostgreSQL 导入结果 840×560](../build/multi-engine-native/run-younok_9/screenshots/import-run-840.png)
- [SQLite 维护预览 1280×800](../build/multi-engine-native/run-cpklkuqc/screenshots/maintenance-plan-1280.png)

复现入口为 `tools/verify_multi_engine_native.py`（需 GTK、Xvfb、截图工具依赖），PG 可使用 `tools/verify_postgres_fixture.sh --postgres-version 18 --native ...`，由脚本创建并清理独立服务器。SQLite fixture 文件和归档保留在对应 `build/` 目录便于审查；测试进程、显示服务和 PG 容器均退出。备份恢复服务创建的私有凭据、临时归档/目录文件在操作结束后删除。

最终资源核对发现并修复了 SQLite WAL 模式下临时恢复副本的 `-wal`/`-shm` 残留：恢复副本和 SQLite 输出暂存使用操作专属目录，在驱动/进程关闭后统一删除。真实 SQLite 测试检查成功及归档变更拒绝路径均无暂存文件；`build/multi_engine_resource_cleanup.json` 记录此前测试遗留的 14 个 sidecar 文件，在确认主暂存文件不存在、没有进程占用后完成清理。最终清单记录私有备份临时资源剩余数量为零。

## 边界

本轮变更按 SQL 可靠性、公共能力与接口、导入、备份恢复、对象维护拆分为五个实现提交，验收工具与本记录另行提交；未发布安装包。PG 新增对象写入遵循 14–18 普通表/普通视图边界；未知或无法完整复制的定义显式拒绝。SQLite 一次备份不是多个附加文件的原子快照；外部锁不强行解除。恢复、导入或维护失去提交确认时，界面结果可能为不确定，需要核对目标后再决定后续操作。
