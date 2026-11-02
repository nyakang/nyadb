# PostgreSQL 结构编辑的序列状态核验 — 2026-11-02

继续完整工作台目标和 PG 正式设计器所需的执行验证，补齐序列当前位置的独立证据。实现 **I**；PG 正式 `SchemaDesign` 仍仅公开 Snapshot，通用 Apply/CanApply 和图形表单仍待接通。本步不标记 PostgreSQL 深化或整个目标完成。

## 实现

- 新增私有 `postgresSequenceSources` / `postgresSequenceStates`，从完整原目录读取所有已捕获序列，包括 identity、serial 所有权及外部默认表达式引用。必须有一致的 pg_class/pg_sequence OID、序列类型、命名空间和名称；缺失、重复、错误子对象和无效记录不产生部分基线。
- 每次读取先按参数化原 OID 查询实际数据库/对象身份，再读取完整限定名称的 `tableoid` / `last_value` / `is_called`。实际行的 tableoid 必须匹配原 OID，拒绝在身份查询与状态查询之间发生的同名替换。数值直接扫描为 int64，保留负数、超过 2^53 的整数、未调用/已调用状态；不调用 nextval、currval 或 setval，不获取或记录密码。
- `postgresCompareSequenceStates` 区分缺失/替换身份与数值/调用状态变化。错误不包含原值、SQL 或用户名称。新序列的创建语义由目标声明验证负责；本比较检查原序列，不能单独授权新序列或新字段。
- `postgresSequenceRemovals` 先执行已有完整规划及删除确认，再按原目录绑定与稳定字段 ID 推导 OWNED 序列的删除许可。允许已确认字段删除及原 identity 移除；删除 serial 默认或不再引用外部序列不允许删除这些资产。相同名称的新 OID 不能提供原序列状态。
- 原生规划验收器在独立 CREATE 探针前保存原状态、探针后比较、DDL 结果后通过原身份补读再比较。类型扩大、字段/身份删除、保留序列及其它现有原生计划都经过此核验。探针读取的验证材料不会替换普通工作台基线。
- 稳定字段/约束/索引/触发器图名称转换移入正式服务代码；已有测试 helper 转发到该实现，正式包不依赖 `_test.go`。

元数据/改名仍走已验证的原生 SQL 和既有目录保留核验，不新增外部序列 SELECT 权限要求。真实权限 fixture 证明表所有者可能无法读外部默认序列，但能够合法修改自己表的注释；严格序列状态读取返回不支持及空证据，原元数据编辑仍提交。不能把“无法读取序列证据”描述为已完成该项证据。

## 测试及真实服务

纯回归覆盖完整/非法目录、跨命名空间及引用字符、确定性排序、不修改证据、精确正负数、调用标记、替换/缺失/OID 错配、受控删除、错误许可、缺失/部分材料及取消。服务错误只保留受控类别或取消状态。

四个新增真实 PostgreSQL fixture：

- 原 identity 与外部下降/CYCLE/CACHE 序列分别保留 `9007199254740993` 和 `-9007199254740993`。第二个 backend 实际执行 setval，repeatable-read 中仍能读到数值及 called 改动并返回冲突；测试明确恢复自己 fixture，不声称 ROLLBACK 能恢复 setval。
- 原 identity 移除及确认 serial 字段删除准确选择原 OWNED OID；删除默认和外部引用无额外删除许可。实际原生删除提交前核验保留序列与获准消失的序列。
- 真实表所有者无外部序列 SELECT 权限时，中途读取失败不返回已读部分；既有表注释编辑仍允许并由数据库复核。取消、错库和实际 DROP 后缺失也使用真实服务检查。
- 真实第二个 backend 在身份读取完成后 DROP/CREATE 同名序列，保持数值 42 及未调用状态；状态读取凭实际 tableoid 拒绝新 OID，返回冲突及空证据。验收驱动包装只安排真实操作的时序，不伪造结果行。

首轮 `build/verify/postgres-jG2Bob` 中状态/权限场景通过，删除场景失败：测试只清空 Default 文本而没有清除 HasDefault，输入无效，已修正。扩大运行 `postgres-etoyNW` 的原生规划、删除、改名及三个新增场景通过，但该较早版本包含额外元数据读取，并出现一次单项 5 秒回执等待超时，整次退出 1，不计最终通过；额外读取已移除。两次失败保留资源/退出日志。

