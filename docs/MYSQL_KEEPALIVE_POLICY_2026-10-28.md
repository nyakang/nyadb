# NyaDB MySQL KeepAlive：静态证据、独立实现与验证（2026-10-28）

## 一、原版证据及边界

- **O（仅 UI/配置）**：Navicat Premium 18 的 MySQL 高级页存在“保持连接间隔”，默认关闭、显示 240 秒；启用后可以编辑间隔。见 docs/MYSQL_ADVANCED_DIFFERENTIAL_2026-10-21.md。
- **S（原版二进制静态）**：存在 COConnKeepAliveThreadable、Conn.UseKeepAlive 和 Conn.KeepAliveInterval 名称。见 docs/NAVICAT_MYSQL_SESSION_STATIC_ANALYSIS_2026-10-27.md。
- **Unknown（原版实际运行）**：是否发送 COM_PING、首次周期起点、活跃查询期间是否跳过、保活对象（元数据/SQL 标签/SSH）、Ping 失败是否重连或断开、是否会改变事务状态等。本轮**没有启动原版 Navicat，也没有连接真实 MySQL 服务器**。不能用 NyaDB 的实现反推这些原版行为。

## 二、NyaDB 的独立策略（I）

KeepAlive 默认为关闭，仅当当前活动 MySQL 连接配置明确启用时，在 Connect 成功后启动每 Profile 一个后台 ticker，周期使用 KeepAliveSeconds（已存在 1–86400 秒持久化校验）。首次定时探测在连接成功后一个完整周期发生；本设计不是 Navicat 的实测值。

每个 tick 通过数据库/sql 的 PingContext 探测连接，真实 go-sql-driver/mysql 会向服务器发送 COM_PING。一次 tick 在后台串行检查：

1. 从已登记的 SQL 标签租约列表获取快照，不在全局/池锁下做网络 I/O。
2. 只 Ping 已获取的、空闲的物理 SQL 标签连接。持有查询锁（包括预连接中）的标签立即跳过，下一次周期再尝试；从未预连接的标签不主动建立连接。
3. SQL 标签 Ping 失败：其租约永久标记失效，取消上下文并关闭物理连接；后续执行返回 ErrMySQLSQLSessionLost，明确提示事务/用户变量可能丢失。**不做静默重连、重放 SQL、事务提交或回滚**。
4. 元数据池只在 Stats().Idle > 0 时执行一次有界 PingContext；不为无物理空闲连接的元数据池主动拨号。元数据/浏览池无用户事务状态，失效连接仍可由 Go 标准连接池在将来的操作中重建；元数据 Ping 失败不自动将整个 Profile 标记为断开。
5. 每个 Ping 最长 5 秒，通过 Profile / 标签上下文取消。关闭 SQL 标签不再 Ping 该租约；Disconnect、重新 Connect 以及 Manager.Close 均取消旧上下文，终止旧一代 tick。没有独立后台无限重试队列。
6. 为避免“默认 240 秒保活周期”被原先 3 分钟物理连接最大生命周期覆盖，启用 KeepAlive 的元数据池不再设置固定 3 分钟 TTL；未启用时保持既有 TTL。SQL 标签连接本来就不会设置 TTL，以保护有状态会话。

**边界**：此处是 SQL/元数据两个池独立的 NyaDB 策略，不承诺在防火墙、代理、服务端 wait_timeout 短于配置周期或网络故障的所有场景中维持连接。尤其是 SQL 会话 Ping 失败后不自动重建，是事务安全优先的选择。SQL 标签正在执行期间可以长于 Ping 周期，期间不会插入探测。当前 UI 在用户下一次执行时展示会话失效提示；没有额外的后台状态通知面板。

## 三、验证方法与结论

- Go mock 驱动验证：禁用时无 ticker、启用时按秒启动、SQL 空闲连接被 Ping、活跃查询被跳过、未拨号的租约不会触发新连接、元数据池只 Ping 已有 Idle 连接、失败后 SQL 会话永久失效且不透明重建、Disconnect 停止 ticker。
- 真实 go-sql-driver/mysql + **本机一次性模拟 MySQL 协议端点**：实际读取到至少两次 COM_PING（首次 Connect 握手探测、然后 1 秒周期定时探测），确认会在 Disconnect 后关闭连接；此测试不用真实数据库和账户凭据。
- Go race 测试验证后台计时与租约取消不存在已检测出的数据竞争。
- **本轮实际结果（2026-10-28）**：KeepAlive 专项 go test 通过；专项 go test -race 通过；
  go test ./...、go vet ./...、go tool mygo vet 和 go tool mygo build 均退出码 0；
  git diff --check 通过。MyGo v0.6.4 Linux/amd64 构建成功。
- 未运行真实 MySQL 服务器测试、原版 GUI 观察、Windows/macOS 实机 GUI 差分。
- 不能把编译通过、模拟协议验证或 Go race 检查等同原版 V。

## 四、仍需原版 O 差分

如需确证 Navicat 原版的真实 KeepAlive 行为，应单独获得执行原版的明确授权，并创建新的**一次性回环 MySQL fixture**：分别设置 1 秒、5 秒、240 秒（后者可能需要较长实验窗口）及关闭状态；同时抓取 MySQL general_log / performance_schema 的 Ping 记录和 SHOW PROCESSLIST 的 connection ID，分别观察元数据池、单个及多个 SQL 标签、执行 SELECT SLEEP 时和关闭标签后的行为。要控制服务端 wait_timeout 小于/大于 KeepAlive 周期的场景，并检查断线后的事务状态。原版 GUI 必须通过正版授权入口启动，原版文件默认只读，不复用旧实验容器或生产凭据。此项尚未执行，不能标注为 O 或 V。
