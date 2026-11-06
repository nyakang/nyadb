# 应用全局审查 — 2026-11-06

审计基线存在逻辑、设计、性能与交互问题。核心模块拆分和数据库操作保护已经较完整；当时最需要优先处理的是连接删除的操作顺序、工作区切换的失败保护，以及数据编辑和 SQL 工作区在 UI 线程上的重复复制。现有测试通过不能覆盖这些遗漏。

**实施更新：A01–A09 已完成首轮修复，以下问题描述与探针结果保留为审计基线证据。** 当前行为、回归用例、性能前后比较和验证情况见 [首轮优化实施与验证](first-optimization-2026-11-06.md)。历史孤立初始 SQL 不清扫；后续架构风险仍属待处理范围。

审查基线：`b023df8091babf6f1d51c6aa7e89a0011978ad76`。审计阶段没有修改生产 Go 代码；新增本报告，探针、日志与正式入口构建保存在忽略目录 [build/global-audit-20261106](../build/global-audit-20261106/)。后续修复与验证单独记录在上述实施报告。

**范围与证据。** 检查 bootstrap、连接模型/存储、runtime、MySQL/PG/SQLite/ClickHouse 适配器以及 shell、连接选择/编辑、navigation、sessionbrowser、SQL、数据、对象、主题等核心流程。`internal` 当前有 468 个生产 Go 文件、约 95,279 行生产代码、472 个测试文件。本轮是核心路径的全局静态审查、已有测试执行、针对性行为复现和局部性能测量，不是对所有代码逐行证明。使用 Go 1.27.3；正式验证和探针使用 `GOWORK=off`，对应固定 MyGo `v0.7.4-nyadb.1`。当前 go.work 仅包含本项目，不覆盖依赖。

2026-11-05 的 UI01–UI15 已有对应实现和测试；本轮没有将旧审查的字号重叠、长名称工具栏、高对比度色对等重新当成当前缺陷。未重新进行原生窗口、MySQL/PG/ClickHouse 真实服务器或 Windows/macOS 桌面验收，也没有进行 Navicat 性能对比。

**优先级。** P1：存在现场/配置完整性问题，或在支持的工作量下阻塞高频操作；P2：局部状态、恢复、维护与扩展问题；P3：低频边界或说明问题。下面的“复现”指针对当前代码的探针；“源码”指已确认的实现事实，不自动等同于已测到生产环境故障。

| 编号 | 优先级 | 问题 | 证据 |
| --- | --- | --- | --- |
| A01 | P1 | 删除连接先写存储，后处理未保存文档；取消不能保留配置 | 行为复现 |
| A02 | P1 | 不支持的工作区版本在报错前已经清除当前标签 | 行为复现 |
| A03 | P1 / P2 | 批量填充逐格复制整页；一次操作产生多条、可能大量丢失的撤销记录 | 行为复现与测量 |
| A04 | P1，较大 SQL 工作区 | 未变化的工作区每帧仍完整 JSON 序列化 | 微基准与主线程调用链 |
| A05 | P2 | 删除 ClickHouse 连接遗漏已保存数据库密码 | 行为复现 |
| A06 | P2 | BrowserReset 回调没有核对导航节点所属连接 | 回调行为复现 |
| A07 | P2 | SQL 取消分支掩盖同时返回的会话失效，恢复状态晚一步出现 | 行为复现 |
| A08 | P3 | 序列填充检查了最后一个目标之后的无用值，错误拒绝合法边界 | 行为复现 |
| A09 | P2 | 初始 SQL 每次修改新增记录，缺少旧记录和失败写入的清理机制 | 源码 |

**A01：删除连接的守卫发生在持久化删除之后。**

位置：[profile_state.go](../internal/ui/shell/profile_state.go) 第 109–146 行、[mysql_session_state.go](../internal/ui/shell/mysql_session_state.go) 第 27 行、[workspace.go](../internal/ui/shell/workspace.go) 第 264 行。

`deleteProfile` 先调用 `profileService.Delete`，随后才调用 `disconnectMySQLProfile`。后者可能因未保存表设计、数据草稿、活动事务或忙碌操作而返回，前者仍会继续刷新连接列表和删除凭据。

探针创建连接及未保存表设计，执行删除，随后点击“保存表设计更改？”里的“取消”。设计草稿保留，但 `service.Get` 已读不到连接。用户的取消只阻止了后续断开，没有阻止更早的配置删除。这也使原连接与草稿的恢复路径产生不一致。

