# 达梦原生数据分页与限量字段读取 — 2026-11-07

在[原生会话/元数据/SQL 基础](dameng-native-workspace-foundation-2026-11-07.md)
上，新增面向唯一共享数据工作区的 `contract.DataReadsProvider`：限定普通表
分页、过滤/排序、显式精确计数、表信息和字段分块读取。数据仍只读，
达梦没有正式工作台注册，连接表单、chooser/provider 和 UI 验收继续待完成，
正式支持产品数量仍为 16，参考清单的两个达梦条目仍为 missing。

## 已完成读取行为

- 每次读取绑定原 ProfileID/Catalog/Schema/Kind/Name 和捕获的 Session。
  元数据与数据使用同一原始物理连接和 SERIALIZABLE 事务；原生 ROW SHARE
  锁保留至结束。完成使用 Rollback 释放原始事务，不走提交或自动重试路径。
  关闭 Session 后捕获的回调拒绝新请求，取消等待原操作确认和连接清理。
- 普通表偏移分页最多 1,000 行/512 列，服务器只传每字段 1,025 个字符或
  字节的预览与哨兵，网格展示 1,024 个字符/字节并标记截断。
  16 MiB 页预算计入二进制副本和十六进制展示成本；超过预算要求减小页。
  特殊/临时表和未验证类型不获得稳定游标能力。
- 稳定游标与 RowRef 只接受非空的原生整数/DECIMAL 主键，包括 IDENTITY。
  约束必须 ENABLED、VALIDATED、NOT DEFERRABLE，物理唯一索引必须有效且
  为已核验类型；表达式、分区和未验证键不继承该能力。复合键保持原生
  索引位置，查询边界使用精确参数，最多 16 个键列。
- 游标签名包含原 Session 的随机范围、限定表、完整结构、原生对象身份、
  创建/DDL 时间、过滤树和实际排序。跨 Session/表、规则变化、结构变化、
  NULL/截断/错误类型/过大边界均拒绝；没有稳定证明时仍可偏移浏览。
- 分组过滤与排序验证列名，值以原生参数绑定，QuerySQL 不包含绑定值。
  支持相等、不等、比较、LIKE/NOT LIKE 和 NULL 检查；二进制/LOB 比较和
  未验证的排序语义显式拒绝。涉及可空或非数字排序时不开启稳定 seek。
- 显式字段读取每次最多 1,024 字符/字节，校验完整限定 RowRef 与精确键。
  文本按 Unicode 字符、二进制按字节推进；EOF 与 SQL NULL 分开表达。
  CLOB/BLOB、普通二进制和字符串的 NULL/空值、末段和越过 EOF 已实测。

此项不提供完整编辑行、保存/验证、显式数据事务、导入导出或分析。
Session 新增 Count、Keyset 和 FieldChunks，EditableTables 保持关闭。
文本/日期/二进制键、可空唯一索引、其它原生表类别与类型仍待独立证明。

## 原生差异与真实证据

固定 DM8 `08134283904-20220804-166351-20005`，mode=0、UTF-8、区分大小写、
LENGTH_IN_CHAR=1。自有 Docker TLS 和显式明文夹具使用三份已核验原厂客户端。

`GET_DDL` 在原生 READ ONLY 事务内返回 -6506，不能将该标志直接套用于
包含原生结构函数的读取链路。当前 SERIALIZABLE 事务只发适配层固定的
元数据/数据读取语句与 SET/LOCK，不开启原生 READ ONLY 标志。
真实测试证明独立连接提交更新后，原请求仍读取原值；并发改表被拒绝且
没有添加字段，同一条 ALTER 在读取结束后成功。零行 SELECT 本身不保留
足够的 DDL 保护。锁冲突可直接返回原生 -608，不能假设必然等到客户端超时，
也不将其当成自动重试许可。

`SUBSTR(VARBINARY)` 返回十六进制 VARCHAR，LENGTH 同样计算该表示；
改用限定的 SYS.UTL_RAW.SUBSTR/LENGTH 保留真实字节。其 SUBSTR 会以
-6803 拒绝超出剩余长度的 amount 或 EOF offset，读取使用剩余长度和
服务端 CASE 边界。CLOB/BLOB 使用 SYS.DBMS_LOB.SUBSTR/GETLENGTH。
空 LOB 夹具通过 EMPTY_CLOB/EMPTY_BLOB 创建，区别于 NULL LOB。

普通 SELECT 账户可以读取授权表的主键约束与索引，但 ALL_CONS_COLUMNS
不返回该外部表的行；以限定的主键状态和可见物理索引列联合证明键。
授权表分页与可靠键通过，未授权表读取失败。没有放宽系统目录访问权限。

四项真实测试覆盖 38 位 DECIMAL、超过 JavaScript 精度的 BIGINT、复合
DECIMAL 键、IDENTITY 键、Unicode/二进制/LOB/NULL/空值、分组参数过滤、
错误/跨会话/改表后的游标、无键/可空唯一键/文本键降级、原生快照/锁、
活动原始操作取消、后续显式读取和 Session 关闭。最初长字符串与长
VARBINARY 同时放入同一普通行触发 -2665；最终夹具分开验证两种大字段，
分别保留长二进制与长字符串用例，不声称已支持所有字段组合或长行存储形式。

2022/2023/2026 客户端均通过 TLS 和明文四项测试，2026 两种传输使用 race。
完整 CGO=0 Go 测试、AGENTS 生命周期包 race、Go/MyGo vet、翻译检查、
Linux amd64/Windows amd64/macOS arm64 的应用和达梦测试程序 CGO=0 编译
通过；另外两平台仍只验证编译。工具未改动，87 项 Python 验证沿用上一项。
源文件/最终日志/六个构建摘要保存在忽略的
`build/dameng/data-read-verification.json`，日志前缀为 data-read-final。

继续完成完整行/LOB 读取与精确编辑、冲突和不确定提交验证、原生数据事务/
导入导出，再接入连接界面和运行时注册。设计、管理、其它原厂版本/平台/
认证及完整参考范围仍见[完整清单](database-middleware-alignment-2026-11-07.md)。
TLS 的客户端证书信任限制仍以 [TLS 报告](dameng-native-tls-2026-11-07.md)为准。
