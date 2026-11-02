# NyaDB Implementation Status

## 核心工作流 — 2026-10-31

当前基线为 Go 1.27.3 / MyGo v0.7.3-nyadb.2。以下是当前实现；本文件后部保留历史记录，不代表现状。

| 能力 | 当前交付 |
|---|---|
| SQL | 真实原物理会话执行、类型化多结果预览/导出、命名查询/文件保存、修改标记、加密草稿恢复、补全/可撤销格式化/EXPLAIN |
| MySQL 结构 | 完整公开 DDL 基线的可编辑表设计器，新建/修改字段和约束/触发器/选项；预览确认、冲突预检及后验 |
| MySQL 对象 | 数据库/视图/函数/过程/事件 CRUD、完整表复制、F2/多选/分组/自有剪贴板、维护和账户/角色/权限；数据入口与高级配置运行接线 |
| 常用入口 | URI 导入及密码暂存、原连接元数据查找、连接星标/对象/命名查询收藏与一级分组、文件/编辑/窗口菜单 |
| 隧道 | 统一 DialContext、SSH 指纹信任、HTTPS/WebSocket 自有网关、认证/TLS/代理及断线撤销 |
| PostgreSQL / SQLite | 真实会话、schema 导航、SQL/多结果、元数据/结构、类型化图形数据编辑、事务/核验及复制/流式导出；PG 高级/TLS、完整目录只读基线、原生 DDL 规划、私有元数据/改名执行及保留值/目标形状核验；SQLite 高级/逐物理连接附加库及六页表设计、创建、改表与改名 |
| 备份恢复 | 类型化校验包、一致性读取、完整 SQL 转储、冲突策略/预览/依赖恢复、SQL DELIMITER 文件执行；失败/部分/不确定状态不重放 |
| ClickHouse | 独立原生驱动/配置/URI/密钥环、SQL 租约与显式恢复、元数据、共享只读网格/分页/筛选/计数/导出 |
| AI SQL | 可配置 OpenAI 兼容 Chat Completions、系统密钥环、opt-in 预览、流式/取消、生成/解释/优化及原连接未执行草稿 |
| 后置 | 高级同步、模型、自动运行、BI、监控/字典/生成/脱敏/命令行、AI 表设计/数据填充、引擎深化、PG 图形表设计、PG/SQLite 对象 CRUD 与导入/备份扩展 |

详见 [核心工作流技术设计、限制与验收](core-workflows-2026-10-31.md)。实现和测试属于 **I**；新功能原版采证 **O** 与差分 **V** 为 **Unknown**，不能由编译、Tester 或历史局部对照代替。原生窗口和跨平台编译的具体环境边界另见验收报告。验收快照生成于提交之前，后续提交状态见 Git 历史及远端。

2026-11-01 对象区域改为单行工具栏及“更多”菜单/分组弹窗；未连接及数据库未打开时无对象列头，已打开空库保留列头。见 [状态对齐与验证记录](object-browser-state-alignment-2026-11-01.md)。

2026-11-01 完成离线命名查询列表、搜索、原连接打开、重命名、保留草稿的删除及逐连接一级分组；加密目录、条件修订更新和删除标记保持原存储兼容。接通函数/过程/事件、账户、备份/恢复/SQL 转储及实际表配置入口。实现 **I**、原生窗口验证范围、检查与密钥环限制见 [查询资产与入口交付记录](query-assets-2026-11-01.md)，不新增原版 **O/V**。

2026-11-02 补齐真实 Secret Service / OSVault 的跨进程、密钥失败和服务恢复验收，以及真实 MySQL Shell 入口与 Linux 正式窗口验收。修复工作区首次写入对错误密钥的覆盖风险，以及 MySQL 适配器遗漏程序对象读取的问题。新增可重复隔离工具和 420 像素原生工具栏组件验证；具体测试、截图、清理及限制见 [系统密钥环与 MySQL 原生交付记录](system-keyring-mysql-native-2026-11-02.md)。证据为独立实现 I 与 NyaDB 运行结果，不新增 Navicat O/V。

