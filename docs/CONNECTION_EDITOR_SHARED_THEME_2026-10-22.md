# NyaDB 连接窗口共享主题收敛（2026-10-22）

## 问题

用户提供的 Windows 截图显示主工作台使用中性深灰配色，而 MySQL「新建连接」属性窗口的内容区域和表单采用蓝灰色。原因有两个：

1. 主工作台每帧调用 `theme.Apply(c)`，但独立原生连接子窗口的 `ConnectionWindowView` 之前没有应用相同主题；MyGo 窗口间不会自动继承上级的 Theme Context。
2. 连接编辑器保留了一组早期独立的蓝灰硬编码值（如 `#31363b`、`#30353a`、`#232629`、`#457e9c`）。主窗口则使用 `internal/ui/theme/theme.go` 中的中性深灰色板。

## 修复（I）

- 在原生 `ConnectionWindowView` 根部应用 `theme.Apply(c)`，让新建连接的类型选择器和编辑窗口从相同的基础主题启动。
- 编辑窗口不再维护旧蓝灰面板色板；内容区域、顶部标签、底部按钮、分隔边框分别使用 NyaDB `theme.App`、`theme.Header`、`theme.Panel`、`theme.Border`。
- `applyConnectionEditorFormTheme` 先应用共享主题，再统一设置输入/复选框的深灰 Surface、Text、Muted、Focus 与 Accent。焦点与主操作按钮复用 NyaDB 蓝色；保留 MyGo 原生输入、IME、Tab 焦点、禁用态、Select 与 Checkbox 行为。
- 编辑态 258 DIP 配置文件侧栏复用 `theme.Sidebar`、`theme.Header` 和 `theme.Accent`，避免侧栏残留独立蓝灰/青色高亮。
- 仅调整视觉色板来源与标签栏填充，不改字段尺寸、表单布局、连接业务逻辑、配置数据、驱动或持久化格式。

## 证据与验证

**I — MyGo 无窗口渲染测试**：`TestConnectionWindowUsesWorkbenchPaletteOnEveryTab` 验证 900×700 原生窗口模式下的「常规 / 高级 / 数据库 / SSL / SSH / HTTP / 备注」全部标签页，检查实际像素的 Header、App、Panel 配色和底部原生操作；`TestConnectionChooserSharesWorkbenchTheme` 验证类型选择器使用工作台背景。现有表单焦点、复选框、交互、配置保存测试全部保留。

运行 `NYADB_CONNECTION_THEME_CAPTURE=<directory> go test ./internal/ui/shell -run TestConnectionWindowUsesWorkbenchPaletteOnEveryTab` 可导出全部标签页的渲染截图；本次输出仅在 `temp/research/connection-theme-unification-20261022/`。

**I — 真实 Linux GUI**：在隔离 Xvfb :137 / Openbox 启动最终构建的 NyaDB，真实打开连接类型选择器、新建 MySQL「常规」「高级」「SSL」「SSH」标签，截图：
- `main-workbench.png`
- `native-type-chooser-ready.png`
- `native-mysql-general.png`
- `native-mysql-advanced.png`
- `native-mysql-ssl.png`
- `native-mysql-ssh.png`

已观察新建连接主体与主工作台呈相同中性深灰，并使用相同的蓝色主操作和焦点边框。新建和切换各标签页时不需要数据库连接，也没有打开或修改 Navicat 原版文件。

**工程门禁**：`go test ./...`、`go vet ./...`、`go tool mygo vet`、`go tool mygo build`、`git diff --check`。

## 限制

- 本轮是 **NyaDB 主窗口与 NyaDB 原生连接子窗口之间的视觉一致性**，不是 Navicat 原版像素级差分。系统标题栏、窗口边框在 Linux Openbox 和 Windows 上仍由 OS 渲染，可能有平台差异。
- 仍可继续改进暗色高 DPI 字体抗锯齿、非活跃控件对比度及弹出菜单阴影。没有实现新的连接功能。
