# 表数据操作栏复刻与验证记录（2026-10-30）

本轮按用户参考图及已留存的 Navicat 运行界面独立实现 NyaDB 表数据操作栏、底部编辑/历史面板和相关数据库操作。命令顺序遵循用户参考图（历史 → AI 自动填充 → 数据分析 → 导入 → 导出）；当前 Linux 原版“数据生成”位置不同，不能混为同一版本布局。原版截图只作为研究证据，保存在忽略目录；实现使用仓库原有 MyGo、主题图标和 MySQL 服务接口。本报告不将界面观察解释为原版内部实现，也不声明所有版本、文件格式或细节均已等价。

本报告的历史 I/V 日志仅对应当时源码。完整向导、附加格式、外部源及流式导入正在续作，当前状态以 [完整实施与验收清单](navicat-table-toolbar-completion-plan.md) 为准；未完成全部必需项前不宣布目标完成。

## 证据标记与边界

| 标记 | 含义 | 本报告的证据范围 |
| --- | --- | --- |
| O | 原版运行界面观察 | 下列本地截图及 `actions.jsonl`；只证明捕获时可见的入口、状态和内容 |
| S | 原版静态证据支持 | 本轮不依赖原版静态证据推导功能 |
| I | NyaDB 已独立实现并测试 | Go 测试、无窗口 UI 测试、隔离数据库或真实窗口，注明验证范围 |
| V | 原版与 NyaDB 差分验证通过 | 仅双方都实际执行且结果匹配的具体行为；测试通过本身不是 V |
| Unknown | 尚未建立证据 | 原版未观察的分支、其它类型/规模、严格 GUI 比较 |

原版证据目录：`temp/research/table-toolbar-20261030/`。截图编号作为本地证据 ID，操作序列保存在 `actions.jsonl`。研究文件不作为产品资源提交。原版精确版本、授权/AI 服务配置及未捕获的工作流不由截图文件名推断。

已复核的命名偏差：`08-cell-editor.png` 实际仍为配置管理界面；`15-analysis-result.png` 实际显示网格和历史，没有原版聚合统计结果。因此编辑器依据 O-11，分析面板依据 O-14/O-16；O-16 可确认本轮四行 fixture 的 NULL/非重复/唯一数；其它统计边界及完整双边数值差分保持 Unknown。

## 功能矩阵

| 功能 | O：原版可见行为 | I：本轮独立实现 | 验证 / Unknown |
| --- | --- | --- | --- |
| 操作栏 | O-04：表配置、事务、单元格编辑器、筛选排序、列、历史、分析、导入导出位于同一行 | 源码1：原生 `ui.Toolbar`，活动状态、分隔和窄窗口 overflow；使用 NyaDB 图标 | 有 headless 顺序/overflow 测试；I：1280×780 和 900×650 原生窗口入口/overflow 实际验证；未做严格像素 V |
| 表配置文件 | O-05：加载、保存、另存、关闭、管理；O-07：管理空态、搜索、删除/加载 | 源码2：按 profile/database/table 保存命名配置，包含列布局、已应用规则和页大小；保存/另存/关闭关联、搜索、加载、删除、重命名；应用走 `GuardLeave` | T1/T2：身份隔离、旧布局兼容、延迟应用深复制、写失败保留状态；原版文件格式 Unknown |
| 事务 | O-16/O-17：开始后按钮变为提交/回滚；O-18：回滚后的界面 | 源码8：限定身份的独立物理事务 token；表读取、完整行/字段读取和保存共用 token；批次使用 SAVEPOINT | T3：事务代理回传专项与真实 MySQL 验证；不据界面猜测原版连接池或事务隔离级别 |
| 单元格编辑器 | O-11：底部文本/JSON/十六进制/图像面板及展开/关闭 | 源码1：底部面板复用现有编辑草稿；JSON 格式化、二进制编辑/预览和 PNG/JPEG 预览；关闭保留已暂存更改 | 有 headless 编辑面板测试；I-79/I-80/I-81 实际编辑、关面板暂存与放弃；外部数据库值未改变 |
| 筛选与排序 | O-04：独立入口；O-14：分析可选择筛选范围 | 源码4：保留服务器端 AND 条件与优先排序，字段名经元数据验证、值参数绑定；刷新/换页有离开保护 | 现有规则/分页回归测试保留；原版全部谓词、组合规则 Unknown |
| 列 | O-13：左侧字段勾选列表与搜索 | 源码1/源码4：搜索和显隐面板，保留列顺序/宽度/类型/行号布局；布局按限定表身份持久化 | 有布局和网格映射测试；I-60/I-62 实际隐藏 amount 后加载配置恢复；不同 DPI/字体 Unknown |
| 历史 | O-12：底部查询/消息/时间戳，SELECT/INSERT/UPDATE/DELETE、错误与总数 | 源码3：文档会话内最多保留 50 条，统计独立于保留条数；参数化读取或写入摘要、类型/错误筛选；恢复读取规则不会自动执行 SQL | T1/T2覆盖基础读取、容量与恢复；CRUD/失败/取消/隐私/清空回归及 race 通过；I-71/I-75 实际显示 INSERT/UPDATE/DELETE |
| AI 自动填充 | O-04可见“数据生成”；所请求的 AI 服务配置和返回行为未观察 | 源码5/I：保留 AI 入口并明确“服务尚未配置”；另提供值、NULL、整数序列的本地填充，先验证整组选区再暂存 | 有选区验证测试；AI 推理、外部数据发送与服务集成均未实现 |
| 数据分析 | O-14：全部记录/添加筛选/开始分析；O-16：列头 NULL 比例、非重复/唯一数量（NULL 计为一个值组）及底部列分布 | 源码6/I：当前页、全部记录、已应用筛选；完整范围用服务端聚合，显示 NULL、不同值、只出现一次的值组、数值范围/均值/中值/总体标准差、零值、最高频率值与 top10 条形分布；本轮采用表内分析模式：工具栏下设置、随原生列表滚动的列头统计和底部列统计 | T1/T2/T5：精度、截断、范围、停放、取消和真实 SQL；V：本轮 fixture 的前四列 NULL/非重复/唯一计数匹配 O-16；内嵌布局已有 I-99–I-104 原生窗口验证；严格像素、其它语义边界和性能规模 Unknown |
| 导入 | O-20：源文件/目标表向导，界面列出多种格式；O-164/O-166/O-171：CSV 类型推断、字段编辑和实际建表；格式细节见 CSV/XML 专项 | 源码7/I：CSV/TSV/JSON/XML；既有表映射或新表字段定义审核、预览、校验、表名确认后插入新行；新表 DDL 与行事务分别报告；显式空字符串转换 NULL，默认关闭 | T4 和传输专项：解析/映射/取消/不确定结果回归；I-153/I-154 实际 XML 导入；I-186–I-194 原生新表成功/失败与外部 SQL 核验；Excel、Parquet、Access、ODBC 及其它导入模式未实现 |
| 导出 | O-21：全部记录/当前记录/取消选择；O-122：格式选择；O-128/O-135：两种 XML 格式实际导出 | 源码7/I：当前页、选区、完整筛选；CSV/TSV/JSON/XML/SQL；完整筛选流式读取，临时文件成功后替换目标文件 | T4 和 XML 专项：完整值、流式读取、原文件保留和取消；I-150 实际 XML 完整范围导出；HTML、Excel、DBF、Parquet 未实现 |

