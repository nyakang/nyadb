# PostgreSQL 与 SQLite 真实运行接口

阶段 5 使用 pgx v5.8.0 的 database/sql 适配器和 modernc.org/sqlite v1.61.0。配置数据库由 localdb 独立持有句柄；用户 SQLite 文件由 engines.Session 持有另一组句柄。正式运行不借用配置库执行用户 SQL。

## 公共契约与身份

internal/db/contract 不依赖 UI 或驱动，提供 ObjectRef、Cell、Column、Table、Page、Edit、SQLLease、Transaction、Session、Capabilities 和 Verification。ObjectRef 始终包含 profile、catalog、schema、kind、name。PostgreSQL 的 profile 绑定一个 database；导航在 profile 下显示固定 catalog，再显示实际 schema。SQLite 首轮仅列出文件的 main schema。

sessionbrowser 注入 EngineRuntime 负责异步连接与元数据，沿用原有 generation、停放及取消边界。导航用名称/种类投影显示真实对象，完整身份在 engine 数据文档中保留。PostgreSQL 结构补读公开约束及索引定义，视图补读 pg_get_viewdef；SQLite 保留 sqlite_schema 中的表/视图原文及独立索引/触发器原文。sqlworkspace.EngineRuntime 复用既有编辑器、类型化多结果、当前结果导出及明确的运行并导出。dataworkspace.EngineState 提供原生网格、筛选、排序、分页、选区复制、字段修改、新行、删除、显式事务和完整类型化 JSONL 导出。

MySQL 的正式入口与偏好键保持兼容。generic 网格列顺序和宽度使用独立版本化 engine_data_layout 键，身份包含完整 ObjectRef。引擎能力禁止进入 MySQL 的设计器、导入扩展、维护、用户权限和备份执行器；通用结构页仅展示公开元数据及可取得的完整定义。

## 数值和编辑边界

PostgreSQL numeric 保留精确文本；bytea 保留原始字节；JSON、日期时间与布尔使用明确类型。生成字段保持只读。SQLite 单元格根据驱动返回的实际值分类，声明 TEXT 中存储的整数仍按 integer 处理，声明类型不会把 BLOB 推断为文本。NULL、文本 NULL、空串和空二进制分别保存。

可靠主键或完整非空、非部分、非表达式唯一键才能开放编辑。视图和无可靠键的对象保持只读。SQLite 的普通复合 PRIMARY KEY 可包含 NULL，因此不会仅凭 PRIMARY KEY 标志开放编辑；单列 INTEGER 主键的 rowid 别名按 SQLite 实际规则处理。

更新与删除使用完整原始行和原始键核对，发现外部修改返回冲突。写入使用参数，不把用户值拼接到 SQL。字段名、筛选及排序依据实际元数据校验。完整编辑限于 1 MiB；普通结果和数据页有独立的行数、内存、单元格预览限制，并显示截断原因。筛选操作符限制为公共契约支持的集合。

INSERT/UPDATE 使用 RETURNING 读取真实生成键和保存值，提交后再以新读取核验；同一事务内多次修改同一行合并为最终待核验状态。已确认提交但核验不完整，与提交结果不确定分别表示。两种状态保留修改并禁止自动重放，提供明确的重新连接后只读核验入口。无法匹配的核验不会声称未提交。

## 物理会话、取消与关闭

每个 SQL 文档租用一条物理连接，临时表及事务保留在该文档租约中。关闭撤销其请求，等待执行器回执，并丢弃物理连接，避免临时表、未完成事务或 search_path 泄漏到另一个文档。断开保留 SQL 文本，要求显式连接原配置和恢复；不重放 SQL 或事务。

数据事务的生命周期与会话绑定，不绑定短暂的“开始事务”UI 请求。消费开始事务回执后的请求取消不会隐式结束事务。SQLite busy_timeout 为 1000 ms，锁等待响应取消；不会修改用户数据库 journal_mode。事务提交取消或连接异常不被描述为事务回滚。

所有后台任务捕获深复制请求和拥有的租约/事务，通过私有缓冲通道回传，UI 线程消费。停放页不替换活动页；generation 变化拒绝旧结果。关闭非活动页的保存守卫仍为原文档消费明确的保存回执，不切换活动页身份。

## SQL 扫描与格式化

纯 sqltext portable 扫描器识别 PostgreSQL dollar 字符串、E 字符串、嵌套注释，以及 SQLite 触发器 BEGIN/CASE/END 中的分号。查询按词法语句顺序执行，遇错停止。格式化保留被引用文本和注释原字节，并沿用编辑器撤销。MySQL 保持已有 DELIMITER 扫描器与格式化入口。

## 已验证与证据边界

I：实际 SQLite 文件测试覆盖动态值类型、复合键、NULL/空值/二进制、显式事务及回滚、乐观冲突、锁等待取消、journal 未变、多结果与 2001 行流式导出。

I：一次性 PostgreSQL 17 容器覆盖真实连接、复合主键、numeric(40,20)、bytea、JSON、生成字段、提交后核验、陈旧原行冲突、视图只读及 SQL 多结果。使用 tools/verify_postgres_fixture.sh；脚本只创建和删除自有随机名称容器，绑定随机回环端口，不复用任何现有服务。

I：ui.NewTester 覆盖原生图形网格保存、事务跨操作保留、修改不重放、非活动页关闭保存等待回执、已提交但丢失回执的保留和只读核验、停放/代际失效；shell 实际 SQLite profile→connect→schema→grid write→typed SQL→disconnect 保留文本的完整接线测试。

I：隔离 Xvfb/GTK 原生夹具显示实际 SQLite 网格中的 NULL、空 BLOB、生成字段，以及一次明确 Run SQL 之后的类型化结果。截图位于 temp/engine-native/run-nFI0Ih/grid.png 和 sql.png；夹具及运行脚本位于 temp/engine-native/，仅创建一次性 SQLite 文件和自有 Xvfb，退出后清理。该夹具验证真实原生组件，生产 Shell 的完整接线另由上述 Tester 测试验证。

Unknown：完整生产桌面人工操作验收与 Navicat 差分尚未完成。原版 Navicat 未运行。PostgreSQL/SQLite 图形表设计器及导入模式扩展属于后续范围。

最终阶段 5 原生夹具截图 SHA-256：grid `749b9a9ab9b651650b57ef5865506b84cc086b340fe61b19d58505e3a71a6684`；SQL `3245a3e04696266cb80c4b426de5538b7ce5de3bc3c83a4268bf19c0bba3e9c9`。PostgreSQL 17 fixture image digest 为 `postgres@sha256:742f40ea20b9ff2ff31db5458d127452988a2164df9e17441e191f3b72252193`。
