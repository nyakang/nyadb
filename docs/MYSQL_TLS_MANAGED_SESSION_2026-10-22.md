# MySQL TLS 与工作台管理会话（2026-10-22）

## 范围和证据

- **I（独立实现）**：使用 Go `crypto/tls`、`crypto/x509`、`database/sql` 和 `go-sql-driver/mysql v1.10.1`，扩展 `internal/services/connections`，由 MyGo 工作台在用户明确操作后建立长期连接。
- **限定验证（I）**：通过本机临时 MySQL 协议模拟端点验证驱动的 `CLIENT_SSL` 升级、TLS 握手和加密 `COM_PING`；单独生成临时自签 CA/证书，验证受信任且主机名正确的连接成功，主机名不匹配则拒绝。
- **GUI（I）**：隔离 Xvfb :131 中新建 MySQL 档案，确认密码输入、连接按钮、失败状态、信息面板和未连接对象浏览空状态。证据在 `temp/research/mysql-tls-session-20261022/`。
- **O/V 界限**：本轮未对 Navicat 真实服务器会话行为进行观察，也未与真实 MySQL/MariaDB 服务做兼容性差分。模拟端点不等于真实数据库的版本和认证插件兼容测试，因此不宣称全行为 V。

## TLS 安全策略

- `verifiedMySQLTLS` 设置 `MinVersion = TLS1.2`；始终验证服务端证书链与 `ServerName`（域名/IP）。不使用 `InsecureSkipVerify`。
- 信任系统 CA；显式指定的 CA PEM 追加到信任根，无效 CA 或读取失败直接报错。若提供客户端证书，必须同时提供私钥，使用原生 X509 密钥对加载。
- MySQL SSL 标签启用或 `TLSMode=require/verify-full` 时启用严格证书校验；不允许非严格验证的 `prefer`、`verify-ca` 自动回退。即使 SSL 标签的可选验证开关关闭，驱动仍强制安全验证而不会静默降低安全等级。
- **任何远程主机在未启用严格 TLS 时均拒绝连接，包括编辑器的连接测试。** 仅 `localhost` 和 loopback IP 允许非 TLS 的本机开发连接。
- SSH、HTTP、命名管道及其他尚未实现的高级行为仍在发起网络访问前拒绝。密码仅作为内存会话参数传给驱动，不保存到 SQLite、不输出 DSN 或日志。
- 可使用原版 UI 中的 CA/客户端证书路径；尚未实现受口令保护私钥及系统密钥库，不能把该能力声称为已验证。

## 管理会话架构

- `MySQLSessionManager` 管理按连接 ID 索引的 `*sql.DB`、会话 context、连接中 cancel、生命周期 generation。连接、断开、重复连接、关闭管理器都具有独立所有权；晚到的已取消连接不能重新注册。
- `Connect(ctx, ActiveSettings, password)` 使用**已保存的 Active 配置**解析后的参数；若连接失败不保留句柄。断开撤销进行中的握手和元数据查询，并关闭连接池。工作台退出时统一释放管理器。
- `Databases(ctx,id)` 执行唯一的实际元数据 SQL `SHOW DATABASES`，使用 15 秒独立查询上限且受到会话撤销控制，逐行读取名称并关闭 rows。
- 工作台左侧使用 MyGo 原生一次性密码输入及连接、取消连接、断开、重连入口；后台结果由容量为 1 的 channel 和 `ui.Services.Invalidate` 回到 UI 线程，工作线程不直接修改 UI State。
- 连接状态区分未连接、连接中、已连接、连接失败；断开或取消后清空数据库名称，晚到结果通过 generation+连接 ID 丢弃。重连须重新输入密码，避免为便利而长期保留秘密字符串。
- 对象工作区在档案模式下只展示**实时 `SHOW DATABASES` 返回的数据库名称**，未连接时显示空状态，不混入模拟表格、模拟 SQL 执行或虚假的对象数量。支持刷新数据库列表。

## 验证与限制

- TLS 单元测试：CA/主机名验证、缺失文件、客户端密钥成对约束、拒绝 TLS 降级、不允许远程明文配置。
- 协议测试：本机临时 TCP 端点升级到 TLS 后成功接收真正的 MySQL 驱动认证与 `COM_PING`；仅模拟协议，不使用生产服务器。
- 会话管理测试：注册/断开/关闭、`SHOW DATABASES`、取消未完成握手、避免晚到会话复活。
- MyGo 视图测试：选择已保存档案、后台连接成功后的数据库列表、断开清空、重连重新输入密码、远程无 TLS 连接被拒绝。
- 工程门禁：`go test ./...`、`go vet ./...`、`go tool mygo vet`、`go tool mygo build`，以及关键包 Race Detector 测试。

### 仍未完成

1. 真正 MySQL/MariaDB 服务端的版本/认证/权限/连接池压力测试和 Navicat 原版差分。
2. 具体 schema 下的表、视图、存储过程和表数据浏览；目前接入的是服务器级数据库名称列表。
3. SQL 编辑器执行、查询取消、自动重连策略及多个连接并行会话。
4. SSH/HTTP 隧道、安全密码库、私钥口令、更多 TLS 模式兼容语义及跨平台真实 GUI/DPI 差分。
