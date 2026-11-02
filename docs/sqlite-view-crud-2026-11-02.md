# SQLite 原生视图管理 — 2026-11-02

本项实现 **I**：SQLite 视图创建、完整定义编辑、改名、删除、事务依赖核验及原范围工作台协调。Go 全量检查、相关 race 及正式 Linux 完整验收通过；没有启动 Navicat，不新增原版 O/S/V。PG 正式 Apply/表单、对象管理及文件工具和完整高级目标继续进行。

## 实现与边界

- [独立规划器](../internal/db/schemaddl/sqlite_view.go) 解析完整普通 CREATE VIEW、显式字段列表和 SELECT/WITH/VALUES 正文。拒绝 TEMP、多语句、重复 ASCII 字段、无效或外部命名空间、系统名称及超过界限的定义。创建/修改不能隐含改名或修改触发器；改名保留查询和字段，只改写自有触发器的 CREATE/ON 头部，正文保持原文。
- [中立接口](../internal/db/contract/view_design.go) 提供独立可选 ViewDesign binding。没有迁移、凭据或持久格式变化；SQLite 不获得 MySQL 管理能力，PG 仍只读。
- [真实服务](../internal/services/engines/sqlite_view.go) 在原会话 schema gate、专属连接和 BEGIN IMMEDIATE 内复核完整 namespace 修订、原定义和目标不存在状态。DROP/CREATE/自有触发器恢复在同一事务；前后编译其它持久视图及表/视图程序，验证完整最终定义、触发器和原名消失后才提交。外部依赖失效时整批回滚，不自动改写引用或覆盖对象。
- 拒绝只读附加库、未结束手动事务和原会话租约/写连接的 TEMP 程序。真实锁等待可取消；取消等待执行器回执。无法确认回滚/提交时保守报告 uncertain、丢弃原物理连接，UI 禁止自动重试。
- [runtime](../internal/services/dbruntime/view_design.go) 翻译公开及驱动代次，深拷贝请求和回执，拒绝错误目标/存在状态的成功回执；迟到结果保留原身份。
- [原生对象工作区](../internal/ui/objectworkspace/view_management.go) 使用完整定义文本编辑器、SQL 预览和独立确认按钮；删除必须输入原视图名。创建名称变化使旧不存在快照失效，必须显式重新读取。失败保留输入，重新读取后才可再次预览；修改时重读保留脏正文。菜单捕获原 profile/namespace/上下文及公开代次，旧选择不能操作新范围。
- 异步线程捕获不可变请求、绑定接口和私有缓冲通道，只有 UI 消费回执。切换/断开将未执行定义草稿停放在内存，原连接恢复后显式继续；断开、其它操作进行中或能力不足时恢复入口禁用。恢复代次变化清除旧快照/预览，不执行 SQL。关闭或取消草稿守卫撤销旧 continuation。
- [Shell 协调](../internal/ui/shell/view_management.go) 只接受原公开/上下文代次。确认提交刷新原数据库缓存；修改刷新对应数据和结构文档，改名/删除清理原限定视图标签和导航选择。SQL 文本、源表草稿/标签及其它数据库的同名对象保留；不确定回执只撤销原 runtime 句柄。

这是普通 SQLite 视图管理，不包括 TEMP 视图、自动依赖重写、跨 namespace 迁移、专门图形查询设计器或持久化视图设计草稿。改名含依赖的视图可能被拒绝；需显式处理依赖并重新读取。原 namespace 的无关 DDL 也会使修订冲突。原正文中的名称保持原文，不能将触发器头部改名解释为任意 SQL 语义重写。

## 自动测试及真实 SQLite

Go 1.27.3 / MyGo v0.7.3-nyadb.2，GOWORK=off，自有 SQLite 文件。

- 服务：完整 CRUD、显式字段/CTE/字面量分号、INSTEAD OF 触发器实际执行及头部/正文保留，依赖改名/删除拒绝、错误替换回滚、修订冲突、伪快照和已有目标拒绝、附加库/只读隔离与真实锁等待取消。
- runtime：公开代次翻译、请求/回执深拷贝、迟到改名回执、错误身份/存在状态拒绝。
- ui.NewTester：预览不执行、不可变输入、旧预览/guard 拒绝、取消等待回执、关闭/停放结果、原连接恢复、删除名称确认、失败保留正文、旧公开菜单代次和只读禁用。
- Shell + 真 SQLite：通过正式菜单创建/修改/改名/删除，840×560 弹窗，数据文档实际重载、限定标签清理、源表及其它范围标签/SQL 保留、源表脏数据保留、坏定义回滚和旧/不确定回执。

