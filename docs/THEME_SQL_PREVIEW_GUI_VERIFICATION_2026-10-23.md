# NyaDB SQL 明暗主题、设置页面与平台 GUI 验证（2026-10-23）

## 实现范围（I）

- 原生 MyGo `editor.State.Theme` 接入 NyaDB 语义化 SQL 深色与浅色编辑器配色；保留 `LanguageNamed("SQL")` 的原生分词、文本缓冲、光标、IME 和虚拟行绘制能力。
- 关键字、函数、类型、字符串、数字、注释、操作符、非法 token 及行号、当前行、光标、选区和缩进线使用不同的明暗调色。
- 已打开的 SQL 页签每次构建时重新绑定 `theme.SQLTheme(c.Theme())`，不替换编辑器缓冲区。
- 新增可关闭且可复用的「外观设置」工作区标签。入口为「查看 → 主题 → 主题设置…」和「工具 → 选项…」。
- 设置页提供深浅主题交互卡、跟随系统选项、SQL 原生只读高亮预览、原生按钮 / 输入 / 复选框以及三行表格。中英文已纳入 MyGo 生成的语言目录。
- 主题选择沿用本轮第一阶段的 `app_preferences.appearance` 持久化（`dark`、`light`、`system`），不接触任何数据库凭据。

## 自动测试（I）

- `internal/ui/theme/sql_test.go` 验证两种 SQL 色板与各 token 对比。
- `internal/ui/shell/settings_workspace_test.go` 验证设置页只读原生编辑器、切换卡片、单例设置标签、已有 SQL 编辑器跨主题更新且缓冲状态未替换。
- `go test ./...`、`go vet ./...`、`go tool mygo vet`、`go tool mygo build`、`git diff --check` 作为工程门禁。

## Linux 真实 GUI（I，已验证）

环境：隔离的 Xvfb 1440×900×24 + Openbox、实际编译的 `build/linux-amd64/nyadb` 原生 GTK/X11 窗口。独立临时 HOME/XDG_CONFIG_HOME，不读取用户连接档案，不连接数据库，也不运行 Navicat。

可复现操作：

```bash
go tool mygo build
xvfb-run -a -s '-screen 0 1440x900x24' python3 tools/validate_theme_gui_linux.py
```

经真实 X11 输入执行「工具 → 选项…」打开独立设置标签、浅色卡片点击与页面再次激活。屏幕固定采样点从深色 `(36,36,36)`（`#242424`）切换至浅色 `(255,255,255)`（`#FFFFFF`）。独立临时 `config.sqlite` 的 `app_preferences.appearance` 为 `light`，表明界面与偏好一致。证据保存在**不打包、不提交**的研究目录：

- `temp/research/theme-gui-2026-10-23/03-settings-dark.png`：深色下完整主题卡片及 SQL 原生语法高亮。
- `temp/research/theme-gui-2026-10-23/04b-settings-light-reopened.png`：浅色下同一页面、选中主题和 SQL 原生语法高亮。
- 同目录的 `01-main-dark.png`、`02-tools-menu.png`、`04-settings-light.png` 等为启动与实际交互补充截图。

在无 GPU 的 Xvfb 软件渲染环境下，主题切换后的一次刷新具有明显延迟：脚本使用显式的实际窗口等待后再执行像素判断。不得将短时间截图里仍为深色误判为已经即时刷新；其他 Linux 桌面/GTK 原生弹出菜单的外观仍需要独立环境验证。

## Windows 交叉构建通过，真实 GUI 待验证

已执行 `go tool mygo build -platform windows/amd64`，生成 `build/windows-amd64/NyaDB.exe`（约 17.3 MB）。Windows 可执行文件交叉编译成功，当前 Linux 环境未安装 NSIS，因此未生成 Windows 安装包。**交叉构建并不等于在 Windows 上真实运行验证。**

已通过授权的 Windows Computer Agent 观察到现有的 NyaDB Dev.exe 原生窗口，其桌面可用且旧版深色主界面可见。但**该进程在本轮代码完成前启动**，Agent 的应用构建身份报告为 unavailable，并且 Windows Agent 的 command 权限/实现不可用、注册 app 清单为空，因此目前无法把本轮 Linux 源码对应的最新 Windows 构建部署到该主机。不能将观察到的旧版界面称为新版的明暗主题/设置页验收（Windows 新版 GUI 状态：**Unknown**）。

Windows 完整验收仍需要使用与本次代码关联的 Windows 可执行文件，在真实 Windows 桌面下记录：

1. NyaDB 应用、构建 SHA/源码指纹、Windows 版本与 DPI；
2. 「工具 → 选项…」打开、单例设置页复用、深浅卡片及系统选项；
3. SQL 实际编辑器与只读预览两种主题，光标、滚动、选择、编辑和关闭标签后的状态；
4. 关闭并重启后保存的主题是否复原，原生菜单、连接子窗口与主窗口是否同步；
5. 96、125、150% DPI 各一组截图（可用的缩放值按实际系统调整）。

这轮只进行 NyaDB 内部验证；尚无同快照 Navicat 差分材料，因此不声明 V。
