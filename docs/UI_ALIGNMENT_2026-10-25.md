# NyaDB UI/UX 对齐实施与验收记录 — 2026-10-25

本轮已实施工作台、统一导航、连接选择器与编辑器，以及现有对象／结构／数据／SQL 模拟工作区的调整。**实现和回归检查通过；整页严格差分尚未完成，不能标记整体 V。**

## 实施范围

- 保持 MyGo `v0.6.3-nyadb.2`，无驱动公共接口、档案格式或 SQLite schema 修改。
- `internal/ui/theme/metrics.go` 集中紧凑字体、栏高、图标和面板尺寸。原版本轮客户区采样修正初始导航／信息面板内容宽为 250／200，分隔各 4；覆盖计划中约 251／205 的初始估计。用户拖动的宽度在会话和文档间共享。
- 原生虚拟树统一组、连接、数据库、分类、对象；对象身份包含连接 ID、数据库、类型和名称。同名对象互不混淆；选择连接不联网。展开读取依次排队，保留缓存、失败重试、取消和迟到结果保护。
- 树、对象列表、右键目标和信息面板同步。修正组右键后目标回退、首次点击对象首行、跨库 ListState 恢复、刷新后行序变化等问题。导航根菜单清除旧目标，空白处提示符合真实行为。
- 选择器为 900×700，列表行高 19、图标 16；确认选择后才更新最近使用，避免点击时网格移动。原生滚动、固定底栏和取消清理覆盖小窗口。
- 常规／高级表单保持 604 宽；SSL／SSH／HTTP 根据新 O 证据改成左右 20 的宽表单；统一标签 166、行距 31、输入 26。七个主页、兼容性、初始化查询和配置侧栏沿用既有草稿与凭据边界。
- 对象页稳定保留操作栏及列头。固定版本 MyGo 对零行 Table 有重复 ListState 异常，空状态通过共享列定义、宽度和横向滚动绘制列头，未伪造数据行。
- 数据页保留双行列头、固定行号、独立页签状态、筛选排序、分页和字段语义；大字段复制／分块操作固定在信息面板顶部，预览独立滚动。
- 结构页保持字段／索引／外键子页和属性区；SQL 页明确模拟状态，无真实 SQL 执行；未支持命令禁用。
- 更新中英文目录并生成 accessors；README 与状态文档区分当前能力和历史记录。

## 可复核截图

原版专有画面仅在 ignored 研究目录，未复制进正式资源或代码。下列均为真实 Linux Xvfb 原生窗口采图；`headless-connections/` 单独属于 `ui.NewTester` 实现证据。

| 页面 | 修改前 NyaDB | 修改后 NyaDB | 本轮验收范围 |
| --- | --- | --- | --- |
| 工作台 | [before](../temp/research/ui-alignment-20261025/baseline-native/00-workbench-group-client.png) | [after](../temp/research/ui-alignment-20261025/native/10-workbench-client.png) | 原生 I；指定边界／颜色诊断通过，严格 V BLOCKED |
| 连接选择器 | [网格](../temp/research/ui-alignment-20261025/baseline-native/01-chooser-grid-client.png)、[列表](../temp/research/ui-alignment-20261025/baseline-native/02-chooser-list-client.png) | [网格](../temp/research/ui-alignment-20261025/native/connection-00-chooser-client.png)、[列表](../temp/research/ui-alignment-20261025/native/connection-01-list-client.png) | 原生 I；逐控件 V 未完成 |
| MySQL 七页 | [before 索引](../temp/research/ui-alignment-20261025/baseline-native/observation-index.json) | [常规](../temp/research/ui-alignment-20261025/native/connection-02-general-client.png)、`connection-03` 至 `08` | 七页原生 I；常规指定输入框诊断通过 |
| 兼容性／初始查询／小窗口 | 本轮独立 before 未补齐 | `connection-09`、[初始查询](../temp/research/ui-alignment-20261025/native/connection-10-initial-query-client.png)、[720×520](../temp/research/ui-alignment-20261025/native/connection-11-small-client.png) | 原生 I；不可计作整页完成 |
| 对象／数据／结构 | 本轮同 fixture before 未补齐 | [对象](../temp/research/ui-alignment-20261025/native/13-objects-client.png)、[数据](../temp/research/ui-alignment-20261025/native/14-data-client.png)、[结构](../temp/research/ui-alignment-20261025/native/15-structure-client.png) | 真实 MySQL 读取 I；缺原版读取，V BLOCKED |
| SQL／设置 | 本轮独立 before 未补齐 | [SQL 模拟](../temp/research/ui-alignment-20261025/native/17-sql-demo-client.png)、[设置](../temp/research/ui-alignment-20261025/native/18-settings-client.png) | 原生 I；整页 V 未完成 |

