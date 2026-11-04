# Navicat UX 对齐实施记录 — 2026-11-04

本轮按五个阶段实施 UX01–UX14，保留 NyaDB 主题、现有连接身份及执行确认机制。基线为 `513d7b9`；对应问题与 Navicat 运行观察见 [差距审计](navicat-ux-gap-audit-2026-11-04.md)。审计描述的是实施前状态，本记录描述最终实现。改动按数据库基础、存储、共享交互、工作台集成和验收记录拆成五个 Git 提交，未推送。

## 14 项交付映射

| 项目 | 最终行为 | 主要实现位置 |
| --- | --- | --- |
| UX01 SQL 结果阅读 | 可见多结果标签；各结果分别保留选区、活动格、滚动、列布局及字段模式。增加单元格/列菜单、列复制、适应宽度、显隐、重排和只读字段面板。文本、JSON、十六进制、图像只消费缓存，明确显示 NULL、空值和截断。新执行替换旧选区，同结构继承阅读布局。 | `sqlworkspace/result_views.go`、`sqlworkspace/view.go`、`gridview/reading.go` |
| UX02 焦点编辑命令 | Shell 按窗口及控件有效期保存编辑目标；编辑器、SQL 结果、数据格和表单注册能力。菜单捕获目标并恢复焦点，命令在分发时重新检查有效性。只读目标禁用写入，表单使用 MyGo 原生菜单角色。 | `theme/edit_commands.go`、`theme/native_edit.go`、`shell/document_menus.go`、`bootstrap/app.go` |
| UX03 连续表设计 | “设计表”直接进入编辑；“查看结构”继续只读。字段表格支持内联名称、类型、非空、默认值及引擎属性；可见子页保留高级定义。Enter 提交草稿，Esc 还原当前格，无效值保留并提示。 | `objectworkspace/inline_fields.go`、`design_view.go`、`schema_design_view.go` |
| UX04 分组筛选 | 可见 AND/OR、组与条件增删；最多 6 条件、3 层组，空组及混用新旧表示明确拒绝。三引擎参数化编译同一树，分页、键集、分析和导出使用相同含义；条件变化撤销旧游标。 | `contract/filter_expression.go`、`dataworkspace/filter_groups.go`、MySQL/engines 数据查询、分析和导出 |
| UX05 身份与颜色 | 同名对象标签按连接、Catalog、Schema 消歧，Tooltip 保留完整身份；SQL 上下文显示连接名。固定色板贯穿导航、标签及上下文，颜色修改只刷新本地展示。 | `shell/workspace_tab_caption.go`、`connections/colors.go`、`theme/profile_color.go`、`localdb/connection_profiles.go` |
| UX06 表单 URI | 表单 URI 按钮已接通；返回、取消恢复原始输入、当前页和临时秘密。成功解析后确认替换连接参数与备用配置，保留 ID、名称、分组、收藏、颜色、备注；密码保存默认关闭。 | `connectioneditor/uri.go`、`shell/connection_dialog.go` |
| UX07 支持状态 | 连接卡片显示可连接状态；能力判断复用 Availability，提示区分连接、对象、引擎、权限、版本和未实现原因。已有连接与目标选择入口可直接执行。 | `theme/availability.go`、`connectioncatalog/view.go`、`shell/chrome.go` |
| UX08 填充命名 | 普通值、序列、NULL 填充使用“填充”，移除无关 AI 配置提示。 | `dataworkspace/toolbar_fill.go`、翻译目录 |
| UX09 标签组织 | Ctrl+Tab / Ctrl+Shift+Tab 按可见顺序循环并恢复焦点；支持拖动、固定、关闭右侧。永久对象页保持首位，固定区连续，批量关闭跳过固定页。操作按文档 ID 执行，关闭非活动页保留当前活动身份。 | `shell/tab_operations.go`、`workspace.go`、`document_menus.go` |
| UX10 工作现场 | Workspace v2 保存标签身份/顺序/固定/活动页、导航、对象列表、模块布局，以及 SQL 文本与光标。启动恢复占位，连接及对象读取保持显式操作。增加命名工作区保存、打开、重命名、删除及明确替换；切换经过离开守卫及当前现场写入。缺失连接可重新绑定或关闭。 | `queries/workspaces.go`、`shell/workspace_snapshot.go`、`workspace_manager.go`、各模块 `workspace_snapshot.go` |
| UX11 引擎信息列 | MySQL 保留原列；PG 显示名称、类型、估算行数、大小、注释；SQLite 显示名称、类型、有索引、有触发器、根页面。未知值保留未知，估算带 ≈。SQLite 用批量目录查询，列布局使用稳定 ID。 | `objectworkspace/engine_columns.go`、`engines/postgres_browse.go`、`sqlite_browse.go` |
| UX12 SQL 辅助 | 按 SQL 表、视图及别名异步读取字段，无需先开数据页；250 ms 防抖，缓存含原连接、命名空间、代次及对象。诊断保留错误链、码、详情、提示、语句序号及可靠位置；文本修改后显示原执行语句。三引擎提供结构 EXPLAIN，同时保留原始计划及普通结果。 | `sqlworkspace/completion_metadata.go`、`query_feedback.go`、`execution_plan.go`、`sqltext/references.go`、`db/queryplan` |
| UX13 设计信息联动 | 信息面板标明“正在编辑”，按内联输入的稳定字段 ID 同步行选择，显示当前尚未提交的单元格草稿；只读页继续显示服务器结构。 | `objectworkspace/inline_fields.go`、`structure_state.go`、`structure_presentation.go` |
| UX14 入口和反馈 | 主区空状态提供新连接、打开 SQL、最近现场恢复；密钥故障提供中文原因、重试及另存 SQL。校验定位错误页面和字段并保留输入。小窗口字段值及连接标题避免被裁掉或逐字折行。 | `shell/empty_workspace.go`、`sqlworkspace/persistence_feedback.go`、`connectioneditor/validation.go`、`shell/profile_view.go` |

