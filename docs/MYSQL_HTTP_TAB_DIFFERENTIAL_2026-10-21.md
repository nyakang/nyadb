# MySQL「HTTP」标签页原版观察与 MyGo 实现（2026-10-21）

## 原版运行观察（O）

- 在独立 Linux Xvfb `:121` + Openbox 中通过正常试用流程操作 `temp/navicat/squashfs-root/AppRun`，只观察已授权程序 UI；没有修改原版文件，也未连接生产数据库。
- MySQL「HTTP」主页面：默认「使用 HTTP 隧道」未勾选，包含「隧道网址」和「使用 base64 编码传出查询」复选框，下面是两个子标签「验证」「代理」，底部为「导出隧道脚本…」按钮。
- 启用 HTTP 后，连接示意图出现 HTTP 服务器节点；字段由灰色禁用转为可编辑。默认验证子页有「使用密码验证」（默认未选）及用户名、密码、保存密码；还有「使用证书验证」（默认未选）、客户端密钥、客户端证书、CA 证书和通行短语。分别勾选验证方式后，相关输入框和原生文件浏览按钮才可操作。
- 代理子页有「使用代理」（默认未选）、主机、端口 `8080`、用户名、密码、保存密码。启用代理后才可编辑子字段。「使用 base64 编码传出查询」是独立开关。
- **HTTP 与 SSH 隧道互斥**。观察到 SSH 已启用时 HTTP 整页禁用；HTTP 已启用时 SSH 整页禁用，并且顶部连接示意区继续显示已启用的隧道类型。
- 原版密码框在观察环境中出现圆点，可能是已有偏好或窗口残留，**不能由截图推断真实默认口令或秘密持久化行为**。
- 原版「导出隧道脚本」存在，但其独立脚本格式、数据库通信协议和导出安全性尚未研究，本轮不复制原版脚本。
- 观察截图位于 `temp/research/mysql-http-20261021/`：`navicat-default.png`、`navicat-http-ssh-off.png`、`navicat-http-enabled.png`、`navicat-password-auth.png`、`navicat-certificate-auth.png`、`navicat-proxy-default.png`、`navicat-proxy-active.png`、`navicat-base64-on.png`、`navicat-ssh-while-http.png`。

## 独立实现（I）

- 新增 `connections.MySQLHTTP` 并嵌入现有 MySQL Advanced JSON；新增独立 MyGo `ui.TabsBase`、`Checkbox`、`TextInput`、Password 输入和原生文件选择器，未使用 WebView、私有源码或原版素材。
- 可配置：HTTP 隧道 URL、base64 查询开关、HTTP 密码认证、用户名、证书认证及三个证书文件路径、代理启用/主机/端口/用户名。代理默认端口 8080。
- 连接配置的 normalize 严格校验非秘密字段、端口范围 1–65535，并拒绝 SSH/HTTP 同时启用。URL 仅允许无凭据、查询参数与 fragment 的 `http(s)` 绝对地址，以降低把访问令牌写入 SQLite 的风险；目前不支持需要查询参数的隧道 URL。
- HTTP 密码、代理密码及证书通行短语只存于当前编辑窗口的 State 中；保存、关闭、返回上一步、重新编辑或 OS 窗口关闭时清理。不向 SQLite / 日志写入，也不执行任何 HTTP 请求。保存密码控制因系统安全密钥库尚未接入而禁用。
- 文件浏览使用 MyGo 原生 `Dialog.Open`，并依据当前编辑会话版本确认异步文件选择结果，忽略过期回调。不会读取文件内容或尝试载入私钥。
- 「导出隧道脚本…」保留控件及位置，但禁用：尚未拥有独立设计、经过安全审查的可执行脚本实现，不能把原版二进制或脚本直接复制出来。
- 开启 HTTP 隧道后顶部使用 NyaDB 自制图标展示 HTTP 服务器。此前 SSH 连接示意也改为跨标签显示，与原版切换页面后的状态行为相符。
- 所有选项目前**只保存为配置**；真实 HTTP 隧道握手、请求编码、代理连接及证书验证尚未实现，不应宣称功能等价。

## 验证情况

- Go 服务测试：默认值、JSON 保存/读取/修改/复制、历史记录缺字段自动补默认值、非法端口/URL/文本和两种隧道互斥拒绝。
- MyGo `ui.NewTester`：主开关、验证与代理子标签、子复选框联动、Base64 开关、代理端口输入校验、SSH/HTTP 互斥、文件选择、旧窗口过期回调、防止秘密持久化。
- Linux Xvfb `:128` 真实 GUI：新建连接 900×700；实测默认、密码和证书验证、代理页、文件选择器。发现子标签多出空行和底部按钮被遮挡，已修正内层 TabsBase 的父级布局及面板尺寸，再进行复查。
- 证据截图：`nyadb-default-fixed.png`、`nyadb-verification-enabled.png`、`nyadb-certificate-picker.png`、`nyadb-proxy-default.png`、`nyadb-proxy-enabled.png` 等，均在同一研究目录。
- 工程门禁：`go test ./...`、`go vet ./...`、`go tool mygo vet`、`go tool mygo build`。尚未对 Windows/macOS/实体 Linux 做完整 UI 测试及像素级 V 验收。

## 下一步

1. 继续 MySQL「备注」标签页的原版观察及 MyGo 实现，补足整个新建连接窗口的基础配置页。
2. 研究安全的 HTTP 隧道协议、密码库与独立脚本导出实现；需要真实本地测试服务和差分验证，不能仅有 UI 就宣称连接可用。
3. 建立跨平台 GUI 截图回归与像素差分，收紧尺寸、字体与颜色偏差。
