# 工作台剩余功能实施记录 — 2026-11-02

目标是完成用户选中的完整功能清单。以下阶段用于安排实施顺序，不缩小交付范围。独立实现与原版观察/差分分开记录；本任务不启动 Navicat、不连接生产数据库。用户后续要求每项完成后本地提交，覆盖此前不提交的边界；不推送或改写历史。

| 阶段 | 必须交付的能力 | 状态 |
|---|---|---|
| SQL 历史与全局日志 | 原连接执行记录、搜索筛选、只恢复文本、加密持久化、保留期限、停用、清除与迟到回执防护 | SQL 编辑器及数据/管理操作全局汇总已实现 I；失败恢复与原生证据见后续日志记录 |
| MySQL 设计器交互 | 字段/索引/外键/检查独立子页、结构化约束表单、完整定义保真、预览与保存回归 | 八子页及常用约束表单已实现 I；未知属性保持只读 |
| SQL 编辑体验 | 方言/别名/限定字段补全、执行与保存快捷键、原连接和编辑状态安全 | 常用上下文与快捷键已实现 I；完整语法分析仍有限制 |
| 常用入口 | 全局数据库对象查找、URI 导入、收藏夹、文件/编辑/窗口文档菜单 | 文档菜单及 URI 导入、对象查找和收藏夹已实现 I；原生验收见常用入口交付记录 |
| PG/SQLite | 图形结构编辑、导入/备份、PG 配置页、SQLite 附加库 | 配置与附加库、SQLite 六页原生设计器/创建/改名/删除/复制及普通视图 CRUD、PG 完整目录、原生 DDL 规划与私有事务后端和限定探针检查已实现 I；PG 正式通用 Apply/表单、常用对象、共享导入/备份/隧道及大数据能力仍待实施 |
| 跨库工具 | 数据传输、数据与结构同步、映射、差异预览、冲突及取消 | 待实施 |
| 自动运行 | 任务配置、执行记录、取消、失败处理、调度 | 待实施 |
| 高级功能与新引擎 | 模型/逆向、BI、监控、字典、生成、脱敏、命令行、AI、新引擎独立阶段 | ClickHouse 原生 SQL/共享只读网格、可配置 OpenAI 兼容 AI SQL 基础及只读数据字典预览/导出已实现 I；其余高级功能待实施 |

每阶段记录实际变更、覆盖行为和执行过的检查；最终按完整清单审计，不以某一阶段通过测试宣称整体完成。原版详细行为没有 O/S 时标为 Unknown，不从菜单名字推断业务规格。

## 本次已实施的工作

- SQL 历史复用 `queries.Store` 的加密记录和修订条件更新，无 SQLite 迁移。普通执行、EXPLAIN、运行导出、SQL 文件执行具有独立回执；支持离线搜索、逐连接/状态/来源/时间筛选、重新打开、保留设置、清除和停用。默认 30 天/1000 条，整体正文上限 32 MiB。清除及停用通过 epoch 阻止旧回执重新创建历史；打开只恢复文本，不执行 SQL。
- SQL 编辑器和 Shell 接通执行、保存、另存为、格式化、历史、新建、打开、关闭快捷键，以及文件/编辑/窗口菜单。菜单保留原标签、连接、generation 和批量目标。修复导航树吞掉连接节点 Ctrl+N 的问题；表节点仍保留新建表快捷键。
- 新增纯 `sqltext` 补全逻辑和 Shell 只读元数据快照：支持别名、JOIN、命名空间限定字段、CTE 显式字段/常见投影、命名子查询投影、相关父查询、UNION 分支隔离、方言关键词和标识符引用。仅消费原连接已加载的表/视图与文档字段，不发元数据查询；旧编辑器修订和关闭文档不接收补全。
- MySQL 设计器拆为字段、索引、外键、检查、触发器、表选项、注释、原始 DDL 八子页。索引表单支持字段顺序、前缀、ASC/DESC、类型、方法、可见性和注释；外键支持字段对应、引用对象及删除/更新规则；检查支持表达式和启用状态。改名保留 clause ID；表单及注释参与草稿守卫和 SQL 预览，预览后的新表单不能借旧预览执行。
- 函数索引、未知索引属性及执行版本注释保持完整原文并锁定。规划器拒绝重写或移除未知约束。真实 MySQL 核验允许明确的 ASC/BTREE 默认、CHECK 外层括号、默认 RESTRICT/NO ACTION 和本库限定名称规范化；字段、前缀、方向、动作和文本值的实质变化仍拒绝核验。
- 外键默认数据库显式限定为原表数据库，避免依赖物理连接当前数据库；本地拒绝字段引用自身，保留 `parent_id → id` 的合法自引用。

