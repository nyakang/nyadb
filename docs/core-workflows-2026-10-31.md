# 核心工作流实现与验收 — 2026-10-31

基线：Go 1.27.3、MyGo v0.7.3-nyadb.2。本验收记录在提交前的工作树上生成，后续提交与推送状态以 Git 历史和远端为准。Navicat 程序及解包文件未运行、未修改；集成测试只使用本次创建的临时数据库容器和 SQLite 文件。

## 交付范围

| 阶段 | 独立实现 | 验证边界 |
|---|---|---|
| 1 SQL 工作流 | 类型化值、多结果预览、选区复制、CSV/TSV/JSON、显式运行并完整导出 ZIP；文件/命名查询保存、修改标记、加密草稿及断开恢复；缓存补全、可撤销格式化、确认 EXPLAIN | Go/Tester、真实 MySQL 8.0/8.4；取消、密钥丢失、认证失败、旧修订与关闭守卫回归 |
| 2 MySQL 表设计 | 完整 SHOW CREATE 基线；独立草稿、字段/索引/约束/生成列/触发器/选项编辑、合并 ALTER、SQL 预览确认、代际与指纹预检、逐项执行与后验 | 真实建表、修改、触发器、冲突与失败；未支持属性保留或阻止修改 |
| 3 MySQL 对象及入口 | 数据库、视图、函数、过程、事件 CRUD；公开 DDL 复制表；F2 行内重命名、多选操作、分组、版本化剪贴板；维护、账户/角色/权限；数据导入导出/配置菜单及空库新表导入；自动连接、压缩、字符集、自定义库列表和加密初始 SQL | 真实对象/复制/账户操作、专用 Charset 选项和物理连接初始化；原目标身份与权限拒绝回归 |
| 4 传输 | 统一可取消 DialContext；SSH 密码/密钥/组合认证、首次 SHA-256 信任与变化拒绝；HTTPS/WebSocket 网关、目标 ID allowlist、认证头、独立 TLS/代理；通道失效撤销会话与租约 | TLS/SSH/代理/并发/取消测试；真实 MySQL 经网关及 SSH 保持临时表、变量与事务，断线不重放 |
| 5 多引擎 | 无驱动公共契约；固定 pgx 与独立 modernc 用户库句柄；PostgreSQL catalog/schema 与 SQLite main；真实元数据、结构、SQL、多结果、类型化网格分页/筛选/排序/编辑、显式事务/核验、复制/流式导出 | PostgreSQL 17 与临时 SQLite：复合键、精确值、并发冲突、锁等待、提交/回滚；Shell SQLite 接线 Tester |
| 6 备份与脚本 | 一致性快照读取、逐对象完整 DDL、类型化 JSONL、SHA-256 清单与原子发布；目标/停止或替换策略预览、结构依赖和外键延后、流式恢复、逐步结果；SQL 转储与 DELIMITER 词法文件执行 | 类型保真往返、大字段、BIT/空间值、外部外键和触发器冲突、校验失败、取消与不重放回归 |

这是实现证据 **I**。新功能的原版运行观察 **O** 和原版差分 **V** 均为 **Unknown**。过去的局部观察/对照不能证明这次整应用或新功能已经通过 Navicat 差分。

## 架构与所有权

`internal/db/contract` 不依赖 UI 或数据库驱动，定义 ObjectRef、Cell、Column、结果、分页、编辑、会话和事务能力。对象身份包含 profile、catalog、schema、类型及名称。`sqlresult` 负责 detached 值与预览/流式扫描；`sqltext` 负责词法分析，不按字符串分号拆 SQL。MySQL 保留旧入口，通过契约结果兼容视图适配。

`sqlworkspace` 持有 SQL 文本/修订/编辑状态与原物理租约；查询仓库、密钥、文件操作均注入。`objectworkspace` 持有完整定义、设计草稿、对象操作和备份预览；`dataworkspace` 持有 MySQL 数据页及公共引擎网格；`sessionbrowser` 持有原 profile 工作区、缓存和 UI 代际。Shell 只协调页签、原目标刷新、离开/事务守卫及原生窗口。各引擎的物理代际与 UI 工作区代际分别保存，不假设它们相等。

后台任务捕获值或深复制请求，使用私有缓冲通道交付。UI 线程消费回执。取消等待执行器完成，断线不悄悄新建连接继续写入。关闭撤销请求/回调并释放租约、文件及缓存。

## 持久化与恢复

localdb schema 从 v8 升至 v9，新增 `sql_documents` 密文记录，迁移兼容新库及旧库。SQL 文档通过 AES-256-GCM、随机 nonce 加密，身份、profile、格式与修订参与 AAD；工作区主密钥使用独立系统密钥库服务，不改变已有数据库密码账户键。

500 ms 防抖保存不可变快照。旧保存回执不能清除新输入修改标记；退出保留草稿等待最新修订。系统密钥库不可用时继续内存编辑和显式文件保存。已有密文缺钥或认证失败时锁住覆盖路径，保留原记录，禁止生成替代密钥覆盖旧文档。

