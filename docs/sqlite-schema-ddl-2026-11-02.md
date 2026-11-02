# SQLite 结构设计后端 — 2026-11-02

本轮完成 SQLite 图形表设计所需的独立结构模型、规划与事务执行后端。当前尚未接通原生设计器、创建/改名/删除对象菜单；`DesignTables` 的产品描述保持原有状态。不能把后端测试通过写成 SQLite 图形表设计已交付。

历史边界：本报告记录后端交付时的状态。随后已接通原生设计器和创建/改名，并完成 Tester/正式 Linux 窗口执行；见 [后续 SQLite 原生设计器交付记录](sqlite-designer-2026-11-02.md)。下文原始检查与二进制身份保持各自的历史范围，表删除/复制及 PG 仍未完成。

完整目标继续有效，包括 PostgreSQL/SQLite 图形设计、导入/备份及隧道、跨库工具、自动运行、ClickHouse、可配置 OpenAI 兼容 AI SQL 和其余高级功能；见 [完整工作清单](workbench-completion-2026-11-02.md)。

## 实现 I

- `internal/db/schemaddl` 是独立纯 Go 包，不依赖 UI、连接、存储或驱动。草稿保留完整字段尾定义、表约束、显式索引/触发器 SQL 和稳定 clause ID，保留 STRICT/WITHOUT ROWID 选项。字段名、定义片段及单条 DDL 有边界校验；不把调用者的预览 SQL 作为执行计划。
- 独立词法器保护普通 SQL 字符串、PG E/dollar 字符串、嵌套注释和 SQLite bracket/backtick 标识符。普通字符串不采用 MySQL 反斜杠转义。SQL 限 1 MiB/65536 tokens/128 层；单表定义限 1 MiB，完整显式定义限 16 MiB、1000 个。
- 新增中立 `contract.SchemaDesignProvider`。dbruntime 返回捕获原 session 的操作绑定，检查原 profile/公开 generation，且仅在运行时边界翻译为物理 generation。快照、请求及回执深复制切片，拒绝另一范围的快照/核验回执；原范围的迟到 commit 回执仍保留供后续 UI 核对。
- SQLite 完整快照读取原表 DDL、显式索引/触发器、整个原命名空间的 schema SQL、rootpage 和 schema_version。缺失的新表可以取得带版本的空快照；虚拟表、AS SELECT、不完整定义及可见临时触发器明确拒绝。快照不截断后继续允许编辑。
- 规划器区分创建、原生表/字段改名、仅定义修改和重建。表/字段改名独立执行，保留 SQLite 原生依赖改写。仅改索引/触发器时，事务内更新这些定义，不复制数据。删除字段要求显式确认。
- 执行占有一条物理连接并使用 BEGIN IMMEDIATE。在任何写入前重新检查完整快照；原命名空间内的并发 DDL，包括其他表的变化，均导致修订冲突。只读连接和只读附加库在执行前拒绝写入。
- 重建在同一附加 schema 内创建随机 shadow table，按稳定字段 ID 复制；生成字段不进入 INSERT。原始与复制后的保留值通过流式存储类别、原始字节/IEEE 浮点和 SHA-256 行摘要的 256-bit 多重集总和核验，另核对行数。NULL、重复行、长 BLOB 不使用 SQL CAST、排序规则、分页或单元格截断。校验是密码学摘要验证，非逐字节存储文件比较。
- rowid 表复制并核验隐藏身份；WITHOUT ROWID 保留相同模式。保留 AUTOINCREMENT 高水位，包括已删去高 ID 的空表。复制在触发器创建之前完成；原表被替换后恢复完整显式索引和触发器。
- 重建临时关闭 FK、使用 legacy_alter_table 保留外部原名称引用；原生改名关闭 legacy 模式以启用依赖改写。检查 foreign_key_check、所有原命名空间视图以及 INSERT/UPDATE/DELETE 的触发器编译；额外拒绝 SQLite 会静默接受的失效 UPDATE OF 字段。
- 取消等待底层回执；失败先回滚，再恢复连接原有 PRAGMA。回滚/恢复不能确认时丢弃物理连接，报告 uncertain 或连接恢复失败。只有明确 commit 回执和结构核验通过才报告 verified committed。
- SQLite 会话增加可取消的 schema/任意 SQL 协调：结构读取/执行排他，任意 SQL 与物理 lease 创建使用共享入口，等待的结构写入不会被后续 SQL 饿死。排他范围内检查所有本会话拥有的 SQL 物理连接上的临时触发器，取消等待不撤销已有 SQL lease。
- 为避免手工 BEGIN/SAVEPOINT 与结构排他形成等待环，结构操作在开始前检测 SQL lease 的显式事务并拒绝执行，不提交/回滚用户事务。检测使用 SQLite 的公开 `foreign_keys` 在事务中不能更改的行为，在排他范围内短暂探测并恢复原值；恢复失败丢弃连接，不读取驱动内部状态。

## 实际回归范围

Go 测试使用真实 modernc SQLite 文件与独立临时目录，不使用 SQL mock：