### 源码索引

- **源码1**：[toolbar.go](../internal/ui/dataworkspace/toolbar.go)、[toolbar_dock.go](../internal/ui/dataworkspace/toolbar_dock.go)、[view.go](../internal/ui/dataworkspace/view.go)：命令行、底部面板、列面板和原生网格组合。
- **源码2**：[toolbar_profiles.go](../internal/ui/dataworkspace/toolbar_profiles.go)、[layout.go](../internal/ui/dataworkspace/layout.go)：命名配置与兼容布局。
- **源码3**：[toolbar_history.go](../internal/ui/dataworkspace/toolbar_history.go)、[page.go](../internal/ui/dataworkspace/page.go)、[editing.go](../internal/ui/dataworkspace/editing.go)：请求摘要、读取/写入结果与历史。
- **源码4**：[rules.go](../internal/ui/dataworkspace/rules.go)、[layout.go](../internal/ui/dataworkspace/layout.go)：现有服务器规则和列状态。
- **源码5**：[toolbar_fill.go](../internal/ui/dataworkspace/toolbar_fill.go)：本地暂存填充。
- **源码6**：[toolbar_analysis.go](../internal/ui/dataworkspace/toolbar_analysis.go)、[toolbar_analysis_grid.go](../internal/ui/dataworkspace/toolbar_analysis_grid.go)、[mysql_data_analysis.go](../internal/services/mysql/mysql_data_analysis.go)：分析 UI、独立请求和服务端聚合。
- **源码7**：[toolbar_transfer.go](../internal/ui/dataworkspace/toolbar_transfer.go)、[toolbar_import.go](../internal/ui/dataworkspace/toolbar_import.go)、[toolbar_transfer_xml.go](../internal/ui/dataworkspace/toolbar_transfer_xml.go)、[mysql_data_import.go](../internal/services/mysql/mysql_data_import.go)、[mysql_data_export.go](../internal/services/mysql/mysql_data_export.go)：导入/导出、新表定义与建表、XML 和完整值流式读取。
- **源码8**：[toolbar_transaction.go](../internal/ui/dataworkspace/toolbar_transaction.go)、[mysql_data_transaction.go](../internal/services/mysql/mysql_data_transaction.go)：事务状态、物理 token 和数据库确认。

## 架构和生命周期

`dataworkspace.Document` 保持限定且稳定的 profile/database/table 身份；工具栏、配置、历史、分析、传输和事务状态均属于该模块。`Runtime` 注入服务方法，偏好读写继续通过 `SetDataLayoutPreferences` 注入。Shell 负责工作台与跨模块协调，不把存储或驱动依赖引入工具视图。

- 配置保存在独立 `mysql_data_profiles:` 键，保留既有 `mysql_data_layout:` 格式。最多 20 个配置、名称最多 80 字符，序列化结果限 256 KiB。仅持久化设置与筛选参数，不保存连接秘密、查询结果行或编辑草稿；写成功后才发布新列表。“关闭配置”清除当前关联，保留当前视图设置。
- 工作线程捕获规则、字段、游标、行身份、指纹和值的深复制。私有缓冲结果通道仅由 UI 线程消费；原 profile 未激活时结果停放，generation 不符时结束忙碌状态并保留旧缓存，不执行延迟重放。
- 分页、计数、字段读取、分析与传输具有独立取消范围。显式保存/事务/导入取消等待执行器确认；完整分析取消保持忙碌直到读请求回执。Close 撤销请求并释放缓存/回调，物理事务关闭回滚。
- 手动事务与导入/导出互斥。事务 token 不继承 SQL 标签的会话变量；失效身份、冲突或不确定提交不触发重连重放。保存批次失败回滚至 SAVEPOINT，保留此前未提交事务工作。
- 完整分析由数据库执行 COUNT、COUNT DISTINCT、MIN/MAX/AVG、零值、单次出现分组与 top10；中值只传回有序非 NULL 数据的中间一至两条的均值，LIMIT/OFFSET 参数绑定；整型/DECIMAL 在转换总体标准差前以精确 DECIMAL 基准居中，避免高基数近邻差异先丢失；最高频率值保留完整并列组数并仅传回有界标签。读取使用只读 RepeatableRead 事务；InnoDB 下各字段共享一致快照。完整范围读取已提交数据，不包含当前数据文档的未保存草稿或其它物理事务中的未提交值。