恢复文档顺序、活动页、选择及光标；恢复页保持断开。用户明确连接原配置后才可使用新会话继续。事务、临时表和 SQL 不会恢复或重放。

## 结构与对象保存

保存顺序为校验、生成预览、用户确认、原会话代际/指纹核对、按顺序执行、重新读取验证。未知表定义和选项不从有限元数据重造；无法保真时阻止相关修改。触发器完整定义、SQL mode 及执行顺序属于快照。函数/过程替换是明确的 DROP+CREATE 两步，后一步失败不会宣称先前 DDL 已回滚。

非表对象重读后比较完整定义、正文、注释、字面值、DEFINER、程序 SQL mode、数据库默认选项及事件状态。`SQLModeKnown` 区分未知模式和明确空模式。DDL 已执行但语义不符或无法解释服务器改写时保留 `executed` 和 `Verified=false`、实际定义及草稿；不把存在性检查当作保存验证。

DDL 步骤分别记录未执行、已执行、部分完成和结果不确定。原身份完成事件刷新原缓存；Shell 在执行前协调相关数据草稿和事务。指纹只能发现预检前已经发生的修改，不能消除预检与执行之间的外部 DDL 竞态。

## 备份、恢复与 SQL 文件

备份包是 NyaDB 自有 `nyadb-backup` v1 ZIP，含清单、逐对象完整 DDL、逐表类型化 JSONL、SHA-256、对象身份和一致性标记。写到私有临时文件，只有完整读取和前后结构核验成功才原子发布；取消/失败不替换现有目标文件。

InnoDB 数据在同一物理连接的 REPEATABLE READ 一致性只读事务内读取。非 InnoDB 对象明确标记一致性无法保证。对象目录限于当前账户可见的对象，清单明确记录这一范围；不宣称服务器全量对象可见。数据库值保持 NULL/文本 NULL/空串/空二进制/原始 bytes/精确 numeric；空间值保留 SRID 和内部 WKB，在导入及 SQL 转储中显式重建。

恢复先验证完整清单、所有校验和及类型化行，再核对目标、生成可审阅步骤。默认遇到已有对象停止；替换需明确选择。已有同名触发器必须属于计划替换的原表。恢复集合外的入向外键会阻止替换，不默默破坏外部依赖。

表先建不含外键的完整结构，数据随后流式导入，外键后加，触发器/事件最后恢复。视图依赖按公开 FROM/JOIN 引用排序，循环依赖拒绝执行。InnoDB 数据复用原流式导入事务/回执；非事务引擎逐行报告已确认数量、部分写入或不确定写入，不宣称可回滚。中途失败停止，禁止自动重放。

SQL 文件在原 SQL 页物理租约上扫描、按顺序执行，不替换编辑器文本；支持 MySQL 引号、注释、SQL mode 与 DELIMITER。文件步骤结果保留早先成功及失败/不确定状态。普通预览上限不阻止后续语句执行。

EXPLAIN 只接受一个完整语句，使用对应引擎的光标语句定位，拒绝多语句选区；确认弹窗显示实际 SQL，不自动添加 ANALYZE。MySQL 未取得原会话 mode 时对两种反斜杠模式保守校验，并拒绝客户端指令与可执行注释。WITH CTE 的 UPDATE/DELETE 使用写入执行入口，保留实际受影响行数。

## 明确限制

- CSV/TSV 以 `\N` 表示 SQL NULL，并转义以反斜杠起始的文本；类型保真交换使用 JSON。普通 SQL 预览仍为 1000 行、8 MiB、单元格 64 KiB、512 列，最多保留 128 个结果集；界面分别标示截断原因。“导出当前结果”只导出已取得的预览；“运行并导出”显式执行一次取得完整数据。
- SQL 转储用有界会话变量分块拼接大字段，避免生成无法重新读取的超长字面量语句。SQL 文件单语句限 8 MiB、每次最多 10000 条语句；超过时停止并保留已执行步骤，不自动重跑。未提供无界脚本历史。
- 备份/流式恢复单原始记录限 16 MiB，编码后 JSONL 行限 32 MiB，表列限 128、对象清单限 10000。越界失败，不能把截断值发布为完整备份。网格完整编辑仍为 1 MiB，与导入限制分别设置。
- typed MySQL 表设计器保存需要标准 MySQL 标识符/字面量 SQL modes；NO_BACKSLASH_ESCAPES/ANSI_QUOTES 下明确拒绝不安全的结构计划。
- 无法确定有效角色的 TRIGGER 权限时标记 Unknown，阻止假定没有触发器后修改。视图复杂依赖/外部依赖不保证自动解算，无法验证的场景停止或返回未验证结果。
- PostgreSQL/SQLite 首轮包括图形数据编辑与结构浏览，不提供 MySQL 图形设计器、导入向导、备份或用户管理假入口。只有可靠主键/非空完整唯一键的表可编辑；视图保持只读。SQLite 不隐式改变 journal 模式。
- 没有生产数据库测试、Navicat 新运行采证、物理桌面 GPU/IME/多显示器测试及 Windows/macOS 实机验收。Linux Xvfb 原生窗口是受限环境 I，不是原版差分 V。

