# 表数据页：选区复制、单元格菜单及运行对照（2026-10-29）

## 本轮结果

继续完善真实 MySQL 只读表数据页。原生网格的矩形、多行及整行选择现在统一驱动工具栏和右键复制；新增 TSV、CSV、JSON，以及基于单元格的服务器筛选和排序。1068px 窗口使用一行操作栏，840px 窗口保留两行操作栏。

本轮有 **O** 原版运行观察、**I** 独立实现/Tester/真实桌面证据，以及两个限定场景的**双边行为 V**：两行两列纯文本复制、`note = 'alpha'` 筛选的返回记录。**没有通过整页像素差分，也没有完成表数据写入交互。**

## 环境、原版身份和边界

- 正式代码基线来自仓库现有 `go.mod`：Go 1.27.3，MyGo `v0.6.4-nyadb.4`；本轮不升级依赖。开始时 `git status --short` 为空。
- 用户本轮明确允许启动原版对比；实际复用历史 Navicat 窗口，Xvfb `:184`，1600×960。原版正常运行输入、菜单和截图，没有动态调试或授权修改。
- AppImage SHA-256：`3b77fe1e3295dc3067402a0bab33a8ed1cbfbdea063d8b59e67c26a6480b6d63`。
- 核对进程映射加载的是历史原包提取的 `pristine-lib/libcc.so`，SHA-256：`6c45353fea04ba4b941aef2477fe9fa4997c77cee6380c3abc60f87f404e8e57`。没有修改解包文件。
- REA MCP 未接入；只读 doctor 返回 Node 版本不满足要求及注册缺失/过期。本轮没有修复配置，使用正常 X11 操作和服务器日志；没有虚构 REA Evidence ID。
- 一次性 MySQL 8.4.11：容器 `nyadb-table-data-20261029`，仅 `127.0.0.1:32789`，库 `table_data_it`。测试账户仅对该库授权，空密码不保存到 Navicat 档案。
- 新建原版临时连接 `NyaDB Data Fixture 20261029`；NyaDB 使用独立 `XDG_CONFIG_HOME`，自有 Xvfb `:198`，同为 1600×960。原生窗口通过 X11 map 显示。
- 所有原始证据均位于 Git 忽略目录 `temp/research/table-data-live-20261029/`。最终桌面运行二进制为 `nyadb-final`，SHA-256：`d6134db1c7821b247c0d1998e4f1afefbdecc763fb05f6ef8334cdbd5ddf39e2`。
- 初次 fixture 导入未指定客户端字符集，第三行中文出现乱码；后来用管理员在本轮一次性库中修正为 UTF-8。早期截图如实保留，乱码不归因于原版或 NyaDB。复制/等值筛选对照使用未变化的 `alpha`、`beta` 两行。

## O：观察到的交互

| 问题 | 运行观察 | 原始证据 |
| --- | --- | --- |
| 数据页布局 | 双行字段/类型表头；紧凑操作栏；网格下方导航行、SQL/记录状态；右侧表信息 | `09-data.png`、`18-navicat-filter-result.png` |
| 单元格菜单 | 复制、复制字段名称、复制为；显示/隐藏筛选排序；筛选、排序子菜单及刷新 | `10-cell-menu.png` |
| 复制为 | Insert/Update 语句；TSV/CSV 各有数据、字段名、字段名和数据；JSON | `11-copy-as-menu.png`；仅观察菜单结构，未执行各格式导出 |
| 选区复制 | 从 id=41 扩展到下一行 note，Ctrl+C 得到 `41\talpha\n42\tbeta`，无末尾换行 | `14-block-selection.png`、`navicat-copy-block.txt` |
| 筛选菜单 | 等于、不等于、类似/不类似、小于/大于、自定义及移除筛选 | `12-filter-menu.png` |
| 排序菜单 | 升序、降序、移除所有排序 | `13-sort-menu.png`；原版排序执行未覆盖 |
| 等值筛选执行 | 对 alpha 单元格选择“字段 = 值”，服务器收到 `WHERE note = 'alpha'`，GUI 仅显示 id=41 | `18-navicat-filter-result.png`、`navicat-filter-log.tsv` |

## I：实现与数据语义