| 检查 | 结果与 build/verify 中的日志 |
|---|---|
| 最终 go test ./... | 通过，Shell 101.935s；`sqlite-view-final-all-tests.log` |
| schemaddl / engines / dbruntime / objectworkspace 全量 race | 通过，4.694s / 7.206s / 1.084s / 26.875s；`sqlite-view-feature-race.log` |
| 最终视图 UI race | 通过，2.658s；`sqlite-view-final-ui-race.log`，含断开及并发恢复入口回归 |
| Shell 全量生命周期 race | 通过，1042.804s；`sqlite-view-shell-race.log`，20m 超时 |
| 最终视图 Shell race | 通过，123.908s；`sqlite-view-final-shell-race.log`，真实 SQLite 菜单及标签生命周期 |
| dataworkspace 相关生命周期 race | Guard/Leave/Close/Generation/Lease/Transaction 通过，5.258s；`sqlite-view-data-lifecycle-race.log`，未运行该包全部 race |
| Go vet / MyGo vet | 通过；`sqlite-view-final-go-vet.log` / `sqlite-view-final-mygo-vet.log` |
| 翻译生成 / 检查 | 通过，1456 条中英消息；`sqlite-view-generation.log` / `sqlite-view-i18n.log` |
| 最终 MyGo 正式构建 | 通过；`sqlite-view-final-build.log` |
| 最终 Python 采集工具测试 | 61 个通过，4.042s；`sqlite-view-final-python.log` |

Shell 全量 race 及 feature 全量 race 在最后草稿恢复可用性修正前启动。最终 UI race、视图 Shell race 和普通全量测试覆盖该修正；不声称早期 Shell race 二进制含最后 UI 改动。

## Linux 正式窗口

```sh
GOWORK=off go tool mygo build
GOWORK=off /usr/bin/python3 tools/verify_entry_acceptance.py -- \
  /usr/bin/python3 tools/capture_sqlite_view_acceptance.py
```

[工具](../tools/capture_sqlite_view_acceptance.py) 使用 fresh owned run、正式二进制和真实系统密钥环隔离基础设施，在主库与 aux 附加库各准备同名源表，仅操作 aux。核对 /proc/PID/exe 的实际 hash，记录 PID/birth/exit。验证创建/修改/依赖拒绝改名/重读后改名/删除及两个尺寸的预览和最终结果；每步另从 SQLite 文件核对字段、行、自有触发器和源表/主库。

最终二进制 SHA256：`1cde3a5eb675a07ecd1739d2615186ee9f1077d7c3ec9f03e734863aecc3fc8d`。完整 run `r-b43a128d877a` / `custom-bad7d66f` exit 0。证据位于 `build/entry-acceptance/r-b43a128d877a/evidence/sqlite-view-native/capture-76eecf04a8f7/`，`result.json` 确认全部 CRUD、依赖改名回滚、重读恢复、自有触发器保留、预览不执行及源表/主库保持不变。

已查看最终 [840×560 编辑预览](../build/entry-acceptance/r-b43a128d877a/evidence/sqlite-view-native/capture-76eecf04a8f7/alter-preview-840-client.png)、[依赖改名拒绝](../build/entry-acceptance/r-b43a128d877a/evidence/sqlite-view-native/capture-76eecf04a8f7/rename-refused-result-1280-client.png)、[删除名称确认与预览](../build/entry-acceptance/r-b43a128d877a/evidence/sqlite-view-native/capture-76eecf04a8f7/drop-preview-1280-client.png) 和 [840×560 删除结果](../build/entry-acceptance/r-b43a128d877a/evidence/sqlite-view-native/capture-76eecf04a8f7/drop-result-840-client.png)。定义及 SQL 预览是可滚动区域，单张截图仅显示其当前视口；完整计划由服务/Tester 和数据库结果核对。截图中的“确认”元数据只记录执行请求，成功以独立 result 为准，拒绝结果不记为已提交。

最终应用 exit 0；原 app/D-Bus/Secret Service/Xvfb/Openbox 的 PID birth、Docker 容器及秘密文件均独立确认不存在，见 run 的 `evidence/sqlite-view-cleanup-audit.json`。只清理本次创建的资源，没有修改系统配置或用户密钥环。

探索 run `r-6977b218826b` / `r-d7b2645baeed` 仅采菜单；`r-2c6a625e4187` 仅预览定位且第一次预览点击未命中，未显示生成预览。`r-13cce700f28c` 使用最终二进制确认创建、840×560 SQL 预览及视图菜单，但未运行完整 CRUD。这些脚本 exit 0 均不替代完整验收 result；已分别独立核对应用/服务原 PID birth、容器不存在和 secrets.json 删除，结果保存在各 run 的 `evidence/sqlite-view-cleanup-audit.json`。

原生脚本未覆盖所有草稿/租约、锁等待或切换代次操作，后者由真实 SQLite 服务、Shell 和 Tester 回归覆盖。未重验系统密钥环跨进程恢复/MySQL 管理，不因隔离基础设施启动而新增这些通过结果。Windows/macOS 原生窗口、字体/缩放及 Navicat O/V 仍未验证。
