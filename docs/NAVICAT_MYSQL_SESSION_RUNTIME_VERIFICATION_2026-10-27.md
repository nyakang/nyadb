# Navicat 18 MySQL 会话行为：真实 MySQL 8.4 双标签运行验证（2026-10-27）

## 范围与证据

本轮在授权原版 Navicat Premium 18.0.2 的正常图形界面中，对**一次性 Docker MySQL 8.4.11** 开展实际实验。所有数据库操作限制于隔离的本机回环 fixture，使用独立用户和测试数据库；未连接生产数据库、未调试原版进程、未改动原版应用和系统其它项目。运行时观察和服务端数据记为 **O**；独立实现尚未对应修改，差分 **V 尚未进行**。

工作树基线 `master` @ `abe490a`。此前静态研究在 `docs/NAVICAT_MYSQL_SESSION_STATIC_ANALYSIS_2026-10-27.md`。当前报告仅记录此次实测，不将源码/符号推断认作运行事实。

## 隔离环境与原始性

- AppImage：`temp/navicat/navicat18-premium-cs-x86_64.AppImage`，SHA-256 `3b77fe1e3295dc3067402a0bab33a8ed1cbfbdea063d8b59e67c26a6480b6d63`。
- 动态核心库必须从 AppImage 内 `SquashFS`（偏移 663872）提取：`usr/lib/libcc.so` 原始 SHA-256 `6c45353fea04ba4b941aef2477fe9fa4997c77cee6380c3abc60f87f404e8e57`。只读验证并临时置于 `temp/research/navicat-session-fixture-20261027/pristine-lib/libcc.so`，在 `LD_LIBRARY_PATH` 中优先使用该字节一致文件；**没有覆盖现存、字节不同的解包目录 libcc.so**。
- Navicat 主程序 ELF 原版 `temp/navicat/squashfs-root/usr/bin/navicat`；独立 Xvfb `:183` + Openbox；隔离 `XDG_CONFIG_HOME` 和 `XDG_CACHE_HOME`。
- 连接库通过相对路径 `./usr/lib/libmariadb*.so` 加载：第一次启动没有将 cwd 指向 `temp/navicat/squashfs-root`，发生库找不到与原版 GUI SIGSEGV；**修复 cwd 后连接成功**，该初始异常不能当作原版 MySQL 8.4 不兼容证据。
- Docker 镜像 `mysql:8.4`；仅本次新建的容器 `nyadb-navicat-session-fixture-20261027`，`127.0.0.1:32777`（动态本机高端口）；试验结束后必须销毁，不能复用既有容器。
- 数据库 `nyadb_session_probe`；InnoDB 测试表 `tx_probe(id INT PRIMARY KEY, note VARCHAR(80))`，初始 `(1,'before'), (2,'before')`；独立账号 `nyadb_probe` 仅有 fixture 数据库 `SELECT` 和 `UPDATE` 权限。测试仅回环隔离容器使用一次性空口令。
- Navicat 正常连接 UI 显示“连接成功 服务器版本: 8.4.11”（`connection-test2-result.png`）。创建独立原版连接档案 `NyaDB Session Fixture 20261027`，保留其它历史档案不动。

## O1：同一连接档案的两个 SQL 查询标签具有不同物理 MySQL 会话

操作：在已连接的同一档案下依次新建标签 A、B，分别执行：

```sql
SELECT CONNECTION_ID() AS cid, DATABASE() AS db, @@autocommit AS ac;
```

| SQL 工作区 | cid | db | ac | 同一时刻仍在线 |
| --- | --- | --- | --- | --- |
| A | **14** | NULL | 1 | 是 |
| B | **19** | NULL | 1 | 是 |

服务端 `SHOW PROCESSLIST` 同时显示两条 `nyadb_probe` 会话，来源分别为两个不同 TCP 端口。关键截图：`query-a-id-after.png`、`query-b-id.png`；相应服务端结果已于运行时记录。

结论（O）：本次 Navicat 18.0.2 在**同一连接档案、同时开启两个查询标签**时保持两条独立的服务端 MySQL 会话。不能由此推广“任何情况下每个标签永远独占 TCP 连接”。

## O2：用户变量在标签之间隔离，且随标签保留

1. A（cid=14）执行 `SET @nyadb_probe = 101;`，返回 Success 1、Error 0；截图 `a-set-marker.png`。
2. B 执行 `SELECT @nyadb_probe AS marker, CONNECTION_ID() AS cid;`，得到 **NULL、19**；截图 `b-marker-result-open.png`。
3. 切回 A 执行相同读取，得到 **101、14**；截图 `a-marker-verified.png`。

结论（O）：不同查询标签未共享该用户变量，跨查询仍保留各自的会话状态。

## O3：同一档案的两条 SQL 可以真实并行

在 A 执行：

```sql
SELECT SLEEP(20) AS a_wait, CONNECTION_ID() AS cid;
```

在 A 查询执行过程中切换 B 执行：

```sql
SELECT SLEEP(4) AS b_wait, CONNECTION_ID() AS cid;
```

**同一份服务器 `SHOW FULL PROCESSLIST`** 实测：

```text
Id  User         Command  Time  State        Info
14  nyadb_probe  Query       2  User sleep   SELECT SLEEP(20) AS a_wait, CONNECTION_ID() AS cid
19  nyadb_probe  Query       1  User sleep   SELECT SLEEP(4) AS b_wait, CONNECTION_ID() AS cid
```