## 当前完整清单及实施顺序

| 顺序 | 缺口 | 源码依据与实际剩余工作 |
|---|---|---|
| P1 | 全局日志汇总与失败恢复 | 已接入全局加密执行日志；逐文档最近 50 条仍保留原功能。首次执行、队列界限、批量保存、显式重试/放弃和旧控制修订冲突已实现。最终验证与分配成本限制见 [全局历史交付记录](global-history-2026-11-02.md)。 |
| P1 | 全局查找、URI、收藏夹 | 已实现 URI 导入及临时凭据、原连接对象查找、收藏稳定身份/一级分组/条件发布与菜单范围。真实 SQLite、系统密钥环恢复及正式窗口验收见 [常用入口交付记录](common-entries-2026-11-02.md)。连接表单的独立 URI 按钮保持禁用，URI 导入从连接类型选择器进入。 |
| P1 验收 | 编辑器与设计器精细行为 | 补全不会从未知关系推导列，不展开星号/所有表达式，不替代完整 SQL 语义分析。单次解析限 256 KiB/65536 tokens/128 层、结果最多 256 个。未知 DDL 保真不等于已支持编辑；设计器仍无图形分区/函数索引编辑。真实 GUI 的覆盖范围见下文，原版行为 O/V 仍 Unknown。 |
| P2 | PostgreSQL / SQLite 深化 | PG 高级/TLS 和 SQLite 高级/附加库已接通真实执行与兼容持久化；见 [引擎配置记录](engine-options-2026-11-02.md)。SQLite 六页设计/创建/改名/删除/复制根据实际 SchemaDesign binding 开启，普通视图 CRUD 亦已交付。PG 已有完整 catalog 只读基线及私有事务元数据/改名执行；正式通用 Apply、图形表单和程序 CRUD 仍待实施。产品 DesignTables/Import/Backup 描述仍仅 MySQL 启用。尚缺共享导入/多格式导出向导、引擎备份及 PG 账户/权限适配。PG SSH/HTTP 与 SQLite HTTP 尚未实现；PG 已启用的隧道配置在运行前明确拒绝。 |
| P2 | 跨库传输与同步 | Shell 数据传输、数据同步、结构同步入口仍禁用。需中立计划、源/目标映射、类型与 NULL/二进制/精确数值保真、差异预览、冲突策略、确认、取消与部分/不确定回执。文件导入导出不是跨库同步。 |
| P2 | 自动运行 | 工具栏能力分支没有自动运行实现。需加密任务配置、显式调度、运行记录、资源租约、取消、失败处理及重启后的任务策略。详细原版调度行为仍 Unknown。 |
| P3 | ClickHouse 深化 | 已按用户选择接入独立原生驱动、描述/配置/URI/密钥环、表/视图元数据、独立 SQL 租约及共享只读网格/分页/筛选/计数/导出，真实服务与 Shell race 通过。按实际能力关闭事务/编辑/设计/导入/备份；复杂类型、集群/字典/函数及完整管理和多版本/TLS 仍待深化。见 [ClickHouse 基础阶段](clickhouse-native-2026-11-02.md)。 |
| P3 | AI 与其他高级功能 | 已实现可配置 OpenAI 兼容 Chat Completions、系统密钥环密钥、opt-in 请求预览、流式/取消及原连接 SQL 草稿建议；见 [AI SQL 交付记录](ai-sql-native-2026-11-02.md)。已交付基础数据字典的只读预览和两个格式导出，真实服务/原生范围见 [字典记录](data-dictionary-2026-11-02.md)。商业 AI 服务、多平台、AI 表设计/数据填充仍未验收或实现；模型/逆向、BI、服务器监控、生成、脱敏、命令行也仍在完整范围内。 |