- NULL/二进制/超过 1 MiB 的 BLOB、64-bit 整数、精确数值文本、生成字段、重复行和 rowid 间隙。
- 普通/空表的自增高水位，外键 ON DELETE CASCADE 不在重建时执行，触发器不在复制时执行，视图/显式索引/触发器在改表后可用。
- 附加命名空间与引号标识符、实际已限定 schema 的索引定义，以及原 main 表隔离；只读 main/attachment 拒绝改表。
- 字段类型转换、CHECK、唯一索引冲突、视图/触发器失效、UPDATE OF 失效和存储列转为生成列全部回滚，原值和原定义保留。
- 原生命名依赖改写、原有 foreign_keys/legacy_alter_table 设置恢复、结构修订冲突、原 profile/generation 拒绝、锁等待取消、无 shadow 泄漏、字段删除确认和 SQL 片段逃逸拒绝。
- runtime 的公开/物理 generation 翻译、输入切片隔离、撤销后旧绑定拒绝、`Require` 不能绕过 wrapper，且迟到回执不改写到新的 profile/generation。
- 另一条本会话 SQL 连接上的实际临时触发器被读取/执行前检查发现；删除该触发器后可以正常改表。手工 SQL 事务在 FK ON/OFF 两种状态下被明确拒绝，COMMIT 不被排他等待阻塞，原 SQL 文本结果/连接 PRAGMA 保留；SQL/结构等待可取消且原 lease 继续可用。

开发中发现并修正新增字段被排列在表级约束之后的 SQLite 语法错误。初始 fixture 给生成字段传入值被 SQLite 拒绝，改为只插入普通字段；不把该失败视为通过。增加回执范围检查后，原 scope 测试 fixture 的空目标名称也被正确拒绝，补齐 fixture 的实际目标后验证通过。

## 验证记录

Go 1.27.3 / MyGo v0.7.3-nyadb.2，Linux amd64，`GOWORK=off`、`GOCACHE=/data/.cache/golang`。HEAD 保持 `5960d26`，全部变更未提交。

| 检查 | 本轮实际结果 |
|---|---|
| `go test ./... -timeout 20m` | 最终通过；Shell 74.831 秒、dataworkspace 50.105 秒、sqlworkspace 2.873 秒。未启用依赖外部服务的集成开关，跳过的服务测试不算新增通过证据。 |
| schema/contract/engines/dbruntime race | 最终通过；engines 3.690 秒、dbruntime 1.081 秒。 |
| SQL/sessionbrowser race | 通过；sqlworkspace 27.767 秒、sessionbrowser 1.127 秒。 |
| Shell 真实 SQLite 范围 race | 两项原连接 SQL/断开保留文本和附加库导航/写入测试通过，17.605 秒。本轮未重跑 Shell 全包 race。 |
| `go vet ./...` / `go tool mygo vet` | 通过。 |
| `go tool mygo generate` / `go tool mygo i18n check` | 通过；1324 条 zh-Hans/en 消息。 |
| `go tool mygo build` | 通过；Linux amd64 正式程序与 deb/tar 包，24.9 秒。 |
| SQLite 表定义解析器 fuzz | 本轮 20 秒目标运行通过，实际 21.037 秒、187334 次执行；没有 panic 或无法重新解析的渲染结果。后续聚合大小边界和会话协调增加后，由上述最终单元/race/全量检查复验。 |
| `git diff --check` | 通过。 |

本轮没有修改 Python 采集工具，未重复 Python 测试；没有新增真实 PostgreSQL/MySQL 服务运行或原生 GUI 验收。SQLite 回归均使用真实本地数据库文件。

最终打包二进制 `build/linux-amd64/nyadb` SHA-256 为 `b8dd7077338f7227af79d61b2491d79aa8b8b88e7e0bf1ad181529571c1b751e`。此哈希仅标识本轮构建，没有关联新的原生截图；旧原生验收报告保留各自的二进制身份。

## 边界与下一步

这部分尚无新的 `ui.NewTester` 或原生窗口证据：本轮没有改变可见 UI，也没有把后台能力直接开启成 MySQL 管理菜单。PG 的 SchemaDesign binding 当前为空，不能把中立接口存在解释为 PG 设计已实现。

下一阶段在 objectworkspace 中接入原生字段/约束/定义表单与草稿守卫，Shell 协调原连接、命名空间、缓存及标签；私有结果通道由 UI 消费，关闭/代次改变后撤销回执。补新建/编辑/改名的统一可用性和 Tester、真实原生窗口截图。后续 PG 设计需完整 catalog 快照与独立事务规划，不复用 SQLite 重建或 MySQL DDL。

当前限制：

- 结构快照的 revision 覆盖整个原命名空间；其他表的 DDL 也需要用户重新读取。此范围较宽，避免漏掉视图和 FK 依赖。
- 不支持切换 rowid/WITHOUT ROWID 模式、全部 rowid 别名都被用户字段遮蔽的重建、把保留存储值改成生成列、一次同时改名并修改其他定义；均需分开操作或独立数据迁移。
- 字段类型变更若改变保留值的 SQLite storage class/字节表示会拒绝，不自动接受有损转换。精确十进制按 SQLite 实际存储类型验证；TEXT 精度保真不代表 SQLite NUMERIC 能无损表示任意十进制。
- 临时触发器检查覆盖本会话拥有的 SQL 物理连接和结构连接；同名目标的临时触发器会保守拒绝。手工 SQL 事务需显式结束后再设计；结构操作不自动结束它。
- SQL 边界解析不是完整方言语义解析，无法安全识别的合法特殊触发器定义也可能被明确拒绝。虚拟表及更复杂依赖需要独立能力支持。

没有启动 Navicat、生产连接、外部发送、commit/push 或数据库迁移。新证据属于独立实现 I；Navicat O/V、PG 图形设计、Linux 新设计器原生验收及 Windows/macOS 原生结果仍 Unknown/未完成。