截图 `concurrency-simultaneous.png` 记录同一时刻 UI。第一次 SLEEP(9) 的样本没抓到重叠，**不作为并发证据**；上述第二次采样有效。

结论（O）：两条 SQL 通过各自服务端会话真正并发执行，不是一个池连接的串行排队。

## O4：跨多次执行保留事务，另一标签不能读取未提交修改

A（cid=14）执行（Navicat 显示处理两条语句、两条成功，UPDATE 影响 1 行）：

```sql
START TRANSACTION;
UPDATE nyadb_session_probe.tx_probe
SET note = 'pending_a'
WHERE id = 1;
```

服务端 `information_schema.innodb_trx` 查询确认 `trx_mysql_thread_id=14`, `trx_state=RUNNING`；另一个管理员连接读取表内容仍为 `before`。

随后 B（cid=19）执行：

```sql
SELECT note, CONNECTION_ID() AS cid
FROM nyadb_session_probe.tx_probe WHERE id = 1;
```

B 得到 **before、19**（`b-tx-read-verified.png`）；切回 A 执行同样查询得到 **pending_a、14**（`a-own-uncommitted-verified.png`）。

A 最终通过 Navicat 执行 `ROLLBACK;`；服务端后验再次读取表两行均为 `before`，`innodb_trx` 不再包含事务线程 14。回滚截图 `a-rollback.png`；只有一次性 fixture 被修改且已恢复，未触及共享/生产库。

结论（O）：Navicat 查询标签保存独立的事务上下文，而且不会把 A 的未提交写入泄露给 B。

## O5：关闭标签、断开和删除临时连接的生命周期

- 关闭 B、再关闭 A：对于未保存的临时 SQL 文本，原版弹出“是否保存更改？”并选择“不保存”。**关闭两个查询标签后，服务端连接 14 和 19 均从 processlist 消失**，但单独用于档案浏览的 MySQL 会话 31 仍可保留。截图 `after-query-tabs-close.png`。
- 连接档案仍被 SQL 标签使用时尝试删除，Navicat 报错 “Server is in use and cannot be deleted.”，截图 `profile-deleted.png`；这说明删除连接受到在用资源保护。
- 关闭相关标签并断开连接后，processlist 中不再有 `nyadb_probe`，然后通过正常 Navicat UI 删除**仅本轮创建**的 `NyaDB Session Fixture 20261027`（`profile-deleted-success.png`），不影响此前的 `NyaDB Alignment Fixture`。

**边界**：看到关闭标签后服务端连接消失，只能确认这两个工作区在此次场景中释放了物理连接，不能宣称 Navicat 无连接池或完全没有可复用会话机制。

## NyaDB 对照（源码设计差异，尚非完整 V）

当前 `internal/services/connections/mysql_connect.go` 的每档案一个 `sql.DB`：

```go
db.SetMaxOpenConns(1)
db.SetMaxIdleConns(1)
db.SetConnMaxLifetime(3 * time.Minute)
```

`internal/services/connections/mysql_sql.go` 的每次 SQL 执行通过 `session.db.Conn(ctx)` 暂借连接，执行后 `defer conn.Close()` 归还池，不是按 SQL 编辑标签长期固定拥有服务端会话。配合 `MaxOpenConns(1)`，一档案内部多个 SQL 标签的执行会互相争抢唯一物理连接；跨次查询可能在同一服务器会话中混合手动事务、临时表及用户变量。这与此次原版 O1–O4 对应的**实际场景**不一致。

**建议设计**：基于现有 `ProfileID` 生命周期管理，增加每 SQL 标签持有的有状态 `sql.Conn` / `SessionLease`；把可安全共享的元数据/列表只读任务交给受连接数总预算限制的单独池；显式保护未提交事务、断线/重连重建和错误处理；应用退出时按所有者可靠释放。不能不加控制地为无限标签开放无限物理连接。此处仅是 NyaDB 的架构建议，并非复用 Navicat 专有实现。

## 尚未完成、不可宣称 O 的实验

- `Conn.SessionLimit` 为 1/2/10 时的实际拒绝、等待或复用规则；
- `KeepAlive` 的真实 Ping 周期和空闲超时处理；
- 中途重启 MySQL/网络断开后的恢复、事务失效提示；
- 取消 `SELECT SLEEP` 及 DML 时是否发送 `KILL QUERY` 或关闭连接；
- 更多标签与频繁开闭时的池复用、物理连接是否能被其它标签再租借；
- NyaDB 与原版同 fixture、同状态、同工作量完整差分 V，Windows/Linux UI 截图及性能对照。

## 截图与文件

所有本轮截图、独立原始核心库只读提取副本、启动脚本、隔离 XDG 配置及日志在 Git 忽略目录：

`temp/research/navicat-session-fixture-20261027/`

其中 `start.sh` 是本轮原始尝试（工作目录不正确）；`restart-cwd-fixed.sh` 是修正之后使用的启动方式。未经清理/授权，不应反复运行原始脚本；未来需要重做时务必使用与原始 AppRun 相同的根目录 cwd 且重建唯一的一次性容器和临时 XDG。原版 AppImage/原始解包资源未修改。

**本轮仅研究与证据记录，没有 NyaDB 正式实现代码变更，也没有 commit 或 push。**