PG 最新进展：已新增私有二进制保留值核验及独立目标形状比较，并在真实 fixture 的事务内提交前执行；完整 catalog/dependency 保留证明、正式通用 Apply 与原生表单仍待实施。准确的验证范围与已修复类型变更问题见 [PG 保留值与独立结构核验](postgres-value-shape-proof-2026-11-02.md)。这不会改变其他待实施阶段或用户选择。

PG 后续进展：原生表/字段改名已具备完整原目录证明及私有事务执行，包含外部 SQL 函数/行类型表依赖、继承和数组名称碰撞；通用类型变更/对象重建 OID 映射及正式表单仍待实施。见 [PG 改名目录保真](postgres-catalog-rename-proof-2026-11-02.md)。

PG 目标图进展：已补齐目录类/OID 稳定对象绑定、探针显式别名及本地表达式树/原生 OID Datum 规范化。完整原目录属性与依赖图比较、仅引用映射、探针副作用策略、正式通用 Apply 和表单继续待实施；见 [PG 目标目录身份与表达式核验](postgres-graph-identity-2026-11-02.md)。

PG 序列状态进展：已补齐按原 OID/范围读取精确 last_value/is_called、受稳定字段 ID/原所有权约束的删除许可，以及原生规划/独立 CREATE 探针前后的实际状态比较。既有元数据/改名保留原权限要求，缺失序列读取权不禁止合法表注释修改。完整并发/探针/新字段语义、正式通用 Apply/PG 表单及其余完整阶段继续待实施，见 [PG 序列状态核验](postgres-sequence-state-2026-11-02.md)。

PG 探针最新进展：显式索引函数调用先做词法收集与 core overload 读取检查，拒绝非 pg_catalog 用户函数和假 core 重载。PG 17 全量真实服务 race 34 个顶层 fixture 全部通过（903.711 秒），PG 14/18 两项专项通过，9 个本轮容器独立确认已移除。完整自动检查和限制见 [PG 显式索引函数检查](postgres-index-probe-routines-2026-11-02.md)。这不补齐类型/cast/运算符/其它表达式或并发证明，不启用正式 Apply；本次不 commit/push。完整当前缺口以 [剩余复刻清单](reconstruction-remaining-2026-11-02.md) 为准。

PG 类型与转换后续：前一项按用户更新后的逐项提交要求归档为 `5294f95`。新增字段/所有声明表达式中的显式类型、CAST、类型字面量和常量候选读取，以及数组、域、复合、范围、core 回调和实际 cast 候选的目录检查。真实证明 enum/numeric 转换可在探针中消费序列并补拒绝；最终 PG 17 41 项全量真实服务、PG 14/18 专项、PG 17 类型/TIME race 和自动检查通过，10 个独立 fixture 容器均已清理。按更新后的要求本地单独提交，不 push。验证边界与未完成的一般表达式/运算符/并发及正式 Apply/PG 表单见 [类型与转换探针记录](postgres-probe-types-2026-11-02.md)，其余完整目标保持有效。

PG 运算符后续：从 `0f1e14e` 接续现有补丁，在独立 CREATE 前检查运算符/同名重载、显式/默认操作符类和 core 访问方法；新增内建 SQL 运算符的严格已解析双参数转发检查。真实回归与 PG 18 QUERY 格式修复、最终检查及限制见 [运算符与访问方法探针记录](postgres-probe-dispatch-2026-11-02.md)。一般表达式/并发和正式 Apply/PG 表单仍未完成；验收时保留工作树，后续用户恢复逐项提交要求后本地归档，不 push，完整其它功能保持待办。

PG 声明图进展：已接通独立目标图投影、原资产仅引用映射、typed 属性向量/依赖、完整记录和未知属性比较，真实 CREATE/ALTER 验收在提交前核验。修复 PG 17 统计默认 NULL/0 混淆。下一步为完整原目录/入站依赖及原权限/RLS/序列状态保留、探针副作用策略、正式 Apply 和 PG 表单；见 [PG 声明图核验记录](postgres-declaration-graph-2026-11-02.md)。其他完整功能阶段继续有效。

## 检查与证据

Go 1.27.3 / MyGo v0.7.3-nyadb.2，`GOWORK=off`，Linux amd64。HEAD 仍为 `5960d26`，变更未提交。