修改前由 HEAD `efe506e06b12c5549e7073fcbe4175e09861774a` 只读归档独立构建，363 个源文件逐项与归档一致；构建记录见 [baseline-build.json](../temp/research/ui-alignment-20261025/baseline-build.json)。未对当前工作树执行 reset 或切换版本。每张采图 JSON 记录运行二进制 SHA、客户区、稳定帧、场景状态及未知字体／缩放；不同轮次截图以各自 SHA 为准。

## 交互与检查

真实桌面观察记录：组／连接选择；选择器网格与列表切换、选择 MySQL、下一步；七页切换；兼容性与初始化查询打开／取消；编辑窗口缩至 720×520 后底栏仍可见；Escape 取消窗口；显式输入一次性凭据连接；展开数据库、列表读取、双击打开数据、显式设计表打开结构；关闭非活动数据页签保持结构页；打开 SQL 模拟页、设置页、切换浅色及英文。

自动回归覆盖档案／分组、搜索／星标、键盘和右键目标、1500 对象虚拟树、共享分隔尺寸、刷新身份保持、模态草稿与凭据清理、迟到请求、分页／筛选／排序／列宽／字段分块及关闭页签取消。不是所有交互都在全部平台、语言和缩放组合下实测。

| 检查 | 结果 |
| --- | --- |
| `go test ./...` | PASS |
| `go vet ./...` | PASS |
| `go tool mygo vet` | PASS |
| `go tool mygo build` | PASS，Linux 可执行程序、deb、tar.gz |
| `/usr/bin/python3 -m unittest discover -s tests -v` | PASS，23 项；系统 Python 有 Pillow |
| `go test -race ./internal/services/connections ./internal/ui/shell` | PASS |
| 本地 MySQL service／UI integration 测试 | PASS，两个包；显式 `NYADB_MYSQL_STRUCTURE_IT=1` |
| Windows amd64／macOS arm64，`CGO_ENABLED=0 go build` | PASS，仅编译 |
| `go tool mygo i18n check` | PASS，313 条消息 |
| `git diff --check` | PASS |

一次性 MySQL 8.4 仅绑定 `127.0.0.1:34306`，使用专用 SELECT／SHOW VIEW 账户，fixture 包含 `nyadb_struct_a`、`nyadb_struct_b`、`nyadb_perf`、`nyadb_types`；密码只来自 ignored、0600 的临时文件，未进入命令正文、截图表单或档案。集成测试真实读取，未以模拟结果替代原版证据。验证结束已清理自有原生进程、一次性容器和临时凭据文件；原版进程保留。

原生浅色／英文见 `native/19-settings-light`、`20-settings-english`。100%／125%／150% 的 `ui.NewTester.SetScale` 和最小布局检查属于无窗口 I，不代表实际显示器缩放验证。Windows/macOS 实际运行、原生 125%／150%、全页文本基线和字体栅格化仍为 **Unknown**。

完成条件尚缺：其余页面的同状态 before/after、逐控件与文本基线度量、原版相同 fixture 的有效读取，以及实际字体／缩放证据。因此本报告不把布局实现、交互回归和像素验收合并为一个“全部完成”结论。无 commit、push 或 Git 历史修改。

## 本轮运行证据与视觉度量

原版运行观察（O）使用既有隔离 Navicat 进程；未建立数据库连接，连接向导草稿均取消。共采集 21 组原始桌面 PNG、客户区 PNG 和 JSON，覆盖工作台、选择器、MySQL 七页及兼容性／初始化查询和菜单。原版最后恢复既有窗口和选择，恢复前后客户区截图逐像素相同。索引位于 `temp/research/ui-alignment-20261025/original/observation-index.json`。