建议将删除协调成完整操作：捕获原 ProfileID → 完成所有离开守卫 → 删除配置 → 断开原会话并清理已知凭据。守卫取消或保存失败应结束整个操作；异步继续执行时仍核对原连接身份。现有删除测试只检查无草稿 MySQL 连接的凭据清理，没有覆盖此路径。

**A02：工作区版本校验太晚。**

位置：[workspace_manager.go](../internal/ui/shell/workspace_manager.go) 第 87–105 行、[workspace_snapshot.go](../internal/ui/shell/workspace_snapshot.go) 第 51–54 行。

`switchWorkspace` 在保存当前快照成功后，先关闭当前文档、缩减标签，再调用 `restoreWorkspace`。后者遇到 `SnapshotVersion > 2` 才返回“不支持”。探针中的 SQL 编辑器已被释放，只剩永久对象标签。

这是降级打开未来版本命名工作区等场景的失败保护缺陷。真实持久化流程虽然先保存了原快照，但之后每帧观察到的空现场仍可能覆盖自动恢复快照；不能把“先保存过”当成最终保留现场的保证。探针验证了关闭顺序，没有模拟这一后续落盘过程。

建议在任何关闭动作前校验版本、标签类型与必要身份，并先构造可恢复的目标描述。恢复失败应保留当前标签和文档。为未来版本、无效目标及目标构造失败补充失败不变性检查。

**A03：批量填充的撤销设计放大了主线程成本。**

位置：[toolbar_fill.go](../internal/ui/dataworkspace/toolbar_fill.go) 第 105–108 行、[editing.go](../internal/ui/dataworkspace/editing.go) 第 745 行、[draft_history.go](../internal/ui/dataworkspace/draft_history.go) 第 22–87 行。

填充循环对每个目标调用 `stageMySQLDataValue`。每次调用都执行 `beginDraftChange`，复制整页 previews、行身份、原始快照和所有草稿，再复制操作后状态、序列化估算尺寸。填充循环没有外层历史分组。已有粘贴路径在 [clipboard.go](../internal/ui/dataworkspace/clipboard.go) 第 298 行使用了外层分组，可以作为修复参考。

行为探针填充同一行的两个字段，生成两条 Undo；撤销一次只恢复最后一个字段。大选区还受 32 条/8 MiB 历史上限影响，先填充的单元格可能已没有可用撤销记录。

性能探针使用 100 行 × 10 列，每个缓存单元格为 512 字节 ASCII，填充其中 200 个单元格。当前机器上单次耗时 **2.141 秒**、累计分配 **458,257,696 字节**，最后仅保留 **3 条**撤销记录。累计分配量不是峰值 RSS；这是直接调用状态操作的测量，不包含真实窗口绘制和数据库写入。填充函数从弹窗点击回调同步调用，因此这段成本会占用 UI 线程。

建议先给一次填充增加外层历史分组，确保一次撤销恢复整个操作；再将撤销改为记录被改变的单元格/行与原身份，避免每次单格编辑复制整页。尺寸预算应在复制大对象前评估；超预算时应明确提示撤销不可用。

**A04：自动保存有防抖，工作区快照计算没有防抖。**

位置：[chrome.go](../internal/ui/shell/chrome.go) 第 35 行、[sql_documents.go](../internal/ui/shell/sql_documents.go) 第 36 行、[workspace_snapshot.go](../internal/ui/shell/workspace_snapshot.go) 第 19 行、[documents.go](../internal/ui/sqlworkspace/documents.go) 第 153–210 行。

每帧调用 `observeSQLDocuments`，先收集全部标签、SQL 文本、布局、导航/对象状态，再在 `ObserveWorkspace` 中 `json.Marshal(w)` 并转成字符串比较。500 ms 定时器只延后落盘，不能减少前面的每帧序列化。这里已有编辑 buffer version 缓存，但不能消除整个 Workspace JSON 的开销。

未变化工作区的微基准：

| 文本规模 | 每次 ObserveWorkspace | 每次分配 |
| --- | --- | --- |
| 1 MiB | 5.77 ms | 2.21 MB |
| 10 MiB | 63.25 ms | 23.61 MB |

CPU 为 Intel Xeon E5-2620 2.00 GHz。基准只测 ObserveWorkspace，不包含 shell 快照、编辑器布局、绘制和加密写入，不能当成完整帧耗时或所有设备上的结果。10 MiB 是模型中的总文本测试量；正常文件打开单文件上限为 8 MiB，多标签总量仍可超过 10 MiB，文本还可能在快照中同时作为 Text/SavedText 保留。

