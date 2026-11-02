# PostgreSQL / SQLite 配置与附加库交付记录 — 2026-11-02

本阶段延续工作台剩余功能清单，完成 PostgreSQL 高级/TLS 配置和 SQLite 高级/附加库。实现为 **I**，HEAD `5960d26`，本阶段及此前工作台改动未提交。新引擎优先 ClickHouse；AI SQL 使用可配置 OpenAI 兼容服务，两个独立阶段尚未实施。

## 实现

- `connections` 增加版本化公开引擎选项。PostgreSQL 支持连接超时、连接池上限、application_name、默认 schema、statement_timeout、lock_timeout、CA 和成对客户端证书/密钥路径。SQLite 支持只读、要求文件存在、busy_timeout、外键约束以及最多 10 个一级附加库。
- 原生编辑页拥有独立数字输入；不完整或非法数字保留输入并拒绝保存/测试。SQLite 附加库使用原生表格，支持添加、选择、改名/路径、逐库只读和移除；移除只修改配置。配置确认不连接数据库；连接初始化才打开文件。
- 附加库名称去除首尾空白，大小写不敏感唯一，拒绝 main/temp/sqlite_ 保留名称、控制字符、重复文件及相对路径。会话初始化检查现有常规文件，并拒绝同一文件的硬链接/符号链接别名；参数化 ATTACH 和标识符引用支持空格、中文及引号等合法名称/路径。
- `localdb` 复用现有 mysql_advanced_json 列：MySQL 原格式保持兼容；PG/SQLite 使用版本化引擎 envelope，无数据库迁移。未知版本/字段/引擎拒绝读取，不覆盖原记录。旧空配置恢复历史默认行为；旧 SQLite 仍允许 rw/create 主库，新建连接默认要求文件存在。
- SQLite 自有 driver.Connector 在每条物理连接应用 busy_timeout、foreign_keys 及 ATTACH，涵盖池中新建连接、SQL 租约、网格读取和事务。附加文件始终以 rw/ro 打开，缺失时拒绝连接；失败预检不创建主库或附加文件。
- 元数据、字段/索引/触发器结构、网格分页和事务使用 ProfileID/Catalog/Schema 的完整身份。所有 SQLite 命名空间的 Catalog 保持 main，Schema 为 main 或配置附加名称。同名表的主库与附加库读写互相隔离。
- 主连接只读关闭编辑能力；逐附加库只读关闭该表编辑资格，底层文件模式也拒绝直接 SQL 写入。配置切片在编辑器、验证、会话请求及异步握手时捕获独立副本。会话断开和重连沿用现有代次/租约撤销机制。
- PostgreSQL pgx 配置显式覆盖配置相关的 libpq 环境凭据/证书和 RuntimeParams；公开文件路径不包含密钥正文。配置应用于每条物理连接，遵循用户选择的 TLS 模式；启用 SSH/HTTP 时在拨号前返回不支持错误，避免静默直连。
- 原生文件选择请求捕获引擎、目标行及编辑版本；取消/关闭/重新打开及附加行添加/移除撤销旧回执。既有 bootstrap/Shell 文件选择接口保留。非 MySQL 测试状态显示实际引擎名。

## 自动检查与真实服务

环境为 Linux amd64，Go 1.27.3 / MyGo v0.7.3-nyadb.2，`GOWORK=off`，Go 缓存 `/data/.cache/golang`。

- 全量 `go test ./... -timeout=20m` 通过；Shell 91.072 秒，dataworkspace 68.343 秒。新增握手切片测试随后通过 sessionbrowser 全包 race；验收工具及 locale 包也通过编译/测试。
- connections/localdb/engines/dbruntime/connectioneditor/sessionbrowser 生命周期 race 通过；连接编辑器 25.464 秒。Shell 完整 race 通过（709.067 秒）；附加库 Shell 回归在 fixture 清理顺序调整后再跑 race 通过（7.470 秒）。本阶段不改 dataworkspace 生产代码，没有重复其完整 race。
- `go vet ./...`、`go tool mygo vet`、`go tool mygo generate`、`go tool mygo i18n check`（1324 条）及正式 `go tool mygo build` 通过。
- Python `unittest discover -s tests -v` 共 48 项通过。新增采集配置身份测试拒绝错误文件、引擎和格式版本。原生采集工具复用已验收的截图后端、按键及资源归属工具。
- 真实自有 SQLite 覆盖：四条同时持有的物理租约、逐连接 PRAGMA 参数、引号库名/中文路径、附加库字段和索引、类型化 NULL/二进制、附加库事务保存、主库不变、重连恢复、未知命名空间、只读、缺失文件及同文件别名。Shell `ui.NewTester` 操作连接/导航/网格保存，并从两个文件独立复核结果。
- `tools/verify_postgres_fixture.sh` 创建唯一的 PostgreSQL 17 容器、随机 localhost 端口，并在退出时清理本次容器。`TestPostgreSQL17Fixture` 和 `TestPostgreSQL17OptionsFixture` 通过，总 0.488 秒：三条独立连接的默认 schema/application_name/服务端超时、第四条租约的池限制、真实 pg_sleep 语句超时、fixture 行锁等待超时以及失败写入后原数据保留。
- 初次新增测试存在测试 API 调用错误，修正后通过；随后原生输入测试发现文本标签与输入框同名导致目标误选，改为独立标题/输入标签后通过。不把这些失败尝试列为通过。

