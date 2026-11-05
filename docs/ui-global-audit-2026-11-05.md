# 全局 UI 审查 — 2026-11-05

当前 UI 的主要问题是：**核心组件已复用 MyGo，但外围控件的布局、状态样式和语义仍由各页面分别实现，放大字号、长名称及切换页面时容易暴露不一致。** 优先处理文字重叠、SQL 命令被挤掉和选中态对比度，再统一表单、子页、工具按钮及弹窗。

本轮只做审查和验证，没有修改生产 UI。审查基于 `6938d09f1dd531bb1f5d23b6a4eaf571ff980a7e`。最近已经实现的 SQL 执行范围菜单、结果快照、编辑器/结果独占布局、可调整字段面板、记录表单、直接跳页及草稿撤销，不再列为缺失功能。

## 证据与范围

- 检查 shell、connectioncatalog、connectioneditor、navigation、sqlworkspace、dataworkspace、objectworkspace、favoritesworkspace、gridview 和 theme；sessionbrowser 的后台会话逻辑不属于此次显示样式审查。
- 使用 `GOWORK=off` 构建正式 `cmd/nyadb`，确保验证的是 `go.mod` 固定的 MyGo `v0.7.3-nyadb.2`。本地 `go.work` 另指向研究目录中的 fork，本轮没有修改该工作区或依赖。
- 在独立 Xvfb/Openbox、独立 XDG 配置中，通过正式界面创建临时 SQLite 连接，检查选择器、连接表单、对象列表、表/视图数据、SQL、外观设置和对象搜索。检查 1280×800 与 840×560 客户区，默认字号与 UI 字号 16。数据库和配置均在忽略目录。
- 使用实际模块的 MyGo `ui.NewTester` 检查选择器键盘行为、UI 字号 16 加系统文字缩放 1.5、英文 MySQL 表单、长连接名称和高对比度按钮。**这些属于 headless 组件证据，不冒充原生窗口截图。** 系统文字缩放与显示 DPI 是不同设置。
- 新证据保存在 [build/ui-global-audit-20261105](../build/ui-global-audit-20261105/)。原生图片对应 JSON 保存二进制哈希、实际客户区几何和稳定帧记录。没有进行 Windows/macOS 原生或 MySQL/PostgreSQL 服务器界面验收。

可先看 [关键问题截图拼图](../build/ui-global-audit-20261105/audit-evidence-sheet.png)：包含文字重叠、中英混排、长名称挤掉工具条、原生字号 16 的尺寸问题和高对比度活动按钮。

P1 表示显示内容不可读或高频操作入口被挤掉；P2 表示交互、样式与发现性改进。下表中的“源码”表示实现可以确认，未声称该页面已完成原生运行验收。

## 问题清单

