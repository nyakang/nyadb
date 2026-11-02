# ClickHouse 原生引擎基础阶段 — 2026-11-02

按用户选择接入 ClickHouse，独立使用 `clickhouse-go/v2 v2.50.0` 原生 TCP 协议。实现 I；没有运行 Navicat、连接生产或复制私有实现。按用户后续“每项完成后提交”要求本地归档，不推送。完整目标继续有效，可配置 OpenAI 兼容 AI SQL 及其它阶段尚未完成。

## 实施范围

- 新增不可变 ClickHouse 描述、默认 localhost:9000 / default 数据库和用户、配置验证、URI 导入、连接选择/筛选、编辑/测试及系统密钥环凭据保存。TLS 仅提供 disable / verify-full；后者使用系统 CA、主机名校验和 TLS 1.2 最小版本，没有不验证证书的回退。连接表单提供常规/SSL/备注页。
- SQLite 配置复用原字段及版本 1 引擎目录，保留其它引擎格式，不新增迁移。密码仍单独注入和保存在 OSVault；档案没有密码字段。
- `internal/services/clickhouse` 提供独立 Session、MetadataProvider、QueryProvider、DataReads 和 DataExports，经同一个 dbruntime 注册和绑定。UI 不导入驱动，不增加另一套数据 State/Document。
- 命名空间为 ProfileID / 实际数据库 Catalog / 空 Schema；表/视图按完整身份浏览，保留引擎、注释、CREATE 定义、完整字段类型及 DEFAULT/MATERIALIZED/ALIAS 信息。查询、结构和只读数据都接入现有工作区。
- 支持 offset 分页、字段排序、明确筛选、计数和完整流式导出/类型化 JSONL。ClickHouse 排序键/PRIMARY KEY 不提供唯一行身份，逐行编辑、事务、keyset、设计器、导入、管理和备份能力保持关闭。页面预览超过 16 MiB 明确失败，避免截短后错误跳页。
- 独立 SQL 文档持有自己的单连接原生客户端。预连接异步；查询作用域切换显式重建客户端，用户 SQL 不重放；专用拨号器拒绝第二次物理连接。执行错误/取消后立即标记租约丢失，由工作区显式恢复。关闭取消原请求并等待执行器回执，再关闭它拥有的客户端；旧代次不会按 profile ID 路由到新连接。
- UInt64/宽整数和 Decimal 保留精确文本，NULL、文本 NULL、空串和二进制保持类型区别；数组使用不经过 float64 的 JSON。支持的原生标量保持规范值，未知值明确拒绝。结果保留最多 2000 行、单格 4096 字节和 16 MiB 预览；流式回调仍收到完整值。
- 服务器异常只显示数值代码，不回显可能包含 SQL/字面量/凭据的服务器消息。未确认的修改失败不能描述为已回滚；不虚构受影响行数。
- 新增独立 ClickHouse SQL 分句、光标语句与格式化规则，保护无空格 `--` 注释、转义引用及 heredoc 中的分号；不套用 MySQL 的 `--` 后空白规则或 DELIMITER。嵌套块注释明确拒绝。补全同样保护 ClickHouse 注释和 heredoc。
- 原生检查发现恢复文档的提示、格式化及补全仍读取未连接的会话编号 0；现统一读取原文档恢复范围，保留原 profile/database 和正确方言。界面不再建议恢复文档重新新建查询；格式化及补全不会建立会话或执行 SQL。新增 UI Tester 回归覆盖这些限制。

## 自动测试与真实服务

`tools/verify_clickhouse_fixture.sh [--race]` 创建唯一命名/label、随机 localhost 原生端口的空 ClickHouse 25.8 容器。密码经私有环境文件与测试环境传递，不进入命令行或证据；退出/失败/中断删除私有文件，并核对身份后清理本轮容器。服务测试显式要求 `NYADB_CLICKHOUSE_IT=1`，默认 Go 测试跳过，不能把默认跳过计为真实服务通过。

最终真实服务为 ClickHouse **25.8.33.6**，镜像身份 `sha256:0152dd511befe6a2c2ef53e930726179669b08116da78500b37c51c96ff5ee77`。包含最终 SQL 分句修复的证据 `build/verify/clickhouse-rl2Bm0`：服务 race 1.332 秒，真实 Shell/UI Tester race 12.527 秒；两个主测试和三个服务子场景通过。服务核对 MergeTree/视图/生成别名、精确 UInt64/Int256/Decimal、NULL/空值/二进制/数组、筛选、完整导出、2500 行流式结果与预览界限、独立会话 SET、原数据库、真实 sleep 取消、显式替换和断开重连后的旧租约/数据回调撤销。Shell 通过原生组件操作连接、共享只读网格、结构、SQL/EXPLAIN、拒绝编辑及断开保留 SQL。无空格注释内的 `DROP TABLE` 文本没有被拆出执行，heredoc 中的 SQL/分号作为完整字面量返回。