## Linux 原生证据

`tools/capture_engine_options_acceptance.py` 通过 `verify_entry_acceptance.py` 的独立运行执行，要求空的本次配置并用 `entryfixture seed-engine-options` 创建两个 SQLite 文件及离线 PG 配置。复用私有 D-Bus/Secret Service、Xvfb、Openbox；启动正式程序，核对 `/proc/<pid>/exe` 的 SHA-256 和实际窗口几何。通用验收环境同时创建 MySQL 8.4 容器，本次采集不连接该容器。

运行目录：`build/entry-acceptance/r-7c4fa5e98161/`。正式二进制 SHA-256：`bfe17ff2acf14a0e01c59c661ff2ede6df80920b085e6272afe7b781670a904d`。修正后的原生脚本完整重放 exit 0（custom-8244846c）；截图内容复核确认 main/aux 导航、aux.shared 的 attached fixture 行数据、1280×800 / 840×560 和配置重启。代表性图像：

- [SQLite 高级页](../build/entry-acceptance/r-7c4fa5e98161/evidence/engine-options-native/sqlite-advanced-900-client.png)。
- [900×700 附加库](../build/entry-acceptance/r-7c4fa5e98161/evidence/engine-options-native/sqlite-attached-900-client.png)、[720×520 附加库](../build/entry-acceptance/r-7c4fa5e98161/evidence/engine-options-native/sqlite-attached-720-client.png)。
- [PG 高级页](../build/entry-acceptance/r-7c4fa5e98161/evidence/engine-options-native/postgres-advanced-900-client.png)、[720×520 TLS 页](../build/entry-acceptance/r-7c4fa5e98161/evidence/engine-options-native/postgres-tls-720-client.png)。
- [1280×800 附加库真实网格](../build/entry-acceptance/r-7c4fa5e98161/evidence/engine-options-native/sqlite-aux-data-1280-client.png)、[840×560 附加库真实网格](../build/entry-acceptance/r-7c4fa5e98161/evidence/engine-options-native/sqlite-aux-data-840-client.png)。
- [重启后的附加库](../build/entry-acceptance/r-7c4fa5e98161/evidence/engine-options-native/sqlite-attached-restart-client.png)、[真实 SQLite main/aux 导航](../build/entry-acceptance/r-7c4fa5e98161/evidence/engine-options-native/sqlite-attached-namespaces-client.png)。

首次运行 r-084aa88c2464 的数据页步骤误选导航分类，两个 data 文件实际是对象列表，不计为数据表格通过；原目录保留分类错误说明。修正后使用新运行完整重放，并逐张核对网格内容。截图内容复核与脚本退出码分开报告。两次验收运行均已通过拥有者工具清理本次进程、容器及 secrets.json，保留公开截图和结果。

环境没有 Tesseract；截图内容通过查看 PNG 复核，不把脚本退出码单独作为数据表格内容通过的依据。

正式主窗口维持 840 最小宽度；720×520 属于连接子窗口验收。重启通过按进程身份停止后重新启动；没有据此宣称人工关闭守卫、真实系统桌面 GPU/IME 或跨平台原生验收。

可重复命令：

```bash
GOWORK=off go tool mygo build
python3 tools/verify_entry_acceptance.py --start-only --keep
# 使用上一步打印的本次运行目录；只复用本工具拥有的运行。
python3 tools/verify_entry_acceptance.py --run <本次运行目录> --keep -- \
  /usr/bin/python3 tools/capture_engine_options_acceptance.py
python3 tools/verify_entry_acceptance.py --run <本次运行目录> --stop
bash tools/verify_postgres_fixture.sh
```

## 验证限制与仍待实施

- PostgreSQL 原生采集只覆盖离线表单；真实服务测试使用 fixture 的 TLS disable。证书配置单元测试不能替代真实 TLS/mTLS 握手，PG SSH/HTTP 和 SQLite HTTP 尚未实现。
- SQLite 没有可切换的默认附加库语义；SQL 编辑器应使用 `aux.table` 等明确限定。附加库网格/结构/事务均限定对象，未改写任意用户 SQL。手工 SQL ATTACH/DETACH 只影响其物理租约，不发布为连接配置或全局导航目录。
- SQLite 取消仍等待驱动确认；锁等待取消延迟受所选 busy_timeout 影响。不以配置支持宣称更强的取消保证。
- PG/SQLite 图形设计/对象 CRUD、完整 PG 程序目录、多格式导入导出向导、备份恢复、管理及大数据能力深化仍待实施。SQLite 不照搬服务器账户权限概念。
- 跨连接传输/结构同步/数据同步、自动运行、完整命名工作区、可视化执行计划、专用程序编辑器和高级模型/BI/监控/字典/生成/脱敏/CLI 尚未完成；用户已选 ClickHouse 和 OpenAI 兼容 AI SQL 后续阶段。
- 没有启动 Navicat、动态调试、生产连接或 commit/push。本次新增证据仅为独立实现 **I** 与 NyaDB 运行结果；新增原版 **O/V**、Windows/macOS 原生、同条件像素/性能差分均 **Unknown**。
