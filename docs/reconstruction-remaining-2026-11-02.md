# 尚需补齐的工作台能力 — 2026-11-02

按当前正式源码和各阶段验收记录核对。用户指定的 ClickHouse 与可配置 OpenAI 兼容 AI SQL 基础已交付；后续新增基础数据字典与 SQLite 原生表删除、结构/数据复制及视图 CRUD。以下未完成项继续属于用户要求的完整目标，不以基础阶段替代最终范围。

## 已有成果

SQL 历史/全局加密日志、SQL 常用上下文补全和文档快捷键、MySQL 八页设计器及常用约束表单、查询资产/分组、URI/对象查找/收藏夹、账户/备份新入口、PG 高级/TLS、SQLite 附加库和六页图形设计/事务表删除及复制/视图 CRUD、ClickHouse 原生 SQL/共享只读网格、AI SQL 建议、只读数据字典、PG 函数/过程/序列目录与只读定义、PG 14–18 普通表正式设计保存分别有实现 **I** 和各自限定的运行记录。不要重复按“菜单曾禁用”将这些列为未实现；也不能把有测试 harness 的代码当作已经启用的正式功能。

## 当前缺口与实施顺序

| 优先级 | 能力 | 实际剩余工作 |
|---|---|---|
| 深化 | PostgreSQL 图形设计 | PG 14–18 核心普通表的字段、默认值、非空、identity、版本允许的生成列、常用约束、索引、触发器、表属性、SQL 预览及正式保存已交付，见 [设计保存记录](postgres-designer-save-2026-11-03.md)。表/字段改名单独保存，删除字段须输入原表名称。仍缺用户类型稳定性方案、分区/继承编辑、物理字段重排、身份序列完整选项修改和更多表达式；当前明确拒绝这些范围。PG 对象 Drop/Copy、视图/程序 CRUD 不随表设计开放。Windows/macOS 原生及 Navicat 行为/性能差分继续待验证。 |
| P1 | PG/SQLite 常用对象与文件工具 | PG 函数/过程/序列目录、重载身份、只读定义及搜索/收藏入口已交付，见 [对象浏览记录](postgres-program-browse-2026-11-03.md)；仍缺 PG 视图/程序 CRUD、账户权限适配；共享导入/多格式导出向导、PG/SQLite 备份恢复。SQLite 普通视图 CRUD 与表删除/复制已交付，自动依赖改写和图形查询设计仍不支持。PG SSH/HTTP、SQLite HTTP 尚未接通；运行必须继续明确拒绝未支持配置。 |
| 验收/深化 | PG/SQLite 大数据体验 | Keyset、Count、FieldChunks、Analysis 已接通真实运行及共享网格；原物理会话/规则限定游标、显式计数、1024 字符/字节字段读取和全表精确分析已补。真实 PG/SQLite 服务、Tester、十万行正确性及 Linux 正式窗口范围见 [大数据交付记录](portable-large-data-2026-11-03.md)。无稳定键/可空排序等不承诺 seek；同条件峰值 RSS、完整数据类型/排序范围、多平台和 Navicat 差分继续深化。 |
| P2 | 数据传输 | 当前入口禁用。建立源/目标映射、类型及 NULL/二进制/精确数值保真、预览、批次执行、取消与部分/不确定结果。已有文件导出不能算数据库间传输。 |
| P2 | 数据同步与结构同步 | 当前入口禁用。需要独立差异计划、稳定对象/行身份、冲突策略、修改确认、执行前复核及失败恢复，不能通过重放 SQL 自动覆盖新范围。 |
| P2 | 自动运行 | 当前工具栏入口没有任务实现。需要加密任务配置、手动运行、调度、租约/取消、运行记录与重启策略；详细原版调度行为仍 Unknown。 |
| P3 | 模型/逆向表到模型 | 当前入口没有模型工作区。需要对象关系图、模型持久化、逆向映射及安全生成/预览。图形表设计器不是关系模型。 |
| P3 | BI 与服务器监控 | 尚缺指标/查询数据集、图表/布局、可复用报告，以及原连接监控采样、取消和历史；监控菜单仍禁用。 |
| P3 | 数据生成、脱敏与 CLI | 菜单仍禁用。需要明确输入/输出范围、类型规则、可复现生成、脱敏预览/执行/验证与命令行连接/查询/导出及退出码。 |
| P3 | ClickHouse 深化 | 基础真实驱动与只读网格已实现。仍需复杂类型、集群/字典/函数、完整管理、TLS/多版本和更多真实服务/原生验证；现有产品能力不支持关系型事务/编辑/设计/导入/备份。 |
| P3 | AI 深化 | SQL 生成/解释/优化和未执行草稿已实现。仍需商业兼容服务验收、多平台、AI 表设计/数据填充及各自的预览与执行边界；自有协议服务通过不等于商业模型实测。 |
| P2/P3 | 完整工作区与连接协作 | 现有加密 Workspace 只保存 SQL Documents/ActiveID；数据/对象页恢复、命名工作区/切换仍需设计和实现。连接颜色、共享菜单仍禁用；共享须另定资产、凭据与协作范围，不能以已有连接分组替代。 |
| 持续 | 已有体验精细行为 | 完整补全语义、图形分区/函数索引、专用程序编辑器、图形查询构建/执行计划、超大元数据性能及多平台布局；图形计划仍需行为规格，已有普通 EXPLAIN 不重复列为未实现。表设计入口已按实际 binding 区分，管理、程序 CRUD、删除和复制仍各自校验能力。数据字典已有基本预览/两个格式导出；完整目录/程序/权限/物理属性不是本次字典交付。 |
| 验收 | Windows/macOS 和原版对照 | 继续分别补跨平台实际窗口、行为规格及合法授权的 Navicat 观察/差分。Linux NyaDB 截图、构建和 Tester 都不能替代 O/V。 |

