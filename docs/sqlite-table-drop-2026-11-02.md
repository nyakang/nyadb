# SQLite 原生表删除 — 2026-11-02

本项实现 **I**：正式 SQLite 删除入口、原结构快照/SQL 预览、名称确认、事务执行、回执和工作台清理。SQLite 表复制、视图 CRUD、PG 正式 Apply/表单和完整目标中的其余功能继续待实施。没有运行 Navicat；不新增原版 O/S/V。

## 正式实现

- [中立可选 binding](../internal/db/contract/schema_design.go) 在既有 SchemaDesign 上增加 CanDrop/Drop，保留 Snapshot/Apply 和存储格式。MySQL 继续使用既有表操作服务，PG 的 binding 仍只读。
- [SQLite 执行](../internal/services/engines/sqlite_drop.go) 在原 session/schema gate 上使用专属物理连接，以 BEGIN IMMEDIATE 锁定写入，复核原 namespace 的 schema_version、完整有序 sqlite_schema、根页和表结构签名。SQL 由服务重建，修改预览不能跳过核验。
- 删除前禁用并验证 foreign_keys，避免 SQLite DROP TABLE 的隐式行删除/级联；事务外恢复原 PRAGMA，恢复失败丢弃连接。失败区分 not_executed、rolled_back、committed、uncertain；取消等候驱动/回滚确认，不自动重试。
- 拒绝其它表的入站外键，包括空表及大小写引用；表自身外键、显式索引和触发器随原表删除。删除前后验证剩余视图、表触发器和 INSTEAD OF 视图触发器：EXPLAIN 只编译程序，不运行 DML。未知 UPDATE OF 字段不能通过“触发器未被选中”逃过检查。
- [运行时封装](../internal/services/dbruntime/schema_design.go) 翻译公开/驱动 generation，复制请求与回执，拒绝另一对象/代次或仍存在表的伪成功回执；迟到提交保留原范围。
- [原生弹窗](../internal/ui/objectworkspace/schema_drop.go) 归 objectworkspace 所有。右键和导航树/网格 Delete 共用 BeginTableChange；删除、清空、截断、复制分别判断可用性，不给 SQLite 开启 MySQL 管理能力。显式读取、只读 SQL 预览、原名称确认后才执行。
- Shell BeforeDrop 同时检查原表的数据草稿、事务和设计草稿；守卫期间预览让出模态范围。关闭或启动另一表操作撤销原延迟确认，关闭工作台撤销全部回调。工作线程持有不可变请求和私有缓冲通道，UI 消费回执。
- 已核验提交仅清理原限定对象的标签、树选择和元数据。其它表、profile/schema 和 SQL 文本保留；不确定/无效提交只撤销原公开会话，不关闭替代连接。

## 安全边界

仅支持现有 SQLite 普通表解析器可以完整读取的表，系统表和虚拟表不提供执行。没有 DROP CASCADE、自动依赖改写或回滚已确认提交。原 namespace 的无关 DDL 也会导致修订冲突，须重新读取。预览捕获结构修订；确认删除的是该表提交时的数据，未冻结预览时的行集合。

有任意 TEMP 视图/触发器时保守拒绝删除，SQL 租约未结束的手动事务同样拒绝。现存不能编译的剩余对象/外键不一致也会阻止操作，不自动修复。目录/编译核验不表示第三方扩展模块、外部应用或 Navicat 的行为相同。

## 自动和真实 SQLite 验证

使用 Go 1.27.3、固定 MyGo fork、GOWORK=off、独立 SQLite 临时文件。

- 真 SQLite：原表/索引/触发器/自增记录删除、删除不运行自己的触发器、入站级联/空表 FK、视图链、INSERT/UPDATE/DELETE/WHEN 0 触发器、可写视图及无效 UPDATE OF、同名附加库/只读库、原修订/替换/伪快照、真实写锁等待取消、SQL 租约事务和跨库 TEMP 程序。
- runtime：公开代次转换、深拷贝、撤销、迟到提交、错误范围和“表仍存在”的回执。
- ui.NewTester：预览不执行、名称确认、脏文档守卫、取消等候确认、失败保留状态/重新读取、关闭/新操作后的延迟确认与回执、parked 原范围、禁用截断/复制/不完整 binding、旧菜单代次。
- Shell + 真 SQLite：导航树 Delete、右键、840×560 弹窗、脏设计守卫、原表标签/缓存/树节点清理、其它表保留及旧公开回执不撤销当前连接。

验证日志在忽略目录 `build/verify/`，结果与范围分开记录：

