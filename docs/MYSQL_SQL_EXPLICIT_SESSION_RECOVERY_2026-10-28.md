# NyaDB MySQL SQL 标签显式会话恢复（2026-10-28）

## 依据与目标

原版 O：`docs/NAVICAT_MYSQL_KEEPALIVE_RUNTIME_2026-10-28.md`。Navicat 18 在 MySQL 8.4 的一次性隔离环境中被管理员主动 KILL CONNECTION 后，原 SQL 标签再次执行 SELECT 可以获得新的 `CONNECTION_ID()`；旧会话用户变量从 777 变为 NULL。不能把换新物理连接当成旧事务或用户变量得到恢复的证据。

NyaDB 采用**明确的恢复操作**而非完全静默重连。设计原则：保护 SQL 文本，避免自动再次执行更新/DDL；明确提醒旧事务、临时表及用户变量不继承，新会话可能观察到不同服务端状态。

## 服务层 I：f34624b

`MySQLSessionManager.ReplaceSQLTabToken(old)` 在当前 Profile generation 和精确 Lease 身份均有效时，原子替换同一 SQL 标签的租约。替换不会进行网络 I/O 或 SQL 执行，并保留同一 SessionLimit 预算槽位。旧令牌即刻不可使用，异步清理旧物理连接；其他 SQL 标签互不干扰。

若 Profile 已显式 Disconnect/Connect，旧 generation 无法替换新 Profile 的租约；UI 在确认同一原始 Profile 已重新连接后，使用新代次 `ReserveSQLTabToken` 重新建立令牌。旧无令牌兼容 API 不参与恢复流程。

## 原生 UI I

- 服务端错误确认为 `ErrMySQLSQLSessionLost`：保留现有 SQL 编辑文本、禁用“运行”，显示“原查询会话已失效”及事务、临时表、变量无法恢复、DML 结果需核对的警告。
- 展示原生按钮 **“以新会话继续”**。按钮旁明确提示新会话不会自动执行 SQL；只有原 Profile 已连接、没有运行中查询/预连接时才可以按下。
- 用户点击后，UI 只调用 Replace/Reserve 新租约，丢弃旧查询结果、保留编辑器与原 Schema，并用后台 goroutine 异步执行 `PreconnectSQLTabToken`。**不调用 `ExecuteSQLTabToken`，不自动重跑 SQL，不提交/回滚任何事务。**
- 若用户主动断开 Profile，旧 SQL 标签继续保留文本与原 Profile/Schema 的恢复备份；在重新连接**同一** Profile 前恢复按钮不可用，重连后仍须用户主动点击。
- 新租约建立之后，用户可手动点击“运行”执行当前 SQL。旧连接状态不可继承；任何尚不确定的 DML/DDL 结果必须自行核查。
- SessionLimit、TabID 唯一性、原 Profile 代次保护、异步连接取消和标签关闭清理规则保持有效。

## 验证

- `TestReplaceSQLTabTokenAfterLoss`：旧租约不能再执行或第二次替换，新租约可用，其他标签不受影响。
- `TestReplaceSQLTabTokenRejectsStaleGeneration`：旧 Profile 代次不可覆盖新租约。
- `TestReplaceSQLTabTokenPreservesQuota`：不绕过 SessionLimit，关闭新标签才释放额度。
- `TestSQLSessionLostOffersOptInRecoveryAndDoesNotExecute`：MyGo `ui.NewTester` 验证失败提示、禁止旧会话再次执行、保留未保存的 SQL 文本。
- `TestDisconnectedSQLTabNeverRebindsAfterProfileReconnect`：旧标签仍不能在未点击按钮时自动取得新会话。
- `TestSQLRecoverSameTabRealMySQLIntegration`：显式开启测试环境变量后，在**全新一次性 MySQL 8.4 loopback fixture** 中运行，MyGo Tester 点击恢复按钮，确认旧令牌无效、新连接可 SELECT、未执行原编辑器中的 UPDATE、用户手动运行 SELECT 后才出现查询结果。还覆盖显式断开 Profile、重新连接及旧标签恢复；fixture 测试完成后销毁容器。

本阶段验证分类为 **I**：服务单测、MyGo Tester 控件测试和隔离 MySQL 实测。**尚未做同一 fixture 的原版与 NyaDB 双桌面差分 V，也未做 Windows 桌面 GUI 验证。** 原版有过静默换新连接的 O，NyaDB 的明示操作是为状态安全而有意采用的不同交互。

## 风险与边界

“以新会话继续”不代表事务恢复。连接丢失时提交/回滚结果仍可能不确定；尤其在 UPDATE/INSERT/DDL/COMMIT 请求发生网络中断时，应先查验数据库最终状态。按钮只更换租约，不会尝试重放业务语句，因此不保证会话中使用的临时表、用户变量、锁、事务隔离级别等仍然存在。