## 边界与兼容

- `gridview` 只提供阅读、复制、列菜单和缓存预览；数据模块继续拥有完整网格、编辑及写入，SQL 模块继续拥有结果状态及执行。数据页复用文本呈现、复制格式和只读字段预览，保留其专用编辑与列操作。
- 查询、对象、数据及补全的异步结果保持由 UI 线程消费，缓存和完成消息检查原连接及会话代次。取消和提交核验机制保留；不确定提交不会重放。
- 设计内联提交仅修改草稿，保存仍经过预检、SQL 预览、确认和核验。EXPLAIN 构造不添加 ANALYZE；服务器未返回的指标保持未知。
- 工作区不保存数据行、查询结果、数据编辑草稿或设计编辑草稿。恢复连接不会执行 SQL；SQL 页仍需显式建立新物理会话并显式运行。对象占位需显式读取。重新绑定不执行 SQL 或写入。
- 快照 `SnapshotVersion=2` 保留旧 SQL 文档字段，旧工作区转换为 SQL 标签。加密封装版本、AAD 和旧记录读取不变，工作区继续采用 500 ms 防抖及串行修订写入。密钥不可用时保留已有加密记录。
- 命名工作区独立于命名查询，替换、重命名和删除检查条件修订。连接颜色通过本地数据库 9→10 新增列，旧连接使用默认色。现有 MySQL / PG / SQLite 数据布局键、存储格式及 bootstrap 接口保留。
- 未升级固定的 MyGo 依赖，没有加入 Navicat 专有素材。数据库同步、建模和自动任务不在本轮范围。

## 行为验证

受影响模块的回归包含：URI 草稿往返及身份保留、字段校验定位、菜单捕获后的目标失效/窗口隔离、同名对象及稳定标签 ID、多结果独立状态、只读字段缓存与剪贴板、连续字段编辑与信息面板、旧工作区读取/缺失连接/修订冲突/惰性恢复、补全撤销延迟结果、UTF-8 错误位置、结构计划与普通结果切换。另有真实 v9 文件迁移到 v10 后的重复打开回归，确认旧连接身份、备注、收藏及加密工作区仍可读取，颜色默认值正确。

原生验收发现并补充回归的三个问题：内联焦点被旧列表选中行覆盖、SQL 恢复入口错误要求已连接、工作区弹窗遮挡设计离开守卫。现在分别以真实点击输入、原连接重新连接、取消切换保留现场的行为测试覆盖。

三引擎任务通过 `tools/verify_unified_grid_fixture.sh` 使用独立临时 MySQL 8.4 / PostgreSQL 17 容器及用户 SQLite 文件验证：