## 已知限制与实现选择

1. **AI**：用户已明确答复“暂不配置，先完成其他功能”；未配置真实 AI provider，不调用外部推理服务。本地填充只暂存编辑，单次限 200 个单元格，需要用户随后保存。
2. **导入**：CSV、TSV、JSON、XML，最多 200 行、输入文件最多 1 MiB，仅插入新行。CSV/TSV 首行为字段名，`\N` 表示 NULL，二进制用 `0x` 十六进制；JSON 为字段一致的对象数组。XML 使用字段一致的 `RECORDS/RECORD` 元素/属性记录，空元素默认是空文本；`xsi:nil` 和显式二进制编码保留 NULL/字节。空字符串转换 NULL 可显式启用，但不可恢复原版默认 XML 已丢失的 NULL/空字符串区别；原版新表推断和空数值转换见 XML 专项。
3. **完整值**：完整编辑限 1 MiB；独立字段信息读取限 1024 字符/字节。完整导出每字段限 1 MiB、每行限 16 MiB；超限失败并保留原文件。截断预览不能冒充完整导出值；缺失可验证行身份时拒绝补读。
4. **事务**：当前手动事务面向 InnoDB 基础表；事务期间禁用导入/导出。未知提交保留草稿并禁止重放，需明确核验或恢复原连接。
5. **分析**：当前页按完整预览文本去重，排除截断/缺失值；数值用有界有理数比较，均值保留 8 位小数。完整范围按字段 collation，二进制按完整字节、JSON 按 `utf8mb4_bin` 文本；显示的非重复/唯一包含 NULL 值组，单次 NULL 算唯一；数值统计仍排除 NULL。底层服务返回非 NULL 聚合值，由显示层补 NULL 值组。非 NULL 分布保留 top10，图表另列 NULL；服务端标签最多 128 字符/字节，标签截断不改变服务器统计。当前页本地快照可保留完整预览值，显示标签另外缩短。
6. **等价范围**：内嵌分析使用 NyaDB 原生组件独立组合；格式覆盖、配置持久化、取消和恢复是 NyaDB 的明确实现选择。截图不能证明原版的内部协议、完整 SQL、持久化格式、AI 行为或所有边界统计规则；已执行下文的十万行分析基准，没有原版性能差分。

## 已完成的验证与复现

以下专项属于 I 证据。测试编号 T1–T5 不表示原版差分 V。最终整包检查与真实窗口结果见下一节。

| ID | 验证及来源 | 已确认范围 |
| --- | --- | --- |
| T1 | 配置/历史/分析实现代理实际执行，专项测试通过 | 配置隔离/兼容/写失败/GuardLeave、基础历史容量/恢复、页分析精度/截断与分析请求生命周期 |
| T2 | 同一专项 `-race` 实际通过 | 深复制、取消确认、原 profile 停放、generation 失效和关闭释放；不替代最终整体 race |
| T3 | 事务实现代理回传：服务测试、专项与真实 MySQL IT 的 race 通过 | token 身份、共享读取/保存、SAVEPOINT、提交不确定、断开/关闭回滚、取消等待 |
| T4 | 传输实现代理回传：新专项、race 和两包 vet 通过 | 文本解析/映射、流式完整导出、文件原子替换、取消等待、停放及不确定导入；该代理没有连接真实数据库 |
| T5 | 分析实现代理实际运行隔离 MySQL IT，含 `-race`，通过 | 超过 2^53 的 DECIMAL 与 AVG 精度、NULL/空字符串、大小写 collation、长 Unicode 完整分组/128 预览、JSON/BLOB、完整筛选及真实在途取消 |

T1/T2 的专项命令：

```bash
go test ./internal/services/mysql ./internal/ui/dataworkspace \
  -run 'TestData(Profiles|History|Analysis)|TestMySQLDataAnalysis' -count=1
go test -race ./internal/services/mysql ./internal/ui/dataworkspace \
  -run 'TestData(Profiles|History|Analysis)|TestMySQLDataAnalysis' -count=1
```

真实 IT 必须显式启用，不能指向个人或生产数据库。本轮使用已有 disposable MySQL 容器的 loopback `127.0.0.1:32790`，专用 schema `nyadb_toolbar_tx_it`；测试账户只对该 schema 有权限。端口仍需调用者明确指定，可选密码从环境读取，不写入配置、报告或源码。

```bash
NYADB_MYSQL_ANALYSIS_IT=1 NYADB_MYSQL_ANALYSIS_PORT=32790 \
  go test -race ./internal/services/mysql \
  -run '^TestMySQLDataAnalysisIntegration$' -count=1 -v

NYADB_MYSQL_DATA_TX_IT=1 NYADB_MYSQL_DATA_TX_PORT=32790 \
  go test -race ./internal/services/mysql \
  -run '^TestMySQLDataDocumentTransactionIntegration$' -count=1 -v
```

分析 IT 只创建随机 `analysis_probe_*` 表并在退出时 DROP；事务 IT 使用自己的随机 `document_tx_*` 表。分析取消测试仅对自己的新表加写锁，通过 PROCESSLIST 确认查询等待后取消，验证驱动回执，随后解锁并删除临时表。原版观察使用的旧表没有被这些 IT 改动。

