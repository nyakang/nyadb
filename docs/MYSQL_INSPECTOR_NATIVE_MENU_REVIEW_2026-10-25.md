# NyaDB — MySQL 字段信息窄窗与原生列头菜单复验（2026-10-25）

## 实施范围与约束

基于已推送的 `71e5ccd` 开展第三个独立 UI 区域改进，保持数据页只读。没有新增 DML/DDL、全量 BLOB 读取、WebView 或第二套网格控件。

- `internal/ui/shell/mysql_data_inspector.go`：选中字段的名称及完整 `database.table` 来源在窄信息栏中改为单行受约束文本，保留原始全名的悬浮 Tooltip。右栏底层由原生 Scroll 继续展示真实数据库字段类型、默认值、键/自增、精度及安全数据预览；未改动分块读取边界。
- `internal/ui/shell/mysql_data_columns.go`：原有 `ui.Menu` 列头动作全部接入 `locales.Text`，中英文内容一致，无隐藏/复制/宽度等操作语义变化；动态字段名不翻译。
- `locales/zh-Hans.json`、`locales/en.json`、`locales/catalog_gen.go`：新增 8 条列头菜单翻译，并通过 MyGo 的 `generate` 与 `i18n check` 维护 accessor。
- `internal/ui/shell/mysql_data_inspector_menu_alignment_test.go`：使用 `ui.NewTester` 检查 180/205 像素宽信息栏下真实元数据标题保持可达，以及英文原生菜单动作和复制结果。

## Linux 原生窗口实拍证据（I）

实验复用同一个固定的隔离 SQL fixture，SHA-256 `06c2f4c0e792fb2289e47a4b9900339ab2947597ce697c5b7a26f9402e374c2c`，运行 MySQL 8.4.11，一次性容器只暴露 `127.0.0.1:34306`；测试账户仅对两个研究 schema 有 SELECT/SHOW VIEW。独立 Xvfb/Openbox 1600×1000，NyaDB 1280×800 与 840×560 客户区，深色主题，实际双击 `nyadb_struct_a.child` 读取三行表数据。

- `temp/research/inspector-menu-review-20261025/04-data-1280-client.png`：字段 `id` 真实元数据、字段标题/来源、数据网格与状态栏。
- `temp/research/inspector-menu-review-20261025/05-data-840-client.png`：窄窗口字段信息单行路径布局与右侧受约束面板。
- `temp/research/inspector-menu-review-20261025/06-column-menu-1280-client.png`：在 `id` 双行列头右击后获得的实际 GTK 弹层。可见 **复制字段名称、复制此列（当前页）、隐藏此列、显示/隐藏列、显示全部列、将此列宽度适配当前页、重置全部列宽、显示字段类型**；它并非 `ui.NewTester` 无弹层截图。
- 每张图对应原始全桌面 PNG 和 JSON 记录（实际客户端尺寸、窗口裁剪、二进制 SHA、X11 虚拟 DPI、render_stability、操作流程）。图片和原生调试脚本在被忽略的 research 路径；没有复制到正式源码或产品资源。
- 取证脚本仅启动 NyaDB 和本轮自有测试容器，完成后清理。**没有启动 Navicat 原版，没有连接生产数据库。**

## 测试和证据等级

| 验证 | 结果 |
| --- | --- |
| `go test ./...` | PASS |
| `go vet ./...` | PASS |
| `go tool mygo vet` | PASS |
| `go tool mygo build` | PASS，Linux/amd64 |
| `go tool mygo i18n check` | PASS，zh-Hans/en 327 条消息 |
| `git diff --check` | PASS |
| 窄字段栏、英文菜单复制原生 tester | PASS（I，无窗口） |
| Linux Xvfb 真窗口、真实只读 MySQL、右键菜单 | PASS（I，原生运行实拍） |
| 同条件 Navicat Qt 字体/尺寸/像素 V | **BLOCKED** |

**O（已有证据）**：原版 Navicat 的数据表头菜单包含复制、显示/隐藏列、适应列宽、显示字段类型等动作，见 `docs/MYSQL_DATA_FIELD_METADATA_NAVICAT_DIFFERENTIAL_2026-10-22.md` 和 `temp/research/mysql-keyset-diff-20261022/navicat-column-context-root.png`。本轮没有重新授权运行原版，因此只可用作历史行为参考，不是同条件逐像素 V。

**Unknown**：原版菜单每条行高/字体/颜色精确值、跨平台 GTK/Windows Qt 对照、用户操作每一种菜单项的完全原版状态机。右侧完整字段属性可能比历史 Navicat 截图更长；本轮只解决标题和路径窄栏截断及菜单翻译，不宣称整个字段面板的像素级复刻已完成。

后续严格视觉门禁 `tools/strict_visual_differential.py` 需双方相同数据服务、fixture、选中字段、视图状态、字体及缩放记录后才能计算 ROI、2 DIP 几何与 3 RGB 色阶结果。当前明确 **V = BLOCKED**。
