# 主题系统收敛计划与实施（2026-10-27）

此文件为历史记录；多配色扩展与当前规范见 [主题系统](THEME_SYSTEM.md)。

目标：消除绘制过程中的全局可变颜色，保留系统外观偏好，修复选中内容的可读性，并让 Linux 原生菜单能随应用主题切换。

## 实施计划

| 阶段 | 内容 | 验收方式 | 状态 |
| --- | --- | --- | --- |
| 1. 主题核心 | 引入值类型 Snapshot；迁移工作区和连接表单颜色；区分应用偏好与窗口有效外观 | 不同窗口明暗隔离、旧快照不被修改、系统模式切换测试 | 完成 |
| 2. 组件与系统偏好 | 接入文字缩放、系统强调色和高对比度；统一选中前景/背景、NULL、交替行和状态色 | 原生控件偏好测试、颜色对比度测试、选中 NULL 与列重排像素测试 | 完成 |
| 3. 原生平台与预览 | 使用可恢复的 GTK 设置替代环境变量固定主题；设置页展示禁用操作、NULL 和网格状态 | Linux 真实窗口与原生菜单明暗切换、SQLite 偏好检查、构建与静态检查 | 完成 |

## 已实施

- `theme.Resolve(mode, systemDark, preferences)` 返回独立的 `Snapshot` 值；`theme.Apply(c)` 将它映射到 MyGo。应用模式单独保存，绘制不再修改 `theme.App/Panel/Text` 等进程颜色；这些旧变量以及连接表单/Chrome 的颜色缓存已删除。
- 视图使用 `theme.Colors(c)` 得到按当前窗口外观解析的局部 Palette。原生主题的局部覆盖不会改变其他窗口或业务组件的色板。`PaletteFor` 提供不受当前设置影响的预设预览。
- 应用字号基线仍是 12 DIP。`theme.Font` 缩放显式字号，`RowHeight` 为文字相关行和控件增加空间；GTK 紧凑菜单字号也随系统文字偏好更新。MyGo 继续负责显示器 DPI 缩放和减少动效。
- 系统强调色有值时覆盖主操作和选中颜色；高对比度提高边框与次要文字对比度，并保留深色输入框边界。重复应用主题不会累计文字缩放或颜色混合。
- `SelectionTheme`、`theme.Table/Outline` 为原生网格和树应用同一组选中颜色；自定义连接配置行显式使用 `SelectionText`。`CellText` 根据 MyGo 的 `CellSelected` API 处理 NULL、行号和状态文字，支持列重排后的选择。
- 深色背景、工具栏及正常密度的布局保留基线。深色主按钮和选中区域改用黑色前景；主按钮按下背景略微提高亮度，使预设及测试系统强调色的普通/Hover/Pressed 文字对比度达到 4.5:1。
- Palette 新增 `GridAlternate/GridNull/InputBorder/Error/Warning/Success`。业务错误提示、原生控件状态与 SQL 诊断使用对应状态色；SQL 注释、行号和辅助线响应高对比度，光标响应系统强调色。
- GTK 深色回退现在在初始化后的 UI 线程临时设置 `gtk-theme-name=Adwaita`，由 MyGo 设置明暗偏好；切到浅色或系统时恢复桌面属性。显式 `GTK_THEME`（含空值）和高对比度桌面设置具有优先权。菜单 CSS 复用同一个 provider，更新字号时不累积 provider。
- 设置页增加禁用操作，表格预览采用相同的交替行/网格线，并包含可选中的 NULL。中英文目录通过 MyGo 生成。

## 验证

以下检查通过：

- `go test -count=1 ./...`；最终代码另通过 `CGO_ENABLED=0 go test ./...`。
- `go vet ./...`、`go tool mygo vet`、`go tool mygo i18n check`、`git diff --check`。
- `python -m unittest discover -s tests -v`：33 项测试通过（使用下述临时 venv）。
- `go tool mygo build`：Linux 原生应用及包生成成功。
- `CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ... ./cmd/nyadb` 和对应的 `GOOS=darwin GOARCH=arm64` 编译成功；产物在忽略目录 `build/theme-verification/`。

Linux GUI 使用隔离 Xvfb/Openbox 和临时 `XDG_CONFIG_HOME`，不读取用户连接档案，不连接数据库。复现：

```bash
go tool mygo build
xvfb-run -a -s '-screen 0 1440x900x24' python3 tools/validate_theme_gui_linux.py
```

Python 需要 Pillow；本轮安装在忽略目录 `temp/theme-refactor/venv`，未改变项目依赖。

最终 Linux 构建经真实 X11 操作完成 **深色 → 浅色 → 深色 → 系统 → 浅色 → 重启**。工作区空白区域从 `(35,38,41)` 切换为 `(255,255,255)`，GTK 弹出菜单从 `(47,47,47)` 切换为 `(255,255,255)`；系统模式恢复桌面菜单，SQLite 保存对应模式，重启后主窗口恢复浅色。

截图和构建身份保存在忽略目录 `temp/research/theme-gui-2026-10-27/`：

- `03-settings-dark.png`、`04b-settings-light-reopened.png`：同一设置页的明暗外观。
- `04c-native-menu-light.png`、`04d-native-menu-dark.png`、`04f-native-menu-system.png`：原生 GTK 菜单切换。
- `06-sql-editor-scrolled-light.png`：SQL、控件及表格预览。
- `07-restarted-light.png`：重启恢复浅色。
- `build-identity.json`：源码/二进制 SHA-256、基准 Git commit、采样值与恢复的偏好。源码包含未提交变更。

软件渲染环境下刷新存在延迟。脚本已改为等待实际窗口背景像素变化，并修正当前菜单/标签坐标；不会仅凭固定等待时间或 SQLite 值判断界面切换成功。

## 验证边界

本轮为 NyaDB 独立实现与验证（I）。没有对应 Navicat 差分证据，不声明 V。Windows/macOS 交叉编译不代表真实桌面验收；这些平台的原生菜单和多 DPI 桌面截图仍需对应运行环境。系统文字缩放已通过无窗口控件测试，所有复杂窗口在极大字号下的完整布局仍需专项验收。用户主动设置的 `GTK_THEME` 仍可能固定原生菜单的外观，这是保留显式用户设置的行为。

自定义主题文件、多套密度预设与主题编辑器不属于本次变更。