初始全量测试/race/vet 发现正式函数引用 `_test.go` 中的图名称 helper，编译失败；已移入正式包并让测试转发。日志 `postgres-sequence-go-test.log` / `postgres-sequence-race.log` / `postgres-sequence-go-vet.log` 保留，不计为通过。

完整 PostgreSQL 17.10 服务 race `build/verify/postgres-HpwOz4` exit 0：26 个主测试及 37 个子测试，662.194 秒，包含元数据、改名、目录/依赖/属性/保留值、原生计划与三个序列场景。该运行编译于最后的 tableoid 修复之前，不能称为最终代码的完整服务验收。最终代码另运行四个序列 fixture：`build/verify/postgres-QbV9TU` exit 0，4 个主测试及 2 个子测试，28.867 秒，其中真实同名替换 1.51 秒；并完成以下全量普通测试及生命周期 race。定向服务范围不替代完整服务范围；服务测试默认需要 `NYADB_POSTGRES_IT=1`，普通 Go 测试的跳过不计真实服务通过。

## 最终检查

Linux amd64，Go 1.27.3 / MyGo v0.7.3-nyadb.2，基线 `a89cfb0`，`GOWORK=off GOCACHE=/data/.cache/golang`。

- 最终 `go test ./... -timeout=20m` 通过，日志 `build/verify/postgres-sequence-identity-go-test.log`。
- contract/schemaddl/engines/dbruntime/objectworkspace 生命周期 race 通过：engines 3.873 秒，其它正常缓存；日志 `build/verify/postgres-sequence-identity-race.log`。
- `go vet ./...`、`go tool mygo vet`、翻译生成及 i18n 检查通过，1396 条 zh-Hans/en 消息。`go tool mygo build` 通过，Linux 正式程序及 deb/tar 包生成；最终日志 `build/verify/postgres-sequence-identity-{go-vet,mygo-vet,generate,i18n,build}.log`。
- 正式程序 SHA-256：`df5ce8212fd7bda41efcb6fee49c137f26665698202a3c236390a2691cf4284e`。本步没有 UI/Python 改动，没有新增原生窗口截图；构建及服务验证不计窗口验收。旧截图仍按各自报告的二进制哈希解读。
- 独立 Docker 查询确认四次运行的原容器 ID 均已移除，查询 exit 0；分别保留各自 `cleanup-audit.json`，汇总在 `build/verify/postgres-sequence-cleanup-audit.json`。只创建唯一命名/label、随机 localhost 端口的隔离 PostgreSQL，不运行 Navicat、不连接生产、不 push 或改写历史。按用户后续逐项归档要求本地提交本步成果。

重放：

```bash
GOWORK=off GOCACHE=/data/.cache/golang PATH=/usr/local/go/bin:$PATH \
  tools/verify_postgres_fixture.sh --race
```

最终定向重放在同一环境下追加 `--run 'TestPostgreSQL17DesignSequence.*Fixture'`。

## 剩余范围

此处读取的是序列 relation 上的 last_value/is_called，包含缓存预留前沿，不等于其它 backend 的 currval 或未用缓存。序列状态不是 MVCC；两次读取之间的比较不是并发锁定证明，读取后仍可能有其它 backend 的 nextval/setval。没有新增序列锁，也不自动回放、恢复或重置计数器。

未捕获的隐式/动态序列、用户函数或非事务性外部副作用、探针权限/安全策略、用户明确要求新字段消费旧序列的精确语义及并发序列变更仍需处理。一般物理/TOAST 重写、依赖重建与入站定义、完整并发锁/新字段数据语义及正式通用 Apply/PG 表单也继续待实施。本步不以保守冲突取代最终应支持的合法编辑能力。

PG/SQLite 对象 CRUD、导入/备份/隧道、跨库传输及同步、调度、模型/逆向/BI/监控/字典/生成/脱敏/命令行、ClickHouse/AI 深化、多平台原生与原版验收继续属于 [完整目标](workbench-completion-2026-11-02.md)。ClickHouse 与 AI SQL 基础已分别交付，不重复列为待实现。本步不新增 Navicat O/S/V。
