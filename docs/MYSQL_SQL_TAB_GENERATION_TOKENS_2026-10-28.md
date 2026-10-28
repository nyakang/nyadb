# SQL 标签会话代次令牌与 Navicat 启动复查（2026-10-28）

## 1. 研究证据

### Navicat 标准启动复查（Unknown）

上一轮全新隔离 fixture 的 Linux 直接启动原版返回退出码 70、无 GUI 窗口，见 `docs/NAVICAT_KEEPALIVE_RUNTIME_ATTEMPT_2026-10-28.md`。

本轮另用 `temp/navicat/squashfs-root/AppRun` 标准入口，单独启动 Xvfb `:194` + Openbox，设置新 `XDG_CONFIG_HOME` 和 `XDG_CACHE_HOME`，开启 `QT_DEBUG_PLUGINS=1`，不修改原版二进制、不写入注册信息、不连接 MySQL、不使用生产凭据。结果同样为 **exit 70，未出现可见 Navicat 窗口**。Qt 日志确认 `libqxcb.so` 成功加载，并于退出阶段卸载；这只能排除“完全无法加载 Qt XCB 插件”，不能证明其它组件初始化均成功。

原版实际退出原因**仍然 Unknown**，本轮未取得 Navicat KeepAlive 运行 O 证据、未达到差分 V。诊断日志、空窗口截图及退出码保留在被 Git 忽略的 `temp/research/navicat-startup-diagnostic-20261028/`。Xvfb、Openbox 和 AppRun 子进程已按诊断脚本退出并清理。没有绕过原版授权或修改其私有实现。**下一次原版实验必须先具备正常授权可启动的 GUI，才能连接新的一次性回环 MySQL fixture 观察 KeepAlive。**

### NyaDB 服务层（I）

上轮 `f583e69` 已修复常见 SQL 连接丢失的错误分类、显式 Disconnect 时解除 UI SQL 标签和 Profile 的绑定。但旧 `ExecuteSQLTab(ctx, profileID, tabID, ...)` 是无代次接口，新 Connect 后若错误地重用相同数字 TabID，仍可能无意创建一条新的物理会话；旧迟到的 Release 操作也不能安全地标识原租约。

本轮新增 `MySQLSQLTabToken`，记录 **profileID、tabID、单调 Profile generation、精确租约指针**，不公开 generation/lease 字段供调用方伪造。

- `ReserveSQLTabToken(profileID, tabID)`：同步无网络登记，返还原 Session 的租约令牌，保留 SessionLimit 的安全配额。
- `PreconnectSQLTabToken(ctx, token)`：校验当前管理器代次和租约指针，然后仅对原租约异步建连。旧 worker 无法在新 Profile 建连。
- `ExecuteSQLTabToken(ctx, token, schema, SQL)`：同样验证代次和身份，SQL 执行期间继续持有原租约锁；保留取消、COM_PING、会话失效、事务状态和元数据连接池隔离。
- `ReleaseSQLTabToken(token)`：旧代次/旧租约令牌的释放为 no-op，不会误关新标签，即使 numeric TabID 被重用。
- NyaDB 正式 UI：创建 SQL 标签时保存 Token，所有预连接/查询/释放均通过 Token；关闭和断开时撤销并清零。未持有令牌的正常 UI 标签不能靠执行 SQL 隐式创建新会话。
- 旧 `ReserveSQLTab/PreconnectSQLTab/ExecuteSQLTab/ReleaseSQLTab` 接口保留以支持原有一次性（generation=1）测试和兼容旧调用，但**Profile 发生过重连时无令牌接口拒绝或不执行**，而不静默改绑新代次；历史调用方需迁移到令牌 API。
- 精确租约身份还能防止**同一代次** TabID 先释放再重用时，旧释放/预连接误作用于新租约。
- 令牌不含密码或其他数据库凭据，UI 不展示令牌内部值。

### 行为边界

预连接及执行持有的是特定 Session 和 Lease 引用。并发 Connect/Disconnect 后旧操作可能以取消或丢失会话错误完成，但绝不会转向新 Session；已提交到服务器的操作仍可能生效，不能视取消为事务回滚。代次令牌不是自动重连，也不会恢复临时表、变量、未提交事务。启动原版未成功的事实与 NyaDB 的 I 结果严格区分。

## 2. 验收标准

1. 重连同一 Profile + 复用相同 TabID：新令牌的 generation 和 lease 均不同，旧令牌不得 Preconnect、Execute 或 Release 新租约。
2. 同一代次复用 TabID：旧令牌不得触碰新租约。
3. 延迟的异步预连接在重连后执行：只能返回 ErrMySQLSQLSessionLost，新租约仍可预连接。
4. 老无令牌方法在 Profile 重连后拒绝执行。
5. SQL 运行、KeepAlive、元数据池、SQL 文本保留、UI 结果回收及错误提示等功能不回归。
6. `go test ./...`、`go vet ./...`、`go tool mygo vet`、`go tool mygo build`、有关接口的 `go test -race` 应取得完整退出码后再标记 I。
7. 原版 GUI KeepAlive O 与原版差分 V：仍是 Unknown、未执行；绝不将 MyGo Tester 或静态证据冒充它们。

## 3. 本轮实际验证

- 无数据库专项单测：不同 Profile generation、同代次同 TabID 释放重用、旧预连接 worker 延迟、旧无令牌 API、SessionLimit 配额、断开与重新连接，均通过。
- 完整 `go test ./...`：通过；`go vet ./...`：通过。
- `go tool mygo vet`：通过；`go tool mygo build`（MyGo v0.6.4 Linux/amd64）：通过。
- `go test -race`（Token/旧编辑器解绑专项）：通过。
- 真实 MySQL 8.4 一次性回环 fixture：`TestMySQLDisconnectDropsTransactionStateIntegration` 与 `TestMySQLSQLTabLeasesIntegration` 通过，前者已使用令牌 API 且显式断言旧令牌不能跨 Connect 使用。fixture 容器已停止并自动移除，没有访问其它容器。
- 首次 fixture 握手出现 unexpected EOF（尚未满足完整服务就绪），在改为实际 TCP SQL 就绪检测并增加稳定间隔后，以上两项测试重新执行并通过；不能把第一次失败的执行记录隐去。
- 标准 AppRun 的正常 GUI 运行再次 exit 70，Qt XCB 插件被成功加载但无窗口；其根因仍未知。D-Bus 会话诊断命令受到仓库执行策略限制，本轮未运行，也未绕过该限制。原版 KeepAlive O、V **仍未取得**。
