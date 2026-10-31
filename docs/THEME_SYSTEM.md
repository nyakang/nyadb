# NyaDB 多配色主题系统

2026-10-31。当前实现以本文件为准，早期主题记录见 [2026-10-27](THEME_REFACTOR_2026-10-27.md)。

## 使用方式

打开 **查看 → 主题 → 主题设置…**，分别选择「明暗模式」和「配色方案」。也可以在 **查看 → 主题 → 配色方案** 中快速切换。

提供经典、海洋、森林、玫瑰、暖沙五个配色家族，每个家族都有独立的浅色和深色版本。「跟随系统」只决定采用哪一版，不改变配色家族。例如「森林 + 跟随系统」会在森林浅色和森林深色之间切换。

选择即时生效并自动保存。主窗口、连接窗口、数据与对象视图、设置预览和 SQL 编辑器共用主题；切换只重新绘制，不重建数据库会话、SQL 缓冲区或数据文档。

## 配色设计

保持数据库工作台的紧凑尺寸，通过背景层次、文字、强调色、选择状态和语法颜色共同表达风格。错误、警告、成功继续保持红、金、绿的语义；数据库品牌图标保留品牌色。

| 配色（持久化 ID） | 风格 | 浅色工作区 / 强调色 | 深色工作区 / 强调色 |
| --- | --- | --- | --- |
| 经典 `classic` | 中性灰阶、经典蓝，兼容原有外观 | `#FFFFFF` / `#0879D1` | `#232629` / `#009DFF` |
| 海洋 `ocean` | 冷白、蓝灰、海水蓝 | `#F5F9FF` / `#1266AD` | `#172536` / `#70BAFF` |
| 森林 `forest` | 雾白、松绿、翡翠绿 | `#F5FAF6` / `#22704E` | `#1B2923` / `#7BCC9E` |
| 玫瑰 `rose` | 柔和灰紫、玫瑰粉 | `#FDF7FB` / `#A23C72` | `#2D2230` / `#EEA2CA` |
| 暖沙 `sand` | 暖白、褐灰、琥珀金 | `#FCF8EF` / `#946019` | `#2C2720` / `#E5BD7B` |

以下为应用实际渲染的 SQL 工作区，包含模拟数据；左列浅色，右列深色。

![五套主题的十种明暗版本](theme-system-preview.png)

## 主题分层与模块边界

```mermaid
flowchart LR
    DB[localdb 偏好] --> Bootstrap[bootstrap 加载与保存]
    Settings[shell 设置页与菜单] --> Bootstrap
    Settings --> Choice[Mode + Scheme]
    Choice --> Resolve[theme.ResolveScheme]
    OS[MyGo 系统明暗与辅助功能偏好] --> Resolve
    Resolve --> Snapshot[窗口本次构建的 Snapshot]
    Snapshot --> Native[MyGo 原生控件]
    Snapshot --> Workbench[导航、数据、对象、连接视图]
    Snapshot --> SQL[SQL 编辑器与预览]
```

- **配色定义**：`internal/ui/theme/schemes.go` 定义稳定的 `Scheme` ID、可翻译的展示名称、浅深两组基础颜色与 SQL 颜色。`Schemes()` 返回元数据副本，业务模块不能修改主题注册表。
- **解析**：`ResolveScheme(scheme, mode, systemDark, preferences)` 是纯函数，返回包含 `Palette`、`SQL`、明暗结果和文字缩放的值类型 `Snapshot`。渲染预览不修改当前偏好。
- **每帧应用**：主窗口和连接窗口在根视图调用 `Apply(c)`，通过 MyGo 根元素的 `ui.Local` 保存本次构建的快照。`Colors(c)`、`SQLThemeFor(c)` 读取同一快照；控件局部 `SetTheme` 或构建中发生的点击不能改变已解析的颜色。下一次根视图构建才整体更新。无全局窗口缓存，也不跨帧保存 `ui.Context`。
- **兼容性**：`Mode`、`SetAppearanceHandler`、`Resolve`、`PaletteFor`、`SQLTheme` 保留原接口；后三者仍表示经典主题的显式解析。新预览使用 `ResolveScheme`，应用视图使用帧快照。
- **跨窗口协调**：shell 更新用户选择，通过注入的保存器和通知回调交给 bootstrap。bootstrap 持有 SQLite、设置 MyGo 原生明暗来源并刷新主窗口与连接窗口。theme 和业务 UI 模块不依赖存储或驱动。

## 语义颜色与控件规范

