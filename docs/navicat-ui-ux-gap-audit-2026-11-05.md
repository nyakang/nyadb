# Navicat UI / UX 剩余差距审计 — 2026-11-05

当前 NyaDB 的工作台框架、紧凑主题和基础交互已经接近 Navicat。下一轮最值得改善的是设计器有效空间、SQL 执行范围的发现性、结果阅读布局和跨页面工具一致性。

续查又确认了整条记录表单、数据保存命令和直接跳页方面的差距，并复现了 SQLite 导出入口的范围歧义：当前页 100 行，工具栏直接导出的 JSONL 含整表 120 行。具体见下文 UX25–29；它们与 UX15–24 一起构成当前待办，而非另一次实施记录。

本报告针对当前 `65d7ad1`，不是重复昨天的实施前审计。[昨日 14 项实施记录](navicat-ux-implementation-2026-11-04.md)中的结果菜单、字段预览、OR/分组筛选、字段内联编辑、URI、连接颜色/同名标签消歧、标签固定/拖排和工作区恢复均已实现，不能再列为“没有”。

## 本轮证据与边界

- **原版运行观察**：实际启动本地 Navicat Premium 18 Linux，创建唯一连接 `NyaDB UX Audit 20261105`，操作连接、对象列表、表数据、筛选、字段设计、索引草稿、两条 SELECT、多结果、运行菜单、字段查看器、导出向导和固定结果入口。
- **NyaDB 运行观察**：从当前 HEAD 编译正式 `cmd/nyadb`，在独立 XDG 配置中操作同一个临时 SQLite 文件，并核对当前生产源码。未使用 preview/mock 窗口或研究图标。
- 两边使用同一私有 X11 / Xvfb / Openbox 显示会话、中文、深色主题，主要比较 **1280×800 客户区**。NyaDB 另检查 840×560；Navicat 表数据/设计页最小实际为 841×560，查询页最小实际为 1033×560。因此不把 Navicat 查询页称为已通过 840×560，也不把不同宽度截图当作严格像素差分。
- 夹具复制自昨日 SQLite 文件，含 120 个 customers、30 个 orders 和一个视图。运行的查询只有 SELECT；索引新增仅为未保存草稿，未执行 DDL 或数据写入。续查在 NyaDB 批量模式暂存一个名称修改，观察撤销/保存快捷键后放弃；另实际导出一个 JSONL 文件到忽略目录。最终夹具 SHA-256 与初始值一致。原版既有两项连接没有打开数据库或编辑档案，本轮唯一临时连接已通过原版界面删除。
- REA 技能用于调查与证据分层；当前会话未提供 REA MCP 工具，诊断后使用已有 X11 工具继续，没有修改 MCP 注册、分析引擎或原版二进制。下文源码只证明 NyaDB 实现边界，不代表原版静态分析。
- 运行环境未提供可用系统密钥服务；NyaDB 已正确显示中文工作区保存故障提示。本轮不以此推断真实桌面上的工作区保存缺陷，也未重复验收昨日的密钥环恢复。

截图、动作 JSONL、窗口几何和稳定帧记录、二进制哈希保留于忽略目录 [本轮证据](../temp/research/ux-audit-20261105/)。先看 [四组并排截图](../temp/research/ux-audit-20261105/ui-ux-comparison-20261105.png)：左 Navicat、右 NyaDB，依次为字段设计、索引编辑、SQL 字段预览、SQLite 数据/信息。

续查另有 [记录表单与导出入口对照](../temp/research/ux-audit-20261105/ui-ux-continuation-comparison-20261105.png)：第一行左 Navicat 记录表单、右 NyaDB 网格；第二行均为 NyaDB，左为工具栏直接 JSONL 保存框、右为对象菜单的导出范围向导。文件框是独立原生窗口，所取主窗口客户区截图不能用来评判它的最小尺寸。

## 建议优先级

P1 表示影响高频操作效率或容易误读执行/结果范围；P2 表示发现性、一致性及呈现改进。优先级是产品判断，不代表安全漏洞或已发生数据损失。

