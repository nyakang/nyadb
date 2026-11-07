# 达梦原生会话、限定元数据与 SQL 基础 — 2026-11-07

在[独立配置与持久化](dameng-profile-foundation-2026-11-07.md)基础上，
完成原生后台 Session、限定元数据和按 SQL 文档持有的物理连接租约。
所有物理连接继续使用已核验的原厂客户端独立进程和产品/版本/Schema
校验。此项是服务基础；达梦尚无工作台 descriptor/provider、连接表单或
chooser 入口，正式注册产品仍为 16，完整参考清单中的达梦仍为 missing。

后台池和 SQL 池分别有界，SQL 文档独占其物理连接。Close 取消原始操作、
等待确认并释放捕获的连接；重复并发关闭等待同一完成通知。原 Session
关闭后元数据与租约拒绝新请求，取消后的 SQL 租约保持丢失状态，必须显式
Replace 才能恢复，不重放 SQL。公开代际仍由 dbruntime 管理。

## 已完成范围

- Schema 和对象保持原始 ProfileID/Catalog/Schema/Kind/Name；拒绝跨配置、
  跨 Catalog 和不匹配对象类型。名称保留 Unicode、引号和首尾空格。
- 原生表、视图、序列、程序与完整定义，以及字段位置/类型/默认值、
  IDENTITY/生成字段、表与字段注释、索引、主键和外键。
  元数据列表和定义分别有数量/字节上限；后台池只有一条连接时也能完成
  对象与结构读取，不持有一个游标再等待第二条连接。
- 原始 SQL 批次、按文档物理会话、过程脚本与 slash 分隔、有界 SQL 文件、
  原生错误码与语句位置、取消确认和显式恢复。
  每次指定 Schema 的执行都重新设置并读取 Schema，用户 SET SCHEMA
  不会使下次工作台请求误用缓存身份。
- BIGINT 与 38 位 DECIMAL、二进制、NULL/空字符串、BIT 和日期时间采用
  中立类型；流式回调获得独立列和值副本。结果缓存最多 1,000 行/8 MiB，
  单字段预览 64 KiB；SQL 文件最多 8 MiB，单语句最多 1 MiB。
  脚本完整性在执行首条语句前检查，失败不会先执行文件中的前缀命令。

当前 Session 只声明 Query、Metadata 和 Schemas，结构始终只读。
数据分页、完整字段编辑、事务保存、导入导出、计划及管理能力未在此项开启。

## 真实 DM8 差异

原生服务器为 `08134283904-20220804-166351-20005`，mode=0、UTF-8、
区分大小写、LENGTH_IN_CHAR=1。使用自有 Docker TLS 和显式明文夹具。

1. ALL_OBJECTS 的 Schema 行类型为 SCH，名称在 OBJECT_NAME，不是 OWNER。
   普通账户可枚举某些未授权 Schema 名；未授权表不会出现在其对象列表，
   直接 SELECT 同样被拒绝。因此没有声称所有未授权 Schema 名都被隐藏。
2. COLUMN_ID 从 0 开始；转换为展示位置从 1 开始。字段注释以
   ALL_COL_COMMENTS.SCHEMA_NAME 关联，OWNER 可能是认证所属账户。
3. ALL_SOURCE.LINE 可为 NULL，源码整段存在一个 CLOB，过程/函数类型可能
   为 PROC。保留完整定义，不假设 Oracle 源码的逐行存储。
4. GET_DDL 能转义表头/字段名，但某些带引号 Schema 的外键引用漏转义。
   原定义保留；字段解析只在词法检查过的顶层规范约束行边界结束，
   外键单独从限定系统视图读取。默认字符串内伪造的换行 CONSTRAINT
   有回归测试，不会误切为约束。
5. GLOBAL TEMPORARY TABLE 定义跨连接可见，其行内容隔离；已确认另一个
   SQL 租约和后台池都读到零行，原租约仍能读到自己的内容。

提交回执丢失测试先在真实服务器完成 COMMIT，再在适配边界丢弃回复。
原连接返回 ErrOutcomeUnknown 和明确丢失状态，恢复后独立读取仅一行，
Preconnect/Replace 不重放 INSERT 或 COMMIT。driver.ErrBadConn 仅用于
显式销毁捕获的 sql.Conn，不从已提交命令返回，避免 database/sql 自动重试。

## 验证与后续

2022/2023/2026 三份已允许的原厂客户端均通过新增四项真实会话/元数据/
精确值/取消/回执测试的 TLS 与明文验证；2026 客户端两种传输使用 race。
完整 CGO=0 Go 测试、Go/MyGo vet、翻译检查、87 项 Python 工具测试和
AGENTS.md 生命周期包的 race 通过。Linux amd64、Windows amd64、macOS
arm64 的 CGO=0 应用构建和达梦测试程序编译通过；编译不证明另两平台
原厂 ABI 可运行。源文件、日志与构建摘要记录在忽略的
`build/dameng/session-foundation-verification.json`，日志前缀为 session-final。

可对自有已核验夹具执行：

```sh
NYADB_DAMENG_DPI_IT=1 \
NYADB_DAMENG_DPI_CONFIG=/absolute/path/private-probe.json \
NYADB_DAMENG_DPI_LIBRARY=/absolute/path/libdmdpi.so \
LD_LIBRARY_PATH=/absolute/path/native-dependencies \
go test -count=1 -run 'TestNativeDameng(SessionQualified|QueryValues|OrdinaryMetadata|SQLCommitted)' -v ./internal/services/dameng
```

后续继续唯一共享数据工作区的原生适配、可靠行键、精确编辑/事务/
冲突与不确定提交、字段/LOB、导入导出，以及原生表单、凭据回滚、
chooser/descriptor/provider 和真实 UI 验收。完整原生类型、其它服务器/
客户端/平台/认证矩阵、设计管理和跨产品参考能力仍保留在
[完整清单](database-middleware-alignment-2026-11-07.md)。固定服务器的
客户端证书信任限制仍以 [TLS 报告](dameng-native-tls-2026-11-07.md)为准。
