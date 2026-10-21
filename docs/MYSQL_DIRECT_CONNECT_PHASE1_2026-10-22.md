# MySQL 真实连接第一阶段：TCP 直连与连接测试（2026-10-22）

## 范围与证据

- **I：NyaDB 独立实现。** `go-sql-driver/mysql v1.10.1`（原生 Go，`database/sql`），无 WebView、无 C 绑定。
- **I + 限定验证：** 真正的驱动在 127.0.0.1 临时 MySQL 协议端点上完成服务端握手、客户端认证响应和 `COM_PING`；测试端点只模拟协议，不等同于真实服务器兼容性验证。
- **UI 验证：** MyGo 原生新建 MySQL 窗口按钮已在隔离 Xvfb :129 实际操作，空名称时在发起网络前校验并显示错误。截图位于 `temp/research/mysql-real-connection-20261022/`。
- **O / V 边界：** 此阶段未在 Navicat 上执行真实 MySQL 会话，也未比对原版连接错误提示，原版连接行为仍记为 Unknown；不将本机协议端点测试说成 Navicat 差分 V。未访问生产数据库。

## 架构

`internal/services/connections/mysql_connect.go`：

- `DirectMySQLConfig(settings, password)`：把活动配置的非敏感字段与仅在会话内提供的密码转换为原生驱动配置，使用 `net.JoinHostPort` 正确处理 IPv6，**不拼装会包含密码的 DSN**。
- `OpenDirectMySQL(ctx, settings, password)`：创建 MySQL 连接池（最多一个会话）、以 `PingContext` 完成真正的 TCP 连接、握手与认证。成功时由调用方负责 `Close()`；失败时立即关闭。
- `TestDirectMySQLConnection(ctx, settings, password)`：仅打开、Ping、关闭；不执行任意 SQL、不创建表、不进行自动查询、不保存凭据。
- 超时：默认连接/握手上限 10 秒；若启用连接超时，使用配置的秒数（默认 30 秒）。读取/写入超时分别遵守已启用的高级选项。外部 `context.Context` 仍可更早撤销请求。
- 第一阶段仅支持 MySQL TCP 直连。若配置启用了 TLS/SSL、SSH、HTTP、命名管道、旧版驱动、非默认字符集、压缩、兼容性覆盖及未接入的连接高级行为，连接前返回 `ErrMySQLUnsupported`，绝不悄悄忽略安全配置。禁用不安全的旧式/明文认证插件、LOCAL INFILE 和多语句。**没有加密传输的保证，不能用于不可信网络中的生产凭据**。
- `ActiveSettings` 与存储服务已经准备了今后由“已保存活动配置”建立真实会话的接口；本阶段编辑窗口中的「测试连接」针对正在编辑的选中配置，不会修改持久化的 Active 指向。

`internal/ui/shell/connection_mysql_test_state.go`：

- UI 线程组装并验证当前草稿；成功通过前置校验后才启动后台 goroutine，调用服务层测试。
- 结果由容量为 1 的通道传回，后台仅请求 `ui.Services.Invalidate()`，**不在工作线程直接修改 UI State**。
- 「测试连接」在运行时切换为「取消测试」；关闭、返回选择页或重新打开时会取消正在运行的请求，并丢弃任何迟到的旧结果。
- 连接成功/失败/取消的结果在当前窗口内提示。驱动错误中如包含当前密码，会替换为 `[已隐藏]`；不会记录密码到日志、SQLite 或状态栏。
- 「常规」页显式告知当前仅支持未启用 TLS 的普通 TCP 测试，避免把表单中的 SSL 选项误当成真正生效。

## 验证

- `mysql_connect_protocol_test.go`：本机临时端口、真正驱动协议握手和 Ping、连接关闭；尚未收到握手数据时撤销 context 可中断连接。循环 20 轮通过。
- `mysql_connect_test.go`：默认超时、读写超时、IPv6、密码配置（仅内存）、禁止危险选项、跳过一切未经实现的隧道和设置。
- `connection_mysql_test_state_test.go`：异步结果交付、取消和 OS/窗口关闭清理、过期结果隔离、密码错误脱敏、未支持的安全配置不会建立网络连接。
- 工程门禁：`go test ./...`、`go vet ./...`、`go tool mygo vet`、`go tool mygo build`，以及 Git diff 检查。

## 仍未实现

1. 真实持久数据库会话管理（对象树、SQL 执行、连接池复用、断线重连与资源所有权），当前仅提供可由未来会话管理层调用的 `OpenDirectMySQL`。
2. TLS/SSL 证书和服务端验证、SSH/HTTP 隧道、密钥库、密码保存、代理、压缩、客户端兼容性参数。
3. 真实 MySQL/MariaDB 服务器及各认证插件之间的端到端兼容性验证；本轮使用隔离协议端点，不代表这些组合已经验证。
4. 与原版的连接测试弹窗、认证失败提示、取消按钮及连接结果的逐像素/行为差分，以及不同系统 DPI 下的复核。

下一轮优先把此驱动接到独立的**可关闭、可取消、可观察状态**的会话管理器，明确用户发起连接后、连接建立后和断开时的 UI 状态，再实现 TLS 安全直连。