- 最终全量 `go test ./... -timeout 20m` 已通过（Shell 76.365 秒、objectworkspace 3.163 秒）；生命周期 race 包括 queries/localdb/sqlworkspace/objectworkspace，以及单独 Shell 全包（683.856 秒）。最终布局/校验变更重跑 mysqlddl/objectworkspace race 通过（objectworkspace 16.141 秒），不重复未修改的 Shell 生命周期测试。
- `go vet ./...`、`go tool mygo vet`、翻译生成/检查（1201 条）和 `go tool mygo build` 已通过。
- `/usr/bin/python3 -m unittest discover -s tests -v`：42 项通过。默认 Conda Python 3.14 缺 Pillow，直接 `python3` 的第一次运行有 3 个 import errors；未把该失败记录算作通过。
- `tools/verify_mysql_core_fixture.sh` 使用自有 MySQL 8.4 容器，SQL 多结果/流式导出、对象、备份恢复及结构化表设计五项真实服务测试通过。新 fixture 同时验证 UNIQUE 前缀降序索引、注释、CHECK 规范化和 `parent_id → id` 外键。早期 `id → id` fixture 被 MySQL 拒绝，修正 fixture 并补本地校验后通过；不以重复执行同一失败 DDL 作为产品恢复方案。
- `tools/capture_history_acceptance.py` 使用正式二进制、真实系统密钥环和自有 SQLite，验证 Ctrl+N/Ctrl+Enter/Ctrl+L、执行历史、1280×800/840×560、跨进程恢复及清除持久化。新增 `--clear-only` 便于只重放清除步骤，`--with-designer` 可串行采集设计器。二进制身份读取 `/proc/<pid>/exe`，重启时不允许混用二进制版本。
- `tools/capture_designer_acceptance.py` 使用原生正式窗口与自有 MySQL，只创建设计草稿和采集页面，不确认 DDL。1280×800、840×560 的列表/索引表单已采集；840×560 保留可选定义列表、固定应用按钮，滚动后可看到约束字段表。530×430 中间面板的 Tester 回归检查列表、应用按钮及滚动后的下部字段控件。真实 MySQL 保存验证来自集成测试，不能由原生截图替代。

此前完整历史重放的 owned runs `r-574b1fd8c61d`、`r-6f0932b88c22` 和设计器/历史 run `r-6ed31e5252f5` 均已停止；配置证据保留在忽略目录，密钥环/D-Bus/Xvfb/Openbox/容器及临时 secrets 文件只按本次资源身份清理。

最终串行重放 `r-865fac4ad6cb` 的 `custom-d08d74d7` exit 0，正式二进制 SHA-256 为 `53ce848bd5a275e8bfeca291b68fb43baad871f609070a8b390e19db4035d5d9`。核对 manifest、`/proc` birth、容器 inspect 和临时 secrets 文件后，确认本次进程、容器和秘密文件均已清理。原生采集使用私有 D-Bus/Secret Service/Xvfb/Openbox，字体、实际缩放及字体尺寸仍 Unknown。

代表性本地证据（都在忽略目录）：

- [SQL 执行结果](../build/entry-acceptance/r-865fac4ad6cb/evidence/history-native/history-sql-result-client.png)、[跨进程历史恢复](../build/entry-acceptance/r-865fac4ad6cb/evidence/history-native/history-restart-client.png)、[清除后历史](../build/entry-acceptance/r-865fac4ad6cb/evidence/history-native/history-cleared-client.png)。
- [1280×800 索引表单](../build/entry-acceptance/r-865fac4ad6cb/evidence/designer-final/designer-index-1280-client.png)、[840×560 索引表单](../build/entry-acceptance/r-865fac4ad6cb/evidence/designer-final/designer-index-840-client.png)、[840×560 滚动后](../build/entry-acceptance/r-865fac4ad6cb/evidence/designer-final/designer-index-840-scrolled-client.png)。

未启动 Navicat、连接生产或新增 O/V，Windows/macOS 原生结果仍 Unknown。完整高级功能清单继续待实施，不能据此标记整体完成。

2026-11-02 后续补齐数据和管理操作的全局加密日志、首条执行加载策略、保存失败的上限/重试/明确放弃及控制修订冲突。实施、验证与性能边界见 [全局历史日志与失败恢复](global-history-2026-11-02.md)。

