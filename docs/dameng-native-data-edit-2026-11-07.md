# 达梦原生精确保存与只读核验 — 2026-11-07

达梦私有原生 Session 新增共享 `contract.DataEdits` 的 Saver 和 Verify，
以及按表限制的 EditableTables。插入、修改和删除使用同一个原始物理
事务；返回原生实际主键、完整保存值和 1024 单位的网格预览。正式
chooser/provider/工作台尚未注册，已注册连接产品数量仍为 16。

## 保存与类型边界

保存先捕获独立的输入、原始快照、类型值和序列化主键副本，再在原始
`sql.Conn` 上开启原生 SERIALIZABLE 事务。EXCLUSIVE 表锁在读取结构前
获取，覆盖结构、完整值验证、全部语句、最终结果读取与提交。真实独立
连接的 UPDATE 在此期间被原生 `-608` 拒绝，原事务完成后同句成功。
该初始策略阻塞目标表的并发写入及需要相应表锁的读取，不能视为行级锁。

只允许已证明结构的普通、非分区、非临时、启用表锁且未启用 LONG_ROW
的表，以及经过原生索引/约束验证的整数或 DECIMAL 主键。无可靠键、
视图、其它类型/存储和含 LOB 且带触发器的表保持只读。列、限定对象、
Profile/Catalog/Schema、原 Session/结构/对象签名、完整列序、类型主键及
序列化键须一致。截断预览不能充当完整快照；SHA256 指纹模式仍被拒绝。

在任何写入前，校验整批请求并重读每个完整原行。重复原行、错误列、
生成列写入、缺少必要列、非法 NULL/DEFAULT、无效 Unicode、越界或
精度损失均拒绝。每次执行前再次核对原行，防止前面的触发器改变后续
待保存行。普通短行分页现在可直接返回完整 values 快照；长字段仍须
通过已有 RowReader 显式取得完整值。

原生 NUMERIC 绑定保持 38 位小数，整数不经浮点中间值。固定服务器会
把超精度 DECIMAL 舍入，把 TIMESTAMP(6) 的九位小数截断或舍入；Saver
在绑定前检查存储精度与长度，不能依赖服务器自动转换。已验证整数
边界、REAL/FLOAT/DOUBLE、BIT、DATE/TIME/TIMESTAMP 与带时区时间、
CHAR、BINARY、TEXT/IMAGE 和精确复合 DECIMAL 主键修改。REAL 只接收
能被 float32 精确表示的输入；显式 FLOAT(p) 的未验证语义继续拒绝。

固定 CHAR 绑定保守按 UTF-8 字节数限制长度，VARCHAR 按字符数限制。
原生目录把 TIMESTAMP WITH TIME ZONE 别名记为 DATETIME WITH TIME ZONE；
适配器统一为中立的 TIMESTAMP 类型，保留原始声明。NULL、空文本、空
二进制及 EMPTY_CLOB/EMPTY_BLOB 保持区别，每字段完整值上限为 1 MiB。
输入与结果各有有界批次预算；保存后和核验重读也计入预算，不仅检查
提交前的草稿长度。

匿名块 `RETURNING … INTO` 返回实际键，`SQL%ROWCOUNT` 必须为 1。
IDENTITY、DEFAULT、虚拟生成列及 BEFORE 触发器结果从原生行读取；不
猜测 `@@IDENTITY`，也不照搬不被此版本接受的 DEFAULT VALUES。全部
语句结束后再次读取最终行，避免后续触发器使前面的保存结果过时。
真实跨行触发器用例证明较早结果显示最后的实际值。

## 原生 LOB 回滚差异

真实原生事务中，触发器通过 `DBMS_LOB.WRITEAPPEND` 修改已有 CLOB
locator 后，即使 Rollback 返回成功，CLOB 的追加仍存在，而相同 UPDATE
的普通文本列恢复原值。独立查询证明长度从 1048576 变为 1048577。
这不是提交回执确认，也不能通过重放或回滚成功推断整行恢复。

