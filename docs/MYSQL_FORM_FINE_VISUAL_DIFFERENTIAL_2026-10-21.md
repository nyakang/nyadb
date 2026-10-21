# MySQL 表单精细视觉差分：复选框、输入焦点与禁用态（2026-10-21）

## 证据与范围

- **O：原版已授权运行观察。** 本轮在已经运行的 Navicat 18、独立 Xvfb `:121` 上重新通过普通连接选择器进入 MySQL「常规」「高级」标签。仅点击和截图，不保存原版连接档案、不使用生产数据库。
- **I：NyaDB 独立 Go + MyGo v0.6.0 实现。** Xvfb `:129`、Openbox、1600×960 屏幕、900×700 连接窗口。对照时扣除原版与 NyaDB 的窗口装饰/屏幕摆放偏移；截图原样保存在 `temp/research/mysql-fine-visual-20261021/`。
- **V：限定验证。** MyGo 组件行为和当前 Linux GUI 均已回归，尚未通过完整逐像素、跨平台差分；输入法、不同缩放和全部 hover/focus 动画仍需继续检验。

## 观察结果与收敛

| 元素 | O：原版观察 | I：NyaDB 修正 |
|---|---|---|
| 复选框 | 方框约 13–14 px，暗色底、浅灰对勾，不使用鲜蓝背景；截图区域主色约 `#232629` | 在共享连接表单引入 MyGo `CheckboxBase`（14 DIP、1 DIP 角/边框），自绘灰白矢量勾选路径；保留原生鼠标、Space、焦点与可访问性语义 |
| 未选/禁用颜色 | 暗灰背景，禁用标签/输入降低对比度 | 统一边框、底色与禁用配色，不把灰度写死到各 MySQL 标签页 |
| 输入框背景 | 正常输入深灰约 `#232629`，与内容背景 `#31363b` 区分 | 只在连接编辑器内部应用窗口局部 Theme.Surface / Border |
| 输入框焦点态 | 灰蓝细边框；在复核截图采样约 `#457e9c`，不是纯亮蓝填充 | 采用 MyGo 原生 TextInput 的焦点/IME/选中处理，只替换局部 Theme.Focus / Accent，使焦点边框接近 O |
| 输入文本基线 | 原版使用 Qt 字体渲染，文本在约 25 px 高字段内部居中 | 保留 MyGo 原生 26 DIP 输入框及上下对称 padding，不以像素偏移补丁改动文本输入；新增光标在输入区域内的无窗口断言 |
| 下拉选择器 | 原版箭头按钮造型与 MyGo 不同 | 继续使用 MyGo 原生 Select，采用与输入框一致的局部表单色；箭头外形差异保留后续处理 |
| 安全选项 | Navicat 的保存密码选项可有勾选态 | NyaDB 因尚未实现安全密钥库而保持不可操作；**没有**启用任何秘密明文存储 |

参考截图：
- `navicat-general-real-focus.png`、`navicat-advanced-real-default.png`、`navicat-advanced-real-focused.png`：2026-10-21 本轮重新取得的 O。
- `nyadb-general-focus.png`、`nyadb-advanced-refined.png`、`nyadb-advanced-refined-focus.png`：I，焦点与勾选路径前后对照。
- `final-{general,advanced,ssl,ssh,http}.png`：最终构建五页默认状态回归；`final-{ssl,ssh,http}-enabled.png`：启用状态；`montage-final.png`：归纳预览。

## 实现方式

- `internal/ui/shell/connection_form_style.go` 新增**编辑器局部**配色配置和共享 `connectionFormCheckbox`，提供紧凑版 `connectionFormCheckboxCompact`。没有修改全局 `theme.Apply`，主工作台的 Theme 不变。
- 复选框使用 `ui.CheckboxBase`，不是仅可看的图形替身。自绘对勾采用 MyGo `Path` 和 `Painter.StrokePath`；早期使用斜向 `Painter.Line` 会出现细节缺失，在本轮真实 GUI 中发现并修正。
- 「常规」「高级」（含兼容性）、「SSL」「SSH」「HTTP」将适用的复选框切换为统一封装。已有启用开关、条件禁用、文件选择回调和密码会话清理逻辑保持不变。
- 原生输入框、Select、标签和焦点事件仍由 MyGo 处理。修改仅影响连接窗口的表单风格，不修改 SQL 执行、配置 schema、数据库驱动或其他工作区组件。

## 验证

- `ui.NewTester` 新增复选框鼠标/Space 切换、禁用不可切换、复选框像素描边、原生输入编辑、禁用不可编辑和光标在输入框内部的测试；历史的 `TestSharedMySQLFormGeometry`、SSL/SSH/HTTP 联动/秘密不落盘测试继续运行。
- Xvfb :129 的 900×700 原生 MyGo 窗口已手动轮巡「常规 / 高级 / SSL / SSH / HTTP」和 SSL/SSH/HTTP 启用态，无溢出或裁剪。
- 工程门禁：`go test ./...`、`go vet ./...`、`go tool mygo vet`、`go tool mygo build`。

## 未完成事项

1. Navicat Qt 与 MyGo 的字体抗锯齿、选择箭头外形和焦点动画仍不同；不得把本轮限定的行为回归冒充整个窗口像素级 V。
2. 非 MySQL 数据库类型尚未在所有桌面系统及 DPI 下完整差分。
3. 密码、TLS/SSH/HTTP 实际连接能力仍是之前记录的独立功能缺口，与本轮绘制改动无关。