| 编号 / 优先级 | 当前差距与用户影响 | 本轮证据 / 建议 |
| --- | --- | --- |
| UX15 / P1 | **设计器有效编辑区太小。** 1280×800 下，6 个字段的表在 Navicat 全部可见；NyaDB 只完整显示 4 行，第 5 行被裁掉，下方大块空白。选中字段后又重复显示名称/类型/非空表单，增加视觉负担。 | `navicat-design-1280-client.png` 对 `nyadb-design-1280-client.png`、`nyadb-design-field-selected-1280-client.png`。`objectworkspace/schema_design_view.go:163` 限制表高 180；176 开始详细表单。让表格随可用高度扩展，将重复属性收进高级区；保持既有草稿校验和保存确认。 |
| UX16 / P1 | **PG/SQLite 常用结构定义的图形编辑仍不足。** 字段已可内联编辑，但 SQLite 新增索引进入“名称 + 完整 CREATE INDEX 文本”；普通用户仍需手写选列/唯一性 SQL。约束也主要依靠完整定义。 | 两边实际新增未保存索引草稿：`navicat-design-index-1280-client.png` 有名称/字段/唯一键格，`nyadb-design-index-1280-client.png` 是完整 SQL；Navicat 字段格可进入带选择按钮的编辑态（`navicat-index-field-picker-root.png`），本轮未完成该选择弹窗。NyaDB `schema_design_view.go:142,176,193`。补字段选择、顺序、唯一性、外键目标等结构化表单，完整定义保留为高级入口。PG 同类边界以源码确认，未做 PG 原版运行差分。 |
| UX17 / P1 | **SQL 执行范围不够显式。** NyaDB 顶部“运行”取当前/选中语句，F5 执行整段脚本；直接点击同名按钮，两边得到的结果范围不同。用户需要知道快捷键或先全选。 | 本轮插入两条 SELECT，光标在第二条，NyaDB 点击运行只得到 orders 结果；全选后得到结果 1/2。Navicat 普通运行得到两组结果，并有“运行当前语句”菜单。`navicat-run-menu-root.png`；NyaDB `sqlworkspace/view.go:35`、`keyboard.go:21,33,38`。做可见的运行菜单，列出当前/选中/全部及快捷键，并明确本次范围。已有 RunAll，不能写成“不支持执行全部”。 |
| UX18 / P1 | **查询结果布局仍容易挤压数据。** NyaDB 把执行步骤、结果按钮、复制/导出分成多行；字段查看器固定 300 宽，外侧对象信息面板同时保留。840×560 时结果只剩约 240 宽，可见 id 和部分 name。未执行时也始终预留空结果区。 | `nyadb-field-preview-open-840-client.png`、`nyadb-multi-results-1280-client.png`、`nyadb-query-1280-client.png`；1280 下 Navicat 字段查看器在结果下方，消息/摘要收为标签：`navicat-field-preview-1280-client.png`。`sqlworkspace/view.go:30,188,214,225,309`。提供字段面板可调整/切换位置、编辑器或结果独占布局，将执行步骤收进消息页，避免多个信息区同时挤占网格。Navicat 查询页本轮最小宽 1033，未声称其在 840 下更优。 |
| UX19 / P2 | **同一查询重跑后缺少结果保留入口。** 当前 NyaDB 一次新执行会替换此前结果，调 SQL 前后难以留在同页参照。Shell 页签固定与“固定本次结果”是两种操作。 | Navicat 点击“固定”后产生 `Pin 1`，重跑后仍有该入口：`navicat-pinned-results-1280-client.png`。NyaDB `sqlworkspace/result_views.go:85` 清理旧结果状态，`nyadb-rerun-results-1280-client.png` 显示新 2 行结果。建议有限额的结果快照，携带原 SQL、上下文、时间及截断信息。**本轮只验证原版生成 Pin 1 并保留入口，未成功核对重新打开固定页后的旧 5 行，不声称结果内容保真或跨重启持久化。** |
| UX20 / P2 | **结果标签和设计子页缺少明确选中态。** 切换后内容变化，但“结果 1/2”“字段/索引/约束”等按钮外观基本相同，多个相似结果尤其难辨认。 | `nyadb-result2-active-1280-client.png`、`nyadb-design-index-1280-client.png`；Navicat 的子页采用可见活动标签。NyaDB `sqlworkspace/view.go:214`、`objectworkspace/schema_design_view.go:48`、`design_view.go:69` 仅使用普通 Button。增加当前页底色/边线及语义状态；不影响已经实现的独立选区/滚动。 |
| UX21 / P1–P2 | **SQL 结果导出与表导出向导不一致。** 表导出向导已有多格式，查询当前结果只有 CSV/TSV/JSON；运行并导出另外走 ZIP/JSONL。把查询结果交给同事时，常用 Excel/HTML 等路径不直接。 | 两边打开查询结果导出入口：`navicat-result-export-root.png` 明示 TXT、CSV、JSON、XML、HTML、SQL、XLS、XLSX、DBF、Parquet；`nyadb-result-export-menu-root.png` 只有三项。`sqlworkspace/view.go:229`、`results.go:96`、`stream_export.go:41`，对比 `dataworkspace/toolbar_transfer.go:1173`。复用格式选择和导出选项，区分缓存结果/重新执行/选区，保留类型和截断提示。本轮只看查询导出向导入口，未比较各格式生成文件。**续查纠正范围：PG/SQLite 数据页工具栏会直接走 JSONL，不能概括为所有数据页导出按钮都已接通多格式向导，见 UX28。** |
| UX22 / P2 | **引擎上下文只适配了一部分界面。** SQLite 下 Navicat 顶部提供索引/触发器，信息面板显示有无索引/触发器和根页面；NyaDB 顶部仍是函数/用户/其它，数据表信息仍有大量无值的 MySQL 日期、行格式和长度属性。 | `navicat-data-1280-client.png` 对 `nyadb-data-1280-client.png`。`shell/chrome.go:245` 固定工具栏；`toolbar_actions.go:19` 的其它菜单无 SQLite 分类；`dataworkspace/inspector_table_view.go:147` 无引擎分支。按引擎组织工具与属性，区分不适用/未知。对象列表信息列和 SQLite 树已经适配，且每表设计器已有索引/触发器能力，不能写成“全部没有”。 |
| UX23 / P2 | **筛选能力已经补齐，但编辑反馈还粗。** 简单条件要切进条件组；最多 6 条件、3 层，单条条件只能删除，缺少暂时停用；错误不定位具体条件。 | 实际打开简单/条件组：`nyadb-filter-1280-client.png`、`nyadb-filter-groups-1280-client.png`；限制和错误由 `dataworkspace/filter_groups.go:67,71,123` 确认。统一条件编辑方式，就地显示限额及错误，增加单条条件启停。Navicat 本轮观察筛选/组入口，未验证其最大规则数，不声称原版无限制。 |
| UX24 / P2 | **视觉层级仍需要一轮一致性整理。** 设计表常驻输入框让非活动格也很重；顶部多个对象/工具使用相近的蓝色线框，识别较依赖文字；数据底栏默认展开大量适配器 SQL，主任务状态被挤压。 | 并排图可见前两项，属于视觉设计判断；`dataworkspace/view.go:301,403` 与 `nyadb-data-840-client.png` 直接显示 `CASE WHEN typeof...nyadb_text_slice_v1...`。先整理选中/禁用/编辑状态、控件边线和文字权重，SQL 细节放到已有 SQL 入口，默认优先行数/筛选/读写状态。无需复制原版专有图标。Navicat 也有 SQL 底栏，本项针对适配器表达式的信息价值，不能写成“原版从不展示 SQL”。 |

