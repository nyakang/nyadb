# PostgreSQL 保留值与独立结构核验 — 2026-11-02

继续 [原生 DDL 规划阶段](postgres-native-ddl-plan-2026-11-02.md)。本次补齐私有二进制保留值核验、独立目标定义规范化和真实服务回归，并修正类型变更的列属性及身份序列边界。**PG 正式通用 Apply 和图形表单仍未公开**；`SchemaDesign` 继续只提供 Snapshot。完整目标和 [剩余功能清单](workbench-completion-2026-11-02.md) 继续有效。

## 实现 I

`postgresValueDigest` 在原物理连接和事务内读取完全限定 `FROM ONLY` 的原生 `record_send(ROW(...))`，不用预览文本或浮点格式化。每行 SHA-256 参与流式多重集合求和，并核对行数；重复行和同行字段关系参与核验，允许原生表重写改变 CTID。数值通过精确有理数规范化，支持整数/numeric 无损转换及 float4→float8。decimal 缩放丢失、超过 2^53 的舍入、负零和 NaN payload 改变均能检测。NULL、文本和 bytea 分开；数组保留维度、下界及 NULL 元素，domain 根据完整类型目录递归解析；其余类型保留原生字节与基础类型身份。

每单元格限 1 MiB，每行限 16 MiB；超限、缺失类型、截断或取消均返回空 digest，不能接受部分读取结果。服务器不向客户端返回超过行上限的载荷，但仍需生成该行的二进制值；本阶段没有声称实现无限大小值核验。生成表达式明确改变时仅排除该计算列；生成列变普通列仍比较其保留值。完整类型目录新增数组元素及 domain 基础类型依赖闭包，避免缺少 pinned 内建依赖造成误判。

`postgresCanonicalDesired` 要求已有事务，在随机命名普通表和 rollback-only savepoint 内执行独立 CREATE 声明，由真实 PG 规范化类型、默认、生成、约束、索引与触发器。它不执行待核验 ALTER 计划。探针只修改已验证的对象名称和目标跨度，保留表达式/字符串/函数体；自身外键目标重绑定，外部引用保持原范围。稳定 clause ID、原 ACL/字段参数及显式身份序列关联仅在返回的私有结构模型中恢复，临时 OID 不成为界面资产。目标名称、database/schema 和 generation 必须匹配原请求。

`VerifyPostgreSQLShape` 比较独立规范化声明与实际结构：字段顺序、类型/默认/非空/生成、显式序列关联及完整设置、约束验证状态、索引/触发器正文和属性、表和列属性。它允许 catalog clause ID 变化及 reloptions 排列差异。错误包含发生差异的字段或约束名，不附带 SQL 正文。该检查是目标形状核验，**不能替代完整原 catalog 和依赖保留核验**。

savepoint 成功、服务器错误及取消路径均撤销探针和局部设置；失败清理会丢弃物理连接并返回不确定状态，不提供可接受基线。真实测试验证原 catalog 修订、精确行数据、当前序列值、ACL/字段参数和 caller search_path 保留，以及缺少 schema CREATE 权限时明确失败且 caller 事务可继续使用。

## 真实验证发现及修复

- 新建空表会将 `NOT VALID` 约束标为已验证。ALTER 的独立基线改用完整约束的独立 ADD CONSTRAINT 保持该状态；普通 CREATE 仍以服务器实际语义为准。检测只读末尾语法，不将字符串中的 `NOT VALID` 当成状态。
- PostgreSQL TYPE 重置列存储属性。规划器在类型/排序规则变更后重新应用草稿中的 storage/compression/statistics，即使用户沿用原值；ACL/字段参数不被清空或重写。
- 身份列 smallint→bigint 会自动扩大默认序列上界。规划器使用已捕获的明确序列名称和完整设置重新应用原边界，不发送 RESTART；真实 fixture 验证 min/max/start/cache、当前序列值和既有行值保留。缺少完整序列身份/设置时明确拒绝。

