# Navicat KeepAlive 原版运行尝试与 NyaDB 断线状态验证（2026-10-28）

## 1. 目标与隔离环境

本轮计划通过授权的 Navicat 18 正常 GUI、新建一次性回环 MySQL 8.4 fixture，区分 KeepAlive 的运行周期、连接 ID、服务端 wait_timeout、断线与事务状态。实际原版 GUI 启动未成功，因此 **没有新取得的 KeepAlive O（原版运行观察）**，更不存在原版/NyaDB 差分 V。

环境详情：

- Git 基线：1a26416（上一阶段 KeepAlive 实现）。
- 原版 AppImage：temp/navicat/navicat18-premium-cs-x86_64.AppImage；本轮重新计算 SHA-256 为 `3b77fe1e3295dc3067402a0bab33a8ed1cbfbdea063d8b59e67c26a6480b6d63`。原始核心库从 AppImage 只读提取到本轮实验目录并通过 SHA-256 校验 `6c45353fea04ba4b941aef2477fe9fa4997c77cee6380c3abc60f87f404e8e57`，未覆盖原版文件。
- 全新容器：`nyadb-navicat-keepalive-20261028`，镜像 mysql:8.4，实际 MySQL `8.4.11`。端口仅映射 `127.0.0.1:32782 -> 3306`，没有映射到公共网卡。
- MySQL 全局 `wait_timeout=30`、`interactive_timeout=30`、`general_log=ON`、`log_output=TABLE`；另建 `nyadb_keepalive_probe` 数据库与 `nyadb_ka` 最小权限账号以供原版使用。没有接触生产数据库。
- 全新 Xvfb `:191` + Openbox；全新 `XDG_CONFIG_HOME`、`XDG_CACHE_HOME`；实验隔离输出位于忽略目录 `temp/research/navicat-keepalive-fixture-20261028/`。
- 检查时其它 Docker 容器均属于不同项目，未对它们执行任何变更。

## 2. 原版 GUI 启动结果（未形成 KeepAlive O）

先通过独立脚本以正确的 AppImage 解包根目录为 cwd 启动 Navicat；启动后截图 `00-initial.png` 为黑屏，未能获得 GUI 窗口。再次以前台持久任务尝试同样的启动方式，进程约一秒后终止，任务退出码为 **70**。`navicat.log` 与 `navicat-live.log` 中仅见附带 libselinux 动态库的版本信息警告，没有足够证据确定退出原因。没有尝试修改授权状态、注入注册信息或绕过试用保护。

MySQL `general_log` 检查 `Connect` 条目，`nyadb_ka` 的连接数为 **0**；此结论仅表示该隔离测试用户没有建立数据库连接。原版启动异常原因及 KeepAlive 运行行为仍为 **Unknown**。不可把 NyaDB 的结果当作原版 O。

原有的 O/S 证据仍来自 `docs/MYSQL_ADVANCED_DIFFERENTIAL_2026-10-21.md`（默认未勾选、显示 240 秒）和 `docs/NAVICAT_MYSQL_SESSION_STATIC_ANALYSIS_2026-10-27.md`（保活名称/键存在）；这不是本轮新发现的原版行为。

## 3. NyaDB 真实 MySQL 实测（I，非 V）

在同一个隔离容器内另建 `nyadb_tab_it`、`tx_probe` 及仅有 fixture 库 SELECT/UPDATE 权限的 `nyadb_tab`，使用 `NYADB_MYSQL_TAB_IT=1` 和 `NYADB_MYSQL_TAB_PORT=32782` 运行 **显式 opt-in** 集成测试。该账号与原版预留账号不同。

- `TestMySQLSQLTabLeasesIntegration`：通过；真实 MySQL SQL 标签连接身份、变量隔离、事务、并发、元数据读取、取消及释放测试。
- `TestMySQLIdleTimeoutAndKeepAliveIntegration`：通过。KeepAlive 关闭、单会话 `wait_timeout=2` 秒，空闲 3.5 秒后服务器断开。第一次后续 SQL 返回 MySQL 错误码 **4031（HY000）**，“client was disconnected ... because of inactivity”；旧 SQL 标签随后保持不可恢复状态，再建新标签不继承变量。KeepAlive 开启、间隔 1 秒、单会话 `wait_timeout=3` 秒，空闲 5 秒后 `CONNECTION_ID()` 与原会话一致，`@nyadb_ka_marker=71` 保留。此测试未测得 Navicat 的实际周期。
- `TestMySQLDisconnectDropsTransactionStateIntegration`：通过。标签 A 的未提交 UPDATE 对标签 B 不可见，显式 Disconnect 后重新 Connect 新标签，旧事务写入未提交，原用户变量不继承，连接 ID 不同。测试不提交 fixture 写入。
- 原版连接检查：`nyadb_ka` 连接数 0。服务端 general_log 留有 `nyadb_tab` 测试账号的 Connect/Query/Quit 记录；未将 general_log 中没有 Ping 命令的现象单独解释为没有发送 COM_PING（NyaDB 另有本地模拟协议测试）。

## 4. 从实测触发的 NyaDB 修正（I）

1. `ExecuteSQLTab` 的断线判断新增 MySQL 驱动原生 `ErrInvalidConn`、EOF/UnexpectedEOF 以及服务端错误 4031、2006、2013、2055 的分类；对语法错误 1064、唯一键错误 1062 不误报会话丢失。
2. 识别到物理断线后，立即标记 SQL 租约失效，主动清理物理连接并在**首次失败**返回含 `ErrMySQLSQLSessionLost` 的错误。不得静默分配新会话，从而假装原事务或临时表仍在。
3. UI 在断开 Profile 时保留原 SQL 编辑文本，但解除旧标签的 Profile/Schema 关联；即使稍后重新连接相同 Profile，也不能在旧标签上静默取得新物理会话。用户须明确新建 SQL 标签。
4. 新增 mock 单测、MyGo Tester UI 回归测试及 opt-in 真实 MySQL 集成测试。普通 `go test ./...` 不会连接任何数据库；只有显式设置两个 opt-in 环境变量才运行真实 MySQL 测试。

**限制**：上述保护针对 UI 可见的断开与底层执行时发现的物理连接失效；它不是自动重连系统，更不能恢复 SQL 事务。在未执行查询时 UI 不一定立即发现服务端断开。网络抖动、DDL/DML 的提交结果仍需人工核对，不能将一次失败解释为事务一定回滚。服务层按 TabID 的旧兼容懒分配 API 仍需下一阶段通过 lease generation/token 进一步防止调用方跨 Connect 误用旧 ID；本轮 UI 已显式解绑旧标签。

## 5. 验证与仍需继续

- Go 全量测试 `go test ./...`：通过。
- Go 静态检查 `go vet ./...`：通过。
- `go tool mygo vet`、`go tool mygo build`（v0.6.4，Linux amd64）：通过。
- `go test -race`（连接错误分类和 UI 回归专项）：通过。
- 真实 fixture 三项 opt-in 集成测试：通过。
- 原版 Navicat KeepAlive O、Windows 实机 GUI、严格原版差分 V：**未通过／未完成，不声称完成**。

下一次原版实验应先排查 exit 70 的正常启动前置条件，并仅通过正常授权 GUI 完成；不得复用已经清理的一次性容器，也不得借助未授权的注册/补丁绕过。之后才能开展 1/5/240 秒、不同 wait_timeout、连接 ID、断线期间事务以及服务器日志的原版差分。