GUI 可按仓库常规命令 `go run ./cmd/nyadb` 在桌面会话启动。进入隔离连接的表数据页，检查操作栏 overflow、配置重开、底部编辑/历史、列显隐、事务提交/回滚、分析三个范围与导入/导出。避免将真实验证等同于截图中仅有的静态状态。

## 最终验证与真实窗口

环境为 Go 1.27.3、仓库固定的 MyGo v0.6.4-nyadb.4、Linux X11/Xvfb 1600×960。没有修改 `go.mod` 或升级 UI 框架。所有日志和截图在本轮忽略目录，`manifest.json` 绑定截图/文件和二进制 SHA-256。

| 检查 | 实际结果与本地日志 |
| --- | --- |
| `go test ./...` | PASS，最终冻结后 `go-test-final-stable.log`；dataworkspace 24.800s、shell 53.480s |
| AGENTS.md 所列 12 个相关包完整 `go test -race` | PASS，`go-race-final-stable.log`；dataworkspace 322.657s、shell 280.698s |
| 历史/填充/分析最新专项 race | PASS，`go-race-toolbar-final.log` |
| `go vet ./...` | PASS，`go-vet-final.log` |
| `go tool mygo vet` | PASS，`mygo-vet-final.log` |
| `go tool mygo build` | PASS，`mygo-build-final.log`；生成 `build/linux-amd64/nyadb`、deb 和 tar.gz |
| `go tool mygo generate` / `go tool mygo i18n check` | PASS，两个语言 746 条消息；`i18n-final.log` |
| 最终 MySQL 事务及分析 IT，含 race | PASS，`mysql-it-final.log`；仅专用临时表 |
| Python 视觉证据工具测试 | `/usr/bin/python3 -m unittest discover -s tests -v` PASS，33 项；`python-tests-final-stable.log`；这是工具单测，不是本轮像素差分 |
| `git diff --check` | PASS；未 commit、push 或改写历史 |

早期完整 race 与代理最后修改测试文件发生交叠，曾抓到临时 `ClickLabel` 编译错误；冻结后重新跑上述完整 race 已通过。默认 `python3` 是没有 Pillow 的 Conda Python，最初失败；复用已有系统 Python/Pillow 10.2.0 后通过，没有安装或修改全局环境。

### 原生 GUI 操作证据（I）

实际窗口连接仅指向本轮 disposable `127.0.0.1:32790` 的 `table_edit_it.toolbar_probe_20261030`，不用生产数据库。GUI 读写没有碰同 schema 的旧研究表。

| 证据 | 实际执行与核验 |
| --- | --- |
| `58-nyadb-final-toolbar-wide.png` | 1280 左右宽度显示完整目标命令顺序、原生图标及后置 overflow |
| `59`–`62` PNG | 保存 `toolbar_default`，隐藏 amount，再加载配置；amount 列恢复，无数据库写入 |
| `63`–`66` PNG、`gui-export.csv` | 原生保存对话框导出完整结果 4 行；文件头、中文、NULL 与空字符串已解析核验 |
| `67`–`69` PNG、`69-external-import.tsv` | 文件选择、自动字段映射、预览、输入表名确认并导入 1 行；外部 SQL 确认 note/DECIMAL/中文详情 |
| `70`–`71` PNG、`71-external-cleanup.tsv` | 通过网格删除刚导入的第 5 行；恢复原始四行，历史显示 INSERT 与 DELETE 请求概要 |
| `72`–`74` PNG | 当前页与全部记录分析实际执行；全部范围回执为 4 行；前四列的 NULL/非重复/唯一计数与原版 O-16 匹配 |
| `75`–`76` PNG/TSV | 最终二进制开始事务、修改 note、应用、历史显示未提交 UPDATE；外部仍为 alpha；回滚后网格及外部仍为 alpha |
| `77`–`78` PNG | 900×650 窗口工具栏保持单行；导入、导出、字段信息、刷新、复制及复制为进入 overflow |
| `79`–`81` PNG、`80-external-dock.tsv` | 实际底部编辑器修改文本，关闭后保留草稿；外部仍为 alpha；随后放弃草稿，网格恢复 alpha |

上述截图编号不改变其归属：NyaDB 的图像是 I，Navicat 的图像是 O；只有本节明确列出的双方结果匹配标为 V。

启动环境限制：本轮 Xvfb 下应用首次出现黑帧，需通过 `xdotool windowmap` 映射 10×10 辅助窗口和主窗口后才能绘制。`54-nyadb-final-start.png` 留存黑帧，`55-nyadb-final-mapped.png` 留存恢复状态。功能验证是在实际原生窗口中完成，但不能据此声称常规桌面首次启动已验证通过。未修改 UI 框架或其它项目来掩盖该限制。

### 原版与 NyaDB 的有限差分（V）

原版复用用户授权的 Navicat 18 运行实例，未修改原版程序、授权或专有资源。`49`–`53` PNG/TSV 实际执行了开始事务、编辑、回滚、待处理记录确认及提交。外部读取确认回滚没有发布草稿，最终提交后 note 为 alpha。NyaDB 的 `41`–`45` 和最终 `75`–`76` 同样确认提交/回滚结果与未提交更改的外部可见性。只对这些具体结果标 V，不推断 Navicat 的连接池或内部事务实现。

O-16 与 I-74 的原始四行 fixture 中，前四列计数如下；这些计数标 V。本次 O-106/O-107 与 I-108/I-109 进一步确认 amount 的最小值 0.00、最大值 25.00、平均值 12.5 数值一致；小数显示精度不同。其它字段、排序规则、分布边界和更大数据集没有建立完整双边差分。

