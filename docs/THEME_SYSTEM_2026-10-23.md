# NyaDB 原生主题系统（2026-10-23）

## MyGo 与 NyaDB 的边界

MyGo Native UI 原生支持外观来源（`ThemeSystem`、`ThemeLight`、`ThemeDark`），
`Theme.OnUpdated` 和窗口 `ui.Context.Theme()/SetTheme()`，以及系统强调色、
高对比度、文字缩放与减少动效等偏好。NyaDB 通过这些原生 API 切换窗口和控件外观，
不引入 CSS/WebView 或自制菜单组件。

NyaDB 在 `internal/ui/theme` 提供面向数据库工作台的语义化 Palette。
UI 以这些 token 绘制菜单栏、工具栏、导航、数据工作区、边界、文字与语义状态色。
保留既有深灰色板作为默认，以不改变既有深色差分基线。

## 使用与持久化

菜单路径：**查看 → 主题 → 跟随系统 / 浅色 / 深色**。选项单选勾选；
切换立即更新 MyGo 的 native appearance，并刷新主窗口与连接子窗口。
偏好保存到用户配置 SQLite 的 `app_preferences`（schema v8），键名
`appearance`，值为 `system` / `light` / `dark`。
旧数据库自动迁移；缺失或未知值回退 `dark`，不修改连接档案或凭据。
跟随系统仅在该模式下动态采用操作系统的浅色/深色偏好。

## 实现细节

- MyGo 的 `ui.Theme` 负责原生输入框、表格、焦点、选区、间距等控件状态。
- NyaDB Palette 负责语义色；`theme.Apply` 每次构建根视图时匹配外观，
  所以主窗口和独立的原生连接窗口不会再分叉。
- 历史上暴露的 `theme.App/Panel/... ` 是进程级颜色 token；
  此轮为兼容既有视图保留，并限定其读写在 MyGo UI 线程。
  后续可逐步改为 `Context` 传递的不可变 Palette，进一步消除全局状态。
- 连接编辑器原先捕获的字段颜色和 Chrome 色在每次渲染时重新绑定。
- 数据库品牌标志仍维持真实品牌色，状态色针对深浅模式分别调整。

## 平台限制与后续验收

Linux 上原有的 GTK3 深色原生菜单兼容回退 `GTK_THEME=Adwaita:dark`
在**以深色启动**时仍生效；以浅色/跟随系统启动时不设置该变量。
GTK_THEME 是 GTK 初始化阶段读取的环境设定，因此某些 Linux 桌面可能需要
重新启动应用才能使 GTK 原生弹出菜单的皮肤与切换后的主题完全一致；
MyGo 原生绘制工作区会即时切换。不能把无窗口测试当作真实 GTK 验证。

证据：MyGo 文档为上游公开 API（S）；源码与相应测试为 NyaDB 独立实现（I）。
尚无 Navicat 浅色主题同 DPI 截图，因此不标 V。