建议使用独立版本号记录文档文本、选择/光标、布局与导航变化；无变化帧直接返回，变化后按防抖生成不可变快照，再交给 worker 编码/加密。需要保留退出和切换工作区的显式 flush，不能因性能优化遗漏最终文本。

**A05：ClickHouse 密码保存和删除不对称。**

位置：[save.go](../internal/ui/connectioneditor/save.go) 第 125 行、[profile_state.go](../internal/ui/shell/profile_state.go) 第 132 行。

编辑器允许 PostgreSQL 和 ClickHouse 保存数据库密码；删除连接时数据库凭据清理只包含 MySQL/PostgreSQL。探针删除 ClickHouse 配置后，原 ProfileID 的主凭据仍在注入 vault 中。

这不是密码进入普通配置文件，也没有验证到密码被其他连接使用；确认的影响是用户删除连接后旧系统凭据遗留。建议清理由“支持数据库密码的产品能力/明确契约”驱动，并为四种引擎分别检查保存、取消、删除和清理失败。

**A06：重置回调越过原连接范围。**

位置：[mysql_session_state.go](../internal/ui/shell/mysql_session_state.go) 第 130–136 行。

`BrowserReset(id)` 在清理指定连接的对象 presentation 和树披露后，只判断当前导航节点是否含 Database，便改成 `ConnectionKey(selectedProfileID)`，没有比较 `navigation.Parse(Key).ProfileID == id`。

回调探针让当前节点为连接 2 的 otherdb，再执行连接 1 的 reset；连接 2 的数据库选择被替换为连接根。证据是宿主回调行为，不是跨连接原生窗口端到端复现。导航选择与会话所属连接本来允许独立存在，因此回调应始终核对原连接。

建议仅修改属于 id 的导航节点；保留其他连接的数据库/对象选择。增加当前/非当前连接、导航与活动标签不一致的交叉用例。

**A07：取消时同时失去 SQL 会话，未立即启用恢复。**

位置：[execution.go](../internal/ui/sqlworkspace/execution.go) 第 257–269 行。

执行结果的错误同时包含 `context.Canceled` 与 `ErrSQLSessionLost` 时，先进入取消分支，只设置“执行已停止”；后面的会话失效分支不执行。探针消费这种回执后，Document 已非 loading，但 `NeedsRecovery()` 仍为 false。下一次执行才可能再次收到失效错误并进入恢复。

建议把“执行结果是取消/不确定”与“物理会话是否可用”分别更新。取消说明仍需保留“服务器端更改可能生效”，同时立即记录原 ProfileID/Namespace 并显示显式恢复入口，禁止自动重放 SQL。

**A08：最后一个序列值合法也会被拒绝。**

位置：[toolbar_fill.go](../internal/ui/dataworkspace/toolbar_fill.go) 第 94–100 行。

对每个目标先使用 value，又无条件计算 value+step。即使当前目标已经是最后一个，也会因下一值溢出返回错误。一个 bigint 单元格，起点 MaxInt64、步长 1，合法填充被拒绝；负数边界同理。

建议只在确实还有下一个目标时计算并检查下一值。保留整个选区在改变任何草稿前完成验证。

**A09：初始 SQL 缺少引用回收和失败清理。**

位置：[initial_sql.go](../internal/ui/connectioneditor/initial_sql.go) 第 74–118 行、[save.go](../internal/ui/connectioneditor/save.go) 第 91–149 行、[initial_sql.go](../internal/services/queries/initial_sql.go) 第 15–35 行、[sql_documents.go](../internal/storage/localdb/sql_documents.go) 第 43–45 行。

修改初始 SQL 会生成新 ID 并写入新加密记录。替换旧 ID、删除备用配置、删除连接或后续凭据保存失败时，没有看到旧/新孤立记录的回收；SQLRepository.Delete 也只允许 kind=query。因为使用新 ID，本轮没有证据表明后续失败会改写原初始 SQL，问题是残留加密内容与增长。

`LoadInitialSQL(id)` 为查找一个 ID，先加载所有连接的全部 initial_sql payload，再线性查找。长期修改和重试会加大读取/内存成本，也缺少明确的删除后保留策略。

建议增加按 ID 读取和明确的保留/回收策略。清理必须考虑所有主/备用配置和可能恢复的快照引用，不能直接删除“看似旧”的记录。新增失败写入应有明确回滚或可追踪的孤立记录清理。

