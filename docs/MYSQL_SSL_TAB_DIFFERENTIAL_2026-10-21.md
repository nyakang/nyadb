# MySQL「SSL」标签页差分与第一轮复刻（2026-10-21）

## 原版观察（O）

- 原版 `temp/navicat/squashfs-root/AppRun` 在独立 Linux Xvfb `:121` / Openbox 中通过普通试用入口运行。本轮不输入凭据、不连接生产数据库、不修改原版文件。
- MySQL「SSL」标签页的窗口内容区约 900×700；顶部保留连接示意区，下面顺序为「使用 SSL」、「使用验证」、「客户端密钥」、「客户端证书」、「CA 证书」、「验证针对 CA 的服务器证书」、「指定密码」。
- 默认「使用 SSL」和「使用验证」均不勾选；文件输入、三个浏览按钮、CA 验证开关及密码输入均禁用。见 `temp/research/mysql-ssl-20261021/original-default.png`。
- 勾选「使用 SSL」后连接示意线上出现小锁图标，并使「使用验证」可勾选；但证书文件和密码栏仍不可用。见 `original-ssl-enabled.png`。
- 再勾选「使用验证」，三个文件输入、浏览按钮、CA 验证开关、指定密码均变为可编辑；「验证针对 CA 的服务器证书」默认不勾选。见 `original-verify-enabled.png`。
- 勾选 CA 验证后开关显示选中，界面不增加字段。见 `original-verify-ca-toggle.png`。
- 点击客户端密钥浏览「…」弹出文件选择对话框，标题 `Open`，文件过滤器为 `All Files (*)`。见 `original-key-browse.png`。
- 没有证据说明原版密码保存方式、服务端握手选项映射或证书验证失败行为。本轮不推断这些行为。

## NyaDB 实现（I）

- 使用 MyGo v0.6.0 原生 `Checkbox`、`TextInput.Password` 和 `Button`；MySQL 配置页通过 `ui.TabsBase` 切换至 SSL 面板。
- 添加独立 `connections.MySQLSSL`，只保存 `Enabled`、`UseVerification`、`ClientKey`、`ClientCertificate`、`CACertificate`、`VerifyServerCA` 等**非秘密配置**，嵌入已有的 MySQL Advanced JSON，无需升级 SQLite schema。路径最长 4096 Unicode 字符，不能包含 NUL/换行。
- 「使用 SSL」控制「使用验证」是否可操作；只有两者都启用时证书路径、三个文件浏览按钮、CA 验证及指定密码才能编辑。关闭父开关不会清除已输入的非敏感配置值。
- 点击「…」由 bootstrap 使用 MyGo 原生 `Dialog.Open` 选择文件；文件内容不由 NyaDB 读取。异步回调记录编辑会话版本，避免窗口关闭/切换后旧文件选择结果覆盖新草稿。
- SSL 密钥口令使用遮蔽输入，只放在 `State.mysqlSSLPassphrase`；返回上一步、保存、取消、OS 关闭或重新编辑时清理，且**从不写入 Profile 或磁盘**。
- 数据库驱动尚未连接：保存 SSL 开关及路径**不等于**配置已应用到真实 TLS 通道，不能宣称 SSL 握手或证书验证成功。

## 验证

- 服务测试：SSL 配置创建/读取/更新/复制、非法路径拒绝、关闭开关后仍保留可复用的证书路径。
- UI 测试：默认状态、两级勾选联动、CA 验证状态、文件选择路径、旧会话异步结果防护、密码清理与不落盘。
- Linux Xvfb `:126` 实际 GUI：窗口实测 900×700。截图：
  - `temp/research/mysql-ssl-20261021/nyadb-ssl-default.png`
  - `temp/research/mysql-ssl-20261021/nyadb-ssl-verification.png`
  - `temp/research/mysql-ssl-20261021/nyadb-ssl-key-browse.png`
- 工程检查：`go test ./...`、`go vet ./...`、`go tool mygo vet`、`go tool mygo build`。
- 本轮没有跨平台 GUI / 像素级 V。原版 SSL 激活后示意图小锁图形、字体细节和密码持久化语义仍待后续研究。

## 后续

1. MySQL 驱动接入后，设计 SSL/TLS 行为映射和证书加载、验证策略，并以本地测试服务验证；不能把已保存元数据直接认定为已生效安全连接。
2. 密钥口令如需持久保存，先实现安全密钥库，禁止明文落盘。
3. 完成 SSH、HTTP 等标签页后进行跨平台真实 GUI 与差分验证。