2026-11-02 后续已完成 URI、对象查找和收藏夹阶段。完整工作清单中的 PG/SQLite 深化、跨库传输/同步、自动运行、模型/逆向/BI/监控/字典/生成/脱敏/CLI、ClickHouse 与 AI 仍待实施；本次完成不代表整体目标完成。最新检查、原生证据、限制和清理结果见 [常用入口交付记录](common-entries-2026-11-02.md)。

2026-11-02 再次补齐 PG 高级/TLS 与 SQLite 高级/附加库：SQLite 每条物理连接初始化附加文件，结构与网格按完整命名空间读取和写入，支持只读及旧记录默认行为。真实 PostgreSQL 17 fixture 验证会话参数/连接池/超时，原生窗口验证两个引擎的配置页和 SQLite 附加命名空间。下一优先阶段仍是 PG/SQLite 图形设计、导入/备份与共享网格能力深化，其次跨库工具和自动运行；ClickHouse 与 OpenAI 兼容 AI SQL 按用户选择保留在独立阶段。检查与限制见 [引擎配置交付记录](engine-options-2026-11-02.md)。

2026-11-02 继续完成 SQLite 结构设计的纯模型/规划器、完整修订快照、BEGIN IMMEDIATE 事务重建、索引/触发器独立更新、原生命名依赖改写、类型化数据/rowid/序列核验及中立运行时 generation 绑定。仅后端实施 I；原生设计器及对象 CRUD 菜单尚未接通，不能将 PG/SQLite 图形设计列为已完成。真实 SQLite 回归、检查与限制见 [SQLite 结构设计后端记录](sqlite-schema-ddl-2026-11-02.md)。完整目标及 ClickHouse/OpenAI 兼容 AI SQL 优先选择保持不变。

2026-11-02 继续实现 PG 完整目录读取与稳定修订、事务元数据规划/执行及真实 PG 17 race 验收；修复复制空数组产生的虚假修订冲突，按实际驱动取消确认区分回滚和不确定状态。公开 binding 仍只读，完整表设计、转换和原生表单没有完成。最终检查、真实服务证据及完整待办见 [PG 结构设计后端记录](postgres-schema-design-2026-11-02.md)。当前所有未完成阶段仍属于用户选中的完整目标。

2026-11-02 继续实现 PG 字段解析、完整 CREATE/原生 ALTER 规划、触发器重建属性保留和原范围定义/外键绑定；真实 PG 17 race fixture 执行规划 SQL并核对代表性保留值及视图改名依赖。正式通用执行内的全量值/依赖核验和 PG 原生表单仍待接通，不能将验收 harness 视为生产 Apply。最终检查和下一步见 [PG 原生 DDL 规划记录](postgres-native-ddl-plan-2026-11-02.md)。完整高级功能、新引擎与验收目标继续有效。

2026-11-02 后续交付 SQLite 六页原生设计器、结构化字段、完整定义草稿、创建/改名入口及预览确认；最终 Go 检查、真实 SQLite Shell/inspector/失效回执回归和正式窗口执行通过。未完成的 PG、SQLite 对象/导入/备份、跨库工具、调度及所有高级功能仍属于完整目标。见 [SQLite 原生设计器记录](sqlite-designer-2026-11-02.md)。

2026-11-02 后续新增 PG 保留目标的身份、权限、注释、安全标签及未知属性核验，真实发现并修复 CHECK 原注释丢失，并补保留索引聚簇/复制标识及显式切换回归。该核验不是完整原目录/入站上下文或物理重写证明；PG Apply/表单和后续全部阶段仍未完成。实际 SQL、证据注入及限制见 [PG 保留资产属性核验](postgres-retained-catalog-2026-11-02.md)。

2026-11-02 后续新增 PG 原目录身份补读与服务真实数据库/schema 名称复核，真实验证退出当前依赖闭包的原外部对象仍取得当前状态。补读不是完整原上下文保留证明；PG 正式 Apply/表单、入站依赖/物理重写及后续所有完整阶段继续未完成。验收、工具与限制见 [PG 原目录上下文补读](postgres-original-context-2026-11-02.md)。

2026-11-02 后续新增未编辑原目录/入站依赖的严格保留核验，真实修复未变化时补读新增 TOAST 索引/发布成员/复合关系依赖的问题。尚未授权一般 ALTER 的上下文副作用，PG Apply/表单、目标物理/序列/并发/探针和其余所有完整功能仍待实施。用户确定的 ClickHouse 和可配置 OpenAI 兼容 AI SQL 独立阶段保持有效；见 [PG 未编辑目录核验](postgres-unselected-context-2026-11-02.md)。