2026-11-02 工作台剩余功能继续实施：新增加密 SQL 执行历史、文档菜单/快捷键、原连接上下文补全，以及 MySQL 八子页和结构化常用约束表单。该阶段之后已接通查找/URI 导入/收藏夹；PG/SQLite 深化、跨库同步、自动运行和高级功能仍未完成。新引擎按用户选择优先 ClickHouse，AI 使用可配置的 OpenAI 兼容接口。当前变更未提交；实现 I、验证范围、限制及完整剩余清单见 [工作台剩余功能实施记录](workbench-completion-2026-11-02.md)。

2026-11-02 后续完成数据及管理操作的全局加密日志接线，首条执行等待原加密设置、待写队列界限、批量原子保存、失败重试/明确放弃和旧控制操作修订冲突防护。逐文档会话历史保留；完整快照分配成本及本轮验证见 [全局历史日志与失败恢复](global-history-2026-11-02.md)。

2026-11-02 后续补齐 URI 导入、全局对象查找和收藏夹：条件持久化、一级分组、原连接对象/最新加密查询打开，以及关闭/代次后的回执撤销。真实 SQLite、Secret Service 加密草稿恢复与正式 Linux 1280×800 / 840×560 验收见 [常用入口交付记录](common-entries-2026-11-02.md)。本次不新增 O/V；ClickHouse 和 OpenAI 兼容 AI SQL 仍按用户选择列在后续独立阶段。

2026-11-02 后续补齐 PostgreSQL 高级/TLS 和 SQLite 高级/附加库：复用旧存储列的版本化公开配置、不可变连接请求、原生文件选择回执撤销、逐物理连接初始化和完整命名空间读写。真实 PostgreSQL fixture、自有 SQLite、原生配置页及范围与限制见 [引擎配置交付记录](engine-options-2026-11-02.md)。PG/SQLite 图形设计、导入/备份、隧道及整体高级功能仍未完成，不新增 Navicat O/V。

2026-11-02 后续完成 SQLite 结构设计后端：独立完整定义草稿/规划器、原命名空间修订冲突检测、事务重建、原生命名及索引/触发器事务更新、NULL/BLOB/精确数值文本/rowid/序列保真回归和中立 generation 绑定。仅后台实现 I，原生表设计与对象 CRUD 入口仍未完成；PG binding 保持为空。验证和下一步见 [SQLite 结构设计后端记录](sqlite-schema-ddl-2026-11-02.md)，不新增 O/V，也不标记整体目标完成。

2026-11-02 后续接通 SQLite 六页原生设计器、字段表单、创建/改表/改名草稿及 SQL 预览确认、保存快捷键和离开守卫。Shell 根据原范围协调标签/缓存，不确定回执撤销原 session，保存后重新读取字段信息；最终全量检查和真实 SQLite Tester/race 通过，正式 Linux 窗口确认新增字段及主/附加库隔离。具体截图、可重复工具和未完成边界见 [SQLite 原生表设计交付记录](sqlite-designer-2026-11-02.md)。PG 设计、SQLite 删除/复制与其余完整目标继续待实施，不新增 O/V。

2026-11-02 后续补齐 PG 完整 catalog 读取与修订、稳定字段/约束身份、外部依赖和全局 DDL hook，以及表/字段注释和字段 storage/compression/statistics 的纯规划与私有事务执行。真实 PG 17 race 验证锁等待冲突、整批回滚、取消、设置恢复及注入的已提交回执丢失；修复空数组克隆导致的虚假冲突。PG SchemaDesign 仍只公开 Snapshot，完整 ALTER/表单/原生验收待实施。最终检查、二进制哈希、资源清理和限制见 [PG 结构设计后端记录](postgres-schema-design-2026-11-02.md)，不新增 O/V，完整目标继续有效。

2026-11-02 后续完成 PG 字段模型/解析、创建及原生 ALTER 规划、稳定字段映射和完整定义/范围校验。真实 PG 17 race fixture 执行预览 SQL，覆盖类型/默认/非空、身份与生成、约束/索引/触发器、属性及依赖改名；修复触发器字段列表阻止类型变更的问题。通用 Apply 的全量值/依赖核验及 PG 表单尚未完成，binding 保持只读。最终检查和严格验证范围见 [PG 原生 DDL 规划记录](postgres-native-ddl-plan-2026-11-02.md)，不新增 O/V。

