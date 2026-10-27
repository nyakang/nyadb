# NyaDB — MySQL SQL 标签独立会话与元数据池隔离（2026-10-27）

## 背景和证据

原版 Navicat 18.0.2 / 隔离 MySQL 8.4.11 在 2026-10-27 同 fixture 的实测记录：
`docs/NAVICAT_MYSQL_SESSION_RUNTIME_VERIFICATION_2026-10-27.md`。

- **O1**：一个连接档案下并列的 SQL 标签 A/B 分别使用 MySQL `CONNECTION_ID()=14` / `19`。
- **O2**：A 中的 `@nyadb_probe=101` 不出现在 B 中，切回 A 保留。
- **O3**：A/B 的 `SELECT SLEEP(...)` 能同时在服务端 `SHOW FULL PROCESSLIST` 中显示为 `Query/User sleep`。
- **O4**：A 的 `START TRANSACTION; UPDATE ...` 跨 SQL 执行保留，B 只能看到提交前的值，A `ROLLBACK` 恢复数据。
- **O5**：关闭 A/B 标签后两条 SQL 物理连接消失，其他连接档案浏览会话可保留。

上述 O 只适用于所测的版本和行为场景，不能认定 Navicat 所有 DBMS/工作区的实现完全相同，也不能据此声称 Navicat 内部池算法已被还原。

## NyaDB 新架构（I：服务层及测试已通过）

```text
MySQLSessionManager
  └── ProfileID → mysqlManagedSession
         ├── db (*sql.DB, metadata/data pool, maxOpen=4, maxIdle=2)
         │    ├── SHOW DATABASES / schema and object information
         │    ├── table metadata
         │    └── paginated table data and field chunk
         ├── sqlDB (*sql.DB, interactive SQL, maxOpen=8, maxIdle=0)
         │    ├── tab 1 → mysqlSQLTabLease → *sql.Conn (persistent)
         │    └── tab 2 → mysqlSQLTabLease → *sql.Conn (persistent)
         ├── profile lifetime Context
         └── per-tab cancel, release and lost-state handling
```

### 生命周期

1. `Connect`：正式 MySQL 使用 `OpenDirectMySQLPools` 创建两个独立的 `*sql.DB`，仅在真正需要执行 SQL 时为每个 SQL 标签懒加载物理连接。测试注入的单池 mock 仍能执行既有单元测试。
2. `ExecuteSQLTab(ctx, profileID, tabID, database, statement)`：同标签跨多次运行复用同一 `*sql.Conn`；`USE database` 和 SQL 使用同一连接。不同 tab ID 分别保留不同 SQL 租借。
3. `ReleaseSQLTab(profileID, tabID)`：标签关闭，取消该租借并异步释放，以避免原生 UI 线程因驱动等待而阻塞。底层 SQL 池 `MaxIdleConns=0`，完成后不复用有状态连接。
4. `Disconnect/Close/re-Connect`：撤销 Profile 下所有标签的连接和操作，不允许旧一代异步 Connect 恢复旧会话。UI 保留 SQL 编辑文本，结果被清空。
5. 用户取消、请求超时、连接损坏：当前标签租借标记永久失效；禁止悄然分配新 MySQL 会话而误导用户认为未提交事务/临时表仍存在。UI 应提示关闭并新建 SQL 标签。
6. 配额与性能：每 Profile 的 SQL 池最多 8 条同时使用的物理连接，超过配额的新标签执行会排队并受 `context.Context` 取消；元数据池最多 4 条，不因 SQL 的 `SLEEP` 或事务占用而饥饿。**此值为 NyaDB 暂定安全边界，不声称复刻原版默认 SessionLimit=10 的准确行为。**

### 兼容边界

- 原 `ExecuteSQL(ctx, profileID, database, statement)` 继续提供临时借用连接的服务 API；真实 UI 的 SQL 标签改为调用 `ExecuteSQLTab`。其它浏览和数据业务仍使用独立 `db`。
- `sql.DB.SetConnMaxLifetime(3m)` 仅用于短生命周期的 metadata 池；长期 SQL 租借不配置会话过期时间，确保数据库会话状态不因固定 3 分钟超时而被悄然置换。
- SQL 取消是请求结束信号，不保证服务器 DML 已回滚；不自动重放 DDL/DML。
- 本阶段不实施未有原版行为证据的 `KeepAlive`、自动重连、SessionLimit 设置映射及精确 `KILL QUERY` 协议。
- `sqlDB` 懒加载物理连接：Navicat 本轮实验在创建 B 标签时已见服务端连接；NyaDB 当前在 B 首次运行时才租用连接，**创建时机仍未达到严格的 O/V 对齐**。避免在 UI 线程同步建连。

## 验证清单

- 常规单元测试 `TestSQLTabLeasesKeepPhysicalConnectionsAndRelease`：无需真实 MySQL，验证两个标签占有不同 `sql.Conn`、单独关闭释放、断开连接后拒绝查询。
- 隔离的真实 MySQL 8.4 测试 `TestMySQLSQLTabLeasesIntegration`：必须在独立容器、固定数据库 `nyadb_tab_it` 及本机回环随机端口，用 `NYADB_MYSQL_TAB_IT=1` 与 `NYADB_MYSQL_TAB_PORT` 显式开启。测试包含连接 ID、变量隔离、隔离事务 + ROLLBACK、双 `SLEEP(2)` 并发、独立 metadata 查询、取消后租借无效、其它标签不中断和释放统计检查。
- `go test ./...`、`go vet ./...`、`go tool mygo vet`、`go tool mygo build` 均应执行，按实际结果报告。
- 本轮不应宣称已完成 NyaDB 与 Navicat 相同 DPI 的真实 GUI 像素差分或 Windows 运行验证。

## 待改进

1. 将 SessionLimit 和 KeepAlive 原版 O 行为实验单列，不按静态属性键猜实现。
2. 配额管理应在真实 UI 暴露“等待空闲连接/取消排队”状态，后续决定允许多少 SQL 标签并发。
3. 查询标签“创建时”是否预连应结合异步 GUI 行为、错误提示和资源预算再对齐。
4. 明确会话丢失后的重建 UX：显式“新建会话”动作，并警示事务丢失；不得静默恢复写 SQL。
5. 研究 MySQL 多结果集、SQL 分句执行与安全的会话级长查询取消（当前 `sqlReturnsRows` 仍有限制）。
