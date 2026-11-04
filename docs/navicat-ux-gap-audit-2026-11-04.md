# Navicat 交互 UX 差距审计 — 2026-11-04

当前 NyaDB 的基础工作流覆盖已较完整。最值得先补的差距集中在查询结果阅读、表设计效率、跨页面命令一致性和多文档连续性。新增大型工具前，先改善这些每天反复经过的操作路径。

## 本轮证据范围

- **原版运行观察 O**：启动本地 Navicat Premium 18 Linux AppImage，实际创建本轮 SQLite 连接，打开数据库、表数据、筛选条件组、多结果查询、结果菜单/字段查看器、表设计器、连接颜色菜单，操作 Ctrl+Tab。
- **NyaDB 运行观察 I**：从当前 HEAD `513d7b9` 编译正式 `cmd/nyadb`，使用独立 XDG 配置，鼠标操作相同临时 SQLite 文件。另核对当前生产源码。
- 使用 Linux GTK/Qt、Xvfb/Openbox、软件渲染；不是物理桌面、多显示器、IME 或 Windows/macOS 验收。默认窗口约 1280×800（含窗口装饰）。
- 临时文件 `temp/research/ux-audit-20261104/ux-fixture.sqlite` 含 120 个 customers、30 个 orders 和一个视图；查询均为只读 SELECT。没有连接既有 Navicat 档案指向的数据库，没有保存表结构或行数据修改。
- Navicat 不完全遵循 XDG 配置重定向，因此本轮新建连接使用唯一名称 `NyaDB UX Fixture 20261104`，结束后通过 GUI 删除；既有两项连接未编辑。删除第一次因连接仍在使用失败，随后正常关闭本轮标签、选择不保存，再删除本轮档案。
- REA 技能用于调查与证据分层；当前会话没有 REA MCP 工具，诊断后使用现有 X11 运行采集工具继续。未修改工具注册、分析引擎或原版二进制。
- 下文“源码核对”只证明 NyaDB 当前实现边界，不能标为 Navicat 静态证据 S，也不能单独认定原版行为等价 V。

原始截图、动作记录、哈希和对照图均保留在忽略目录 [ux-audit-20261104](../temp/research/ux-audit-20261104/)。截图是本地研究证据，不是可分发产品素材。

可先查看 [三组并排对照图](../temp/research/ux-audit-20261104/ux-comparison.png)：左侧为 Navicat，右侧为当前 NyaDB，分别对应 SQL 结果、表设计和条件组筛选。

## 优先补齐的交互

P1 表示高频工作受阻或容易操作错对象；P2 表示效率、发现性或信息表达欠缺。优先级是本轮产品判断。