2026-11-02 后续新增 PG 私有原生二进制保留值核验、完整类型闭包、rollback-only 独立定义规范化与目标形状比较，验收 harness 在提交前比较全部保留值及结构。真实服务发现并修正 TYPE 重置列属性、身份类型扩大默认序列边界及 NOT VALID 探针差异。正式通用 Apply 仍缺完整 catalog/dependency 保留证明与 PG 原生表单，binding 保持只读。最终验证、失败日志和限制见 [PG 保留值与独立结构核验](postgres-value-shape-proof-2026-11-02.md)，完整目标继续有效，不新增 O/V。

2026-11-02 后续补齐 PG 原生改名的完整目录保留证明和私有事务执行，扩大外部行类型表/SQL 函数依赖读取；真实 fixture 覆盖原生表/字段改名、继承、数组名称冲突、外部读取、精确数据/序列、修订冲突、取消、请求隔离及丢失提交回执。正式 CREATE/类型变更/重建依赖的完整证明及 PG 表单仍待接通，binding 保持只读。最终结果、失败记录和边界见 [PG 改名目录保真与事务执行](postgres-catalog-rename-proof-2026-11-02.md)，不缩小完整目标或新增 O/V。

2026-11-02 后续新增 PG 目标目录类/OID 稳定身份、探针随机名称的显式草稿别名、生成资产及序列归属校验、公开 pg_node_tree 表达式规范化和服务器字节序捕获。真实 fixture 比较带删除字段空洞的原表与独立 CREATE 探针，识别数值转换显示差异并保持实质表达式变化可见。完整目录图投影、原目录/依赖保留、探针副作用策略及 PG Apply/表单仍未完成；验证和失败记录见 [PG 目标目录身份与表达式核验](postgres-graph-identity-2026-11-02.md)。ClickHouse、OpenAI 兼容 AI SQL 和其余完整阶段继续有效，不新增 O/V。

2026-11-02 后续完成 PG 独立目标声明图、显式原资产引用、typed 属性/依赖映射、未知精确属性/依赖重数和完整证据检查，并在真实原生 CREATE/ALTER 验收中比较。声明图发现并修复 PG 17 将 NULL 统计默认值读成 0 的问题，真实验证显式统计和默认重置。原目录/入站依赖及原 opaque/physical 状态保留、探针副作用策略和正式 Apply/表单仍待实施；完整结果见 [PG 声明图与统计默认值修复](postgres-declaration-graph-2026-11-02.md)，不新增 O/V，也不标记整体完成。

2026-11-02 后续新增 PG 保留目标资产的原生身份、权限/注释/标签、未知属性及依赖容器/序列嵌套属性核验。真实提交前检查发现并修复 CHECK 替换丢失原注释，补恢复保留索引的聚簇及复制标识，并验证显式切换规则。完整原上下文/入站依赖/物理重写及探针副作用证明、正式 Apply/表单和其余完整目标仍待实施；检查、实际 SQL 与注入测试的区分见 [PG 保留资产属性核验](postgres-retained-catalog-2026-11-02.md)，不新增 O/V。

2026-11-02 后续新增私有 PG 原目录身份补读及服务实际数据库/schema 名称复核；真实验证本地字段/外键删除后退出当前闭包的外部函数/类型/表/序列/发布仍可读取当前状态，实际删除不恢复旧内容。原生验收使用补读材料，但保持普通返回基线。完整原上下文差异/入站依赖/物理重写及探针、正式 Apply/表单和其余完整目标仍待实施；结果与限制见 [PG 原目录上下文补读](postgres-original-context-2026-11-02.md)，不新增 O/V。

2026-11-02 后续新增已捕获未编辑 PG 原记录及入站依赖的严格核验，按原目录类/OID 保留完整未知/物理属性及重复边，拒绝缺失、新增与归属变化。真实发现并修复 TOAST 索引、发布成员和复合关系的闭包读取遗漏，未变化的复杂目录重复补读保持修订。正式一般 ALTER 的授权副作用/物理/序列/并发/探针证明及 Apply/表单仍未完成；本步验证与全部剩余目标见 [PG 未编辑目录核验](postgres-unselected-context-2026-11-02.md)，不新增 O/V。