**设计与性能风险：源码已确认存在这些结构，但尚未给出真实环境故障频率。**

| 风险 | 实现事实与可能影响 | 建议 |
| --- | --- | --- |
| UI 线程同步 I/O | connectioneditor.Save 包含配置读写、初始 SQL 写入和多次 vault Get/Set/Delete；sessionbrowser.connectEngine 在启动 worker 前读取密码；布局的 write 回调直接在 View/Close 中写 localdb。配置 DB 只有一个物理连接，等待后台写入或系统密钥服务会占住界面回调。 | 注入捕获不可变请求的任务服务，提供忙碌反馈与有界等待；UI 只消费回执。短小本地操作不是必然可观察卡顿，本轮未测系统 keyring 延迟。 |
| 终止清理可能同步等待驱动 | sqlworkspace.dispose 在 UI 上 Release；ClickHouse Release 在 cancel 后等待 ops.Lock，PG/SQLite 的 Release 调用物理 SQLLease.Close。正确地等待关闭并不等于 UI 应承担等待。 | 保留取消/释放完成边界，在后台等待物理清理，立即撤销 UI 文档身份。需要真实慢服务器、锁等待与隧道断网测试。 |
| 内存上限主要是每文档 | 页面、结果快照、草稿历史有边界；总标签数、多个停放连接、已载入命名查询文本没有统一应用预算。SetPersistence 启动时 ListNamed(0) 解密并加载全部命名查询正文。 | 给应用总缓存设预算，对非活动结果/对象使用 LRU 或按需读取；查询目录先加载元数据，打开时读取正文。不能清掉未保存草稿和不确定提交的核验资料。 |
| 大表统计成本可能很高 | startDataAnalysis 将 page.Columns 全部传入；引擎逐列运行计数、分组、分布、数值和中位数查询。返回行数有界不能证明服务器扫描有界。 | 允许选择分析字段；将共享计数合并、显示预计工作量/阶段，必要时提供明确的抽样模式。当前支持显式启动和取消，是已有保护。 |
| 深页 offset 回退 | 游标正常前进可用 seek；未缓存的直接跳页、无稳定键和 ClickHouse 使用 offset。不会因为网格虚拟化而自动得到低成本深页查询。 | 保留清楚的回退提示；缓存/稀疏锚点应有预算。按相同数据、排序、索引和冷暖缓存测实际延迟。 |
| 中立抽象仍有遗留耦合 | objectworkspace.TableRunner/TableEvent 等公开接口仍引用 mysqlsvc.MySQLTableChange/MySQLTableAction；通用表操作混合 MySQL 与 schema 分支。没有发现因此必须新建第二套数据 State 的理由。 | 逐步抽取 neutral 的对象操作请求/回执，适配层完成转换；保持现在唯一 dataworkspace 和宿主协调边界，避免再次大范围拆分。 |
| 持久化故障隔离较粗 | queries.Store.open 的单条鉴权失败设置 Store.blocked；加载所有命名查询遇到一条错误立即整体返回。这有防止错误密钥覆盖的价值，但降低局部内容恢复能力。 | 区分错误密钥、单条损坏与版本不支持；保留原密文并提供逐条诊断/导出修复流程。不要弱化已有认证保护。 |

**交互评估。** 主要交互问题集中在“取消后到底发生了什么”“恢复入口何时出现”“一次填充能否一次撤销”和原连接身份是否被正确保留。它们比单纯样式统一更直接影响可预测性。命名工作区对话框的选择/名称控件主要使用 .Label，没有像连接表单那样的可见 Field；部分恢复说明仍写“原 MySQL 配置”，即使用于其他引擎。ClickHouse 的错误展示只保留代码，虽然减少敏感文字泄露，也降低语法/对象错误的可诊断性。建议完善可见字段、按引擎给出准确说明，并设计不泄露字面量的诊断内容。本轮未为这些显示细节采集新的原生截图，不把它们描述为已证实的裁切或 IME 故障。

**已有合理设计应保留。**

