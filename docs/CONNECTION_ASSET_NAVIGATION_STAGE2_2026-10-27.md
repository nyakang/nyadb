# NyaDB 连接资产导航第二阶段：编辑、筛选和底栏

日期：2026-10-27

## 目标

在第一阶段 `d06f02b` 基础上，细化连接导航的原生交互，避免重命名时树整体位移、活动标签定位后丢失用户筛选，同时保留 MyGo 原生搜索和右键菜单机制。

## 独立实现（I）

1. 对已经存在的连接组，右键“重命名”或 F2 显示在原树行里的名称输入框及保存/取消图标；新增组时仍在树首插入编辑行。两条路径继续通过 `connections.Service.CreateGroup/RenameGroup` 持久化，不复制原版实现。
2. 重命名输入框使用原生 `ui.TextInput`，首次出现时自动聚焦；Enter 或保存按钮提交，取消不持久化；检查错误仍可展示。
3. “导航至活动标签的对象”临时清空搜索、数据库类型、星标筛选时保留一次先前值，并在底栏显示“恢复之前的筛选”按钮。用户自主改变筛选会清除暂存内容，避免覆盖用户的新选择。
4. 搜索框保持 MyGo `ui.SearchField`，缩小边框和圆角装饰，面板底部原有过滤、星标、定位及全部折叠仍保持顺序和原生交互。
5. 使用独立绘制的通用勾选图标，不导入专有图标素材；补充恢复筛选的中英文词条。

## 验收

- `TestNavigatorGroupRenameEditsInsideOriginalTreeRow` 覆盖树行布局、真实服务层重命名、取消后名称保持。
- `TestActiveTabNavigationRestoresPriorFiltersOnRequest` 覆盖搜索、类型、星标筛选快照／恢复和手动修改优先。
- 原有菜单目标身份测试、组/连接 CRUD、导航过滤与数据库对象读取测试不得退化。
- 运行 `go test ./...`、`go vet ./...`、`go tool mygo vet`、`go tool mygo build`、`go tool mygo i18n check`、`git diff --check`。
- 此阶段 GUI 自验属于 I；没有新原版同尺寸同状态证据，**不宣称 Navicat 严格视觉 V**。

## 后续设计门禁

多连接会话仍是独立架构工程：现有 MyGo UI 工作台只有一个活动 MySQL context，虽然服务层 `MySQLSessionManager` 已以 ProfileID 管理多连接。下一阶段先做每个连接独立的会话/异步作用域，再隔离元数据缓存、数据与结构页签代际，最后覆盖真实多连接桌面操作；不能简单删掉 `startMySQLSession` 中的 `disconnectMySQLSession()`。

## 验证记录

- `go tool mygo generate`、`go tool mygo i18n check`：PASS，共 439 条本地化消息。
- `go test ./...`：PASS（新增原位编辑与筛选恢复测试）。
- `go vet ./...`、`go tool mygo vet`、`go tool mygo build`、`git diff --check`：PASS；Linux/amd64 原生应用构建成功。
- NyaDB 隔离 Xvfb/Openbox 实际 GUI：工作台成功启动，`temp/research/navigation-asset-stage2-20261027/native/main.png` 已截图核对；使用一次性 XDG 配置、无数据库连接。
- 组内编辑和筛选恢复由 MyGo `ui.NewTester` 控件交互验证（I）；没有同状态真实 Navicat GUI 差分（V），也没有 Windows 真实窗口测试。