因此含 CLOB/TEXT/BLOB/IMAGE 的表只要存在触发器，就不开放保存，包括
暂时停用的触发器。Saver 在任何 mutation 前检查原生 ALL_TRIGGERS；
普通 SELECT 授权账户也能看到该触发器并把表标为只读。读取、完整字段
及预览仍可用。该保守限制不通过解析触发器正文猜测其调用图安全性。
无触发器表的正常 CLOB/BLOB 替换已通过真实整批回滚验证；恰好 1 MiB
文本和二进制保存及上限加一拒绝分别通过。

## 回执、取消与恢复

任何后续 UNIQUE 失败都回滚原始批次，清空暂存 Applied/Outcomes/Saved；
独立完整读取证明前面的普通值和 LOB 替换均恢复。取消等待原始执行确认，
然后使用原始事务的独立 Rollback。注入的原始执行确认门证明取消不会
提前返回；真实正在执行的 DBMS_LOCK.SLEEP 触发器取消关闭原始物理连接，保存
保留不确定和不完整状态，不重放。

DBMS_LOCK.SLEEP 的服务器过程可能继续到睡眠结束，并暂时保留原事务
表锁。客户端原始调用已确认取消，不代表远端回滚已完成；此时独立读取
可能返回 `-608`。测试只重试有界的独立只读查询，待原锁释放后确认原值，
没有重发 UPDATE。产品继续保留不确定草稿，显式核验也不能补造提交回执。

COMMIT 获确认才设置 CommitConfirmed。已实际提交但原始 driver.Tx 回执
丢失时，保留实际主键和完整结果，返回 ErrDataCommitUncertain；独立查询
证明只插入一次。原生 COMMIT 后自动提交属性恢复失败或取消也返回
ErrOutcomeUnknown 并关闭原始句柄。提交前已取消则保留独立回滚机会。
这些操作错误不会返回可触发 database/sql 自动重放的 ErrBadConn。

Verify 在原始 Session 身份及 SHARE 表锁下只读比较完整状态，返回
applied、not_applied 或 indeterminate。已恢复状态仅标记 Verified，不能
补造 CommitConfirmed。无实际保存键、不完整结果、无变化修改、不同
行混合状态或被其它写入改变的状态不成为确定的批次恢复。该比较无法
证明是谁产生了相同状态；仍需要用户显式触发核验，禁止自动重放草稿。

## 验证与保留范围

固定自有 Docker 服务为 DM8 `08134283904-20220804-166351-20005`，mode=0、
UTF-8、大小写敏感、LENGTH_IN_CHAR=1。三个已准入客户端均运行明文与
服务器身份校验 TLS；2026 客户端两种传输使用 race。新增保存/核验测试
与原有分页、完整字段、普通账户和元数据回归一起验收。

首次并行 TLS/race 的取消用例暴露了夹具问题：定时从保存创建开始，可能
在写入前取消；原 SF_SLEEP 触发器创建虽成功，实际编译状态为 INVALID。
最终用例检查 DBMS_LOCK.SLEEP 触发器为 VALID，先证明原生 UPDATE 实际
等待五秒并回滚，再从捕获的原始 UPDATE 开始触发取消。该修正用例在
三个客户端与两种传输重跑，2026 两种传输仍使用 race；其余已通过用例
沿用完整矩阵结果。首次失败及修正后日志均保留在本机验收记录中。

最终完整 Go 测试、AGENTS 生命周期包 race、Go/MyGo vet、翻译检查和
Linux amd64/Windows amd64/macOS arm64 的应用及达梦测试程序 CGO=0
编译记录在忽略的 `build/dameng/edit-verification.json`。跨平台编译不证明
那些平台的原厂 ABI 已可运行。界面和工具没有变化，不新增截图或界面
对齐声明。厂商库、头文件、代码、密码和完整私有配置均未提交。

下一项仍需完成共享数据事务、导入导出与真实工作台注册。更多服务器/
兼容模式/键/存储/账户/平台、指纹快照和触发器/LOB 的更广泛语义，以及
设计、管理、备份与完整 Navicat/DBX 对齐目标继续保留在
[总清单](database-middleware-alignment-2026-11-07.md)。