| 字段 | NULL | 非重复（含 NULL 值组） | 唯一（只出现一次，含单次 NULL） |
| --- | ---: | ---: | ---: |
| id | 0 | 4 | 4 |
| note | 1 | 4 | 4 |
| amount | 1 | 4 | 4 |
| state | 0 | 2 | 0 |

没有声明严格像素 V。原版采用内嵌分析设置、列头统计与底部列统计，NyaDB 已实现相同的内嵌分区结构；原生组件的尺寸、字体和细节仍有差异。导入/导出使用简化原生向导。两侧字体、DPI、窗口主题和操作阶段没有形成严格同场景的完整匹配元数据。未观察的授权/AI 分支没有模拟成原版事实。

### 本轮审计修复

- 填充采用可见视觉列序，支持重排列后的矩形与整行选择；不填充隐藏列。空间字段和非 NULL 二进制填充先整组选区拒绝，防止部分暂存或把十六进制预览误当字节。
- 显示编辑器只在单单元格选区自动建立编辑，保留多行/矩形选区，避免 View 重绘破坏复制与填充。
- 不确定导入保留禁止重放状态，但提供“外部核验后关闭表”出口；取消则保留原状态，不再永久阻止退出。
- 原生 Toolbar overflow 请求早先按钮菜单时，在本帧结束前再次接入菜单构建，保证复制格式、配置与填充下拉可用；历史清空后验证选择索引，防止访问已清空列表。

这些修复均有对应 `ui.NewTester` 或 worker/状态回归测试，已纳入最终 test/race。

### 内嵌分析补齐（本次续作）

- **O-83/O-84**：点击分析先显示工具栏下设置，默认“全部记录”；开始后数据网格仍可见，列头下显示 NULL 比例、分布、非重复和唯一计数。
- **O-86**：分析模式中 F2 仍显示所选单元格的编辑控件；没有输入或保存测试值。**O-87**：右上切换底部“列统计”，其内容跟随选中的 note 列。
- **I-99/I-100**：NyaDB 默认先进入设置；完整异步分析后，各列统计在原数据表中展开，底部 note 的计数为 4、NULL 1（25%）、非 NULL 3（75%）、非重复 4、唯一 4。
- **I-101/I-102/I-103/I-104**：列统计打开时 F2 显示 beta 的行内编辑器；原生列宽拖动、横向滚动后统计仍对应各标题；退出保留第二条记录和 note 列选择。外部 SQL 的 `analysis-final-fixture.tsv` 确认原四行没有变化。
- **O-105/O-106/O-107**：amount 列还可滚动查看零值计数、最高频率值、最小/最大/平均/中值/标准差。此样本可见最小 0.00、最大 25.00、平均 12.5、中值 12.5、标准差 10.21；这些已在本次续作独立实现；原版此样本符合排除 NULL 的总体标准差，不能据此推断原版私有计算代码或所有类型边界。
- 非选择统计区使用原生 `ListState.Header`；真正记录始终以页内行号为选择 key，只有原生显示游标加一。所有复制、编辑、行身份、字段信息保持真实行索引；滚动定位显式转换。没有修改 MyGo 模块缓存或另一个仓库。
- `ui.NewTester` 覆盖默认设置、开始/退出、Header/记录游标映射、Home 跳过统计区、复制、F2、列统计下行内编辑、重排/列宽、共享横向滚动和选区恢复。原有异步取消确认、停放、generation、Close 测试继续保留。
- `analysis-inline-comparison.png` 对 O-87/I-100 使用原尺寸裁剪并列审阅，没有缩放，也没有声明严格像素 V。早期 I-72/I-74 是旧独立面板证据，不作为当前布局截图。

内嵌分析冻结版本的 `go test ./...`、`go vet ./...`、`go tool mygo vet`、`go tool mygo build` 均通过；翻译生成/检查通过（754 条，zh-Hans/en）。AGENTS 列出的 12 个相关包 `go test -race` 全部通过（dataworkspace 330.199s、shell 284.760s）；`git diff --check` 通过。日志为 `go-test-analysis-inline-final.log`、`go-race-analysis-inline-final.log`、`go-vet-analysis-inline-final.log`、`mygo-vet-analysis-inline-final.log`、`mygo-build-analysis-inline-final.log`；这些日志只证明该历史版本，当前数值统计/XML 验证见后续小节。

### 数值统计与规模验证（本次续作）

- **I-110/I-111** 对照 **O-106/O-107**：amount 的零值 1、中值 12.5、总体标准差 10.21、最高频率值 0.00/12.50/25.00/NULL 与原版四行样本匹配，标记为该样本的 V；均值/中值小数显示精度仍不同。原四行数据在 `statistics-fixture-confirmed.tsv` 再次核验没有改动。
- 当前页使用有理数计算排序、中间值、均值和总体方差，再用 256 位 `big.Float` 计算方差平方根。缺失/截断/无效数值不进入数值统计；单个数值的标准差为 0，全 NULL/空表显示无数值结果。NULL、字符串 `NULL` 和空字符串的最高频率组分别保留；超过标签限额时显示未列出的并列组数。
- `statistics-mysql-it.log`、`statistics-services-final.log`：真实 MySQL（含 race）覆盖 2^53 附近相差 1 的 DECIMAL 中值和 0.5 标准差、奇偶中间行、原四行数值、全 NULL/空筛选、29 个并列最高频率组以及在途取消。只修改测试创建的随机表，随后删除。
- `TestDataAnalysisLargePageVirtualizesRecordsAndRevealsLastRow` 用最大 200 行加载页验证分析头未绕过原生行虚拟化，并能定位最后一行、保留真实记录 key；这是 headless I，不是十万行 GUI 或原版性能 V。
- `TestMySQLDataAnalysisLargeTableIntegration` 在独立随机 InnoDB 表生成 100000 行。一次 3 列完整分析验证 100000 记录、中值 0.5、零值 1、完整最高频率并列组数（数值 100000/text 100）、最多 10 个传输标签；分页返回 100 行并保留 hasMore/稳定顺序。
- 本机实际 race 测量（`statistics-services-final.log`）：完整分析 wall **1.454621305s**，服务内部 **1.448840218s**，客户端 `TotalAlloc` 增量 **59248 B**，该次 `HeapAlloc` 增量 **59248 B**；100 行分页 **32.562278ms**。这些是该机器、MySQL 容器、3 列数据和该次进程的测量，不是峰值 RSS、128 列门限、通用时延保证或原版差分性能。