## 续查：记录操作与导出入口

续查复用原来运行中的两应用和相同 SQLite 文件，没有重新构建或改变 HEAD。独立源码核对与原生操作分别进行；下面只采用核实过内容的帧，不以动作名称证明结果。

| 编号 / 优先级 | 当前差距与用户影响 | 证据、边界与建议 |
| --- | --- | --- |
| UX25 / P1–P2 | **缺少整条记录的表单视图。** 宽表查看一个客户时，NyaDB 仍要横向滚动或逐格打开查看器，不能把字段名和值纵向并排查看。 | Navicat 点击底部表单图标显示六个字段；左侧选第 2 条后，右侧同步为 Customer 002、user2@example.test，见 `continue-navicat-record3-verified-client.png`（文件名来自探索动作，实际成功帧是 id=2）。NyaDB `dataworkspace/view.go:137` 渲染网格，`toolbar_dock.go:92` 只绑定一个字段；已有文本/JSON/十六进制/图像查看器。建议在同一 Document 中增加记录表单，复用字段读取和草稿逻辑；不另建引擎专用数据页。本轮没有在原版表单中提交数据。 |
| UX26 / P1 | **数据页保存命令未跟随页面。** 完成内联编辑后，Ctrl+S 无响应，文件菜单仍是禁用的“保存查询”；需要改用底栏 ✓ 或“应用更改”。 | 在批量模式把第 1 行 name 暂存为 `UX staged name`，Enter 回到网格，再单独按 Ctrl+S，仍显示“1 条待处理记录”：`continue-nyadb-grid-save-verified-client.png`；`continue-nyadb-grid-file-menu-verified.png` 显示禁用“保存查询”。`shell/document_menus.go:57,195` 只处理查询/设计，`dataworkspace/view.go:347` 已有保存按钮，`unified.go:50` 已有 Save 方法。建议按活动文档路由保存菜单和快捷键，保持取消等待、会话身份与不确定提交限制。本项是 NyaDB 内部一致性缺口；原版文件菜单在数据页也未显示保存项，本轮没有证明原版 Ctrl+S 会保存记录。 |
| UX27 / P2 | **缺少直接跳页入口。** 已有首页/上一页/下一页和页大小选择，但“第 N 页”只是文字，查远处记录需要连续翻页或改写筛选。 | Navicat 将每页条数从 1000 改为 50，再在页号框输入 2、Enter，实际显示 id=51 起的记录，底栏 SQL 为 `LIMIT 50,50`：`continue-navicat-page2-50-verified-client.png`。NyaDB `dataworkspace/view.go:378` 至 399 的页号为 Text，仅提供 50/100/500 每页。建议增加可校验的页号输入/定位入口，保留分页边界、草稿离开守卫及加载取消。早期尝试跳到 1000 条/页的第 2 页只得到空页，不作为非空定位成功帧。 |
| UX28 / P1 | **同一张表的导出按钮分成两套格式与范围语义。** PG/SQLite 工具栏“导出”直接保存 JSONL，不显示格式/范围选项；对象列表“导出向导”则有格式和范围选择。 | SQLite 点击工具栏先出现 `customers.jsonl` 保存框（`continue-nyadb-export-wizard-client.png`，实际是文件框，不是向导）；保存到 `continue-sqlite-toolbar-export.jsonl` 后解析得到 **120 条数据 + 1 条列元数据**，而数据页仍显示当前页已读 100 行：`continue-nyadb-jsonl-export-complete-client.png`。再由对象列表右键打开真正向导，`continue-nyadb-export-wizard-confirmed-client.png` 显示格式页，`continue-nyadb-export-range-confirmed-client.png` 显示“当前页”与字段选择。`dataworkspace/toolbar.go:88` 按 JSONL 能力分流；`unified.go:73` 只传 ObjectRef，`engines/adapter.go:551` 使用无筛选 `SELECT *`；PG 同一路由由 `connections/descriptors.go:18` 确认，未做 PG 原生复现。建议把一般“导出”统一接到格式/范围向导，将整表 JSONL 明确命名为快捷选项。**这不是“SQLite 没有多格式导出”；能力已有，入口和默认范围不一致。** |
| UX29 / P2 | **已暂存的数据格修改缺少逐步撤销。** Ctrl+Z 能撤销输入框内文字，但 Enter 完成暂存后不能恢复上一笔格修改；现有取消/放弃处理整批草稿。 | `continue-nyadb-grid-undo-verified-client.png` 在 Enter 后按 Ctrl+Z，仍保留暂存名称；`dataworkspace/edit_commands.go:10` 的网格命令只有复制/全选/粘贴，`view.go:350` 为整批放弃。建议增加仅针对未提交草稿的撤销/重做或单格恢复，明示范围。本轮没有观察 Navicat 的暂存修改撤销，不宣称原版存在跨记录、跨提交撤销；这是一项 NyaDB 内部编辑体验建议。 |

