# MySQL「数据库」标签页原版观察与第一轮实现（2026-10-21）

## 原版观察（O）

- 原版在隔离 Linux Xvfb :121 下使用正常试用界面操作，未连接生产数据库。截图：`temp/research/mysql-database-20261021/navicat-database-default.png`。
- 「数据库」页不是一个“默认数据库”输入框。顶部有「使用自定义数据库列表」复选框，默认未选中；下方是宽表格，列标题为「数据库」「自动打开」，底部有「添加数据库到列表」「从列表中移除数据库」按钮。
- 默认未勾选时，空表格处于禁用状态，两个操作按钮禁用。
- 勾选后原版尝试访问本机 MySQL 元数据，在本研究环境出现错误 2002（本地 socket 连接失败）；确认错误后复选框保留勾选，空表格可用，「添加数据库到列表」按钮可用。截图：`navicat-database-checked.png`、`navicat-after-unavailable.png`。没有可用 MySQL 服务，故 **未确认有真实数据库时添加对话框和每行「自动打开」的操作语义**；不推断其默认内容。

## NyaDB 独立实现（I）

- 使用 MyGo 原生 `ui.Table` 构建两列空表格，保持 900×700 窗口内的布局、顶部复选框与底部双按钮结构。
- `MySQLAdvanced.UseCustomDatabaseList` 存储自定义列表开关，通过现有非敏感 MySQL JSON 档案保存，无需改动本地 SQLite schema；保存/重新编辑后恢复。
- 尚无真实 MySQL 驱动会话，因此列表数据必须为空，不伪造库名；添加与移除按钮禁用并提供可访问说明。勾选只更新当前配置，不尝试联网。待真实数据库服务接入后再实现读取列表、添加、删除与自动打开。
- 与 Navicat 的按钮可用性不同是明确的安全/能力限制，并非差分验证通过。

## 界面验证与测试

- NyaDB 在隔离 Xvfb :125 下真实运行；窗口几何实测 900×700。截图：`temp/research/mysql-database-20261021/nyadb-database-default.png`、`nyadb-database-custom-enabled.png`。
- MyGo `ui.NewTester` 测试包括：页面导航、原生表格标题、复选框默认状态与切换、禁用添加按钮不触发网络流程、保存及重新编辑保留自定义列表开关。
- 工程验证：`go test ./...`、`go vet ./...`、`go tool mygo vet`、`go tool mygo build`。
- 原版逐项行为与像素级对照（V）、数据库实际连接、平台交叉差分均待后续。
