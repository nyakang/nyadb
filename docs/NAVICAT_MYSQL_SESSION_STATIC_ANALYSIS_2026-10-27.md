# Navicat 18 MySQL 会话管理：ELF 静态研究（2026-10-27）

## 研究范围与证据等级

本轮只读检查 `temp/navicat/navicat18-premium-cs-x86_64.AppImage` 及其已解包文件。**没有执行、动态调试 Navicat，未连接数据库、未绕过授权、未改写原版文件**。研究目标为连接租借、SQL 工作区会话、池、保活、会话限制、断线重连和取消。

- **S**：已验证 AppImage 原始二进制的静态证据。
- **O**：此前文档中的合法 UI 观察，非本轮新运行。
- **Unknown**：仅从静态名称不能确定的运行时行为。
- **V**：本轮没有形成原版与 NyaDB 双方同 fixture 的行为差分。

## 样本原始性检查（重要）

AppImage 内嵌 SquashFS 起始偏移为 **663872**，可只读校验：

```bash
unsquashfs -s -o 663872 temp/navicat/navicat18-premium-cs-x86_64.AppImage
unsquashfs -cat -o 663872 temp/navicat/navicat18-premium-cs-x86_64.AppImage usr/lib/libcc.so | sha256sum
unsquashfs -cat -o 663872 temp/navicat/navicat18-premium-cs-x86_64.AppImage usr/bin/navicat | sha256sum
```

| 对象 | SHA-256 | 比对结果 |
| --- | --- | --- |
| AppImage 内原版 `usr/bin/navicat` | `7930570bc336570326ce78c2df22d05aaf740b462028b146213df6f004ab721e` | 与解包 `usr/bin/navicat` 一致 |
| AppImage 内原版 `usr/lib/libcc.so` | `6c45353fea04ba4b941aef2477fe9fa4997c77cee6380c3abc60f87f404e8e57` | 与解包 `usr/lib/libcc.so.bak` 一致 |
| 解包 `usr/lib/libcc.so` | `0d09193af7171cdba068f34eb2ebe1c34cd82bccb5e7bdfed74965def0c4d677` | **不一致；不要作为原版权威样本** |

两个 `libcc` 文件 ELF Build ID 同为 `24e162a9e8ebec6562b88b951b9b84b6aefc5174`，但**Build ID 相同并不代表字节相同**。较大的 `libcc.so` 存在可见字节差异；未调查修改来源，也未触碰文件。

以下静态证据全部在与 AppImage 一致的 `libcc.so.bak` 上重新复核。ELF 共享库地址是 ELF 虚拟地址，并非进程 ASLR 之后的绝对地址。

## S1：明确的上层会话接口（S）

```bash
nm -D -C temp/navicat/squashfs-root/usr/lib/libcc.so.bak \
  | grep -E 'CCNavicat::(acquireConnectedSession|releaseConnectedSession|runSQLWithSession|beginUseConnection|endUseConnection|openConnection|closeConnection|switchConnectionProfile|stopQuery|cancelOperation)'
```

| 动态导出函数 | ELF 虚拟地址 |
| --- | --- |
| `CCNavicat::acquireConnectedSession(CMObject*)` | `0x6088c70` |
| `CCNavicat::releaseConnectedSession(CMObject*, CMObject*, bool)` | `0x60857c0` |
| `CCNavicat::runSQLWithSession(...)` | `0x6089940` |
| `CCNavicat::beginUseConnection(...)` | `0x6131eb0` |
| `CCNavicat::endUseConnection(CMObject*)` | `0x60e4930` |
| `CCNavicat::openConnection(...)` | `0x6130fc0` |
| `CCNavicat::closeConnection(CMObject*)` | `0x60fc810` |
| `CCNavicat::switchConnectionProfile(...)` | `0x60e4a90` |
| `CCNavicat::stopQuery(ICSForm*)` | `0x609ff30` |
| `CCNavicat::cancelOperation(int)` | `0x60ef200` |

**能确定：** Navicat 的业务层存在独立的获取/释放连接会话，以及带会话执行 SQL 的接口，而非只有单一的裸 MySQL 客户端函数。

**不能确定：** 返回一个会话对象是否新建物理 TCP 连接、租借何时结束、是否按 SQL 标签独占、是否对会话重置或自动提交。

## S2：会话池、保活与隧道分层线索（S）

在已校验的 `libcc.so.bak` 中，以 `strings -a -t x -n 7` 查到下列**原始文件偏移**（不是函数入口地址）：

| 偏移 | 字符串证据 | 最小可证结论 |
| --- | --- | --- |
| `0x6057570` | `CSConnAction::acquireConnectedSession(...CMConnSession**...)` 的 lambda 标识 | 内部连接操作涉及 `CMConnSession` |
| `0x5ff6650` | `CSConnAction::endUseSessionPool(CMServer*)` 的 lambda 标识 | 存在命名为 SessionPool 的生命周期操作 |
| `0x187dcb0` | `COConnKeepAliveThreadable` | 存在保活相关任务类型标识 |
| `0x19b1b00` | `CSSSHTunnelPoolCenter` | 存在 SSH 隧道池中心类型标识 |
| `0x5f1d9a8` | `Conn.SessionLimit` | 存在连接会话上限设置键 |
| `0x5f10cc6` | `Conn.UseKeepAlive` | 存在保活开关键 |
| `0x5f25c8d` | `Conn.KeepAliveInterval` | 存在保活间隔设置键 |
| `0x5ecfb02` | `Conn.IsAutoConnect` | 存在自动连接设置键 |

