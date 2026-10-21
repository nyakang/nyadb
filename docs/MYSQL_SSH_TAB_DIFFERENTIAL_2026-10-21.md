# MySQL「SSH」标签页原版观察与 NyaDB 第一轮复刻（2026-10-21）

## 原版观察（O）

- 在独立 Xvfb `:121` / Openbox 下，使用 Navicat 18 原版 `temp/navicat/squashfs-root/AppRun` 的正常试用流程观察。不修改原版文件、不绕过授权、不连接生产数据库。
- MySQL「SSH」标签页在新建连接窗口中。默认「使用 SSH 隧道」未勾选。主机/用户名为空、端口默认 `22`，验证方法默认为「密码」。未启用隧道时输入控件不可编辑。
- 启用 SSH 后，顶部连接示意图由应用—数据库变为应用—SSH 服务器—数据库（两段连接线中间出现锁形标记），主机、端口、用户名、认证方式等可编辑。
- 验证方法下拉实测仅有三项：「密码」、「公钥」、「密码和公钥」。
- **密码模式**：显示密码遮蔽输入与「保存密码」勾选框。原版截屏出现非空圆点，但其来源及实际秘密内容未知，不能据此复制密码默认值。原版保存密码勾选状态可能受用户偏好影响，不能据一次观察宣称全局默认。
- **公钥模式**：密码区域消失，变成 `Private Key:` 文件路径与「…」浏览、`Passphrase:` 遮蔽输入、「Save Passphrase」。
- **密码和公钥模式**：两个认证区同时显示，顺序为密码、保存密码、Private Key、Passphrase、Save Passphrase。
- Private Key「…」打开原生文件对话框 `Open`，文件过滤器为 `All Files (*)`。
- 主要原版截图：`temp/research/mysql-ssh-20261021/original-default.png`、`original-enabled.png`、`original-methods.png`、`original-second-method.png`、`original-combined-method.png`、`original-key-picker.png`。不能从这些截图推断 SSH 握手、Host Key 校验或密钥口令持久化机制。

## NyaDB 独立实现（I）

- 新增 `connections.MySQLSSH`，保存非秘密 SSH 端点元数据：隧道启用状态、主机、端口、用户名、认证方法和私钥文件路径，嵌入现有的 MySQL Advanced JSON。原有档案缺少 SSH JSON 时自动补默认值；无需新的数据库 schema 迁移。
- 使用 MyGo `ui.Checkbox`、`ui.TextInput`、`ui.Select` 和原生文件选择器；三种认证模式动态显示对应字段。默认 SSH 端口为 22，字段可用性随「使用 SSH 隧道」状态切换。
- 端口接受 1–65535 的整数；认证方法枚举和各非秘密文本字段由连接服务验证。配置保存、更新、复制及重新编辑恢复通过服务测试。
- 顶部 SSH 服务器示意图使用 NyaDB 独立绘制的服务器 SVG 和已有 MyGo 原生图标，没有复用 Navicat 私有素材。
- 密码和 Passphrase 分别存放在 `State.mysqlSSHPassword` 和 `State.mysqlSSHPassphrase`，使用遮蔽输入，不进入 Profile / SQLite / 日志。选择「上一步」、取消、保存、重新编辑或 OS 关闭窗口均清理它们。
- 「保存密码」与「Save Passphrase」因安全密钥库尚未接入而禁用。认证密钥文件仅记录路径，不读取或拷贝其内容。异步文件选择使用会话版本号，拒绝上个连接窗口的迟到结果。
- 真实 SSH 隧道未实现；即使保存了配置也没有建立 SSH 会话，没有验证 Host Key 或转发数据库连接。不能称为原版 SSH 功能等价。

## 验证与差异

- MyGo `ui.NewTester`：SSH 开关及示意图、认证模式条件显示、主机/端口/用户名配置、私钥浏览触发、失效的旧文件选择结果、安全清理。
- 服务层测试：初始默认值、存储/更新/复制、旧档案兼容、非法端口/认证枚举/无效文件路径拒绝。
- Linux Xvfb `:127` / Openbox 实际 GUI 观察，窗口内容大小 **900×700**。截图：`nyadb-default.png`、`nyadb-enabled-password.png`、`nyadb-auth-methods.png`、`nyadb-public-key-real.png`、`nyadb-combined-method.png`、`nyadb-native-privatekey-picker.png`，均位于 `temp/research/mysql-ssh-20261021/`。
- 工程检查：`go test ./...`、`go vet ./...`、`go tool mygo vet`、`go tool mygo build`。
- 原版 SSH 锁标记、美术细节和字体间距仍不同；未进行 Windows/macOS 实机对照，不宣称像素级差分（V）。

## 下一步

1. 继续 MySQL HTTP 与备注页的原版运行观察和 MyGo 实现。
2. SSH 连接服务需要明确 Host Key 校验、密钥存储、取消、超时、异步会话生命周期及转发安全边界，再实现真实隧道行为。
3. 实现跨平台真实 GUI 的 O/I 对照及 V 验收。