2026-11-02 后续按原稳定字段 ID 推导明确删除后的原属性槽，保留原长度/对齐/位置/未知状态，并区分 PG 14–16 与 17–18 的原生清理规则；原生验收器所有字段删除计划在提交前检查原上下文。真实 PG 14/17 删除及 PG 18 普通删除验证，发现并修复 PG 18 已移出 attcacheoff 的布局要求。一般重写/重建/并发/探针及正式 Apply/表单、其它完整目标仍未完成；见 [PG 删除字段属性槽核验](postgres-dropped-attribute-2026-11-02.md)，不新增 Navicat O/S/V。

2026-11-02 后续按用户选择接入 ClickHouse 独立原生驱动、配置/URI/密钥环、表/视图元数据、独立 SQL 客户端和共享只读网格、分页/筛选/计数/流式导出。真实 ClickHouse 25.8.33.6 服务与 Shell race 验证精确值、会话隔离、取消/恢复和代次撤销。ClickHouse 高级管理、可配置 OpenAI 兼容 AI SQL 及其余完整目标继续有效；本阶段及原生采集发现的修复、检查和限制见 [ClickHouse 基础交付记录](clickhouse-native-2026-11-02.md)，没有新增 Navicat O/S/V。

用户后续要求逐项本地提交，覆盖此前不提交的约束。此前保留的已验证前置成果与 ClickHouse 基础分别归档；不推送、不改写历史。文中各阶段的“未提交”描述为当时状态，当前归档和完整剩余项见 [工作台记录](workbench-completion-2026-11-02.md)。

2026-11-02 后续完成可配置 OpenAI 兼容 AI SQL 基础：独立配置/HTTP/SSE 服务、系统密钥环 immutable 密钥及条件发布、原生请求预览、取消、生成/解释/优化与原连接未执行草稿。真实 Secret Service、自有协议服务和正式 Linux 1280×800 / 840×560 窗口验证生成/插入/重启恢复及密钥丢失/错误不覆盖配置；完整检查、截图、限制见 [AI SQL 交付记录](ai-sql-native-2026-11-02.md)。实现 I 与 NyaDB 运行证据不等于商业模型实测或 Navicat O/V；完整其余阶段继续有效。

2026-11-02 后续新增 PG 原序列位置核验：按完整原目录身份读取精确 last_value/is_called，稳定字段 ID/原所有权限定删除许可，原生规划验收在独立 CREATE 探针和执行前后比较。元数据/改名保留原权限要求；表所有者缺少外部默认序列 SELECT 权限仍可编辑自己的注释。真实服务、失败修复、最终检查及剩余通用 Apply/PG 表单等完整范围见 [PG 序列状态核验](postgres-sequence-state-2026-11-02.md)，不新增 Navicat O/S/V 或标记整体完成。

The current Linux alignment target is Navicat 18, Simplified Chinese, dark
theme, and compact density. Shared theme metrics now provide 19-DIP navigation
and chooser list rows with 16-DIP icons, low-emphasis toolbar selection, and
initial navigation/inspector content widths of 250/200 DIP shared across workspace
pages. The calibrated dark workspace/panel fill is `#232629`, and toolbar,
status bar, and connection-editor surfaces use `#31363b`.

The chooser and MySQL editor use a 900×700 reference client area. General and
Advanced use a centered 604-DIP form; SSL, SSH, and HTTP use the available
client width minus 40 DIP (860 DIP at the reference size). These forms share
a 166-DIP label column, 31-DIP row rhythm, and 26-DIP input height. The current
19-DIP chooser rows and full-width tunnel forms supersede the roomier chooser
and propagated 604-DIP form assumptions described in the historical milestones
below. Fixed editor footers and scrolling keep actions reachable in smaller
windows; cancel/back/close retain draft and credential-cleanup boundaries.

### Evidence and current acceptance

