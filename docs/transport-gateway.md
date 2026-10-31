# SSH 与 NyaDB TCP 网关

实现证据为 I：独立 Go 实现、临时回环 SSH/HTTPS/WebSocket 服务器测试、UI Tester；原生桌面与 Navicat 差分仍为 Unknown。协议为 NyaDB 自有 `nyadb-tcp-v1`，不兼容原版 PHP 隧道或私有脚本。

## 连接

SSH 页填写跳板机、端口、用户及密码/私钥/组合认证，数据库主机填写跳板机可访问的数据库地址。首次物理连接暂停握手，UI 显示 `SHA256:…` 指纹并要求确认。确认后的公开指纹原子写入配置目录 `NyaDB/ssh-hosts-v1.json`；重启仍校验，指纹变化、损坏或不可读的旧文件拒绝连接。公开信任记录与秘密分开。

HTTP 页填写 HTTPS 网关 URL、管理员配置的目标 ID、访问令牌及可选 CA/mTLS/HTTP CONNECT 代理。令牌只走 Authorization 请求头；目标 ID 使用 X-NyaDB-Target-ID，客户端数据库 Host 不传给网关。目标路由由网关配置限定。外层 TLS 校验网关证书及主机名；连接的数据库 TLS 仍由数据库驱动独立校验。

SSH 密码、私钥口令、网关令牌、代理密码及 mTLS 私钥口令保持内存；选择「在系统密钥库保存隧道凭据」后写入独立 `NyaDB transport secrets v1` 命名空间。连接/备用配置分别保存，不修改已有数据库密码账户键。系统密钥库不可用时可在编辑器输入并使用内存；不能覆盖无法读取的旧秘密。PEM 私钥支持常见 SSH/OpenSSH 加密，mTLS 口令支持传统加密 PEM；加密 PKCS#8 mTLS 文件需要外部解密后使用，失败明确返回。

SSH 同一传输范围共享一个 SSH 连接及多个 direct-tcpip 通道；HTTP 每条 WebSocket 对应一条数据库 TCP 连接。二进制消息最多 64 KiB，应用层保持字节流。元数据连接池、SQL 固定物理租约和数据事务持有同一传输范围。通道读取/写入错误会撤销整个隧道范围；断线后的新拨号被拒绝，只有显式连接创建新范围。不会重放 SQL、临时表、事务或提交。主动关闭单页通道不影响其它租约。SSH 取消会关闭原 SSH 连接并等待拨号执行器回执；SSH 通道期限适配器等待中断的读写执行器回执。

旧的 base64 查询和用户名/密码 HTTP 脚本配置仍可解码，base64 入口明确禁用，不会当作新协议使用。

## 网关部署

执行 `bash tools/gateway/package.sh` 生成 `build/gateway/` 的 Linux 二进制、配置、systemd 模板、文档和 SHA256SUMS；脚本只创建本地安装包。Windows/macOS 可通过相应 GOOS 编译 `./cmd/nyadb-gateway`。

管理员在自己的网关环境进行以下部署：

1. 将二进制安装到 `/usr/local/bin/nyadb-gateway`，创建无登录权限的 `nyadb-gateway` 系统账户。
2. 将配置模板复制到 `/etc/nyadb-gateway/gateway.json`，填写 TLS 证书/私钥与严格限定的目标 ID/服务器地址。删除不使用的示例目标。
3. 建立 `/etc/nyadb-gateway/token.env`，内容为 `NYADB_GATEWAY_TOKEN=<高熵令牌>`；权限 0600，由 systemd 读取。证书私钥只允许服务账户读取。令牌不能放入 URL 或日志。
4. 安装 systemd 模板，再由管理员执行 daemon-reload、enable/start。限制防火墙暴露范围并按证书主机名连接。

HTTPS 强制启用 TLS 1.2+，不会提供明文模式。配置 `clientCA` 可额外要求并校验客户端证书（mTLS）；令牌仍然必需。令牌验证失败、目标 ID 不在配置或子协议不同会拒绝。网关没有数据库凭据，认证始终由数据库驱动完成。SIGTERM/退出关闭已升级的通道和原 TCP，随后关闭 HTTP 服务。日志只记录连接状态、耗时和上/下行字节数；不记录 SQL、令牌、数据库认证、目标地址或载荷。

## 验证边界

`go test -race ./internal/transport ./internal/services/credentials` 覆盖二进制类型保真、并发通道、TLS 拒绝、认证/目标拒绝、HTTP CONNECT 代理认证、SSH 密码/私钥/组合与加密口令、首次确认和指纹变化、断线撤销/无新通道、取消及回执、公开信任记录跨实例和损坏保留、秘密内存/密钥库与丢失保护。MySQL 真实会话测试由 `NYADB_MYSQL_CORE_IT=1` 显式开启，fixture 仅连接测试启动的临时回环数据库。
