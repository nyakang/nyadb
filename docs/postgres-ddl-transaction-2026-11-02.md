# PostgreSQL CREATE/ALTER 事务服务 — 2026-11-02

从 `395020c` 继续完整工作台目标。将既有独立声明、目录/存储保留、类型化保留值及序列核验整合到服务内部 `postgresDesignDDLApply`，统一 CREATE/ALTER 的请求捕获、事务、错误和最终回执。该路径不再依赖测试 harness 来执行 SQL；**仍不向 SchemaDesign binding 或工作台开放**。一般表达式与并发副作用、通用新字段值核验、PG 表单及原生保存验收继续未完成，不能将本步称为 PG 正式设计器交付。

## 行为与边界

- 深复制原请求，复核原 profile/generation/catalog/schema/table，拒绝失效或不匹配范围。纯规划在获取连接前执行；现有 noop 和原生改名复用其既有完整事务路径。
- 使用私有物理连接，BEGIN REPEATABLE READ，事务局部固定 search_path、字符串和 RLS 设置。原表 ACCESS EXCLUSIVE 锁先于首个目录 SELECT；精确原快照复核后在锁内重新规划，不执行 caller 传入的预览 SQL。
- 捕获序列原位置、独立规范化定义、新外部引用的独立 before-image 及保留字段二进制摘要，再逐项执行 native DDL。后验比较目标结构/声明图、原目录保留、物理变化、未编辑入站上下文、保留值/行数和原序列状态。
- 全部检查成功且原会话有效后才发送 COMMIT。只有成功回执可返回 committed/Verified/Actual；语句/核验失败回滚整批，取消等待驱动确认。回滚失败或 COMMIT 已发送但没有成功回执返回 uncertain，并丢弃物理连接，禁止自动重放或替换工作区基线。
- 成功结果只返回普通当前目录闭包，补读旧目录/引用 before-image 仅作为核验材料；本步未改变凭据、存储格式、UI 或数据库迁移。

本服务沿用现有核验能力及限制，不是一般 SQL 副作用/并发稳定性证明。CREATE 的完整新引用及通用新默认/生成值验算、任意用户函数、动态 dispatch、并发定义变化及 PG 18 独立 NOT NULL 约束仍需后续补齐。测试直接从数据库确认了代表性默认值和生成值，但这不证明所有新字段有生产独立值核验。保留字段摘要仍受原有单格 1 MiB/行 16 MiB 限制。

## 真实回归

- 首次 `postgres-IiEiZJ` 的 ALTER 尝试改动视图引用字段的类型，被 PostgreSQL 以 0A000 拒绝，事务返回 rolled_back。后续将该情况保留为显式失败/原目录不变回归；成功场景单独建立仅引用保留字段的视图。视图调整是 fixture setup，不是服务自动删除依赖。
- CREATE 与成功 ALTER：真实核对 NULL、00ff 二进制、40 位精确数值、保留视图、额外 DEFAULT 9 和 STORED `value*2=14`。真实 CHECK 约束失败发生在较早 ADD COLUMN 后，整批回滚且原修订不变。
- 真实锁等待：原连接 writer 在等待期间提交注释，执行器在锁获得后拒绝旧修订；观察到等待后修改 caller 草稿/原目录字节，执行仍使用原捕获值。取消、单连接池等待取消和会话关闭都没有成功回执或新 Actual。
- 物理连接：成功执行后读取实际 search_path、standard_conforming_strings、row_security 和 transaction_isolation，原用户设置恢复。
- 丢失提交回执：仅 connector 注入丢弃真实成功 COMMIT 的回执；实际 SQL、连接和核验仍运行于自有服务。独立原连接读取已提交值 7/9，但服务必须返回 uncertain/无 Actual；这是故障注入，不是实际网络故障验收。

## 检查与重放

Linux amd64 / Go 1.27.3 / MyGo v0.7.3-nyadb.2，`GOWORK=off`，SDK `/usr/local/go`，缓存 `/data/.cache/golang`。本步没有新 UI、实际窗口或 Navicat O/S/V。

| 检查 | 实际结果 |
|---|---|
| Go | 全量 `go test ./... -timeout 20m` 通过，Shell 98.550 秒；相关 schemaddl/engines/dbruntime/objectworkspace race 通过，engines 6.684 秒、dbruntime 1.079 秒，其余使用有效缓存。 |
| PostgreSQL 17.10 | 最终 `postgres-akbn3G` 三个完整新 fixture 的真实 race 通过，98.875 秒，包含物理设置/单连接池和关闭撤销。较早 `postgres-UY3ixu` 三项普通测试 63.194 秒通过，但不包含后补的设置/池/关闭子用例。 |
| PostgreSQL 14.24 | 最终 `postgres-bHati4` 三项完整新 fixture 通过，71.198 秒；不替代该版本所有设计能力验收。 |
| PostgreSQL 18.6 | 最终 `postgres-s8OWLj` 生命周期/丢失回执两项通过，43.615 秒；没有独立 NOT NULL 的普通表/加字段范围，不含 CREATE/主键/类型变更完整场景。 |
| 静态/翻译/打包 | `go vet ./...`、`go tool mygo vet`、翻译生成及检查（1456 条）、`go tool mygo build` 全部 exit 0；打包 29.3 秒，未执行实际窗口。 |

此前 49 项 PG 17 全量真实 fixture 属于 `395020c` 前置成果，本步没有将其重复运行或作为新事务执行器全范围证明。新增服务只由显式集成 fixture 进入，自动 Go 测试未启用真实服务旗标时会跳过这些 fixture。

```bash
PATH=/usr/local/go/bin:$PATH GOWORK=off GOCACHE=/data/.cache/golang \
  bash tools/verify_postgres_fixture.sh --race \
  --run 'TestPostgreSQL17DesignDDLTransaction.*Fixture'
```

PG 14 使用同一模式加 `--postgres-version 14`。PG 18 使用 `--postgres-version 18 --run 'TestPostgreSQL17DesignDDLTransaction(Lifecycle|LostCommit)Fixture'`，只重放上述限定范围。

最终 `build/linux-amd64/nyadb` SHA-256 为 `bb3b9e52b85aa61815ff934d011ef0f2e8944773183be61a61363683fd08673e`；日志和检查汇总位于忽略目录 `build/verify/postgres-ddl-apply-*.log` / `postgres-ddl-apply-checks.json`。

本步五个成功/失败运行的唯一容器均通过 daemon 可用、明确不存在响应及全量容器 ID 列表独立核对，审计为 `build/verify/postgres-ddl-apply-cleanup-audit.json`，未操作其它资源。按用户逐项提交要求，本步验收后本地归档，不 push 或改写历史。[完整剩余清单](reconstruction-remaining-2026-11-02.md) 和其它功能目标继续有效。