| 编号 / 优先级 | 用户场景与当前差距 | 证据与建议 |
| --- | --- | --- |
| UX01 / P1 | **SQL 结果页与表数据页的阅读能力不一致。** 表数据已有成熟网格与字段工具；查询结果只有选区复制、CSV/TSV/JSON 导出和结果下拉框。没有接通结果单元格菜单、字段 dock、列操作和独立结果状态。 | 两边实际执行相同两条 SELECT。Navicat 可见结果 1/2 标签、字段查看器的文本/JSON/十六进制/图像页、列复制/适应宽度菜单；NyaDB 实测结果区右键无对应菜单。`sqlworkspace/view.go:118–230`。复用成熟网格的阅读与布局能力，同时保留查询结果的只读和预览范围。 |
| UX02 / P1 | **全局编辑命令不遵循当前焦点。** 查询结果选中 Customer 001 后，Ctrl+C 复制该格，“编辑→复制”却复制整段 SQL。数据页的全局复制/全选等直接置灰。 | 本轮剪贴板实测；`shell/document_menus.go:37–38,62–68,132–134` 只路由 SQL Editor。先统一编辑器、结果格、数据格、表单等焦点命令分发。这是 NyaDB 内部一致性问题；本轮 Navicat 顶层复制可用性也受格编辑状态影响，不声称其全局路由已全面验证。 |
| UX03 / P1 | **“设计表”入口和连续改字段的操作负担偏高。** NyaDB 先打开只读结构，再点击“编辑表结构”；编辑态是下拉子页、定义列表、独立字段表单和“应用定义修改”，之后再 SQL 预览/保存。 | SQLite 两边实测。Navicat 直接进入可编辑字段表格，名称/类型/非空等同屏，类型单元格可进入下拉编辑；NyaDB 字段类型是文本框。`objectworkspace/schema_design_view.go:49,161–189`；MySQL 同类结构在 `design_view.go:69,162,213`。让设计入口直接表达编辑意图，增加字段表格内编辑与类型候选；保留原预检、SQL 预览和执行确认。 |
| UX04 / P1 | **图形筛选无法表达 OR 和条件组。** “启用或待处理”“(A OR B) AND C”等条件必须改写 SQL。 | Navicat 实际添加括号组并把 and 切为 or；NyaDB 明示“所有已启用条件均满足 (AND)”。`dataworkspace/rules.go:208,231`。增加有括号结构的条件模型及可见 AND/OR，按各引擎规则生成参数化条件。现有多字段排序已实现。 |
| UX05 / P1 | **多连接同库同表的可见身份不足。** 来自生产/测试两个连接的 main.customers 标签都叫 customers；SQL 工具栏用“连接 #1 · main”，需要额外辨认。 | Navicat 表标题实测包含 `customers @ main (连接名)`，连接颜色菜单可见多色选项；NyaDB 标签消歧只比较 Database，没有比较 ProfileID（`shell/workspace_tab_caption.go:33–48`），颜色菜单永久禁用（`profile_navigation_menus.go:150`）。冲突时显示连接名，连接色贯穿树/标签/SQL 上下文。跨连接同名页签在 NyaDB 的问题本轮以源码确认，没有新建第二个真实连接复测。 |
| UX06 / P1 | **已有功能的入口仍断路。** URI 新建功能在类型选择器可用，但连接编辑表单底栏 URI 永久禁用，语义名称还是“URI 导入尚未支持”。 | 两边表单截图确认按钮状态；Navicat 底栏 URI 可用，但本轮未操作其完整 URI 流程。`connectioneditor/view.go:306–308`，实际已实现入口在 `shell/connection_dialog.go:176`。转发已有流程，并处理表单草稿保留与返回。 |
| UX07 / P1–P2 | **支持范围与禁用原因不易发现。** 默认连接卡片把未支持引擎与支持项画得相近，点完才说明无法下一步；自动运行/模型、部分工具常驻置灰，tooltip 只重复名称。 | 默认 chooser 实测，支持判断及可见标记通过 `connectioncatalog/view.go:171–198` 核对；`shell/chrome.go:179–189,245–256`。卡片就地显示支持状态；禁用提示区分未连接、无目标、引擎限制、版本/权限限制与未实现，并给出可执行下一步。 |
| UX08 / P2 | **“AI 自动填充”与实际行为不符。** 菜单总是显示“AI 服务尚未配置”，能做的是普通值/序列/NULL 填充；独立 AI SQL 配置不会改变这个入口。 | NyaDB 实测菜单；`dataworkspace/toolbar_fill.go:20–24,136`。先使用“填充”准确命名，待连接真实 AI 能力时再暴露 AI 操作和配置入口。本轮没有测试商业 AI 服务。 |
| UX09 / P2 | **多标签的键盘和组织效率有限。** 已有水平滚动、窗口菜单和关闭其它/全部；缺少全局标签循环接线、拖排/固定等组织能力。 | 原版从查询按 Ctrl+Tab 实际切到设计页；NyaDB 从结果格按相同组合仍留在 SQL 页。`shell/workspace.go:26–77`、`document_menus.go:146–172`。先补 Ctrl+Tab/反向切换与可靠焦点恢复，再补拖排和关闭右侧。原版拖排/固定本轮未实测。 |
| UX10 / P1–P2 | **工作现场恢复只覆盖 SQL 文档。** 草稿、光标、选区和 ActiveID 已有；数据/设计/程序标签、导航现场与命名工作区不在当前恢复模型中。 | 源码核对：`queries/store.go:36–48`；`shell/sql_documents.go:14–24,43–50`。先恢复文档身份、顺序、所选对象和可恢复布局，显式恢复原连接；不自动执行 SQL 或重放写入。本轮没有重启两应用验证完整工作区，Navicat 的恢复规格仍待专门观察。 |
| UX11 / P2 | **共享对象列表沿用 MySQL 信息列，SQLite 页面出现大量空白。** 自动递增值、修改日期、数据长度、引擎、行等列大多没有值，字段占据空间却不能帮助选表。 | 两边同一 SQLite 对象列表实测；Navicat 选择名称/有索引/有触发器/根页面等 SQLite 属性。NyaDB `objectworkspace/browser_view.go:15–23,122–149` 使用固定 MySQL 列。保持同一浏览模块，按引擎/对象能力选取有意义的列。 |
| UX12 / P2 | **补全、报错与执行计划的辅助程度还有差距。** 字段补全依赖已打开的数据或结构标签；错误主要是单行字符串；EXPLAIN 进入普通结果格。 | 源码核对：`shell/sql_completion.go:37–54`、`sqlworkspace/execution.go:201–217`、`editor_commands.go:102–121`。增加按需列元数据加载、错误字段/行定位、可读执行计划。Navicat 本轮可见代码补全刷新和解释入口，未测试这些能力的完整语义或性能。 |
| UX13 / P2 | **设计器信息面板没有跟随编辑对象。** 左侧设计列表选 name，右侧仍显示名称 id、INTEGER、主键，容易误读。 | NyaDB 本轮截图 `38-nyadb-field-form.png`：只读结构最初选 id，进入编辑态再选 name 后，右侧仍保留原字段信息。先确认面板显示“原始结构”还是“正在编辑的字段”，通过标题和联动消除歧义。该路径实测，不扩展为所有信息面板都不同步。 |
| UX14 / P2 | **反馈和空状态仍偏实现细节。** 新用户主区空白；本轮独立桌面没有密钥服务，底栏直接显示英文 `SQL workspace encryption key unavailable; encrypted records retained`。表单校验也缺少错误字段定位。 | 主窗口截图与 `connectioneditor/save.go:51–61,view.go:41–54`。将错误表达为用户可理解的原因和动作；空状态提供新建连接、打开 SQL、最近工作。密钥提示来自本轮受限桌面，不认定正常桌面一定失败；加密记录保留和显式文件保存边界应继续保留。 |

