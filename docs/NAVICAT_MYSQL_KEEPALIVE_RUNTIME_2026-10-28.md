# Navicat 18 MySQL KeepAlive 与断线恢复：原版运行观察（2026-10-28）

## 范围与证据

本轮在正常运行的 **Navicat Premium 18** 原版 GUI（已有隔离 Xvfb `:184` 上的历史实验进程）中，创建一次性 loopback MySQL 8.4.11 fixture，实际观察空闲会话、KeepAlive、两个 SQL 标签的独立保活，以及服务端主动断线后的行为。原版 GUI 截图、MySQL 的 `performance_schema` 和 `general_log` 属于本次 **O（运行观察）** 证据；不代表 Navicat 内部源码或通用平台行为。

**新观察带来的环境修正**：本轮开始时发现系统中已有历史研究的 Navicat 实例（GUI 窗口可见，原显示器 `:184`），因此没有杀掉该进程或重装原版。此前另起实例返回 `exit 70`，可能与单实例转发/冲突相关，但**该退出码的根因仍 Unknown**，不把推测写为结论。此前“无法开展原版观察”的阻碍已通过复用正常运行的既有 GUI 消除。

- 一次性容器：`nyadb-navicat-ka-observe-20261028`（`mysql:8.4`，实际 8.4.11），仅监听 `127.0.0.1:32785`，没有向外网开放。
- 独立实验数据库 `nyadb_ka_observation`、只读用户 `nyadb_ka_obs`，一次性空密码；**密码未保存到 Navicat 连接档案**。
- MySQL 服务端 `@@global.wait_timeout=7` 秒、`@@global.interactive_timeout=7` 秒，`general_log=ON`、`log_output=TABLE`。
- Navicat 仅新建临时连接档案 `NyaDB KeepAlive Fixture 20261028`；没有改动既有 `NyaDBAlignment Fixture`。通过常规 GUI“测试连接”实际获得“连接成功，服务器版本 8.4.11”。
- 截图、原始 `general_log` 及 `performance_schema` 数值位于被 Git 忽略的 `temp/research/navicat-keepalive-live-20261028/`，并保存 `evidence-sha256.txt`。
- 本轮没有动态调试原版、修改授权或 AppImage，也未连接生产数据库。

## O1：KeepAlive 默认设置及关闭对照

原版高级设置的“保持连接间隔（秒）”默认不勾选，值显示为 **240**，未启用时数值框不可编辑（`04-advanced-default.png`）。保存该状态的档案，正常打开连接后，MySQL `general_log` 显示测试用户线程 12 在 `2026-10-28 10:27:10` 连接并执行若干初始化查询。超过服务端 7 秒空闲阈值后，再查询 `PROCESSLIST`，该线程已不在列表中。它不包含正常的 `Quit` 记录。

**O：** 在本次测试条件下，关闭 KeepAlive 的这条空闲会话未能持续保持在线。此样本只针对该档案、该 MySQL 版本、该服务端超时，不证明原版没有其它内部健康检查。

## O2：KeepAlive=1 秒实际发送 COM_PING 并维持会话

通过原版“编辑连接”界面，将“保持连接间隔（秒）”勾选并设置 **1**，截图 `14-ka-enabled-1s.png`；关闭后重新连接该测试 Profile。首次 SQL 查询标签执行：

```sql
SET @nyadb_ka_marker = 777;
SELECT CONNECTION_ID() AS cid, @nyadb_ka_marker AS marker, @@wait_timeout AS wt;
```

取得 **cid=15，marker=777，wt=7**，截图 `21-set-marker.png`、`24-select-exec.png`。保持空闲数十秒之后，在同一 SQL 标签再次执行 SELECT，仍为 **15、777、7**（`25-sql-after-idle.png`）。在此期间针对仅该测试用户的服务端 `PROCESSLIST` 采样，于 `10:30:36` 和 `10:30:46` 均显示线程 **15** 且 `Sleep TIME=0`。

虽然 `mysql.general_log` 没有列出 Ping 类型条目，**`performance_schema.events_statements_summary_by_thread_by_event_name` 直接显示事件 `statement/com/Ping`**，在原版线程 15 上：

| MySQL 服务器时钟 | 线程 ID | Ping 计数 |
| --- | ---: | ---: |
| 10:35:02.432 | 15 | 304 |
| 10:35:10.560 | 15 | 312 |

**O：** 该连接在约 **8.13 秒内收到 8 次 COM_PING**，符合设置为 1 秒的预期；也证明这是协议级保活，而不只是界面刷新。不能据此直接推断其内部使用何种 Qt 定时器/线程类。

