# MyGo 原生紧凑菜单适配（2026-10-25）

## 目标

NyaDB 保留 MyGo 原生 HMENU 菜单交互，针对 Windows 11 的偏大菜单行高和字形，启用可选的紧凑菜单密度。不使用 WebView、自绘前端菜单或 Win32 Hook。所有研究均使用独立实现，不复制 Navicat 私有资源。

## 版本和配置

- 上游 MyGo：`v0.6.3`
- NyaDB fork：`github.com/nyakang/mygo v0.6.3-nyadb.2`（`nyadb/v0.6.3` 分支）
- 新 API：`mygo.App.SetMenuDensity(mygo.MenuCompact)`。NyaDB 在 `bootstrap.Run` 中、`App.Run` 前设置。
- 默认：`mygo.MenuDefault`，完全沿用 MyGo 原生默认菜单指标；必须主动调用紧凑模式才变化。
- Windows 10、macOS 保留原生系统菜单。Linux 继续由 `internal/bootstrap/gtk_menu_density_linux.go` 的 GTK CSS 管理。
- 菜单密度配置必须在 `App.Run` 前完成，不允许运行中改变已创建的 Windows 菜单句柄或字体缓存。

## Windows 11 菜单参数（逻辑像素 DIP）

| 指标 | 默认 Fluent | NyaDB Compact |
| --- | ---: | ---: |
| 弹出菜单行高 | 32 | 24 |
| 分隔线高度 | 9 | 5 |
| 高亮留白 | 4 | 2 |
| 文字内边距 | 12 | 8 |
| 标记列宽 | 28 | 24 |
| 快捷键间距 | 32 | 20 |
| 原生菜单栏参考高度 | 30 | 26 |
| 字体 | 系统菜单字体放大 14/12 | 系统菜单字体原始大小 |

仍然由 Windows 自身按 DPI 缩放；无障碍更大字体保留系统给定高度。弹出菜单仍由 Windows HMENU、MFT_OWNERDRAW、系统键盘命令和辅助功能机制管理。

**注意：** NyaDB 顶部“文件/编辑/查看”等标签本身是 MyGo UI 绘制的水平按钮，`MenuCompact` 修改的是点击后出现的 Windows 原生下拉菜单，以及左侧连接资产右键菜单。MyGo 自带传统菜单栏在 Windows 11 的适配也已包含。

## 验证范围

- I：MyGo API 测试、平台状态测试、Windows 菜单几何/字体策略测试；Windows 编译检查（`GOOS=windows GOARCH=amd64 go test -c`）和 Vet。
- I：NyaDB `go test ./...`、`go vet ./...`、`go tool mygo vet`、`go tool mygo build`、Windows AMD64 交叉编译。
- Unknown：Windows 11 真机 100% / 125% / 150% 的截图差分，键盘和多级子菜单的实际桌面验收；编译通过不代表真实 GUI 通过。
- MyGo fork 的完整测试中，`plugins/terminal/internal/pty/TestClose` 在当前 Linux 环境失败（进程未随终端关闭，重复可复现）。该测试与本轮菜单修改没有直接关系，但不能声明 MyGo fork 全量测试通过。

## 后续视觉差分验收

同 DPI、相同菜单内容与窗口大小，分别采集 Navicat 和 NyaDB 的顶部「查看」「工具」菜单，以及「我的连接」标题／分组／资产的右键菜单；测量字号、行高、分隔线和宽度，并检查 Ctrl/Alt 快捷键、Esc 关闭、方向键导航、子菜单翻转、禁用态、亮色/暗色主题和高对比度模式。完成前不标 V。
