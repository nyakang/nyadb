# Navicat 18 MySQL「高级」页差分与 NyaDB 实现（2026-10-21）

## 研究边界

- 原版：`temp/navicat/squashfs-root/AppRun`，在已有、独立的 Linux Xvfb `:121` 环境内，经正常试用入口观察。未绕过软件授权，未连接数据库，未修改原版程序文件。
- 原版截图（O）：`temp/research/mysql-advanced-20261021/navicat-advanced.png`、`navicat-keepalive-on.png`、`navicat-compat-button.png`、`navicat-drivers-open.png`、`navicat-charset-dropdown-real.png`、`navicat-settings-browse.png`。
- NyaDB 在原生 MyGo v0.6.0 上实现；不使用 Navicat 私有源码、商标或资源。

## 原版观察（O）

- 子窗口内容约 900×700，顶部标签维持「常规/高级/数据库/SSL/SSH/HTTP/备注」，顶部中央始终保留应用至数据库的示意图区，底部操作栏固定。
- 「高级」页从上到下：设置位置（文本和目录选择按钮）、客户端驱动程序（下拉，默认「默认」，还可选「旧版」）、客户端字符集（下拉，默认「自动」）。字符集列表前列实测含 `自动`、`None`、`armscii8`、`ascii`、`big5`、`binary`、`cp1250`、`cp1251`、`cp1256`、`cp1257`。
- 复选框与时间默认值：保活间隔 240 秒（未勾选，数字框禁用）；连接超时 30 秒（已勾选）；读取超时 30 秒（未勾选，数字框禁用）；写入超时 30 秒（已勾选）。点击保活勾选框后右侧 240 可编辑。
- 使用压缩、自动连接、使用命名管道/套接字、限制连接会话均默认未勾选；限制会话右侧默认 10，禁用时显示灰色。初始查询包含文本区域及省略号按钮。
- 设置位置「…」打开 **Find Directory** 目录选择对话框，并非文件选择器。
- 点击「兼容性」按钮会在 HTTP 和备注之间动态出现「兼容性」标签，并立即切到此标签。新标签有「使用兼容模式」及多个高级覆盖字段，默认禁用。这里只确认了默认界面，没有研究所有兼容性联动或数据库语义。

## 独立实现（I）

- `internal/ui/shell/connection_mysql_advanced_view.go`：MyGo `ui.Select`、`ui.Checkbox`、`ui.TextInput` 复刻上述高级页。可切换的复选框控制对应数字框、套接字输入的启用状态，按钮跳转兼容性子标签。
- 为 MySQL 创建独立、非敏感的 `connections.MySQLAdvanced`，包含目录、驱动类型、字符集、保活/四类超时、压缩、自动连接、命名管道和会话限制等。枚举均在持久化边界验证，超时在 1–86400 秒，会话上限在 1–10000；校验错误阻止保存。
- 本地档案数据库从 `user_version=1` 迁移到 `2`，新增 `mysql_advanced_json` 非秘密字段。旧档案记录完整保留，旧 MySQL 档案读入默认高级选项；创建/更新/复制高级参数可跨重启恢复。
- 设置位置的「…」使用 MyGo 原生目录选择器；正常编辑不影响其他数据库类型。NyaDB 保留自身应用图标和自制示意图。
- 「初始查询」以及兼容性高级覆盖项暂时禁用，原因是尚无实际驱动和安全地处理可能包含敏感内容的初始化 SQL 的策略。能保存的高级选项仅作为**配置数据**，尚未应用到数据库会话，不宣称连接功能可用。
- 字符集下拉除前列已观察到的条目外，还包含独立整理的通用 MySQL 字符集名称；这些选项尚未逐一与原版比对，不能列为 O。

## 验证矩阵

- O：以上原版窗口运行观察与截图。
- I：MyGo 新 UI、非敏感存储、向前迁移、选项校验、互相独立的数据库类型、ui.NewTester 测试。
- V：待进行逐个字段的像素、焦点、目录浏览与实机跨平台差分验证。
- Unknown：真实连接参数如何映射驱动；初始化 SQL 编辑器的完整行为；全部字符集列表；兼容性高级覆盖的完整依赖规则。

## 待办

1. 继续研究「兼容性」子页及初始查询编辑器交互，设计安全处理方式后实现。
2. 真正连接数据库前将已保存参数逐项映射到驱动并进行连接/取消/超时测试，避免仅存储不生效。
3. 真实 Windows/macOS 与实体 Linux GUI 差分。

## 本轮最终实测结果（Linux Xvfb）

- 使用 NyaDB 正式 Go/MyGo 构建，在独立 Xvfb `:124` 与 Openbox 下打开 MySQL「高级」页。截图：`temp/research/mysql-advanced-20261021/nyadb-advanced-final.png`。
- 经过第一次差分修复，「使用命名管道，套接字」标签从两行缩为单行；其右侧输入框在禁用状态下正常显示。页面大体顺序与原版一致，但复选框、字体度量和颜色仍有差异。
- 点击「兼容性」按钮能出现动态子标签；返回「高级」正常。截图：`temp/research/mysql-advanced-20261021/nyadb-compatibility-final.png`。
- 点击设置位置旁的省略号能实际打开 MyGo Linux 原生目录选择器（截图：`temp/research/mysql-advanced-20261021/nyadb-settings-directory-picker.png`），用户取消后没有写入配置。
- Go 服务测试、SQLite v1→v2 迁移测试、MyGo Tester 的控件联动与保存后重新编辑测试通过。本阶段只确认 UI/配置行为，不宣称 DB 握手通过或像素级 V。