## O3：两个 SQL 标签分别收到 KeepAlive

同一档案新建第二个查询标签并执行 `SELECT CONNECTION_ID(), @nyadb_ka_marker, @@wait_timeout`，返回 **cid=28、marker=NULL、wt=7**（`28-second-tab-result.png`）。第一标签仍为 cid=15 且变量 777，说明两者不共享此变量。

相隔约 9.14 秒采样两个独立线程的 `statement/com/Ping`：

| SQL 标签连接 | 开始计数（10:36:53.263） | 结束计数（10:37:02.403） |
| --- | ---: | ---: |
| 15 | 414 | 423 |
| 28 | 77 | 86 |

**O：** 此次测试中两条同时打开的 SQL 会话分别增加了 9 次 Ping，均获得 KeepAlive；并非仅一个全局或元数据连接发送 Ping。对元数据连接池在无 SQL 标签、任意数量连接或不同底层驱动下的范围仍不能作普遍断言。

## O4：服务端主动断线后，原标签下一次 SELECT 允许换新会话，但会话状态丢失

该一次性 fixture 中使用管理员会话执行 `KILL CONNECTION 28`。原版第二 SQL 标签下一次执行原 SELECT 时**成功返回**，物理连接 ID 从 **28 变为 32**；未看到需要新建标签的错误提示（`30-after-kill-retry.png`）。第二标签原先变量为 NULL，无法单凭此样本推断状态恢复规则。

为验证状态变化，再对第一 SQL 标签的连接 15 执行 `KILL CONNECTION 15`。在下一次用户查询前的 `PROCESSLIST` 采样仅显示测试用户线程 32、未看到线程 15 的即时替代连接。随后在**同一原 SQL 标签**点击运行相同 SELECT，成功返回 **cid=35、marker=NULL、wt=7**（`31-first-tab-after-kill.png`），而断线前为 **cid=15、marker=777、wt=7**。

**O：** 至少在上述简单只读 SELECT 场景下，Navicat 可以在同一个 SQL 标签中使用新物理会话继续查询，并且**不会恢复旧会话用户变量**。可观察到的行为是下一次查询成功、ID 改变及变量变 NULL；尚未证实是否由 KeepAlive 检测失败触发重连、由下一次查询懒重连，或由其它连接管理路径协调，两者均应标为 Unknown。不能从本实验的用户变量丢失直接推断任何具体事务已提交或回滚：本次没有开启未提交事务。

## NyaDB 的已知差异与后续验证

NyaDB 当前 `mysql_sql_tabs.go`、`mysql_keepalive.go` 和 `mysql_sql_tab_tokens.go` 已在独立服务层实现每标签物理会话、周期 `PingContext`、断线标记 `ErrMySQLSQLSessionLost`、代次绑定。对于已确认丢失的状态，NyaDB **故意不静默替换旧 SQL 标签的物理连接**，优先保护用户对事务、临时表和变量状态的预期。

本轮取得新的 Navicat **O**，但**尚未在同一 fixture、同一时间窗口、同一 GUI 操作下重新运行 NyaDB**，因此没有把上述行为宣称为严格 **V**。当前差异为：

- Navicat：用户在旧标签继续执行 SELECT，可观察到重新连接及变量重置；本实验没有出现显式的“原会话失效”阻断。
- NyaDB：一旦明确确认原会话丢失，旧标签返回会话失效，需新建标签，避免静默改变事务上下文。

建议下一阶段考虑明确的 **“会话已断开／以新会话继续”** 交互，允许用户知情后恢复只读查询工作流；不得默认重放 DML、提交或声称恢复原事务。仍需单独实验：240 秒默认周期、5 秒对照、KeepAlive 跨元数据连接池的作用范围、正在执行长查询时的 Ping、不同服务端断线原因、未提交事务断线后原版具体 UI 提示及最终持久化状态。

## 实验清理与原版保护

完成采样后，关闭本轮两个临时 SQL 标签并选择“不保存”；断开并删除**仅本轮新建**的 `NyaDB KeepAlive Fixture 20261028` 连接档案（`38-own-profile-deleted.png`）。一次性 Docker 容器已停止并自动删除；既有 Navicat 原版窗口、Xvfb `:184` 和历史连接档案保持不动。未修改正式 NyaDB 源码或 Navicat 原版文件。研究截图及服务端原始证据仍在忽略目录，未作为可分发产品资产提交。
