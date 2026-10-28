# NyaDB MySQL SessionLimit：证据边界与会话配额策略（2026-10-28）

## 证据分级

- **O（原版 UI）**：Navicat 18 的 MySQL「高级」页存在“限制连接会话”，默认未勾选、显示 10。见 docs/MYSQL_ADVANCED_DIFFERENTIAL_2026-10-21.md。
- **S（原版静态）**：只读分析识别 Conn.SessionLimit 设置键及 endUseSessionPool 等名称。见 docs/NAVICAT_MYSQL_SESSION_STATIC_ANALYSIS_2026-10-27.md。
- **O（其它会话行为）**：独立 SQL 标签拥有不同服务端 CONNECTION_ID()；元数据会话可能独立于 SQL 标签。见 docs/NAVICAT_MYSQL_SESSION_RUNTIME_VERIFICATION_2026-10-27.md。这不能用于推断 SessionLimit 如何执行。
- **Unknown**：原版开启 SessionLimit=1/2/10 时限制的是 SQL 标签、后台元数据会话、整档案连接池还是隧道；到达上限是拒绝、排队或复用；是否允许已持有的标签继续使用等。本阶段**未重新运行 Navicat 或创建 MySQL fixture**，不宣称原版 SessionLimit 运行行为已验证。

## NyaDB 独立实现（I，不是 Navicat 内部算法）

安全上限为每连接档案 **8 个同时登记的 SQL 标签会话租约**（maxSQLTabSessions）：

| 高级设置 | SQL 标签配额 | 元数据池 |
| --- | --- | --- |
| “限制连接会话”未开启 | 8（既有保护值） | 独立，最多 4 连接 |
| 已开启，填写 1 | 1 | 独立，最多 4 连接 |
| 已开启，填写 2 | 2 | 独立，最多 4 连接 |
| 已开启，填写 10 | 8（安全上限） | 独立，最多 4 连接 |

其他有效值同样取 min(填写值, 8)；这不是对整个 Profile 的物理 TCP 连接总数设限。一次性的连接测试、短暂关闭窗口以及数据库自身的连接占用不在 SQL 标签租约计数之内。当前暂不支持放宽内置上限到 9 及以上。

在 Connect 时按当时活动配置固定预算，修改连接设置需重新连接才会生效；不同 Profile 独立计数。进入 UI 的 openQuery 时先尝试登记租约，再异步执行预连接。登记不涉及网络，未获得租约的标签保留编辑内容并显示错误；已有标签可以继续执行 SQL。通过 ReserveSQLTab 和 ExecuteSQLTab 双重检查，不能绕开上限直接执行；到达上限返回 ErrMySQLSQLSessionLimit，**立即拒绝**，不对用户造成无限排队。关闭其他 SQL 标签释放租约后可在原编辑标签再次点击运行，尝试获取可用租约。

配额按**登记中的租约**而非实际已完成握手的 TCP 连接计算，因此连尚在预连接中的标签也会占一个名额；此规则避免多个后台预连接同时突破预算。失效标签在显式关闭前继续占一个登记名额，防止静默恢复其事务或变量状态。后台元数据读取仍使用独立的 db，SQL 标签使用 sqlDB 且 MaxIdleConns=0。SQL 物理池上限与租约预算一致；元数据池原有最大 4 条限制不变。

UI 的高级设置中在启用时提示当前实现范围；查询错误不会覆盖未保存的 SQL 文本。现有断线、查询取消及长事务警告继续保留。

## 验证与限制

- 无 MySQL 单元测试：设置开启/关闭、值 1/2/10/10000、安全上限、不同 Profile 并发隔离；预留与直接执行的限制、同一标签重复登记、释放后重试。
- MyGo ui.NewTester：显示限额错误、SQL 文本保留、再次执行恢复；原生布局实际像素差分尚无 V。
- 完整 Go 测试及静态检查应分别记录；真实 MySQL 接入测试仍要求明确的本机隔离 fixture，不能使用生产服务器。
- **下一步原版 O 实验**：使用合法授权 GUI 与一次性 loopback MySQL，分别启用 1/2/10，会话标签 A/B/C 和数据库对象刷新同时进行；只读采集 SHOW FULL PROCESSLIST，记录新标签是否创建连接、是否拒绝/等待、已有标签是否继续可运行、关闭释放、metadata 是否挤占配额。届时再决定是否对齐原版。

## 本轮实际验证（I）

- SQL 标签配额专项 Go 单元测试：通过。
- 32 路并发登记及限额逻辑 Go race 专项测试：通过。
- MyGo Tester SQL 工作区错误提示及重试：通过。
- go test ./...：通过（不包含需要专用 MySQL fixture 的 opt-in 测试）。
- go vet ./...：通过。
- go tool mygo vet：通过。
- go tool mygo build：通过（Linux/amd64；不等于真实 GUI 验收）。
- git diff --check：通过。
- 原版 SessionLimit 运行 O、真实 MySQL 限额测试及 Navicat 差分 V：未执行。