- shell 负责协调，sessionbrowser/SQL/data/object 工作区拥有各自状态；统一 dataworkspace 没有为 PG/SQLite 再造网格。
- dbruntime 拥有公开 generation，操作绑定原连接与命名空间；worker 使用私有缓冲通道、UI 消费回执，许多路径深拷贝请求。
- SQL 保留物理租约，断开后保留文本、显式恢复且不重放；数据提交不确定时保留草稿、禁止盲目重试。
- 数据读取有页面/字段/编辑边界，seek 在不能证明稳定性时拒绝或回退；参数与对象身份校验已集中在适配器。
- 普通配置与 OS vault 分离，SQL 资产/恢复/历史加密，文件输出有临时文件替换；AI 发送内容可预览，建议不自动执行。
- 原生工具栏、Outline、Tabs/Form、主题度量与共享图标的近期统一已经减少重复交互实现。

这些保护降低了跨连接误操作和迟到结果破坏现场的风险，但不能推导出所有协调入口都已遵守同一契约；A01/A02/A06 正好是宿主协调仍有遗漏的例子。

**验证与局限。**

- `GOWORK=off CGO_ENABLED=0 go test -json ./...`：exit 0；37 个有测试的包通过，顶层用例 1,274 PASS / 140 SKIP。计数不含子用例；集成与显式图像生成的 SKIP 不算通过。日志见 [tests.jsonl](../build/global-audit-20261106/tests.jsonl) 与 [test-summary.json](../build/global-audit-20261106/test-summary.json)。
- `CGO_ENABLED=0 go vet ./...`、`GOWORK=off CGO_ENABLED=0 go tool mygo vet`：exit 0。
- `GOWORK=off go tool mygo i18n check`：exit 0，1,903 条消息；它检查 catalog，不证明运行时所有文案都已翻译。
- `GOWORK=off CGO_ENABLED=0 go build -o build/global-audit-20261106/nyadb ./cmd/nyadb`：exit 0；本轮只编译，不发布安装包。
- `/usr/bin/python3 -m unittest discover -s tests -q`：64 项通过，有测试 fixture 的未关闭 SQLite handle ResourceWarning。PATH 中默认 Python 缺 Pillow，首次运行因依赖缺失失败；改用已装 Pillow 的系统 Python 通过，没有更改系统依赖。
- 7 个行为探针与 1 个填充分配探针复现上述行为，另有 ObserveWorkspace 微基准。探针测试的 PASS 表示“当前缺陷/成本已被复现”，不表示修复。首次 shell 探针引用不存在的 helper 导致编译失败，修正探针后 4 项 shell 探针通过；原日志保留，不把编译失败算产品问题。见 [probes.log](../build/global-audit-20261106/probes.log)、[shell-probes.log](../build/global-audit-20261106/shell-probes.log) 及三个 probe.txt。临时 *_test.go 在 finally 中移除。
- `GOWORK=off go test -race -timeout 20m -p 2`：dbruntime、engines、sessionbrowser、sqlworkspace、connectioneditor、navigation、dataworkspace、objectworkspace 八包完整用例通过，exit 0；dataworkspace 246.314 秒。另运行 shell 的工作区恢复/切换、标签操作、离开守卫与凭据删除相关已有用例，race 通过，15.727 秒。这是 shell 的定向检查，不是 shell 全包 race。日志见 [race.log](../build/global-audit-20261106/race.log) 和 [shell-race.log](../build/global-audit-20261106/shell-race.log)。

CI 目前运行跨平台 Go 测试、静态检查、编译和翻译检查，没有常规 race、数据库 fixture、Python 视觉工具或真实桌面交互作业。源码中不少 Tester 测试主动反复 Frame；这证明回执消费行为，不能单独证明原生窗口的后台唤醒、焦点或 DPI。建议增加周期性的 lifecycle race、四引擎关键路径 fixture，并把发布桌面验收与日常轻量 CI 分开。

**建议修复顺序与验收重点。**

1. 先修 A01/A02。分别验证保存失败、取消、活动/停放连接、未来版本与无效目标；任何失败都应保留原连接/当前文档。
2. 修 A03/A04。一次填充一次撤销；同一规模重新测主线程耗时、累计分配和峰值内存。未变化帧不应再随总 SQL 文本量线性增长。
3. 修 A05/A06/A07。完成四引擎凭据生命周期、原连接导航回调和取消加会话丢失的交叉检查。
4. 修 A08/A09，再处理同步 I/O、资产按需读取、总缓存预算与大表分析成本。保持现有 generation、取消确认、草稿保护和存储格式兼容。
5. 最后进行 840×560/1280×800、大小字号、中英文、深浅/高对比度，以及 Windows/macOS、IME、多显示器的原生交互验收。通过代码审查或历史图片不能代替这些结果。