| 角色 | Token / 入口 | 规则 |
| --- | --- | --- |
| 背景层次 | `App / Panel / Sidebar / Menu / Toolbar / Header` | 配色定义不同层次的底色；避免业务视图内写颜色字面量 |
| 文字、图标 | `Text / Muted / Blue / Gold / Green / Purple` | `Blue` 为历史兼容名称，在新配色中表示主要强调图标色 |
| 主操作与焦点 | `Accent / AccentText / AccentHover / AccentPressed / Focus` | 主按钮的正常、悬停、按下文字对比度至少 4.5:1 |
| 表格与导航选择 | `Selection / SelectionText / NavigationSelection / NavigationSelectionText` | 表格使用 `theme.Table`，导航使用 `NavigationOutline`；选中行的 NULL、状态文字使用 `CellText` |
| 网格和输入框 | `GridAlternate / GridNull / InputBorder` | 跟随当前配色；高对比度下输入边界保持可见 |
| 待处理记录 | `Notice / NoticeText` | 从警告语义派生柔和背景，文字保持 4.5:1；应用/放弃仍为原生按钮 |
| 统计图 | `Chart` | 按配色选择，和工作区背景至少 3:1 |
| SQL | `Snapshot.SQL / SQLThemeFor` | 每套配色定义关键字、函数、字符串、数字等；注释保留斜体 |

新配色的正文、次要文字在工作区、侧栏和表头背景上至少 4.5:1。SQL 选区保留语法色，因此使用较淡的选择背景；`readableInk` 会适当调整语法文字亮度，保证新配色在正常、活动行和选区背景下至少 4.5:1。经典 SQL 配色保持原有基线，不声明它满足新配色的全部对比度约束。

## 系统偏好与原生平台

- **经典**沿用系统强调色（系统提供时）；其他四套配色使用自己设计的强调色，防止桌面强调色覆盖用户选中的颜色家族。
- 所有配色保留系统高对比度与现有文字缩放支持；DPI 和减少动效继续由 MyGo 管理。设置页内容可滚动，控件预览行在小窗口中换行。
- 跟随系统时，设置页显示当前解析为浅色或深色。MyGo 的主题更新通知触发窗口重绘，不写回或覆盖用户的 `Scheme`。
- GTK 原生弹出菜单和系统标题栏沿用平台皮肤并跟随明暗模式；内容区域使用 NyaDB 配色。Linux 保留现有可恢复 GTK 设置逻辑，以及显式 `GTK_THEME` 和桌面高对比度的优先权。

## 存储与失败处理

复用 `app_preferences`，不改变 SQLite schema 和连接档案格式：

| 键 | 值 | 缺失或未知值 |
| --- | --- | --- |
| `appearance` | `dark / light / system` | `dark`，保持原默认行为 |
| `color_scheme` | `classic / ocean / forest / rose / sand` | `classic`，保留已保存的明暗模式 |

两项独立写入，启动时在创建窗口前读取。保存成功才更新当前选择并通知其他窗口；保存失败时保留旧选择，在设置页和状态栏显示「无法保存主题设置，请重试。」，可以再次点击目标选项重试。不会把未保存的状态标为已保存。

## 添加内置主题

1. 在 `schemes.go` 增加稳定 ID 和 `schemeDefinitions` 条目，提供浅深两组背景、文字、边界、强调色和 SQL 色。
2. 在中英文 JSON 目录中增加名称与描述，然后运行 `go tool mygo generate` 和 `go tool mygo i18n check`。不要直接改生成的 Go 文件。
3. 运行主题对比度、窗口同步、存储恢复测试；扩展主题数量断言及原生验证脚本的预期侧栏颜色。
4. 生成并检查浅深两版设置页、SQL 与表格截图。菜单和设置页自动从注册表生成。

本次提供内置配色，不引入主题文件解析或可执行插件。

## 验证与复现

主题测试覆盖五个家族的浅色、深色和系统解析、快照隔离、桌面偏好优先级、对比度、主窗口/连接窗口同步及 SQL 草稿保留。bootstrap 测试使用临时 SQLite，关闭并重新打开后验证全部 15 个偏好组合，并覆盖旧配置缺失配色键和未知配色值。shell 测试覆盖主题卡片、原生菜单、分别保存及失败后重试。

截图生成：

```bash
NYADB_THEME_CAPTURE="$PWD/temp/theme-system-2026-10-31" \
  go test ./internal/ui/shell -run '^TestThemeGalleryVisualMatrix$' -count=1
```

Linux 真实窗口验证使用临时 `XDG_CONFIG_HOME` 和隔离 Xvfb，不连接数据库：

```bash
go build -o build/theme-system/nyadb ./cmd/nyadb
xvfb-run -a -s '-screen 0 1440x960x24' /usr/bin/python3 \
  tools/validate_theme_gui_linux.py --binary build/theme-system/nyadb
```

脚本逐一切换十种明暗配色，同时等待实际窗口像素和 SQLite 偏好吻合，再验证主题卡片、系统模式、GTK 菜单明暗及重启恢复。证据保存在 `temp/theme-system-2026-10-31/native/verification.json` 及同目录截图中。

Windows/macOS 的原生桌面行为仍需对应平台运行环境；交叉编译只验证构建。小窗口和放大文字截图是设置页验证，不代表所有复杂工作区在任意字号下都已完成布局验收。