复现规模测试：

```bash
NYADB_MYSQL_ANALYSIS_LARGE_IT=1 NYADB_MYSQL_ANALYSIS_PORT=32790 \
  go test ./internal/services/mysql \
  -run '^TestMySQLDataAnalysisLargeTableIntegration$' -count=1 -v
```

### XML 与导入空值选项（本次续作）

原版观察、元素/属性文件、新表导入和外部 SQL 记录见 [XML 专项](navicat-table-toolbar-xml-2026-10-30.md)。**O-122** 才是导出格式列表；`120-original-export-formats.png` 是表页，`121-original-export-formats.png` 是范围确认，`129-original-import-formats.png` 是表页，`130-original-import-xml-type.png` 是带连接错误的源文件页。不得仅凭这些文件名声称对应步骤成功。

- **I**：XML 支持 `RECORDS/RECORD` 元素记录和字段齐全的属性记录，按字段名重新排列；普通空元素为长度零的文本，按 namespace URI 识别 `xsi:nil`。只有显式 `encoding=base64/hex` 解码二进制，二进制值仅可映射二进制字段。非法 XML 字符拒绝导出，特殊字段名用可逆的 `FIELD/name` 保留。
- **I**：导入默认关闭“使用 NULL 取代空字符串”。启用只转换长度零的文本，不转换空二进制或空格；验证、预览、提交使用同一选项，选择不修改源记录，worker 持有已校验的深复制。NOT NULL/数值/映射校验继续执行。预览现在显示显式 XML 二进制的有界十六进制字节。
- **I-147/I-150**：`nyadb-statistics-xml` 原生窗口实际选择 XML 和完整范围，导出 `xml-nyadb-four-rows.xml` 4 行，保留 `12.50`、中文、NULL 与空串差别。`xml-export-file-validation.json` 用 XML 解析核验文件；原版默认文件 `12.5` 且 NULL/空串均为空元素，不能声明无损互通 V。
- **I-153/I-154/I-156**：原生文件选择、映射预览、表名确认、导入一条新行；外部 `xml-native-import-verified.tsv` 确认 amount 为 NULL、details 为非 NULL 空串。随后通过 GUI 删除新增 id=5，`xml-fixture-final.tsv` 与初始四行一致。
- **I，最终选项版本**：`xml-options-source-empty-default.png` 用原版 XML 显示数值空文本校验错误；`xml-options-source-empty-null.png` 勾选后预览为 NULL 且通过校验；`xml-options-source-empty-restored.png` 关闭后恢复原值/校验错误。没有提交这四条重复主键记录。
- **I，最终选项版本**：`xml-options-one-row-default.png`/`xml-options-one-row-null-preview.png` 在同一独立单行文件中明确显示 details 从空串变为 NULL；`xml-options-import-verified.tsv` 确认实际 id=6 的 amount/details 为 NULL。通过 GUI 删除该行，`xml-options-fixture-final.tsv` 与初始四行逐字节一致。
- **I-158/I-159**：同一合并版本的完整分析显示 4 行、4 个 amount 值组、零值 1、中值 12.5、标准差 10.21，NULL 图表标签为 `(NULL)`。这些仍只是四行样本的有限数值 V；XML 和分析布局没有严格像素 V。

原版默认 XML 自动建表导入将空 amount 写入 double 的 0，将空 note/details 写为空串。该路径勾选空白替换 NULL 后仍是同一结果（见专项外部 SQL）。本小节验证的历史 NyaDB 版本仅按既有表元数据校验；当前新表推断、字段审核与实际建表已在下节补齐。NyaDB 不将无效空数值自动变成 0，显式 NULL 选项仍具有上文独立转换语义；这些转换差异未通过 V。

本次进程复核确认旧桌面和数据库容器已不存在后，新建仅 loopback 暴露的 `nyadb-toolbar-xml-20261030`（MySQL 8.4.11）及独立 Xvfb 会话。原版使用 `:184`，NyaDB 使用 `:212`。原版两次写入只针对新建的 `toolbar_xml_original_probe_20261030`，随后删除；NyaDB 只插入并删除自己的临时记录，现有四行保持不变。未修改原版文件、授权或其它项目。本次窗口在 Openbox 下正常绘制，无需手动 windowmap；仍不据此宣称常规物理桌面启动已验证。

本次真实 MySQL 分析/race (`statistics-xml-services-it.log`) 在新容器重新验证所有数值边界及 100000 行测试：3 列完整分析 wall **1.495666829s**、service **1.462306357s**，客户端 TotalAlloc/HeapAlloc 当次增量各 **62360 B**，100 行分页 **53.804762ms**。范围和限制与前文相同，不是峰值内存或原版性能 V。MySQL 服务源码随后没有变更，后加的空值选项测试属于 UI 模块。

当前数值统计/XML/空值选项的合并版本验证：