Implementation, headless UI tests, native GUI observations, and original/runtime
comparisons are separate evidence levels. **O** means original runtime
observation, **S** static original evidence, **I** independent implementation
and tests, **V** a passed comparison for the stated scene and measurements,
and **Unknown** insufficient evidence. Historical V results do not establish
current whole-page or whole-application parity.

Current differential tooling accepts workbench and connection-form UI-state
evidence without inventing database results. Objects, structure, and data
scenes still require matching real service, fixture, read-result, and selected
state evidence. Without a valid original read, those scenes remain **BLOCKED**.
The gate records client crops, actual scale, fonts, source identity, readiness,
selection/expansion/menu state, and explicit geometry/color measurements;
see [the current gate specification](UI_VISUAL_GATE_2026-10-25.md).

The [iteration report](UI_ALIGNMENT_2026-10-25.md) records passing tests, race,
static checks, packaging and cross-compilation, plus native screenshots and
the remaining strict visual BLOCKED conditions. Windows/macOS runtime behavior
and any unobserved minimum-window, scale, theme, or language combinations remain
**Unknown**, even when compilation or headless tests pass.

## Historical milestones

The entries below describe the implementation and verification **at their
recorded milestone**. Version pins, pending features, UI measurements, and
limitations in those entries are historical; the current snapshot above takes
precedence. Linked reports retain their original evidence scope.

## M0 — MyGo Native UI framework verification

Status: **M0 source implementation and headless/cross compilation checks passed. Native GUI smoke tests pending.**

### Scope

- Go module: `github.com/nyakang/nyadb`, Go 1.27.1+.
- MyGo pinned to v0.5.5 with its CLI declared as a module tool.
- Native window via `cmd/nyadb`, responsive sidebar, SQL editor, and synthetic DataGrid preview.
- SQL language selection, schema-outline mock, and headless UI tests.
- Cross-platform Go CI for Windows, Linux, and macOS.
- No database network access, secrets or persisted user data at M0.

### Verification

Go installed in Repo Code executor: `go1.27.3 linux/amd64`. Commands
were run with `CGO_ENABLED=0` and `GOPATH`, `GOMODCACHE`, `GOCACHE`,
`GOTMPDIR`, `GOENV` explicitly placed under `/data/.cache/golang`.

| Verification | Result |
|---|---|
| `go mod tidy` | PASS |
| `go test ./...` | PASS |
| `go vet ./...` | PASS |
| `go tool mygo vet` | PASS |
| Linux amd64 cross-build | PASS |
| Windows amd64 cross-build | PASS |
| macOS arm64 cross-build | PASS |
| `go tool mygo doctor` | PASS |
| `go tool mygo build` | PASS: Linux executable, .deb and .tar.gz archive |

Go builds validate platform compilation, not interactive UI operation.
Real Windows/Linux/macOS GUI smoke tests and CI job executions remain pending.

### M0 limitations

- Results are synthetic; no database queries are executed.
- The object tree is a mock schema. Database adapters start in M2.
- 100,000-row × 50-column preview is generated lazily in memory, not a DB paging benchmark.
- GUI input/rendering still needs real interactive Windows/Linux/macOS smoke tests.

## UI-1 / UI-2 — Navicat-inspired native UI layout

Status: **Implemented locally, headless UI interactions and compile checks completed**.

- Replaced the simple M0 shell with compact dark in-window menu row, icon toolbar,
  connection tree, object list, multi-tab SQL workspace, inspector and statusbar.
- Explorer, center pane and inspector use adjustable proportional splitters.
- Added a searchable mock connection tree, object filters, mock table browsing,
  independent SQL editor tabs, demo execution status, tab close, and inspector toggle.
- All UI graphics are new simple SVG icons; no third-party application icons.
- Saved a real MyGo headless preview at `docs/ui-preview.png`, generated on demand
  by `NYADB_WRITE_PREVIEW=1 go test ./internal/ui/shell -run TestGenerateUIPreview`.
- Detailed behavior and limitations in `docs/UI_LAYOUT.md`.
- Remaining: physical GUI smoke test on native operating systems, backend M1/M2.

See `docs/M1_PROFILE_STORAGE.md` for the initial M1 local-storage and profile CRUD slice.