## 已有能力应从旧缺口清单移除

当前代码已经包含 SQL 历史、命名查询与文件保存、补全、格式化、EXPLAIN、多结果执行、取消及显式会话恢复；数据格编辑、草稿/事务、字段 dock、列布局、多格式导入导出；MySQL/PG/SQLite 受支持范围内的表设计。2026-11-04 又已交付 PG/SQLite 共享导入、备份恢复、PG 普通表复制/删除、普通视图管理和维护，见 [多引擎最新记录](multi-engine-reliability-2026-11-04.md)。

不能把这些写成“没有功能”。本轮指出的是这些能力的入口、阅读方式、交互效率和支持范围说明还不一致。

## 建议实施顺序与验收

1. **先修一致性与误导入口**：焦点复制/全选路由、URI 转发、AI 填充命名、禁用理由、设计器信息联动。验收应覆盖“同一选区，菜单/快捷键/右键的结果一致”，以及按钮的实际行为与文案一致。
2. **统一结果阅读体验**：SQL 结果接入成熟网格的复制、列布局、字段查看器；每个结果集分别保留选择和滚动。验收包含多结果来回切换、长值/NULL/二进制查看、预览截断提示；不通过隐式重跑获取完整值。
3. **提高设计与筛选效率**：可编辑字段表格、类型候选、可见子页、AND/OR/组。用连续编辑多个字段和 `(A OR B) AND C` 筛选作为任务级验收，确认预览 SQL 与实际提交一致。
4. **改善多文档连续性**：连接身份/颜色、Ctrl+Tab、标签重排、工作区上下文恢复。验收使用两个同库同表连接和重启恢复，确认始终保留原身份，不回放 SQL。
5. **再深化补全/计划与大型工具**：图形查询构建、执行计划可视化、数据传输/同步、自动任务、模型等仍有投入价值，但需要各自完整操作规格；本轮没有从可见菜单推断其原版内部行为。