2026-11-02 后续以原稳定字段 ID/属性位置独立推导明确字段删除的 native residual，并在原生验收删除计划提交前检查。真实验证 PG 14/17 的字段/身份/生成删除和 PG 18 的普通删除，修复 PG 18 cache offset 已不在公开目录的问题；一般重写/依赖重建/并发/探针、PG 正式 Apply/表单及其余完整目标保持未完成。验证范围见 [PG 删除字段属性槽核验](postgres-dropped-attribute-2026-11-02.md)。

## 本地提交归档

2026-11-02 后续新增 PG heap/TOAST 物理变化与探针回滚后新引用的不可变、请求限定证据。修复真实合法类型修改的 TOAST 重建/消失、新 cast 闭包和持久性切换校验，拒绝未知依赖容器的猜测重绑。普通测试及真实服务范围、PG 18 独立 NOT NULL 约束拒绝和最终检查见 [物理存储证据记录](postgres-storage-transition-2026-11-02.md)。公开 PG binding 仍只读；并发/探针副作用/新字段语义、正式 Apply/原生表单以及全部后续目标继续实施，不新增原版 O/S/V。

2026-11-02 后续完成 SQLite 普通视图创建、完整定义编辑、改名及删除，事务内复核原 namespace 修订、依赖及自有触发器，失败保留草稿并要求重读，原公开/上下文代次限定缓存和标签协调。正式 Linux 全流程验证依赖改名回滚、重读后改名、删除及源表/主库保持不变，最终应用和基础设施独立审计清理；检查与限制见 [SQLite 视图管理交付记录](sqlite-view-crud-2026-11-02.md)。PG 正式 Apply/原生表单、文件工具、传输/同步/任务及全部高级目标继续实施，不新增 O/S/V。

2026-11-02 后续完成 SQLite 原生事务表删除，按可选 binding 独立启用入口，复核原 namespace 修订，拒绝外键/视图/触发器依赖，取消等候回执，并补原表草稿守卫及提交后的限定标签/缓存清理。真实 SQLite、Tester、Linux 正式窗口结果和限制见 [SQLite 表删除交付记录](sqlite-table-drop-2026-11-02.md)。

2026-11-02 后续完成 SQLite 表结构/数据复制，保留完整定义、类型化行、可访问 rowid 和自增状态，预览/守卫/取消/回执保留原范围；修复取消文档离开决定后预览隐藏及 Unicode 字段判重问题。全量 Go、相关 race、静态/翻译/构建/Python 和最终正式 Linux 窗口均通过；结果、清理及限制见 [SQLite 表复制交付记录](sqlite-table-copy-2026-11-02.md)。SQLite 视图 CRUD、PG 正式 Apply、跨库工具/调度与其它高级功能仍未完成，完整目标继续进行，不新增 O/S/V。

此前各阶段在禁止提交的边界下保留于工作树。按用户更新后的要求，将这些已验证的工作台、引擎配置及 SQLite/PG 结构基础归为前置成果提交；ClickHouse 驱动及其接线、独立方言和验收工具单独提交。两个提交均保持源码完整可编译，完整复刻目标仍未完成。

前置成果使用独立 review 快照排除 ClickHouse 后运行全量测试；验证日志为忽略目录 `build/verify/clickhouse-rl2Bm0/go-test-baseline.log`。后续 ClickHouse 最终检查、最新二进制和原生结果以 [ClickHouse 交付记录](clickhouse-native-2026-11-02.md) 为准，前文结果保留为各阶段当时的验证记录。

2026-11-02 后续交付可配置 OpenAI 兼容 AI SQL 基础，按独立服务/凭据/SQL 工作区边界实现。真实 Secret Service 与正式窗口验证请求预览、生成、插入保留草稿、重启恢复及缺失/错误密钥不覆盖配置；AI 不执行建议。自动测试、二进制/截图、隔离清理、未验证场景和最终归档见 [AI SQL 交付记录](ai-sql-native-2026-11-02.md)。完整 PG/SQLite、跨库工具、调度与其余高级功能仍待完成，不新增 Navicat O/S/V。