存储/表单回归另覆盖无迁移重启、错误引擎目录、凭据恢复/显式删除、vault 失败回滚、原生标题及关闭中的不确定修改；这些注入回归不计为真实密钥环故障验收。

中间失败保留：`clickhouse-URa0yl` 拉取镜像阶段中断（143，不计测试通过）；`clickhouse-XidqSd` 发现 UI 对象 kind 使用小写而适配器只接受大写；`clickhouse-AbKmBz` 发现新建查询的引擎白名单遗漏，查询未绑定连接。修复后 `clickhouse-O1D9iu`、`clickhouse-Pkm7ok` race 通过；最终以 rl2Bm0 为服务验收结果。各工具均记录本轮清理结果。

## 正式 Linux 窗口

新增 `capture_clickhouse_acceptance.py` 和 `entryfixture seed-clickhouse`。只在 `verify_entry_acceptance.py` 创建的私有 D-Bus/Secret Service、Xvfb、Openbox 和空配置中运行；采集器另建自己拥有的 ClickHouse 容器。辅助程序通过正式 OSVault 保存凭据，并创建加密查询/工作区；正式应用为新进程，使用原生窗口。采集同时核对 `/proc/<pid>/exe` 哈希，记录窗口几何、动作和稳定截图。

原生中间重放 r-e87a2e881ef3 和 r-89d012ff6ba5 exit 0，分别核对共享只读网格及恢复的正文。r-89d012ff6ba5 捕获恢复提示编号 0 的问题，其二进制哈希为 `1cda9f29f12d211c5287d86ec19fe792df92255228d669a523bbacf883f6eb84`；不把此版本列为最终提示通过。

首次 r-da416ad7528a 已成功 seed，但缺少采集器必需的 settled 状态标记；r-432d5e29ad79 发现连接编辑器的独立标题映射遗漏 ClickHouse，正式窗口出现但未匹配目标标题。r-a00387b678eb 的 fixture 初始选中 SQL 标签，使导航 Enter 输入编辑器；已改为明确从对象页开始。r-7c22126844f4 正式窗口已成功恢复和执行两行 SELECT，但检查工具错误要求执行记录保留末尾分号；修正后该 run 的独立 `native-clickhouse` 加密记录检查 exit 0。完整流程仍重放以避免把一次检查修正等同于整套工具通过。所有失败均保留，已按资源身份清理。

最终完整重放为 `build/entry-acceptance/r-7a2f18a5e975`，`custom-53435fec` **exit 0**。正式二进制 SHA-256 为 `15c1d4de042c81874e7e1092f7cc224f1a1c0e9e3c94f9c2e2cc8ff00c05e009`，启动后核对 `/proc/<pid>/exe` 一致。确认真实系统密钥环凭据遮罩、四个实际命名空间、MergeTree 结构、共享只读网格和 1280×800 / 840×560 布局；恢复正文保留 `SELECT id,label,amount,note FROM records ORDER BY id DESC;`、原连接 #1 / default，以及尚未执行状态。显式“以新会话继续”后再点击运行，返回两行四列；独立辅助进程从正式加密仓库复核文档身份、原数据库、正文和成功执行历史。

已查看以下本地截图，均在忽略目录；正式窗口和组件截图不替代 Navicat 差分：

- [连接及密钥环凭据遮罩](../build/entry-acceptance/r-7a2f18a5e975/evidence/clickhouse-native/connection-general-client.png)、[TLS 页面](../build/entry-acceptance/r-7a2f18a5e975/evidence/clickhouse-native/connection-tls-720-client.png)。
- [1280×800 只读数据](../build/entry-acceptance/r-7a2f18a5e975/evidence/clickhouse-native/grid-1280-client.png)、[840×560 只读数据](../build/entry-acceptance/r-7a2f18a5e975/evidence/clickhouse-native/grid-840-client.png)。
- [原连接 SQL 正文恢复](../build/entry-acceptance/r-7a2f18a5e975/evidence/clickhouse-native/restored-sql-client.png)、[显式执行结果](../build/entry-acceptance/r-7a2f18a5e975/evidence/clickhouse-native/sql-result-client.png)。