NyaDB 图像来自真实 Linux Xvfb 原生进程，不是 ui.NewTester。客户区采用 XGetGeometry 加 XTranslateCoordinates 测量，避免窗口装饰重复偏移；采集等待非黑且连续稳定的帧。Windows/macOS 的编译检查不计作实际桌面验收。

改动前截图另存于 `baseline-native/observation-index.json`，来自本轮对齐前 HEAD（`efe506e06b12c5549e7073fcbe4175e09861774a`）的独立构建，覆盖工作台、选择器网格／列表及 MySQL 七页。`native/00-empty` 已由后续会话覆盖，不能作为改动前基线。改动后 `native/connection-03` 至 `connection-09` 保留较早迭代构建的真实 SHA；已逐张复核可见页签并补充状态，不将它们冒充常规页最终构建的截图。

`native/13-objects`、`native/14-data`、`native/15-structure` 记录 NyaDB 对本地一次性 fixture 的实际读取及原生 GUI 状态（I）。本轮原版没有对应有效读取证据，三页的原版差分均为 **BLOCKED**；不会以模拟结果或单方读取替代双方服务、fixture、结果和选择状态一致的证据。

### 指定区域的原图诊断

下表只报告客户区设备像素与指定纯色采样，不将其转换成未经证实的 DIP，也不代表整页像素 V。可复现脚本为 `temp/research/ui-alignment-20261025/measure_alignment.py`，输出 `verification/*-measurements.json`，包含原图 SHA-256、采样位置、测量方法及差值。

| 区域 | 原版 / NyaDB 客户区 | 物理像素几何结果 |
| --- | --- | --- |
| 工作台面板、分隔及状态栏 | [1280, 800] / [1280, 800] | 测得 x/y/w/h 最大差 0 px；DIP 判定 Unknown |
| MySQL 常规页五个输入框 | [900, 700] / [900, 700] | 测得 x/y/w/h 最大差 1 px；DIP 判定 Unknown |

| 指定颜色区域 | 原版 RGB | NyaDB RGB | 各通道差值 | 局部颜色阈值 |
| --- | --- | --- | --- | --- |
| 工作台 / toolbar | 49/54/59 | 49/54/59 | 0/0/0 | PASS |
| 工作台 / workspace | 35/38/41 | 35/38/41 | 0/0/0 | PASS |
| 工作台 / navigation-selection | 72.5/179/235 | 72/179/235 | 0.5/0/0 | PASS |
| 工作台 / left-splitter | 49/54/59 | 49/54/59 | 0/0/0 | PASS |
| 工作台 / right-splitter | 49/54/59 | 49/54/59 | 0/0/0 | PASS |
| 工作台 / statusbar | 49/54/59 | 49/54/59 | 0/0/0 | PASS |
| 常规页 / surface | 49/54/59 | 49/54/59 | 0/0/0 | PASS |
| 常规页 / input-fill | 35/38/41 | 35/38/41 | 0/0/0 | PASS |
| 常规页 / footer | 49/54/59 | 49/54/59 | 0/0/0 | PASS |

名称输入框顶边另作单像素线诊断：原版 RGB 90/183/230，NyaDB 90/183/230，通道差 0/0/0，颜色阈值结果 PASS。该细线不冒充门禁要求的至少 2×2 纯色样本。

### 严格差分状态

工作台：**BLOCKED**；MySQL 常规页：**BLOCKED**。对应 `verification/workbench-manifest.json` 和 `connection-general-manifest.json` 可直接交给正式差分工具；结果保留空 regions，不输出伪造的严格像素指标。

双方尚缺实际应用缩放及逐控件解析字体、DIP 字号的可核验证据。只读进程映射确认原版加载了其 Noto Sans、系统文泉驿正黑及 DejaVu 字体，NyaDB 加载了系统 Noto Sans CJK；加载集合不能证明某个控件实际使用的字体。详情见 `verification/font-scale-observation.json`。Xlib DPI 接近不能替代应用缩放证明。

常规页还明确记录现有能力差异：原版“保存密码”启用且勾选，NyaDB 为禁用未勾选，符合本轮不增加凭据持久化能力的边界。因此没有把该页声明为完整行为等价。

以上仅覆盖列出的运行观察和原图采样；字体栅格化、独立图标、未测控件及其他平台真实桌面均不能据此标为 V。