数据编辑的提交核验、不确定结果禁止重放、SQL 原会话恢复及 DDL 预检/预览应保持。UX 改善应减少寻找和重复输入的负担，而不是省略这些已实现的执行边界。

## 可复查证据

证据文件在 `temp/research/ux-audit-20261104/`，编号只是本报告的本地证据索引，不是 REA 返回的 Evidence ID。

| 范围 | 原版 O | NyaDB I |
| --- | --- | --- |
| 查询结果 | `17-navicat-results.png`、`18-navicat-result-menu.png`、`20-navicat-result-cell-editor.png` | `31-nyadb-results.png`、`32-nyadb-result-right-click.png` |
| 编辑菜单/选区 | 原版菜单受格编辑状态影响，本轮不作全面等价结论 | `27-nyadb-data-selected-edit-menu.png`、`33-nyadb-result-edit-menu.png`、`clipboard-focus-receipt.json` |
| 条件组/OR | `12-navicat-filter-groups.png`、`13-navicat-and-or.png` | `28-nyadb-filter.png` |
| 设计字段 | `36-navicat-designer.png`、`37-navicat-inline-type.png` | `34-nyadb-designer.png`、`38-nyadb-field-form.png`、`39-nyadb-designer-pages.png` |
| 类型选择/URI | `03-navicat-chooser.png`、`05-navicat-sqlite-form.png` | `21-nyadb-chooser.png`、`22-nyadb-sqlite-form.png` |
| 对象信息列 | `08-navicat-objects.png` | `24-nyadb-objects.png` |
| 颜色/标签切换 | `42-navicat-connection-color.png`、`43-navicat-ctrl-tab.png` | `44-nyadb-ctrl-tab.png`，颜色禁用以源码核对 |
| 填充入口 | 未观察原版 AI 执行 | `29-nyadb-ai-fill.png` |

原始动作包括探索时误点和未完成加载截图；只有上表选出的已检查状态用于对应结论。`16-navicat-results.png` 是执行中，结果完成使用 `17`。紧凑窗口探索中 Navicat 会限制宽度，因此 `45`/`46` 不用于同尺寸像素或效率差分。未对海量数据、网络延迟、滚轮/IME、提交恢复或原版工作区重启开展本轮性能/完整生命周期验收。

- 构建：`GOWORK=off CGO_ENABLED=0 go build -o temp/research/ux-audit-20261104/nyadb ./cmd/nyadb` 成功。
- NyaDB 二进制 SHA-256：`fb654f1e7265dbf0509e77188b1e90d3c2f48b4f26cac9fca014cea42bda4f76`。
- Navicat 原始 AppImage SHA-256：`3b77fe1e3295dc3067402a0bab33a8ed1cbfbdea063d8b59e67c26a6480b6d63`。
- 本次修改只有本报告；未更改生产源码、翻译或依赖，未运行或声称新的全量测试。截图哈希见忽略目录中的 `evidence-sha256.txt`。
- 本轮新建 Navicat 档案已删除，临时应用/窗口管理器/Xvfb 已退出；临时数据库复查仍为 120/30 行且首个客户名称未变。记录见 `cleanup.json`。进程关闭用于清理，不作为完整工作台关闭守卫验收。