本轮结束已停止私有 Secret Service / D-Bus / Xvfb / Openbox，核对本轮 wrapper MySQL 与 ClickHouse 容器已删除，临时秘密文件已清理。独立按 PID/birth 与唯一容器身份审计全部七次 ClickHouse 原生 runs，证据 `build/verify/clickhouse-rl2Bm0/cleanup-audit.json`；Docker 查询成功且没有残留，不能以 daemon 查询失败误判清理。Xvfb 中实际窗口几何可确认，字体/缩放精度仍 Unknown。

## 最终检查

Linux amd64 / Go 1.27.3 / MyGo v0.7.3-nyadb.2。源码从 `5960d26` 接续，已验证前置成果归档为 `9cf7605`，ClickHouse 单独归档。命令使用 `GOWORK=off GOCACHE=/data/.cache/golang`。

- 最终恢复范围修复后，全量 `go test ./... -timeout=20m` 通过：Shell 77.049 秒，sqlworkspace 2.788 秒。独立前置 review 快照同样通过全量测试，Shell 80.680 秒，避免产生无法编译的归档提交。
- 最终 SQL 分句版本的 sqltext/clickhouse/sqlworkspace race 1.040/1.089/30.482 秒通过；恢复范围修复后 sqlworkspace 全包 race 再次通过（28.264 秒）。此前 services/runtime/connections/localdb/editor/catalog/navigation/session 的生命周期 race 通过，标题修复后 editor race 25.659 秒通过。
- Shell 全包 race 743.131 秒通过，使用 20 分钟超时。其编译版本早于最终 lexer/恢复提示修复，不能声称覆盖这些末尾改动；新 lexer 有纯测试和真实服务/Shell fixture race，恢复范围另有 sqlworkspace 全包 race 与最终正式窗口证据。
- `go vet ./...`、`go tool mygo vet`、翻译生成及 i18n 检查通过，1343 条 zh-Hans/en 消息。恢复范围修复后的 `go tool mygo build` 通过（25.8 秒）。验收工具追加后单独编译/vet entryfixture。
- `/usr/bin/python3 -m unittest discover -s tests -v`：51 项通过（最终 2.315 秒）；采集器 py_compile、fixture bash 语法检查通过。默认 conda Python 首次缺 PIL，未计为通过。原生工具使用带 Pillow 的系统解释器。

证据日志在 `build/verify/clickhouse-rl2Bm0`；更早的完整生命周期/Shell race 和 Python 日志保留于 `build/verify/clickhouse-Pkm7ok`。编译、无窗口测试、真实服务和原生窗口分开报告，均不等同 Navicat 差分。

重放：

```bash
GOWORK=off GOCACHE=/data/.cache/golang PATH=/usr/local/go/bin:$PATH \
  tools/verify_clickhouse_fixture.sh --race
GOWORK=off GOCACHE=/data/.cache/golang PATH=/usr/local/go/bin:$PATH \
  /usr/bin/python3 tools/verify_entry_acceptance.py --start-only --keep
# 使用上一步打印的本轮目录：
GOWORK=off GOCACHE=/data/.cache/golang PATH=/usr/local/go/bin:$PATH \
  /usr/bin/python3 tools/verify_entry_acceptance.py --run <本轮目录> --keep -- \
  /usr/bin/python3 tools/capture_clickhouse_acceptance.py
/usr/bin/python3 tools/verify_entry_acceptance.py --run <本轮目录> --stop
```

## 未验证及剩余工作

真实 TLS/证书失败、跨版本服务、复杂 Map/Tuple/JSON/聚合状态类型、集群/字典/函数元数据、图形变更/导入/备份恢复和隧道尚未验收或接通。offset 分页没有稳定唯一行身份，不承诺并发数据变化时不会重复/遗漏。原生 Xvfb 不替代实体桌面 GPU/IME、Windows/macOS 或 Navicat 差分；本步没有新增 Navicat O/S/V。

PG 正式 Apply/设计表单和完整物理/依赖/并发/探针证明、PG/SQLite 对象/导入/备份/隧道、跨库传输及同步、自动运行、可配置 OpenAI 兼容 AI SQL、模型/逆向/BI/监控/字典/生成/脱敏/CLI 和精细交互/多平台/原版验收仍属 [完整目标](workbench-completion-2026-11-02.md)。本阶段不是整体完成。