1. `mysql_data_copy.go`：按可见列的显示顺序收集实际选区；忽略隐藏列、行号列和失效行。支持矩形、Ctrl 多行、整行、全选；仅复制已加载页。单格复制保持直接文本，多个单元格使用 TSV；CSV/TSV 引号、制表符和换行可往返解析。字段名导出也按当前显示顺序。
2. JSON 使用有序对象，SQL NULL 输出 `null`；非 NULL 值保持字符串，包括 BIGINT、定点小数、日期、字面量 `NULL` 和空字符串，避免浮点转换造成精度丢失。TSV/CSV 中 SQL NULL 保持文本 `NULL`，这两种文本格式不能区分 SQL NULL 与字面量 `NULL`。
3. 截断字段复制保留明确提示；提示加入中英文 catalog。复制不发起完整大字段读取。选区和数据值不进入布局持久化。
4. `mysql_data_cell_menu.go`：复制、字段名、复制为、字段信息、筛选排序展开、快速筛选、排序和刷新。行号区域也可复制整行；右键选区内部保留选区，外部由原生 Table 选择被点击的单元格。
5. 等于、不等于、包含、IS NULL、IS NOT NULL 使用现有参数化查询服务；NULL 等于/不等于转换为 NULL 谓词。快速筛选替换同字段条件，保留其他字段条件和排序，并同步编辑器草稿。六条件上限仍有效。截断及二进制/空间十六进制预览不能作为等值/包含比较输入，NULL 谓词仍可用。
6. 菜单排序切换为指定字段的全表服务器排序，保留筛选；移除排序清空排序草稿和实际规则。不能排序的字段禁用对应操作。查询继续使用异步读取、取消、页大小约束及迟到结果防护。
7. `mysql_data_view.go`：明确启用原生拖选；Ctrl+C/F5 仅由网格焦点处理。筛选输入框中的复制仍由文本输入处理，F5 不刷新表格。
8. `mysql_data_toolbar.go`：主复制按钮作用于选区，旁边下拉提供复制格式。`mysql_data_rule_editor.go` 将换行阈值调整至 1000px；1068px 实际桌面单行显示，840px 双行显示。沿用原生 Toolbar 的溢出菜单。

Insert/Update SQL 生成、粘贴写入、设置 NULL/空字符串、删除记录、事务、数据片段、LIKE/大小比较和单元格编辑器没有在本轮实现。菜单提供已实现的操作，不将只读预览写回数据库。

## 双边行为对照及桌面验证

| 场景 | Navicat | NyaDB 最终二进制 | 结论 |
| --- | --- | --- | --- |
| 两行两列复制 | `41\talpha\n42\tbeta` | 相同字节；脚本直接比较两个剪贴板文件并断言相等 | 限定纯文本选区复制 V |
| alpha 等值筛选 | `SELECT * ... WHERE note = 'alpha' LIMIT 0,1000`；返回 id=41 | 参数化 Execute：有限字段投影、同一 WHERE、稳定键排序、LIMIT 101；返回 id=41 | 此筛选的谓词和可见结果 V；SQL 投影/页大小有意不同 |
| 1068px 操作栏 | 紧凑单行 | `30-final-data.png` 单行；`31-final-block.png` 选区；`32/33` 为真实复制/筛选子菜单 | 原尺寸人工观察，无像素 V |
| 840×560 浅色/深色 | 本轮未采同尺寸双边场景 | `35-final-narrow.png`、`40-final-dark-840.png`、`42-final-dark-menu.png` | NyaDB 实际 GUI I |

SQL 双边证据见 `both-filter-log.tsv`。NyaDB 最终剪贴板见 `nyadb-final-copy-block.txt`；最终过滤界面见 `34-final-equal-filter.png`。

`43-final-dark-client` 和 `44-original-filter-client` 使用现有采集工具记录 XGetGeometry/XTranslateCoordinates 的真实客户区坐标及稳定帧，并生成客户区 PNG/JSON。实际字体、字号、应用缩放仍为 Unknown，两张图的主题、选区和过滤状态也不同，不能用于严格像素差分。早期 `xdotool getwindowgeometry` 的坐标不作为严格裁剪依据。

截图采集默认 `python3` 缺少 Pillow，改用现有 `/usr/bin/python3`（Pillow 10.2.0）成功完成，没有安装新依赖。

## 检查与清理

- `go test ./...`：PASS；新增行为测试覆盖选区内部/外部右键、CSV/TSV 往返、NULL/空字符串/大整数、隐藏及重排列、截断提示、输入焦点隔离、刷新服务调用、筛选/排序规则保持及条件上限。
- `go vet ./...`：PASS。
- `go tool mygo vet`：PASS。
- `go tool mygo i18n check`：PASS，475 messages，zh-Hans/en；catalog 通过 `go tool mygo generate` 生成。
- `go tool mygo build`：PASS，Linux/amd64 可执行程序、deb 和 tar.gz 均在 `build/linux-amd64/`。
- `git diff --check`：PASS。
- 没有启用既有 MySQL 集成测试旗标；本轮真实数据库结果来自实际 GUI 和一次性服务日志，不能宣称所有 MySQL 自动集成测试已运行。
- 原版临时表配置变更选择“不保存”；临时连接先关闭后删除，最终 `51-original-restored.png` 仅有历史连接。第一次连接仍在使用时删除被原版拒绝，原始错误图保留。
- 本轮 NyaDB 两个自有运行实例、自有 Openbox/Xvfb `:198` 和一次性容器已停止。历史 Navicat/显示服务保留，未接触生产库，未 commit/push/reset。

整页字体、表头宽度/高度、选区颜色、菜单行高和快捷键文字仍有差异。CSV/JSON 与原版的精确格式、原版排序/NULL 筛选的实际执行、Windows/macOS 真 GUI、写入及完整编辑体验均为后续验证范围。
