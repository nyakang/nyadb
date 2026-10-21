# MySQL 兼容性与初始查询编辑器差分（2026-10-21）

## 范围与边界

- Navicat 原版运行观察（O）：`temp/navicat/squashfs-root/AppRun`，沿用隔离的 Linux Xvfb `:121` 环境，以正常试用流程操作，不触及授权、生产数据库或程序文件。
- NyaDB（I）：使用 Go / MyGo v0.6.0 独立实现，使用 MyGo 原生 `Checkbox`、`Select`、`TextInput`、`TextArea`、`Modal`。
- 本轮不声称数据库驱动已实现覆盖参数的语义，也不执行初始 SQL。

## 兼容性子页原版观察（O）

- 在 MySQL「高级」点击「兼容性」，顶栏才出现动态「兼容性」标签；排在 HTTP 和备注之间，点击后立即进入。
- 顶部「使用兼容模式」默认不勾选。其下五项强制覆盖均默认不勾选、子输入禁用。
- 启用父复选框后，五项子复选框可单独点击；仅子项勾选时对应下拉框或文本输入可用。
- `lower_case_table_names` 默认 `0`；下拉候选为 `0`、`1`、`2`，下面有提示 `跳过 "show variables like 'lower_case_%'"`。
- `sql_mode` 是文本输入而非下拉。其完整默认值通过运行时文本选中与剪贴板验证为 `ONLY_FULL_GROUP_BY, STRICT_TRANS_TABLES, NO_ZERO_IN_DATE, NO_ZERO_DATE, ERROR_FOR_DIVISION_BY_ZERO, NO_ENGINE_SUBSTITUTION`。
- NDB 支持下拉默认 `Not Support`，另一个选项为 `Support`。
- 数据库列出方法默认 `SHOW DATABASES Statement`，另可选 `INFORMATION_SCHEMA`。
- 视图列出方法默认 `SHOW FULL TABLES Statement`，另可选 `INFORMATION_SCHEMA`。
- 截图：`temp/research/mysql-compat-initial-query-20261021/compat-default.png`、`compat-enabled.png`、`lowercase-dropdown.png`、`sqlmode-checked.png`、`ndb-dropdown.png`、`database-methods.png`、`view-methods.png`。

## 初始查询编辑器原版观察（O）

- 在「高级」页「初始查询」后点击省略号，会打开叠加于当前连接子窗口的编辑弹层（没有独立 OS 标题栏），居中约 700×500。
- 弹层顶部标题「初始查询」；正文含说明、黑底多行编辑区和 SQL 示例 `e.g.`、`SET ROLE ALL;`、`SET @var_name = 'value';`（示例仅作占位）。
- 底部有「复制到剪贴板」、「确定」、「取消」。
- 多行编辑后点取消，返回「高级」页且不更新值；确认后把 SQL 以单行形式回填到主表单的预览字段。
- 截图：`initial-query-dialog.png`、`initial-query-typed.png`、`initial-query-after-cancel.png`、`initial-query-multiline.png`、`initial-query-confirmed.png`。

## NyaDB 独立实现（I）

- 新增 `connections.MySQLCompatibility`，嵌入现有 `MySQLAdvanced` JSON 配置；沿用 SQLite v2 schema，不需要迁移列。
- 每个覆盖选项保存开关和值；父开关关闭时子项不可修改，但原来选择和值保留，重新开启可以继续编辑。所有枚举值与 `sql_mode` 文本长度及 NUL 由服务层验证；旧档案缺少兼容性字段时读入原版默认值。
- 初始查询编辑使用 MyGo `ui.Modal` + `ui.TextArea`，确认/取消/复制到剪贴板均已实现；最多 64 KiB，禁止 NUL。确认更新单行预览，取消放弃本次修改；返回、取消、关闭连接窗口和保存均清除临时内容。
- **隐私安全边界**：初始 SQL 可能包含口令，因此这一步只支持当前窗口内的临时编辑，不写入 Profile / SQLite / 日志、不发往数据库。再次编辑已保存档案时初始 SQL 为空。它与原版持久保存行为尚不等价。
- 兼容性设置**仅保存配置，尚未应用到数据库驱动**；不宣称 SQL 模式 / 表大小写等配置实际生效。

## NyaDB GUI 与工程验证

- 真实 GUI 在 Linux Xvfb `:124` 启动新构建，确认「兼容性」及「初始查询」弹层布局、确认后预览。截图：
  - `nyadb-compat-final.png`
  - `nyadb-initial-query-modal-first.png`
  - `nyadb-initial-query-edited.png`
  - `nyadb-initial-query-confirmed.png`
- `ui.NewTester`：原生标签导航、父子复选框联动、初始查询取消/确认/超长错误/复制到剪贴板/生命周期清理。
- 服务测试：JSON 保存/更新、非法枚举拒绝、旧数据兼容、确认 SQL 不落盘。
- 工程检查：`go test ./...`、`go vet ./...`、`go tool mygo vet`、`go tool mygo build`。
- 未通过像素级 V，Windows/macOS 和实体 Linux 桌面尚未验证；多行编辑器文本的键盘快捷键与高亮细节仍需继续差分。

## 后续

1. 数据库驱动接入后，明确哪些兼容性覆盖可以安全映射为驱动行为，并验证结果。
2. 为需要跨会话保存的初始化 SQL 设计安全凭据存储/敏感信息审查，而不是明文写入档案。
3. 完成 MySQL 其他标签页并进行系统性的跨平台 GUI 差分。