| 编号 / 优先级 | 问题与影响 | 依据 | 建议 |
| --- | --- | --- | --- |
| UI01 / P1 | **连接选择器放大文字后出现行间重叠。** 右侧树固定 19 DIP，左侧过滤行固定 23 DIP，文字却跟随 `theme.Font` 放大。UI 字号 16、系统文字缩放 1.5 时，文字达到 24 DIP，供应商名称上下重叠。卡片也固定为 116×94，布局计算没有计入文字缩放。 | headless 复现：[放大后的列表](../build/ui-global-audit-20261105/probe-catalog-24-list.png)。[tree.go:13](../internal/ui/connectioncatalog/tree.go#L13)、[tree.go:147](../internal/ui/connectioncatalog/tree.go#L147)、[view.go:12](../internal/ui/connectioncatalog/view.go#L12)、[view.go:177](../internal/ui/connectioncatalog/view.go#L177)。 | 行高至少使用 `theme.RowHeight`；卡片采用最小高度和自然内容尺寸，列数根据实际可用宽度及缩放后的最小卡宽计算。 |
| UI02 / P1 | **SQL 工具条会被长连接名称挤掉。** 右端上下文文本没有宽度预算；左边虽然已使用 MyGo Toolbar，仍会被压到几乎没有空间，只剩很小的溢出入口。运行、取消等命令全部从可见区消失。 | 实际 sqlworkspace headless，宽 672 DIP：[短名称](../build/ui-global-audit-20261105/probe-sql-profile-long-false.png) / [长名称](../build/ui-global-audit-20261105/probe-sql-profile-long-true.png)。[view.go:101](../internal/ui/sqlworkspace/view.go#L101)、[view.go:118](../internal/ui/sqlworkspace/view.go#L118)。 | 为上下文设置 `MaxWidth`、`MinWidth(0)` 和省略提示，给运行/取消保留明确的最小宽度；完整身份放在 Tooltip 或单独上下文行。 |
| UI03 / P1 | **共享子页按钮在部分高对比度组合下几乎看不见文字。** `Selected(active)` 设置强调色与对应文字颜色，随后 `PageButton` 将底色改成 `Focus`，却保留原 `AccentText`。高对比度下 `Focus` 会变成正文颜色，导致错误的前景/背景配对。 | [page_button.go:7](../internal/ui/theme/page_button.go#L7)。实际组件截图：[白字落在浅色底上](../build/ui-global-audit-20261105/probe-pagebutton-highcontrast.png)。经典深色、高对比度、红色系统强调色时比值约 **1.16:1**；经典浅色、高对比度、黄色强调色约 **1.35:1**。完整组合见 [计算结果](../build/ui-global-audit-20261105/page_button_contrast.json)。 | 选中填充和选中文字使用同一语义色对；焦点边框与选中填充分开。对组件最终组合测试，而非仅检查各个主题 token。 |
| UI04 / P2 | **英文界面仍有成块的中文和内部枚举。** MySQL 高级页标题/按钮已翻译，但“设置位置”“客户端驱动程序”“客户端字符集”和“默认/自动”等仍是中文。用户管理显示 `grant`、`create_user` 等原始操作值；若干表操作说明及确认文案也未翻译。 | headless：[英文高级页](../build/ui-global-audit-20261105/probe-mysql-16-en-advanced.png)。[mysql_options.go:74](../internal/ui/connectioneditor/mysql_options.go#L74)、[object_admin.go:229](../internal/ui/objectworkspace/object_admin.go#L229)、[table_actions.go:325](../internal/ui/objectworkspace/table_actions.go#L325)。 | 静态文案统一进 catalog；内部值保持稳定，单独映射显示标签。优先覆盖错误、权限、删除及确认文案，再覆盖一般表单。 |
| UI05 / P2 | **共享字号只覆盖了部分几何尺寸。** UI 字号 16 时主工具栏“新建查询”成为“新建…”、“自动运行”成为“自动…”，原因是项目宽度仍为 64 DIP。数据页有些按钮随字号增高，有些撤销/分页按钮固定 26 DIP，出现两套按钮大小；信息面板切换栏也固定 42 DIP。 | 原生：[字号 16 的数据页](../build/ui-global-audit-20261105/customers-ui16-dark-840-client.png)。[chrome.go:295](../internal/ui/shell/chrome.go#L295)、[metrics.go:31](../internal/ui/theme/metrics.go#L31)、[dataworkspace/view.go:372](../internal/ui/dataworkspace/view.go#L372)、[inspector_table_view.go:34](../internal/ui/dataworkspace/inspector_table_view.go#L34)。本次分页仍可操作，不将尺寸不一致夸大为分页失效。 | 定义完整的控件尺寸规则：文字按钮按内容保留宽度，图标按钮统一尺寸/命中区域，工具栏允许溢出。不要只放大字体与局部行高。 |
| UI06 / P2 | **选择器树可以直接复用 MyGo Outline。** 当前供应商展开、缩进、箭头及叶节点均用 ButtonBase 手工实现；每行是一个独立按钮。选中 MySQL 后按 Down，不会移动到 PostgreSQL；缺少原生树的方向键导航与树语义。 | [tree.go:122](../internal/ui/connectioncatalog/tree.go#L122)、[tree.go:146](../internal/ui/connectioncatalog/tree.go#L146)、[tree.go:187](../internal/ui/connectioncatalog/tree.go#L187)。headless 行为见 [probe.log](../build/ui-global-audit-20261105/probe.log)：按 Down 后选择和焦点仍在 MySQL。主导航已经采用 [navigation/view.go:87](../internal/ui/navigation/view.go#L87) 的原生树。 | 使用 MyGo `Outline` / `OutlineState` 和现有 theme 包装，保留 catalog 的过滤、选择、recents 和临时搜索展开逻辑；让 MyGo 提供树导航和披露行为。 |
| UI07 / P2 | **表单重复实现标签列，却没有充分利用 Form/Field。** 各页面手工固定标签宽度、拼接说明及全局错误。点击连接表单的“主机”文字不会聚焦输入；选择器过滤器的可见名称也在 Checkbox 之外，点击文字不会切换复选框。**部分输入已经设置 Label、错误也有定位，问题不是完全没有可访问名称或校验。** | headless 验证主机标签点击未聚焦输入；[connectioneditor/view.go:198](../internal/ui/connectioneditor/view.go#L198)、[connectioncatalog/view.go:61](../internal/ui/connectioncatalog/view.go#L61)。另有标签宽 110 的 [schema_design_view.go:227](../internal/ui/objectworkspace/schema_design_view.go#L227)，和标签宽 300 的 [toolbar_transfer_wizard.go:263](../internal/ui/dataworkspace/toolbar_transfer_wizard.go#L263)。 | 优先用 MyGo `Form`、`Field`、`Fieldset`、`Description`、`Error`：统一对齐、标签点击、控件描述和错误边框。图标复选行用 CheckboxBase 的完整 Children 承载可见内容，让整行可点击。 |
| UI08 / P2 | **有些 Label 只有辅助技术名称，没有可见字段名。** 缓存结果导出的两项 Select 没有显示“格式/范围”；账户管理的账户名/主机、授权目标等输入缺少可见标签。对象搜索输入也没有提示文字。空输入或熟悉度不足时很难知道用途。 | 原生对象搜索：[空输入框](../build/ui-global-audit-20261105/object-search-ui16-dark-840-client.png)。其余以源码确认：[cached_export_view.go:27](../internal/ui/sqlworkspace/cached_export_view.go#L27)、[object_admin.go:225](../internal/ui/objectworkspace/object_admin.go#L225)、[search_view.go:22](../internal/ui/objectworkspace/search_view.go#L22)。 | 用 Field 显示字段名；搜索用 `SearchField` 并提供 Placeholder。不要把 `.Label(...)` 当成页面上显示的标签。 |
| UI09 / P2 | **子页切换存在多套视觉和键盘规则。** 工作台、连接页和只读结构使用 TabsBase；设计页及 SQL 结果用 PageButton；数据编辑器和信息面板用普通按钮加背景。它们都能点击切换，但后几种没有原生 TabList 的方向键、Home/End 和单组焦点规则。部分数据开关只改背景，未声明选中/开关状态。 | [schema_design_view.go:48](../internal/ui/objectworkspace/schema_design_view.go#L48)、[sqlworkspace/view.go:272](../internal/ui/sqlworkspace/view.go#L272)、[toolbar_dock.go:66](../internal/ui/dataworkspace/toolbar_dock.go#L66)、[inspector_table_view.go:35](../internal/ui/dataworkspace/inspector_table_view.go#L35)、[toolbar.go:22](../internal/ui/dataworkspace/toolbar.go#L22)。 | 页面切换统一用 TabsBase，有限选项切换用 Segmented，开关用 Toggle/ToggleGroup；在 theme 中统一活动态外观。保留各模块原有草稿切页验证。 |
| UI10 / P2 | **ButtonBase 用在普通工具按钮上，丢失了默认悬停/按压外观。** 对象页打开/设计/查看结构/新建按钮和数据页工具按钮由 ButtonBase 拼文字与图标，未统一绘制 hovered/pressed 背景。主工具栏却专门实现了这两个状态，反馈不一致。 | [browser_view.go:196](../internal/ui/objectworkspace/browser_view.go#L196)、[dataworkspace/view.go:470](../internal/ui/dataworkspace/view.go#L470)，对比 [chrome.go:386](../internal/ui/shell/chrome.go#L386)。源码确认，未把所有 ButtonBase 自定义控件视为错误。 | 普通工具操作使用 `ui.Toolbar` 中的 `ui.Button(...).Children(...)`，由 MyGo 提供状态外观。只对确需独特外观的按钮使用 ButtonBase，并集中补齐状态样式。 |
| UI11 / P2 | **选择器切换视图后，支持状态与空结果反馈不一致。** 网格有“尚未支持/可连接”提示，树叶节点只显示名称；选择未支持类型时需要靠下一步按钮猜测原因。网格无匹配时各 section 直接返回，右侧为空；树模式则显示“没有匹配的连接类型”。 | 原生：[网格](../build/ui-global-audit-20261105/chooser-dark-900-client.png) / [列表](../build/ui-global-audit-20261105/chooser-list-dark-900-client.png)。[view.go:153](../internal/ui/connectioncatalog/view.go#L153)、[view.go:182](../internal/ui/connectioncatalog/view.go#L182)、[tree.go:185](../internal/ui/connectioncatalog/tree.go#L185)。空结果差异以源码确认。 | 网格/树复用同一支持状态呈现和空状态提示；提示不可连接原因，保留对未支持条目的浏览能力。 |
| UI12 / P2 | **图标按钮混用 SVG、手绘图形和 Unicode 字符。** 上方是统一 Lucide 风格，底部使用 `↻`、`↶`、`↷`、`✓`、`■`、`«`、`‹` 等字符；编辑器关闭和展开也使用字符。图形尺寸、线宽、基线和不同系统的字体回退都不一致。 | [dataworkspace/view.go:345](../internal/ui/dataworkspace/view.go#L345)、[toolbar_dock.go:81](../internal/ui/dataworkspace/toolbar_dock.go#L81)、[connectioneditor/view.go:300](../internal/ui/connectioneditor/view.go#L300)。原生数据页可以直接看到上方图标与底部字符的差异。 | 操作图标统一到现有 Lucide/theme 图标系统；品牌图标仍保留品牌资源。不要重绘已有图标或强行取消合理的品牌差异。 |
| UI13 / P2 | **弹窗样式和尺寸策略未统一。** MyGo Modal 已给 panel 加 Padding/背景/圆角，很多页面又包一个 Padding 14–23 的 Column，有些改成 Header 背景，有些沿用 App；导入导出向导又显式清零外层 Padding。确认按钮位置和主操作强调也不同。 | [cached_export_view.go:24](../internal/ui/sqlworkspace/cached_export_view.go#L24)、[history_view.go:56](../internal/ui/sqlworkspace/history_view.go#L56)、[table_actions.go:305](../internal/ui/objectworkspace/table_actions.go#L305)、[toolbar_transfer_wizard.go:24](../internal/ui/dataworkspace/toolbar_transfer_wizard.go#L24)。MyGo 固定版本的 `ui/widgets.go:618` 已提供外层 panel。 | 用 Modal/必要时 DialogBase 做薄样式包装，统一标题、内容滚动、页脚、padding 和尺寸预算。保留具体流程和忙碌/取消/关闭决策在原模块。本轮对象搜索弹窗可用，不声称所有弹窗均裁切。 |
| UI14 / P2 | **外观设置页仍保留上一对象的信息面板。** 在 customers 数据页打开设置，右侧仍显示 customers 的类型、连接和数据库；840×560 下占用约 130 DIP，设置内容区只有约 540 DIP。它与当前设置无关，同时表名较长时右侧标题缺少统一省略处理。 | 原生：[设置页](../build/ui-global-audit-20261105/settings-dark-840-client.png)、[字号 16 设置页](../build/ui-global-audit-20261105/settings-ui16-dark-840-client.png)。[chrome.go:46](../internal/ui/shell/chrome.go#L46) 只对 SQL 排除 inspector；[profile_view.go:68](../internal/ui/shell/profile_view.go#L68) 的其他页面回退到导航对象信息。 | 设置等独立页面使用完整中心区，或明确提供与页面相关的信息内容；不要让陈旧对象信息持续挤占宽度。页面暂时隐藏与用户保存的 inspector 开关偏好分开。 |
| UI15 / P2 | **列宽“适应”及身份显示还有手工估算/内部信息问题。** 查询结果列宽按字符数×8+24 估算，不读取实际字体及缩放，中文和比例字体无法可靠适应；历史连接筛选/单元格以 `#ID` 表示，SQLite 对象列表默认展示根页面，普通任务中难以帮助用户识别对象。 | 列宽：[gridview/reading.go:77](../internal/ui/gridview/reading.go#L77)。身份：[history_view.go:74](../internal/ui/sqlworkspace/history_view.go#L74)、[history_view.go:142](../internal/ui/sqlworkspace/history_view.go#L142)；原生搜索显示“连接 #1”。[engine_columns.go:20](../internal/ui/objectworkspace/engine_columns.go#L20) 展示根页面。列宽属于源码确认，信息优先级属于产品判断。 | 按实际文本测量计算列宽，同时保留最大宽度及采样预算；连接显示名称并用 ID 辅助消歧；存储内部属性放入可选列/高级属性。 |

## 已有基础应保留

- 主导航已使用 MyGo Outline；工作台已使用 TabsBase 和原生 Split；核心表格已复用 MyGo Table。问题集中在外围控件，不建议把这些成熟组件换成另一套自绘实现。
- SQL/数据工具栏已有 MyGo Toolbar 溢出机制。UI02 是外部上下文文本挤占空间的问题，不能归因为“项目没有使用 Toolbar”。
- theme 已有统一色板、字体、行高、选择文字对、SQL 样式和框架主题映射。继续在此处完善**最终控件**的样式规则，避免新增各模块自己的颜色和尺寸常量。
- 连接表单自绘复选框已经基于 CheckboxBase，保留了键盘、checked 和焦点绘制；有合理的紧凑设计需求，不应一概判为重造交互。品牌缓存和统一数据 State/Document 也应保留。

## 建议实施顺序

1. **显示正确性**：UI01、UI02、UI03；随后补 UI05 的完整字号尺寸规则。这些问题有直接重叠、命令消失或低对比度证据。
2. **MyGo 组件复用**：选择器 Outline、表单 Field、子页 TabsBase/Segmented、普通工具按钮 Button、搜索 SearchField；统一字段名、错误和空状态。
3. **样式与信息层级**：图标按钮、弹窗边距/页脚、设置页 inspector、完整本地化、身份显示与列宽计算。

复用原生组件时保留现有的模块依赖边界、原 profile/generation 绑定、草稿离开校验、取消确认和 parked-result 行为。此次审查没有发现需要以视觉重构为由修改这些生命周期契约的依据。

## 本轮验证与缺口

以下检查使用 `GOWORK=off`，均通过：

```sh
CGO_ENABLED=0 go build -o build/ui-global-audit-20261105/nyadb ./cmd/nyadb
go tool mygo vet
CGO_ENABLED=0 go test ./internal/ui/theme ./internal/ui/connectioncatalog \
  ./internal/ui/connectioneditor ./internal/ui/navigation ./internal/ui/sqlworkspace \
  ./internal/ui/dataworkspace ./internal/ui/objectworkspace
```

本轮没有运行全量测试、race 或服务器集成测试，也没有把它们写成已通过。

现有 [theme/reading_test.go](../internal/ui/theme/reading_test.go) 校验了 token 色对，但没有校验 UI03 的最终 `AccentText + Focus` 组合。[shell/ui_visual_regression_test.go:23](../internal/ui/shell/ui_visual_regression_test.go#L23) 的矩阵需要显式环境标志，主要生成图片，不能单靠常规测试通过来证明没有遮挡。

后续修复宜补有针对性的验收：24 DIP 文字时选择器行不重叠、长上下文时 SQL 主命令和溢出入口可见、高对比度下活动子页可读、树/页签方向键导航，以及中英两种语言的字段名和确认说明。字号、长标签、窄窗口应同时组合验证。