Navicat 18 and NyaDB were both launched under isolated Linux Xvfb desktops
for first live GUI comparison. See `docs/NAVICAT_GUI_DIFFERENTIAL_2026-10-20.md`.

## M1-B — Native connection profile manager

Status: **Implemented and locally verified (I)**. NyaDB's real application
now opens a MyGo connection-type chooser and typed profile form, persists
non-secret MySQL/PostgreSQL/SQLite profiles in local SQLite, and shows them
in a searchable explorer with edit/copy/delete/refresh and inspector.
Isolated Xvfb tested profile creation, selection and restart persistence.
No database sessions or credentials are implemented (M2 pending).
Evidence, checks and limitations: `docs/M1_B_VERIFICATION.md`.

### Connection chooser fidelity iteration (2026-10-21)

Connection chooser now uses a native MyGo parent-owned modal child window
instead of an in-workspace overlay. Sidebar type/vendor rows have icons;
additional browsable database/cloud types and icon-only grid/list controls
are implemented. Supported profile kinds are unchanged. See
`docs/CONNECTION_CHOOSER_NATIVE_WINDOW_2026-10-21.md` for differential
screenshots, tests, security boundaries and remaining visual differences.

The connection chooser **list view** was subsequently rebuilt as a compact
expandable vendor tree after live Navicat 18 runtime observation. At this
milestone it used 25-DIP rows (roomier than the observed 19px original;
superseded by 19 DIP on 2026-10-25), independent vendor expansion, full-row selection and two-pane
search filtering. See `docs/NAVICAT_LIST_TREE_DIFFERENTIAL_2026-10-21.md`.

A subsequent sidebar iteration unified vendor filter ordering with the tree,
added a separate source-attributed provider badge pack, adjusted spacing, and
fixed the Dameng vendor checkbox. See `docs/PROVIDER_ICON_SIDEBAR_2026-10-21.md`.

### MySQL native connection editor (2026-10-21)

First UI reconstruction pass (I): original MySQL General tab observed in isolated
Linux Xvfb; NyaDB now displays native tabs, General fields, password masking,
and a fixed footer in a 900×700 editor window. Unimplemented tabs and connection
testing remain explicitly pending. Build/test checks and live GUI comparison:
`docs/MYSQL_CONNECTION_EDITOR_DIFFERENTIAL_2026-10-21.md`.

### MySQL advanced settings (2026-10-21)

The Advanced tab has native MyGo controls for driver, charset, timing,
compression, auto-connect, named pipe and session settings. Its non-secret
settings are validated and persisted with a backward-compatible local SQLite
schema migration (v1 to v2). Original behavior was observed in an isolated
Navicat session; driver execution is not implemented.
See `docs/MYSQL_ADVANCED_DIFFERENTIAL_2026-10-21.md`.

### MySQL compatibility and initial query (2026-10-21)

The Compatibility subtab now has native MyGo switches, dropdowns, exact
observed defaults and individually enabled overrides. Its validated settings
are persisted in the existing advanced JSON. The Initial Query overlay uses a
MyGo multiline editor with confirmation, cancellation and clipboard copying.
Because initialization SQL can contain credentials, its text is session-only,
not saved or executed. Details and original observation evidence:
`docs/MYSQL_COMPATIBILITY_INITIAL_QUERY_2026-10-21.md`.

### MySQL Database tab offline reconstruction (2026-10-21)

The Database tab now matches the observed custom database list design using a
native MyGo Table with Database and Auto Open columns. The custom-list toggle
is persisted; add/remove stay disabled until real MySQL metadata retrieval is
available. Original and NyaDB Linux GUI observations:
`docs/MYSQL_DATABASE_TAB_DIFFERENTIAL_2026-10-21.md`.

### MySQL SSL editor (2026-10-21)

The native MyGo SSL tab reproduces the original two-stage enablement for SSL,
verification, certificate paths, CA checking and an obscured key passphrase.
Only flags and certificate paths persist in the existing MySQL advanced JSON.
The passphrase is session-only and is cleared on close, cancel, save or back.
Real TLS handshakes remain unimplemented. Original UI screenshots, MyGo GUI
evidence and the unresolved behavior boundary are documented in
`docs/MYSQL_SSL_TAB_DIFFERENTIAL_2026-10-21.md`.

