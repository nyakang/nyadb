# SQL Server 接入与验证

SQL Server 作为独立 `sqlserver` 连接类型接入已有运行层、SQL 工作区、对象工作区和唯一的共用数据网格。驱动使用 Microsoft `go-mssqldb v1.11.2`，无需 CGO。连接选择器、独立配置存储、系统密码库、连接测试、数据库/schema 导航、过滤和断开恢复均沿用既有模块边界。

## 当前能力

| 功能 | 行为 |
| --- | --- |
| 连接 | SQL 用户名/密码、TCP 主机及端口；默认端口 1433、数据库 master、TLS verify-full。密码仅传给驱动或系统密钥库。 |
| TLS | TLS 1.2 起；verify-full 校验证书链和主机名，支持 PEM/DER CA 文件及证书服务器名；require 仅加密，disable 关闭 TLS。 |
| 身份 | 每个连接绑定一个数据库；对象身份为 ProfileID/Catalog/Schema/Kind/Name。需要多个数据库时创建多个连接。 |
| 元数据 | schema、表、视图、函数、存储过程、序列；字段类型、空值、默认值、identity、计算字段、rowversion、索引和外键。程序保留服务器定义；序列保留完整配置，超出 int64 的值保留在定义文本中。 |
| SQL | 每份文档保留独立物理会话；支持临时表、DECLARE、过程定义、多结果集、受影响行数、typed values、流式结果和取消。SQL 错误或取消撤销该物理租约，恢复须显式操作，不重放 SQL。 |
| T-SQL | `GO` 按独立行分隔完整批次；保护 `N'…'`、`[escaped]]identifier]`、变量和嵌套注释。拒绝 GO 重复次数。格式化保留行位置，补全使用 T-SQL 关键词和方括号。 |
| 执行计划 | SELECT/WITH 的 `SHOWPLAN_XML` 估算计划；不执行被分析的查询，保留服务器估算行数和成本。成功后恢复 SHOWPLAN 模式；失败时撤销租约。 |
| 网格 | 共用 State/Document/grid、typed previews、分页、参数化分组筛选、排序、字段布局、行计数和字段信息。OFFSET/FETCH 分页；可靠键补齐稳定排序。 |
| 编辑 | 非空完整主键或可靠唯一键、原始行指纹/完整值校验、原子 insert/update/delete、默认值和生成主键。生成字段不可编辑。`OUTPUT INTO` 支持带触发器的表。 |
| 显式事务 | 保留文档事务；Page/Save/Row/Chunk 同属原连接、原对象和代际。取消、关闭或断开会等待驱动操作结束。 |
| 字段 | 页预览与显式字段读取最多 1024 字符/字节；完整编辑最多 1 MiB。保存完成后的预览继续保留截断标记。 |
| 导入导出 | 导入仅追加到已有表，全部行在同一事务中提交。共用格式导出和 typed JSONL；服务端大小检查加客户端上限验证，超限报错。 |
| 分析 | 行数、NULL、distinct、min/max、数值平均值/标准差、非 NULL singleton、最高频率和有界分布。使用同一只读事务快照；不推断缺失指标。 |

SQL Server 无会话级默认 schema 切换。连接表单的默认模式用于导航顺序；SQL 中使用 `[schema].[object]` 限定对象。SQL 文档保留用户执行的会话状态，数据与元数据使用独立连接池。

视图、无可靠键的表、生成字段、过滤/禁用唯一索引和不支持的键类型不会取得编辑权限。未知类型或不支持的筛选语义明确拒绝。表/视图设计、对象修改、备份/恢复、维护、账号管理、Windows/Entra 认证、命名实例发现、SSH/HTTP 隧道与 keyset 分页尚未开放。

提交确认失败或保存取消保留不确定状态，禁止自动重放。独立核验不能证明部分确认操作及触发器的全部结果，因此返回 indeterminate，不把读回结果当成重试授权。

## 连接 URI

支持 `sqlserver://` 和 `mssql://`，例如：

```text
sqlserver://reader@localhost:1433/tenant?sslmode=verify-full
sqlserver://reader@localhost:1433?database=tenant&encrypt=true&TrustServerCertificate=true
```

路径与 database 参数、sslmode 与 encrypt/TrustServerCertificate 不能混用。未知、重复或无法精确执行的选项会被拒绝。URI 密码保留在临时输入中，不写入连接记录。

## 真实验证

使用随机名称的独立 SQL Server Developer 容器、随机回环端口、临时 CA/服务器证书和强制 TLS。脚本不复用已有实例，结束时仅清理自己创建的容器、密码和私钥。

```bash
tools/verify_sqlserver_fixture.sh
tools/verify_sqlserver_fixture.sh --image mcr.microsoft.com/mssql/server:2019-latest --output build/sqlserver-2019
tools/verify_sqlserver_fixture.sh --race --output build/sqlserver-2022-race
```

已验证 SQL Server 2019 `15.0.4500.2` 与 2022 `16.0.4305.5`，包括：真实握手与证书主机名校验、错误密码拒绝、精确 decimal(38,20)、UUID/二进制/NULL/空串、字段预览、完整值读取、分页、两种原始行校验、生成主键、并发冲突及大小写/尾空格差异、显式回滚、流式导入、typed 导出、分析、XML 执行计划、临时表与批次、查询/保存取消和旧代际撤销。

UI 测试通过真实连接打开共用网格并保存、执行 SQL、断开后保留 SQL 文本及显式恢复状态，同时释放 scoped 数据文档。截图与日志位于忽略的 [SQL Server 2019 验证目录](../build/sqlserver-2019/) 与 [SQL Server 2022 race 验证目录](../build/sqlserver-2022-race/)。其他 SQL Server 版本、Azure SQL、认证方式及高级类型未作兼容性承诺。

最终源码已通过 `CGO_ENABLED=0 go test ./...`、`CGO_ENABLED=0 go vet ./...`、`go tool mygo vet`、`go tool mygo i18n check`（1918 条消息）和 `git diff --check`；改动的 Go 文件均通过 gofmt 检查。AGENTS.md 指定焦点包及新增 T-SQL/SQL Server 包的 race 检查通过，SQL Server 2022 的最终适配器与真实 UI fixture 也通过 race 验证。Python 视觉工具测试通过 64 项。

使用 Go 1.27.3、`CGO_ENABLED=0` 编译通过 Linux amd64、Windows amd64 和 macOS arm64，产物位于忽略的 `build/sqlserver/`。临时开发容器及其凭据文件已清理，验证脚本创建的容器和临时密钥也已自行清理。