## 源码依据与验收区分

[PG 对象浏览交付记录](postgres-program-browse-2026-11-03.md) 区分只读目录与对象 CRUD，记录 PG 14/17/18、工作台回归及 Linux 正式窗口；序列 SQL 是静态参数重建，不是完整备份，该阶段没有开放 PG 设计 Apply；随后正式保存见 [设计保存记录](postgres-designer-save-2026-11-03.md)。

早期 PostgreSQL 新字段值核验的故障复现、真实服务结果、失败及兼容性修复记录见 [独立新值核验](postgres-new-value-proof-2026-11-02.md)。该内部实现不会自行开启正式设计入口；原生 GUI、跨平台及 Navicat 差分仍按各自实际证据计算。

后续 PG 原生表单组件与预览已通过 Tester，[表单准备记录](postgres-designer-forms-2026-11-02.md) 保留当时真实 domain 并发失败。当前[类型并发边界修复](postgres-probe-type-stability-2026-11-02.md) 已在隐藏 CREATE 前明确拒绝 domain 和可变用户类型，覆盖捕获类型 ID 后新增 CHECK、同名 enum 替换及不进入原失败注入点。普通表依赖没有稳定类型锁；随后 [正式保存阶段](postgres-designer-save-2026-11-03.md) 补齐受支持声明检查闭包、依赖锁定、版本矩阵与窗口验收，并公开 Apply；用户类型及其它未支持表达式继续拒绝。历史组件与拒绝记录按当时范围保留。

- [PG 当前 binding](../internal/services/engines/sqlite_design.go)、[通用设计器准备](../internal/ui/objectworkspace/schema_design_state.go)、[PG 私有 CREATE/ALTER 事务](../internal/services/engines/postgres_design_ddl_apply.go)。
- [实际工具菜单及工具栏](../internal/ui/shell/chrome.go) 保留传输/同步/生成/脱敏/CLI/监控等禁用入口；预览页的静态演示不作为正式能力依据。
- [产品引擎能力](../internal/services/connections/descriptors.go) 的 Import/Backup 仍仅 MySQL 开启，DesignTables 已包含 PostgreSQL；SQLite 原生设计由实际 SchemaDesign binding 单独启用，不能只根据描述表漏算它。
- [完整工作台记录](workbench-completion-2026-11-02.md) 保留逐阶段结果；[字典交付记录](data-dictionary-2026-11-02.md) 和 [PG 序列核验](postgres-sequence-state-2026-11-02.md) 为本轮新增成果。
- [SQLite 表复制交付记录](sqlite-table-copy-2026-11-02.md) 单独列正式入口、真实 SQLite、Tester、race、Linux 窗口及未验证边界。
- [SQLite 视图管理交付记录](sqlite-view-crud-2026-11-02.md) 单独列完整定义、依赖回滚、触发器保留、原连接草稿/回执、正式窗口及资源清理。
- [PG 物理存储及新引用证据](postgres-storage-transition-2026-11-02.md) 区分代表性合法重写、严格未知属性保留、PG 18 拒绝及正式 Apply/新字段/并发/探针仍未完成；规范化证据不作为工作台目录或 COMMIT 授权。
- [PG 显式索引函数探针修复](postgres-index-probe-routines-2026-11-02.md) 完成函数拒绝回归、PG 14/18 专项及 PG 17 全量真实服务 race；类型/cast/运算符/域及其它表达式、并发定义变化尚未形成完整探针证明，不据此启用正式 Apply。
- [PG 类型与转换探针检查](postgres-probe-types-2026-11-02.md) 区分真实 enum/numeric 转换序列消费、没有观察到副作用的域样例、结构化类型兼容及尚未完成的一般表达式/运算符/并发证明。
- [PG 类型并发边界](postgres-probe-type-stability-2026-11-02.md) 更新私有探针支持范围：仅固定系统类型；此前的无副作用 domain/用户结构类型样例不再授予隐藏 CREATE/投影许可。完整目录和已有值摘要读取范围保持独立。
- [PG 运算符与访问方法探针检查](postgres-probe-dispatch-2026-11-02.md) 补拒绝运算符重载、显式/默认操作符类、用户访问方法和被替换的内建 SQL 转发体；不构成一般表达式/并发证明，也不启用正式 Apply。
- [PG CREATE/ALTER 事务服务](postgres-ddl-transaction-2026-11-02.md) 将既有核验接入服务内部事务并覆盖真实生命周期；binding/表单、通用新字段与一般表达式/并发证明仍未完成。
- [引擎能力](../internal/services/connections/descriptors.go)、[分页策略](../internal/services/engines/metadata.go)、[SQL Workspace 保存模型](../internal/services/queries/store.go)、[连接颜色/共享菜单](../internal/ui/shell/profile_navigation_menus.go) 分别支持上述大数据、完整工作区与协作缺口。

本表的代码依据来自 NyaDB，不能标为原版静态证据 **S**。已有成果按实际测试标 **I**；新增原版 **O/S/V** 均未采集。对未完成能力只列实现需求，不从菜单名称推断 Navicat 的私有内部行为。用户恢复“每完成一项请提交一次”后，按逐项验收结果本地归档，不 push 或改写历史；前期未提交记录描述当时结果。完整目标保持进行中。