对象列表选表后 Ctrl+N 实际打开“新建表”弹窗，Escape 关闭后没有多出 SQL 标签：`continue-nyadb-object-ctrl-n.png`、`continue-nyadb-after-object-shortcut-client.png`。源码中本地新建表与 Shell 全局新建查询虽然使用同一组合，本次焦点路径分发正确，**不把潜在冲突列为已复现缺陷**。内联 Enter/Tab/Shift+Tab/Escape、矩形粘贴和批量提交均已有，不能再列为未支持。

导入向导在 1280×800 与 840×560 的第一步均保留取消/下一步，见 `continue-nyadb-import-wizard-client.png`、`continue-nyadb-import-wizard-840-client.png`；未加载文件或执行导入，不能据此宣称整个导入流程已通过小窗口验收。

续查复核记录保存在忽略目录 `continuation-verification.json`，新增证据哈希在 `continuation-sha256.json`。探索中发生过捕获脚本交叠、模态窗口激活超时和菜单误点；已串行重做有关操作，误点打开的导入向导不作为导出证据，文件名中的 `verified` 也不能替代内容核对。清理时先关闭原版临时连接再删除，确认只剩原有两项；最后核实 PID、程序路径和进程组后结束本轮独占的两应用、窗口管理器及 :240 显示会话。