`pgNativeFixtureApply` 现在在事务内先独立规范化目标定义、读取全部保留值，再执行规划 SQL，并在 COMMIT 前读取实际目录、比较目标形状和二进制 digest。它仍是 disposable 验收 harness，不是正式 Apply。表/字段改名沿用原阶段真实视图依赖测试，没有用 CREATE 探针替代改名的 OID 依赖证明。

## 检查与证据

环境：Go 1.27.3 / MyGo v0.7.3-nyadb.2，Linux amd64，`GOWORK=off GOCACHE=/data/.cache/golang`，HEAD `5960d26`，已有变更均保留，未提交/推送。

| 检查 | 实际结果 |
|---|---|
| 最终 `go test ./... -timeout 20m` | 通过，Shell 73.114 秒、objectworkspace 3.575 秒，其余未修改包允许缓存；默认跳过集成测试不计真实服务证据。 |
| contract/schemaddl/engines/dbruntime/objectworkspace race，20 分钟超时 | 通过，schemaddl 1.492 秒、engines 4.107 秒、objectworkspace 18.410 秒。Shell/dataworkspace 全包 race 沿用此前阶段，本轮未重跑。 |
| `go vet ./...` / `go tool mygo vet` | 通过。 |
| `go tool mygo generate` / `go tool mygo i18n check` | 通过，1338 条 zh-Hans/en 消息，生成文件保持最新。 |
| `go tool mygo build` | 通过，26.5 秒；正式 Linux amd64 程序及 deb/tar。 |
| `bash -n tools/verify_postgres_fixture.sh` / `git diff --check` | 通过。 |
| 最终 PostgreSQL 17.10 自有 fixture `--race` | 10 个场景全部通过，engines 81.028 秒，native plan 45.69 秒、retained properties 2.37 秒。 |

最终正式二进制 SHA-256：`c59f7a66e88ad8a58f72e2e52913dca816146a93a8f7a362dec5aca6ab7529aa`。

真实运行已分别保存原始日志，失败项不计通过：`postgres-QdwVUK` 是二进制值核验阶段通过；`postgres-1i95gU` 发现 NOT VALID 探针误判；修正后 `postgres-Hw9coR` 全部通过；`postgres-xebovv` 发现 TYPE 重置列属性；`postgres-2WPiS3` 发现身份序列上界自动扩大；修正后 `postgres-gIF8MO` 针对性通过。最终完整证据为 `build/verify/postgres-y6vdDF`。所有上述自有容器均记录 `owned_container_removed=true`，独立 `docker inspect --type=container` 确认不存在，各目录保存 `cleanup-audit.json`；没有把 Docker 其他错误当成清理成功。

可重复完整验证：

```bash
GOWORK=off GOCACHE=/data/.cache/golang PATH=/usr/local/go/bin:$PATH \
  tools/verify_postgres_fixture.sh --race
```

工具新增 `--run` 正则筛选，仍创建唯一自有容器并只清理本次资源；例：`--race --run TestPostgreSQL17DesignNativeRetainedPropertiesFixture`。筛选结果不能当成完整服务包通过。

## 剩余工作和验证边界

正式 PG Apply 仍需要完整 catalog/dependency 保留与重建 OID 映射、锁下重规划、创建并发冲突、提交确认/不确定回执及 PG 原生表单。约束注释、内部触发器状态、opclass、security label、RLS、未知 JSON 字段等不能借目标形状和数据 digest 获得覆盖权限。探针要求原 schema 的 CREATE 权限/相应 tablespace 权限；自定义类型输入和表达式折叠可能执行用户函数，其非事务副作用尚未纳入正式执行策略，因此尚未公开此后端。

本次仅真实 PG 17 验证；PG 14/18、多版本权限与网络故障、Windows/macOS 原生验收未新增结果。没有 UI 变更或新原生截图；既有截图按原报告的二进制身份判断，不能代表本次程序的原生窗口验收。未修改 Python 采集工具。未运行 Navicat、连接生产或新增 O/V。

随后仍需 PG/SQLite 对象 CRUD、导入/备份及隧道；跨库传输/数据同步/结构同步；自动运行；用户选择的 ClickHouse 和可配置 OpenAI 兼容 AI SQL；模型/逆向、BI、服务器监控、数据字典、数据生成、脱敏及命令行。整体目标未完成。