### MySQL SSH connection tab (2026-10-21)

The native MyGo SSH tab reproduces the observed tunnel toggle, SSH server
diagram, host/port/username inputs and three authentication modes: password,
public key, and password plus key. Non-secret metadata is validated and saved
inside the existing MySQL advanced JSON. Passwords and private-key passphrases
remain session-only and are cleared on editor lifecycle boundaries. The key
path browser uses a native file picker with stale-result protection. Real
SSH tunneling and host-key verification are not yet implemented. See
`docs/MYSQL_SSH_TAB_DIFFERENTIAL_2026-10-21.md`.

### MySQL HTTP tunnel tab (2026-10-21)

The MyGo-native HTTP tab now includes the original tunnel URL, Base64 query
setting, Authentication / Proxy subtabs, password and certificate sub-controls,
native certificate file browsing, and proxy defaults (port 8080). HTTP and SSH
tunnels are mutually exclusive. Non-secret settings persist in the MySQL
advanced JSON; authentication passwords and certificate passphrases remain
session-only. Actual HTTP transport, secure credentials storage and tunnel
script export are not implemented. Original and NyaDB evidence:
`docs/MYSQL_HTTP_TAB_DIFFERENTIAL_2026-10-21.md`.

### MySQL Notes editor and full-window refinement baseline (2026-10-21)

The MyGo-native Notes tab now provides the observed large multiline input,
label and footnote. User-authored notes are saved on successful profile
confirmation, reloaded on edit, preserved on duplication and discarded when
editing is canceled. Notes live in a new general Profile.Notes field, backed
by a backward-compatible SQLite v3 migration (v2 → v3); existing records
start with empty notes. Notes are stored as plain text and must not contain
credentials. See `docs/MYSQL_NOTES_TAB_DIFFERENTIAL_2026-10-21.md`.

All seven MySQL editor tabs now have a first independent UI implementation (I).
Visual and behavior parity (V) remains incomplete. The next prioritized work
is tracked in `docs/MYSQL_EDITOR_VISUAL_REFINEMENT_PLAN_2026-10-21.md`.

### Shared MySQL connection-window chrome refinement (2026-10-21)

The common 900×700 MyGo editor has a locally scoped blue-gray palette,
updated main-tab border, normalized footer-button height/spacing, and
re-centered independent connection/tunnel diagrams. All seven tabs were
visited in a Linux Xvfb GUI session. A headless UI regression test checks
shared controls and cross-tab tunnel state. Unimplemented profile-list
navigation is visibly disabled rather than made functional without evidence.
See `docs/MYSQL_CONNECTION_CHROME_DIFFERENTIAL_2026-10-21.md`.

### Shared MySQL form geometry (2026-10-21)

At this milestone native MyGo connection forms shared a 604-DIP centered layout, 166-DIP
label column, 31-DIP row rhythm and 26-DIP text/select controls. Browse
buttons use 22 DIP with 7 DIP spacing. General and Advanced were compared
against original screenshots before propagating these rules to SSL, SSH and
HTTP. Runtime observation on 2026-10-25 superseded that propagation: those
three pages use the full client width minus 40 DIP. At the earlier milestone,
existing MyGo checkboxes retained their native 16-DIP size. Headless
rectangular-geometry tests and isolated Linux GUI screenshots confirm
consistent positioning, without claiming pixel-perfect parity. See
`docs/MYSQL_FORM_GEOMETRY_DIFFERENTIAL_2026-10-21.md`.

### MySQL form fine-visual refinement (2026-10-21)

The editor now uses a window-scoped MyGo theme for its input background,
focused border and muted/disabled text. A shared 14-DIP native CheckboxBase
renderer replaces blue-fill checkboxes with the observed dark-square/light-tick
style on General, Advanced, Compatibility, SSL, SSH and HTTP. Native focus,
keyboard and accessibility behavior remain intact. GUI and headless regression
evidence is recorded in `docs/MYSQL_FORM_FINE_VISUAL_DIFFERENTIAL_2026-10-21.md`.

