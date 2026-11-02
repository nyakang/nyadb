# SQLite 原生表复制 — 2026-11-02

本项实现 **I**：正式 SQLite“复制表 → 仅结构 / 结构和数据”入口、原结构读取、SQL 预览、事务复制、回执及工作台协调。SQLite 视图 CRUD、PG 正式 Apply/表单、导入/备份、跨库工具和其它完整目标继续待实施。没有启动 Navicat，不新增原版 O/S/V。

## 正式实现

- [纯复制规划](../internal/db/schemaddl/sqlite_copy.go) 深拷贝完整字段、约束、选项、显式索引和触发器。只替换 CREATE 名称及头部 ON 目标；注释、字符串和触发器正文保持原文。默认仅将原表的自引用外键绑定到目标，也可选择保留原引用。新索引/触发器名称由目标及原名称稳定生成，超长名称保留合法 UTF-8 前缀与摘要。
- [可选中立接口](../internal/db/contract/schema_design.go) 在现有 SchemaDesign 增加 CanCopy/Copy 和可克隆请求；没有数据库迁移、凭据格式变化或 SQLite 的 MySQL 管理能力扩张。MySQL 继续使用既有服务，PG binding 仍只读。
- [真实执行](../internal/services/engines/sqlite_copy.go) 使用原会话 schema gate 和专属连接、BEGIN IMMEDIATE，重新检查原 namespace 的完整快照/修订/结构，拒绝已有目标与重名定义。服务重新生成 SQL，不接受修改预览绕过复核。
- 默认保留隐藏 rowid；普通 INTEGER PRIMARY KEY、别名遮蔽、inline PRIMARY KEY DESC 和 WITHOUT ROWID 分别处理。所有 rowid 别名都被遮蔽且没有真实别名主键时，必须显式取消保留 rowid 才能复制数据。字段判重使用 SQLite 的 ASCII 规则，`K` 与 `K` 保持不同字段。
- 数据通过 INSERT SELECT 完整复制，生成字段由 SQLite 重新计算。流式核验全部字段的存储类别、精确字节/int64/IEEE 表示及重复行数；不经过网格截断或 SQL CAST。含数据复制保留原自增高水位、缺失状态及合法人工设定值；仅结构复制重置自增状态。重复或非整数自增记录明确拒绝。
- 复制数据后才创建目标触发器，避免复制过程执行它们；外部外键和触发器正文中的原对象引用保持不变。事务前后验证外键和程序，复制不删除入站引用或源表。事务外恢复并复核 foreign_keys，恢复失败丢弃该物理连接。
- [runtime](../internal/services/dbruntime/schema_design.go) 翻译公开/驱动代次，深拷贝请求和回执，拒绝错误 profile/catalog/schema/目标/代次或不存在目标的成功回执。迟到提交仍属于原范围。
- [原生工作区](../internal/ui/objectworkspace/schema_copy.go) 捕获不可变目标、选项、快照及操作接口；私有缓冲通道由 UI 消费。预览不执行 SQL，名称去除首尾空白，失败保留输入并要求显式重读。取消等待执行器确认，复制与删除互斥；关闭、换连接或替换弹窗撤销旧读取/确认。
- Shell 检查原表的数据草稿、事务和设计草稿，保留源标签及租约。保存源设计后旧复制修订会冲突；保存数据后确认复制提交时的源行。取消离开决定会恢复复制/删除预览，并撤销该次延迟确认，防止预览一直隐藏。仅核验提交刷新原数据库缓存；不确定或无效提交仅撤销原公开句柄，保留草稿，不关闭替代连接。

## 边界

仅支持现有解析器可完整读取的 SQLite 普通表，不支持系统/虚拟表、跨库复制、自动覆盖、自动依赖改写或自动重试。SQL 租约的未结束手动事务、TEMP 程序、只读库或无效外键/程序会阻止操作。原 namespace 的无关 DDL 也会导致冲突，必须重新读取。

预览固定结构修订，不固定行集合；数据复制读取事务获得写锁时的源内容。触发器的显式源引用在目标上仍指向源，这是预览说明的保留行为；没有推断或重写任意 SQL 正文。

中断可能使 SQLite 自行回滚，后续显式 ROLLBACK 返回“没有活动事务”。没有确认回滚时服务保守报告 uncertain、丢弃物理连接并禁止 UI 重放；验收另从存储检查目标不存在、源行未变，不把该检查改写为驱动已确认回滚。

## 自动测试与真实 SQLite

Go 1.27.3 / MyGo v0.7.3-nyadb.2，GOWORK=off，自有 SQLite 临时文件。

- SQLite：完整字段/约束/索引/触发器/STRICT、生成值、超过 1 MiB 的 BLOB、NULL/int64/精确数值文本、重复行及 rowid、WITHOUT ROWID、自引用选择、自增缺失/负值/低于最大值/异常记录、重名目标与定义、原修订/伪快照、附加库/只读隔离、真实锁等待取消。
- 插入中途取消使用测试独占 SQLite 驱动的确定性 STORED 函数触发 context 取消；实际事务/复制/核验/回执仍使用正式服务。普通 fixture 使用正式连接初始化。函数只用于确定中断发生在 INSERT，不能算扩展函数原生兼容验收。
- runtime：代次翻译、请求隔离、迟到提交、撤销及错误目标回执。
- ui.NewTester：结构/数据菜单、预览、原身份/选项、草稿守卫、输入变化、取消确认、失败重读、parked/关闭/新弹窗回执、旧菜单代次及能力不足时禁用。补取消离开决定后的预览恢复和过期 continuation 回归。
- Shell + 真 SQLite：两种菜单复制、840×560 弹窗、源数据/设计标签保留、缓存刷新、设计保存后的修订冲突、数据取消/保存后再复制、旧公开回执不撤销替代连接、不确定结果保留草稿。