- `(a OR b) AND c` 首/次键集页分别返回 ID 1、2；改变条件拒绝旧游标；分析与导出均为 2 行。
- PG 表/视图列元数据与估算行数、大小可读取；SQLite 索引、触发器及根页面由批量目录取得。
- MySQL JSON、PG JSON、SQLite QUERY PLAN 可解析真实结构；请求无隐式 ANALYZE。PG 中文及 emoji 错误位置与驱动提供的位置一致。
- 共享网格三引擎代表截图及已有导入、备份恢复、编辑/事务回归继续通过。

最终检查：Go 全量测试、Go vet、MyGo vet、翻译生成与 i18n check、正式 MyGo 构建及 CGO_ENABLED=0 构建；Python 视觉工具 64 项测试。生命周期相关模块已运行 race，Shell 使用本轮恢复、菜单、标签及工作区切换的集中 race 回归；完整 Shell race 曾达到超时，因此不将其列为通过。

日志保存在忽略目录 `build/navicat-ux-go-test-final.log`、`build/navicat-ux-fixtures-final.log`、`build/navicat-ux-python-final.log`。

## 原生截图与动作记录

Linux GTK / Xvfb / Openbox 使用本轮独立配置、密钥服务和数据库夹具。中文、英文以及 1280×800 / 840×560 有任务截图；没有接入用户现有数据库。截图和逐步动作 JSON 在 [本轮证据目录](../build/entry-acceptance/r-f952cace0d90/evidence/navicat-ux/)。

| 验收路径 | 已检查证据 |
| --- | --- |
| 同一 SQL 结果选区的 Ctrl+C、编辑菜单、右键复制 | `native-copy-three-routes-1280.json`；脚本实际断言三次剪贴板均为 `owned UX fixture` |
| 紧凑 SQL 字段预览 | `field-preview-final-840-client.png`、`field-preview-final-1280-client.png` |
| 连续修改三个字段，信息面板指向第三字段 | `en-three-fields-inspector-final-1280-client.png`、`en-three-fields-inspector-final-840-client.png` |
| 中文连续修改三个字段 | `zh-three-fields-inspector-final-1280-client.png`、`zh-three-fields-inspector-final-840-client.png` |
| 命名工作区选择、设计离开守卫和占位恢复 | `en-named-workspace-final-840-client.png`、`en-workspace-design-guard-final-840-client.png`、`en-named-workspace-restored-final-840-client.png`、`en-named-workspace-restored-final-1280-client.png` |
| 重启恢复 SQL 草稿及对象占位、保持断开 | `restart-zh-1280-client.png`、`restart-zh-840-client.png`、`restart-en-1280-client.png`、`restart-en-840-client.png` |
| 英文 SQL 显式恢复与字段阅读 | `en-sql-original-connected-final-client.png`、`en-query-final-executed-840-client.png`、`en-field-preview-final-840-client.png`、`en-field-preview-final-1280-client.png` |
| PG 原生结构 EXPLAIN 与请求预览 | `en-explain-structure-final-840-client.png` 是请求确认页，无 ANALYZE；执行后的结构见 `en-explain-executed-final-840-client.png`、`en-explain-executed-final-1280-client.png` |
| PG / SQLite 原生字段设计 | `postgres-samples-design-1280-client.png`、`postgres-samples-design-840-client.png`、`sqlite-inline-design-1280-client.png`、`sqlite-inline-design-840-client.png` |

原生字段修改只停留在未保存草稿。PG 夹具结束前再次读取服务器字段，仍为 `id,label,amount`。探索中的误点、加载中截图和命名错误文件不作为成功证据，尤其不采用早期 `field-preview-fixed-840` 及误标的 MySQL 设计截图。

正式原生构建 SHA-256：`54d7e5d4fe16b88756f2b48862088c0c473b06796014a201df2ae34243dca130`。截图和动作文件哈希保存在证据目录 `sha256.json`；本轮创建的应用、显示会话、密钥服务与数据库容器在验收后清理，证据文件保留。

## 验收范围

本轮完成 14 项实现及上述行为验证。原生验收采用代表任务，没有穷举每项功能 × 每引擎 × 两语言 × 两尺寸的全部组合，也没有覆盖物理桌面、多显示器、IME 或 Windows/macOS 原生行为。菜单控件销毁及跨窗口路径以回归测试补充原生复制验证。

SQL 引用解析对复杂语法及超过两段的限定名保持保守，无法可靠识别时不伪造字段补全。执行计划只展示真实返回的数据；解析失败可回到原始计划或显式普通 EXPLAIN。现有页面的一些旧状态文本仍使用原有表达，本轮新增关键入口和反馈具备中英文目录。