| 检查 | 实际结果 / 日志 |
| --- | --- |
| `go test ./...` | PASS，`go-test-xml-options.log`；dataworkspace 23.220s、shell 18.428s |
| 12 个 AGENTS 相关包 `go test -race` | PASS，`go-race-xml-options.log`；dataworkspace 323.892s、shell 233.110s，未变包复用匹配缓存 |
| `go vet ./...` / `go tool mygo vet` | PASS，`go-vet-xml-options.log` / `mygo-vet-xml-options.log` |
| `go tool mygo build` | PASS，`mygo-build-xml-options.log`，Linux 原生包 |
| 翻译生成 / i18n check | PASS，765 条 zh-Hans/en；`xml-options-generate.log` / `i18n-xml-options.log` |
| XML/选项专项（含 `ui.NewTester`） | PASS，`xml-options-focused.log`；整包/race 同时覆盖，源数据/开关/worker 隔离和二进制预览 |
| Python 视觉工具自身测试 | 系统 `/usr/bin/python3` PASS，33 个测试，`python-tests-statistics-xml-system.log`；默认 conda Python 缺 Pillow 的失败另存 `python-tests-statistics-xml.log`，未安装或修改系统依赖；这些测试不是 GUI 严格像素差分 |
| `git diff --check` | PASS |

中间 `go-*-statistics-xml.log` 等验证的是加入空值选项之前的合并版本，不替代当前日志。没有在上述检查之后改动 Go 或翻译源码。

### 证据完整性

原版运行研究遵循 [reverse-engineer-anything 技能](/root/.agents/skills/reverse-engineer-anything/SKILL.md)。REA MCP 当前不可用，复用已建立的 X11 操作与截图证据，没有通过缺失工具推导原版私有实现。

| 文件 | SHA-256 |
| --- | --- |
| 原版 AppImage | `3b77fe1e3295dc3067402a0bab33a8ed1cbfbdea063d8b59e67c26a6480b6d63` |
| 历史内嵌分析 GUI 二进制 `temp/research/table-toolbar-20261030/nyadb-inline-final` | `983f9c0c2f855da0c9ee3a712ebc76caeb31a53769d0ac136e686040ade891c4` |
| 历史内嵌分析打包版本（原 `build/linux-amd64/nyadb`，后续构建已替换） | `d8ff87f1ffe4b836483d21fff5bc0eec18956e661ce28af720c6b9cf7a4af4df` |
| 历史 XML 选项 GUI 二进制 `temp/research/table-toolbar-20261030/nyadb-xml-options` | `4395af0d75a14b334918a7557627385cea3d1c1440695534256fd91dc28c12df` |
| 历史 XML 选项打包二进制（原 `build/linux-amd64/nyadb`，后续构建已替换） | `ba98da51f40e1b8183e2f2258aa2efdcc763c76272e64b6053215f5bdc6f48f5` |
| I-58 宽窗口截图 | `6b94cf46d172ace26d2e6554d7b1335312871eb83912ea0bcb22851e08f9cd61` |
| I-74 完整分析截图 | `9dd7dc769d536b403607e3b5b0486eb95332515df17b7cf19dcafc329720dc21` |
| I-78 窄窗口菜单截图 | `3c18646c43064da35305a59bdf9bcd1fb3561fe37f5f81ddb4a7d8564fe4c7f8` |

两种二进制分别由 `go build ./cmd/nyadb` 和 MyGo 根入口/打包工具生成，来源入口及打包元数据不同，不能只凭不同 SHA 推断源码变化。

### 后续验收项

核心非 AI 工具栏入口和常见数据操作已独立实现、测试并在原生窗口操作；新表导入已补齐。完整复刻目标仍有差异：更多导入/导出格式、既有表更新等导入模式、逐行错误继续和更大文件能力、其它列统计类型/边界、更完整的原版交互/数值差分、原版性能差分和常规桌面首次启动验证。AI 按用户答复暂不配置。Goal 保持 active，不能用本轮测试绿色代替这些待验收项。

### 新表导入、字段审核与空二进制（本次续作）

原版 CSV 观察和真实建表结果见 [CSV 专项](navicat-table-toolbar-import-2026-10-30.md)。O-164/O-166 确认类型、目标列名、主键可修改，O-171 确认三行实际导入。原版映射页比例 2 与实际 `DECIMAL(10,0)` 不一致，12.50 实际写成 13；本实现没有照搬这一精度损失。其它导入模式和“遇到错误时继续”只确认了可见入口，原版错误实验止于建表冲突，不能宣称已验证逐行失败行为。

**I：实现。** “目标表”可选当前表或新建目标表。新表从全部有界输入提出可编辑类型建议，支持字段重命名、忽略、普通 MySQL 类型、可空和复合主键；主键勾选后不可空。整数和 DECIMAL 使用精确解析，无法保持精度的建议退回文本；CSV 的 `0x` 文本保留为文本建议，可显式改为二进制类型。既有表继续按元数据映射，生成列不可写。

新表名须再次输入确认。所有值在 DDL 前完成整批类型/长度/范围校验，拒绝表达式及任意类型 SQL；限定数据库/表/字段标识符独立转义。服务固定原 managed metadata 连接，创建 InnoDB 表后在独立行事务内提交，临时严格 SQL 模式在退出时恢复。MySQL DDL 无法随行事务回滚，因此结果区分建表成功、建表不确定、行事务失败和提交不确定；失败后保留已经创建的表，已提交向导不重放。

新表完成事件携带原 profile/generation/database/table。Shell 只在该会话仍有效时刷新对象缓存；“打开目标表”新建或聚焦目标文档，源数据文档身份和缓存保持独立。失效、停放、取消和关闭沿用私有结果通道和等待执行器回执的现有机制。