| 检查 | 结果 / 忽略目录 build/verify 中的日志 |
|---|---|
| 最终 go test ./... | 通过，Shell 89.630s；`sqlite-copy-final-all-tests.log` |
| schemaddl / dbruntime / objectworkspace 全量 race | 通过，4.107s / 1.090s / 26.060s；`sqlite-copy-feature-race.log`。该批 engines 的早期失败另列，不将整批标为通过 |
| 最终 engines 全量 race | 通过，5.640s；`sqlite-copy-engine-final-race.log` |
| 最终复制/删除 UI race | 通过，5.578s；`sqlite-copy-ui-final-race.log` |
| Shell 全量生命周期 race | 通过，922.001s；`sqlite-copy-shell-race.log`，20m 超时 |
| dataworkspace 相关生命周期 race | Guard/Leave/Close/Generation/Lease/Transaction 集合通过，5.475s；`sqlite-copy-data-lifecycle-race.log`，未运行全部 dataworkspace race |
| Go vet / MyGo vet | 通过；`sqlite-copy-go-vet.log` / `sqlite-copy-mygo-vet.log` |
| 翻译生成 / 检查 | 通过，1435 条中英消息；`sqlite-copy-generation.log` / `sqlite-copy-i18n.log` |
| MyGo 正式构建 | 通过；`sqlite-copy-build.log` |
| Python 采集工具测试 | 59 个通过，4.535s；`sqlite-copy-python.log` |

Shell 全量 race 在最后 ASCII 判重修复及追加 UI 测试前启动。最终 engines 全量 race、追加 UI race 和最终普通全量测试覆盖最后源码；不声称早期 Shell 二进制包含这些后续改动。

早期失败保留：Checkbox API 不匹配导致 UI 未编译、Tester 命中了背景同名“SQL 预览”按钮、草稿取消后预览隐藏、函数注册在错误的测试驱动，以及 Python STRICT fixture 缺少字段类型。均已修正并重跑。首次插入取消 race 将实际 uncertain 错断言为 rolled_back，测试改为保守状态及独立存储核验，正式服务没有放松提交确认。

## Linux 正式窗口

```
GOWORK=off go tool mygo build
GOWORK=off /usr/bin/python3 tools/verify_entry_acceptance.py -- \
  /usr/bin/python3 tools/capture_sqlite_copy_acceptance.py
```

[工具](../tools/capture_sqlite_copy_acceptance.py) 在 fresh owned run 中准备带自引用、NULL/BLOB/int64/精确文本/生成字段/索引/触发器/自增高水位的附加库表，运行正式二进制并复核 /proc 哈希，记录源菜单、两个复制预览/结果的 1280×800 与 840×560 实际窗口。结果由 SQLite 文件检查，不以截图取代数据核验。

最终二进制 SHA256：`c50477aa0854e38f5491ec720080375757ecb5af9f8f98137a2815972a9c8573`。

最终 run `r-3fcef1af98e7`，`custom-e14aa8e4` exit 0。证据：`build/entry-acceptance/r-3fcef1af98e7/evidence/sqlite-copy-native/capture-d17dc764bd3e/`。`result.json` 确认仅结构副本 0 行、完整副本 2 行，字段/索引/约束/自引用/触发器正文和自增状态符合预览，源表及主库同名表保持不变。已查看最终 840×560 数据复制预览；较早同一最终二进制的完整采集 `r-ec3688e03aba` 也通过，已查看其结构预览和两种尺寸结果。

`app-identity.json` 记录实际 PID/birth、二进制 hash、exit 0 及停止状态。最终应用、D-Bus/Secret Service/Xvfb/Openbox 原进程身份、Docker 容器及秘密文件均经独立查询确认不存在；结果见 run 的 `evidence/sqlite-copy-cleanup-audit.json`。工具只清理自己创建的资源，不修改系统配置或用户密钥环。

`r-3d56db41c7fc` 是菜单定位探索；`r-faaa0ae3af19` 仅预览探索，按钮坐标未正确读取结构。它们虽然脚本 exit 0，但没有完整验收 result，不计为复制通过。此前同二进制完整 run `r-ec3688e03aba` 缺少最终应用身份文件，后续工具补齐后新 run 重验。前三个 run 的已捕获服务身份、容器和秘密均独立审计清理；旧应用只记录脚本 finally 终止，不宣称有未留存 PID 的独立证明。

原生脚本未操作全部源标签/草稿或真实取消情形，后者由真实 SQLite Shell/Tester 与服务测试覆盖。工具提供 Secret Service/MySQL 基础设施，但本项没有重新验收系统密钥环恢复或 MySQL 管理；不新增这些服务通过结果。字体/缩放、Windows/macOS 实际窗口及 Navicat O/V 仍未验证。