### MySQL new/edit lifecycle and configuration sidebar (2026-10-21)

MySQL editing now has a native, collapsible 258-DIP configuration sidebar,
with a selected main configuration and up to 16 named alternative drafts.
Alternatives can be added, switched, renamed, duplicated and removed.
The editor separates new/create from edit/update, omits the Back action when
editing, preserves unfinished settings across configuration changes, and
discards changes on Cancel or OS window close. Confirm validates and saves
non-secret main and alternative settings atomically in one profile row.
SQLite schema v4 adds a backward-compatible variants JSON column; real
database-session activation of an alternative remains unimplemented.
See `docs/MYSQL_EDITOR_PROFILE_LIFECYCLE_DIFFERENTIAL_2026-10-21.md`.

### Active MySQL configuration and future session resolution (2026-10-22)

Selecting an editor row no longer changes the Active target. A context-menu
action marks the primary or an alternative as Active in the window draft;
Confirm persists the selection with the alternatives. SQLite schema v5
adds `active_variant` (empty for primary) and migrates older profiles
without changing their default. The non-secret `ActiveSettings` resolver
is available to a future MySQL session factory and fails closed on invalid
references. The workbench inspector now shows the resolved Active
configuration but does not claim to be connected. The sidebar has keyboard
up/down navigation and one-time name focus. See
`docs/MYSQL_ACTIVE_CONFIGURATION_DIFFERENTIAL_2026-10-22.md`.

### MySQL direct TCP connection test (2026-10-22)

The native Go MySQL driver can establish, authenticate, Ping and explicitly
close a direct TCP session from the selected draft configuration. The editor's
Test button starts asynchronous, context-cancellable work, shows completion
and errors without storing credentials, and rejects unsupported SSL/SSH/HTTP
or other advanced settings *before* dialing. Closing the editor cancels work
and ignores stale results. The driver is also exposed as OpenDirectMySQL for
a future managed session layer, which is not yet connected to the workbench.
Protocol tests use a disposable local mock MySQL peer rather than a real
production database. Full database compatibility, TLS and encrypted credential
transport remain unverified/unimplemented. See
`docs/MYSQL_DIRECT_CONNECT_PHASE1_2026-10-22.md`.

### MySQL verified TLS and managed workbench sessions (2026-10-22)

The native MySQL driver now supports strict certificate/hostname-verified TLS,
optional CA and paired client key material. Remote plaintext connections are
rejected even in editor Test Connection. A session manager owns asynchronous
connection setup, cancellation, disconnect and cleanup, with stale-result
prevention and real SHOW DATABASES metadata queries. The workbench exposes
explicit Connect, Cancel, Disconnect and credential reentry before Reconnect;
its object view shows real database names rather than demo tables in profile
mode. This does not yet implement schema/table browsing or SQL execution.
See docs/MYSQL_TLS_MANAGED_SESSION_2026-10-22.md.

### MySQL database tree and real table/view metadata (2026-10-22)

Connected MySQL workspaces now offer a native MyGo database tree with lazy
per-schema object loading, real INFORMATION_SCHEMA table/view metadata,
engine/comment columns, database switching, type filters and explicit refresh.
Bound schema-name parameters and session-scoped cancellation prevent unsafe SQL
construction and late-query UI corruption; caches clear on disconnect.
The service enforces a bounded 10,000-object response and MyGo renders the
metadata list with its native Table. This does not execute arbitrary SQL or
load table rows. See docs/MYSQL_OBJECT_BROWSER_PHASE1_2026-10-22.md.

### Native connection editor shared palette (2026-10-22)

The native child window and type chooser now initialize the common NyaDB
dark theme. The editor's body, tabs, footer, form controls, checkboxes and
edit-profile sidebar use the same workbench color tokens rather than a
separate blue-grey palette. The shared layout, native MyGo focus and IME
behavior and existing connection functionality remain unchanged. Headless
all-tab raster checks and Xvfb desktop screenshots are recorded in
`docs/CONNECTION_EDITOR_SHARED_THEME_2026-10-22.md`.