真实 IT 暴露了 `Binary=true, Bytes=nil` 被驱动解释为 SQL NULL 的问题。本轮统一将这种值作为非 nil 空字节绑定，并在 INSERT/UPDATE 生成的二进制行身份中保留空值；显式 `Null=true` 仍单独绑定 NULL。专项实际验证空二进制主键的插入、更新和删除。

**I：原生窗口。** 使用新编译的 `nyadb-import-new`，GTK/MyGo 实际原生窗口运行于已有 Xvfb `:212`，复用隔离配置和 MySQL 8.4.11 容器，不等于常规桌面首次启动或严格像素差分。GUI 写入仅限此前确认不存在的自有 `table_edit_it.toolbar_import_nyadb_probe_20261030`。

| 证据 | 实际确认的行为 |
| --- | --- |
| I-180–I-185 | 启动、连接、打开源表、导入入口、新目标选择及名称输入 |
| I-186–I-188 | 三行类型建议，重命名 title→label，设置 BIGINT 主键和 LONGBLOB；`DECIMAL(3,0)` 被拒绝，恢复 `DECIMAL(20,4)` 后审核通过 |
| I-189 / `import-nyadb-created-table.tsv` | 完成提示、对象树刷新及真实 DDL；`9007199254740993.2500`、中文、微秒时间、`00FF80`、空 BLOB、空文本、显式 NULL 全部保留 |
| I-190–I-191 | 从完成页打开目标表，源表标签仍存在；关闭目标文档后清理成功样本 |
| I-192–I-194 / `import-nyadb-rows-failed.tsv` | 同一自有表名用重复主键 CSV 测试失败；已创建表保留，行数为 0，错误说明显示且提交按钮禁用；可打开空目标表 |
| I-195 / `import-nyadb-final-cleanup.tsv` | 最终删除自有目标、刷新树、恢复源表，源四行保持中文/NULL/空串原值 |

上述新表案例是独立实现 I。双方输入、日期选项和精度行为不同，不能据三行均导入就声明完整新表 V。原版字段比例可靠提交、其它模式的实际匹配和逐行继续语义仍为 Unknown。

源码与无窗口测试见 `toolbar_import_test.go`、`mysql_data_import_test.go`、`mysql_data_import_integration_test.go` 和 `mysql_data_import_test.go`（shell）。覆盖字段/类型审核、工作线程的深复制、完成事件身份、停放及失效 generation、提交不确定禁止重放、取消等待、既有目标拒绝覆盖、行失败后保留空表及会话设置恢复。

```bash
NYADB_MYSQL_IMPORT_IT=1 NYADB_MYSQL_IMPORT_PORT=32790 \
  GOCACHE=/data/.cache/golang go test -race ./internal/services/mysql \
  -run 'TestMySQLDataImport|TestMySQLDataEditBinary' -count=1 -v
```

该命令只使用专用 `nyadb_toolbar_tx_it` schema 的随机 `import_probe_*` 表和已有 loopback fixture，测试表退出时删除。GUI 使用的目标也已删除，未修改原版文件、授权、其它项目或 Git 历史。最终源码检查及打包结果见下方验证记录。

#### 新表导入冻结版本验证

使用 `/usr/local/go` 的 Go 1.27.3、仓库固定的 MyGo v0.6.4-nyadb.4 和 `/data/.cache/golang`。本节日志统一位于 `temp/research/table-toolbar-20261030/`，区别于前面历史版本的通过记录。

| 检查 | 结果与日志 |
| --- | --- |
| `go test ./...` | PASS，`go-test-import-new-resume.log`；dataworkspace 23.296s、shell 19.271s |
| AGENTS 所列 12 包完整 `go test -race` | PASS，`go-race-import-new-resume.log`；dataworkspace 342.977s、shell 235.671s |
| `go vet ./...` | PASS，`go-vet-import-new-resume.log` |
| `go tool mygo vet` | PASS，`mygo-vet-import-new-resume.log` |
| `go tool mygo generate` / `go tool mygo i18n check` | PASS，`import-new-generate-resume.log` / `i18n-import-new-resume.log`，zh-Hans/en 782 条 |
| `go tool mygo build` | PASS，`mygo-build-import-new-resume.log`；生成 Linux 原生二进制、deb 和 tar.gz |
| 新表与通用空二进制真实 MySQL IT，含 race | PASS，`import-new-resume-it.log`；SQL 模式恢复、无主键表、空二进制身份及真实事务回滚 |
| 新表无窗口 UI 和 Shell 协调专项 | PASS，`import-new-resume-ui.log`；完成事件校验、源/目标文档独立及不重复开标签 |
| Python 视觉工具自身测试 | 系统 `/usr/bin/python3` PASS，33 个测试，`python-tests-import-new-resume.log`；不等于严格像素 V |
| `git diff --check` | PASS；未提交或推送 |

变更源码/翻译的 63 个文件 SHA-256 清单保存在 `import-new-source-sha256.json`，完整 race 结束后逐个核对匹配。`import-new-final-test-cleanup.tsv` 确认服务测试随机表与 GUI 自有目标的残留数量均为 0。当前原生 GUI 二进制 `nyadb-import-new` 的 SHA-256 为 `a512d4853cabd2eaf2754373e5727168073d9f4579350c744382972b89975c04`；当前打包二进制 `build/linux-amd64/nyadb` 为 `e2eb140a4dbd9e915c1a626afa88bb0002c0dbdd13ddb15e2fd584294ec5fbc7`。研究用命令入口与打包用根入口不同，不能仅凭二进制不同 SHA 断言源码不同。