**证据强度说明：** 已确证这些类型/名称存在于原始二进制，不能仅由字符串断言保活任务一定被启动、限制数值精确约束、或所有 DBMS 都共用一个池。

## S3：客户端 MySQL API 与终止操作（S）

原版 `libcc.so.bak` 保留 `LoadMySQLLibraries()`、`LoadMySQLLibraries_Silent()` 导出符号，并带有 `mysql_real_connect`（`0x5ec882d`）、`mysql_thread_id`（`0x5f14868`）、`mysql_close`（`0x5f1d34e`）、`mysql_ping`（`0x5f2c44d`）等 API 名称字符串；AppImage 打包了 MariaDB/MySQL 客户端兼容库。此证据支持动态客户端 API 装载机制，**尚不足以证明每个 API 在指定流程下必定被调用**。

原版 `CCNavicat::stopQuery(ICSForm*)`（`0x609ff30`）反汇编为对表单实例做类型转换后，通过其 vtable 偏移 `0x58` 跳转。意味着该 API 本身是转发入口，**不能仅凭上层入口确认最终使用 `KILL QUERY`、连接关闭或特定 MySQL 协议命令**。

原版 `releaseConnectedSession`（`0x60857c0`）也包含若干虚函数派发；仍需查明目标具体类型和释放语义。

## O：已有原版 UI 观察（非本轮新的连接行为实验）

- `docs/MYSQL_ADVANCED_DIFFERENTIAL_2026-10-21.md`：保活默认未勾选、间隔 240 秒；连接超时 30 秒；写入超时 30 秒；连接会话上限默认显示 10 且未启用。
- `docs/MYSQL_ACTIVE_CONFIGURATION_DIFFERENTIAL_2026-10-22.md`：活动配置切换、取消回滚、保存恢复。
- `docs/MYSQL_SSH_TAB_DIFFERENTIAL_2026-10-21.md`：SSH 隧道设置的界面联动。

这些仅证实**UI/配置**，未包含 MySQL 服务端连接 ID、并发会话数和断线恢复的 O 证据。

## 不能宣称已经确定的运行时事实（Unknown）

1. 同一 MySQL 连接下面的两个 SQL 编辑标签是否使用不同 `CONNECTION_ID()`。
2. 事务、临时表、`SET @user_variable` 的作用域是否持续绑定某个标签。
3. 元数据刷新与正在执行的长 SQL 是否使用独立 MySQL 会话。
4. `Conn.SessionLimit` 限制的对象、行为是阻塞/拒绝/排队/复用，以及是否包含 SSH 隧道。
5. `COConnKeepAliveThreadable` 是否定时 Ping、是否因网络波动暂停/恢复，以及保持哪个会话。
6. MySQL 断线后是否静默重连，以及是否保留事务状态或向用户显式提示会话失效。
7. 停止长查询时具体的协议行为、服务端副作用和是否需要第二条控制连接。

## 下一步原版行为实验（须独立授权启动原版及使用隔离 fixture）

使用一次性本机回环 MySQL 8.4 容器、无生产秘密。先独立建立两个 SQL 编辑标签 A/B；记录每个标签执行 `SELECT CONNECTION_ID(), DATABASE(), @@autocommit;`，以及同时通过只读监控 `SHOW PROCESSLIST` 记录 ID。然后：

1. **会话身份**：A/B 各重复 `CONNECTION_ID()`，关闭重开某一标签，记录 ID 变化。
2. **变量隔离**：A 执行 `SET @nyadb_probe = 'A';`；B 查询 `SELECT @nyadb_probe;`，A 再查询，判定会话状态隔离。
3. **事务隔离**：专用可回滚 fixture 中 A 开启事务并修改记录，B 查询及修改另一记录，分别 `ROLLBACK`，验证副作用边界；禁用自动提交/限制权限以保证不会修改共享数据。
4. **并发与池**：A 执行 `SELECT SLEEP(10);`，B 同时查询，另一工作区刷新对象列表；分别在连接会话限制 1/2/10 下记录持续时间与服务端连接数。
5. **保活与断线**：在隔离 server 下缩短 `wait_timeout`，用 server-side `general_log` 或 Performance Schema 查看定时动作；重启仅测试容器，观察界面变化和用户需否手动重连。
6. **取消语义**：A 运行 `SELECT SLEEP(20);` 后点击停止；观察 server-side 命令、A/B 的 ID，以及下一条查询是否仍可复用原会话。
7. **Active Profile 切换**：切换同一连接的活动配置，观察原工作区保留/断开/恢复以及凭据更新时的状态。

为区分 S/O/V，请保存每一步截图、`CONNECTION_ID()`、`SHOW PROCESSLIST`、MySQL server 版本、协议日志、fixture 哈希和原版程序/核心库哈希。**完成这些实验前，不应把“Navicat 每个 SQL 标签独占一条 MySQL TCP 连接”写为 O/S 事实。**

## NyaDB 独立改造启示（设计建议，不是对原版源码的复制）

- 将 `Profile`、`ConnectionRuntime`、`SessionLease` 和物理 `sql.Conn` 概念区分。
- 交互式 SQL 工作区的事务/变量/临时表必须受明确的物理会话所有权约束。
- 只读元数据操作可用有界共享池，避免一个长查询堵塞所有资产刷新。
- 保活、重连、取消、失败恢复必须有独立状态机；不能静默重放未知执行结果的写 SQL。
- 这一改造建议基于 NyaDB 自身逻辑风险和 Navicat 已证实的会话抽象**共同提出**，不声称已获得 Navicat 内部算法实现。

本轮只读研究；没有修改 `temp/navicat/`，没有启动 Navicat，没有测试生产数据库；本文件之外无正式代码修改。