## 其它已确认边界

- **执行计划交互**：结构 EXPLAIN 已存在，但 `sqlworkspace/execution_plan.go:69` 主要是全展开缩进文本；后续可补节点折叠、选中、查找及真实指标对照。本轮未做 Navicat 计划页运行差分，不列为已验证的原版优势。
- **引擎提示文案**：共享导入/导出在会话失效时仍有“原 MySQL 会话已失效”，见 `dataworkspace/toolbar_transfer_wizard.go:45` 和 `toolbar_transfer.go:736`。源码确认的跨引擎文案问题，本轮没有制造真实失效来复现。
- **大功能范围**：数据/结构同步、传输、生成/脱敏、自动运行、模型、服务器监控等仍有禁用入口（`shell/chrome.go:189,255`）；这属于独立功能投入，宜排在高频编辑/阅读体验之后。AI SQL、查询历史、备份/恢复和工作区恢复不能统称为没有。
- **仍需单独原生验收**：Windows/macOS、物理桌面、IME/中文输入、多显示器/DPI、复杂拖动和键盘焦点、MySQL/PG 的对应操作。未覆盖是验证范围，不能直接当作产品缺陷。

## 推荐下一轮工作顺序

1. UX15/16/20：设计器布局与普通索引/约束表单，加清楚的当前子页标记。
2. UX17/18/19/21/26/28：SQL 运行范围菜单、结果阅读布局、有限额结果保留、统一导出和数据保存命令。
3. UX25/27/29：记录表单、直接定位和未提交草稿撤销。
4. UX22/23/24：引擎相关工具/信息、筛选反馈和整页视觉层级。
5. 再评估同步、自动运行、模型等独立功能投入。

这份审计包含实际界面观察与源码边界核对，未修改生产代码，未以测试通过替代原版行为观察。