| 检查 | 结果/证据 |
|---|---|
| Go 全量测试 | 通过，最终 Shell 88.796s；`sqlite-drop-final-all-tests.log` |
| engines / dbruntime / schemaddl / objectworkspace 全量 race | 全部通过；`sqlite-drop-feature-race.log`，分别 4.386s / 1.073s / 3.278s / 21.452s |
| Shell 全量生命周期 race | 通过，826.018s，20m 超时；`sqlite-drop-shell-race.log` |
| 最终新增服务/运行时/纯规则 race | 通过；`sqlite-drop-final-race.log`，engines 2.001s / dbruntime 1.034s / schemaddl 1.032s |
| 最终 UI 范围 race | objectworkspace 2.681s、Shell 56.701s，全部通过；`sqlite-drop-final-ui-race.log` |
| dataworkspace 相关生命周期 race | 关闭/代次/租约/事务离开正则集合通过，2.228s；`sqlite-drop-data-lifecycle-race.log`。本项未运行全量 dataworkspace race |
| Go vet / MyGo vet | 通过，`sqlite-drop-go-vet.log` / `sqlite-drop-mygo-vet.log` |
| 翻译生成/检查 | 通过，1421 条中英消息；`sqlite-drop-generation.log` / `sqlite-drop-i18n.log` |
| MyGo 正式构建 | 通过，`sqlite-drop-build.log` |
| Python 采集工具测试 | 56 个通过，4.037s；`sqlite-drop-python.log` |

全量 race 在最终 TEMP/视图 UPDATE OF 及延迟 UI 回调修复前启动；最后的实际服务与 UI race 针对性集合覆盖这些修复，不把此前二进制的全量 race 宣称为最终逐项重跑。最终普通全量测试覆盖最后源码。真实服务结果来自 SQLite 临时文件；runtime/Tester 注入测试单独列出，未用其替代真实数据库或原生窗口结果。

## 正式 Linux 窗口

```
GOWORK=off go tool mygo build
GOWORK=off /usr/bin/python3 tools/verify_entry_acceptance.py -- \
  /usr/bin/python3 tools/capture_sqlite_drop_acceptance.py
```

[采集工具](../tools/capture_sqlite_drop_acceptance.py) 创建/验证 owned run 的附加库依赖 fixture，运行正式二进制并复核 /proc 哈希，记录 1280×800/840×560 预览、FK 拒绝和删除结果；对源及主库分别核对 schema 和行数据。调用器只清理自己的进程、容器和秘密文件，不安装系统包、不使用原用户密钥环、不连接生产数据库。

最终正式二进制 SHA256：`2c41a7d6a265b01b7cae950bfc5c340d5ca618eb8cfa00b84d66148252cfb0c4`。

最终 run `r-cf53dc447b24`，`custom-2100675b` exit 0；证据目录为 `build/entry-acceptance/r-cf53dc447b24/evidence/sqlite-drop-native/capture-fdbbc9111119/`。已查看实际 840×560 预览及 1280×800 结果截图；完整采集包括两种尺寸的预览/结果、原菜单、FK 拒绝、重新读取后的确认。`result.json` 记录源表预览不变、FK 失败时 schema/行不变、最终原表/索引/触发器删除以及主库同名表不变。原表标签/脏设计守卫由真 SQLite Shell + Tester 覆盖，未宣称该原生脚本也验证全部标签/草稿情形。

Linux NyaDB 截图和真实数据库侧复核不等于 Navicat 对照；字体/实际缩放保留 Unknown。Windows/macOS 和原版观察/差分尚未验证。隔离工具提供 Secret Service/MySQL，但此项没有重新验收凭据/查询加密恢复或 MySQL 管理，不能计为这些功能新增端到端通过。

首次采集缺少必填 loading 标记，exit 1 保留。随后两个 preview-only 探索因菜单/读取按钮坐标错误，没有形成完整验收。修正为原导航节点 Delete 和实际窗口坐标后，`r-c6f68293dff8/custom-b6b12d18` 首次完整验收 exit 0；后续守卫撤销修复后，最终正式程序以新 run 单独验收。探索和旧二进制截图不替代最终记录。

收尾回归另修复取消确认后按钮锁定、另一表操作保留旧延迟确认、宿主回执替换弹窗后旧收尾清理新弹窗的问题。曾失败的 Tester 文本读取/浏览器 fixture/缺失刷新帧均已修正并重跑；测试失败不被记为通过。

本项全部 owned runs `r-c6f68293dff8`、`r-9d03475b3825`、`r-cf53dc447b24` 的 Docker 容器、D-Bus/Secret Service/Xvfb/Openbox PID/birth 与秘密文件均经独立查询确认不存在。逐次结果在各 run 的 `evidence/sqlite-drop-cleanup-audit.json`。应用进程由脚本 finally 按原身份终止；工具按自己的资源范围清理，未改系统配置或用户密钥环。