## 验证与指纹

最终命令和产物指纹写入 `build/verify/core-verification.json`。源码树指纹包含最终 Go、依赖、翻译及配置代码；安装包与跨平台编译产物单独 SHA-256，不以 Git HEAD 代替未提交源码身份。仓库中的 fixture 工具只创建自己的 `--rm` 容器、loopback 随机端口及临时测试账户，退出清理自己的资源。

验收源码树 SHA-256：`7676d1c7b3d17026ee69c402fad99f7304989b7bf61ab6cd61c6d540ef6f57bc`（668 个文件，排除 docs/build/temp 及被忽略文件）。提交前再次核对与该指纹一致；构建产物及本地日志不纳入 Git 提交。

`tools/core_verification_manifest.py --receipts build/verify/check-receipts.json --output build/verify/core-verification.json` 记录显式执行回执及日志哈希，不代替执行检查。源码清单纳入 tracked 与未忽略的 untracked 文件，排除 docs/build/temp，避免文档引用形成循环哈希。每个文件和依赖都有独立 SHA-256；该清单可以审阅本次未提交源码身份。

- `GOWORK=off CGO_ENABLED=0 go test ./...`
- `GOWORK=off CGO_ENABLED=0 go vet ./...`
- `GOWORK=off CGO_ENABLED=0 go tool mygo vet`
- `GOWORK=off CGO_ENABLED=0 go tool mygo generate` 与 `go tool mygo i18n check`
- 生命周期与新工作流 focused `go test -race`
- `tools/verify_mysql_core_fixture.sh` 的 MySQL 8.0/8.4；`tools/verify_mysql_tunnels_fixture.sh`；`tools/verify_postgres_fixture.sh`
- `/usr/bin/python3 -m unittest discover -s tests -v`（系统 Python 的 Pillow 可用；默认 conda Python 缺 Pillow）
- Linux `go tool mygo build`、Windows/macOS CGO=0 编译和网关部署包编译

### 最终验收记录

最终 Go 全仓测试、Go vet、MyGo vet、翻译生成/校验均通过（zh-Hans/en 共 1066 条消息）。系统 Python 的 33 项回归通过。Linux `.deb`/`.tar.gz`、Windows amd64、macOS amd64/arm64 应用和本地网关部署包已生成，具体文件哈希见指纹清单。Windows/macOS 仅编译，不代表实机运行验收。

最终编译和全仓检查在最后的原生提示修正及 gofmt 后执行；fixture/race 的功能代码与最终源码语义一致，执行时间边界记录在指纹清单中。`go mod verify`、最终修改文件 gofmt 检查和 `git diff --check` 通过。检验回执、安装包和证据文件逐项哈希，不将早期编译产物混入最终交付清单。

公共契约/扫描器、MySQL/PG/SQLite 服务、凭据/查询/备份、传输、本地存储及 SQL/数据/对象/会话/连接编辑器的 lifecycle race 全包通过；Shell 的 SQL 关闭/断线、真实 SQLite、SSH 主机信任及对象操作 focused race 通过。对象 SQLModeKnown 的最终补丁另以语义核验 focused race 复验通过。完整 dataworkspace race 用时约 534 秒，没有超时或报告数据竞争。

最新一次性 MySQL 8.0/8.4 的 core fixture 分别通过，包含 SQL 类型/流式导出/CTE 写入行数、设计器、对象语义重读、备份恢复和非事务部分结果；两版经 SSH/HTTPS 网关的真实会话 race 也通过。PostgreSQL 17 fixture 通过。日志保存在 `build/verify/`，各阶段测试仍受本报告上面的范围及限制约束。

Linux 真实原生证据使用隔离 XDG 和自建 Xvfb、GTK 软件渲染：生产安装包打开主窗口及 SQL 页签，记录在 `temp/research/core-native-20261031/production/`。独立 EngineState/SQLWorkspace 原生组件在临时 SQLite fixture 上完成点击单元格、修改、保存以及 SQL 读回，证据为 `temp/engine-native/run-tOhZYa/grid-edited.png` 与 `sql.png`；生成列随原始值更新。生产 Shell 的真实 SQLite 全流程另由 `ui.NewTester` 回归验证。以上均为 I，不能合并成整应用人工桌面验收或 Navicat V。

原生采集工具首次因缺少 `scrot` 失败，后续改用 ImageMagick 根窗口截图。早期窗口相对坐标误点产生相同图片，不计入 SQL 页签验证；最终使用实际窗口位置修正点击后截图确认打开。隔离环境系统密钥库不可用，生产程序保持内存/主动文件保存回退，没有为旧记录生成替代密钥。
