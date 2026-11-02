# PostgreSQL 未编辑目录核验与闭包读取修复 — 2026-11-02

继续 [原目录上下文补读](postgres-original-context-2026-11-02.md)，新增已捕获未编辑记录的严格保留核验。实现 I；正式 `SchemaDesign` 继续仅公开 Snapshot，Apply/CanApply 未接通。

后续已增加明确字段删除的独立原属性槽预期，原生验收中的所有字段删除计划在提交前执行；本页记录此前严格不允许新 residual 的阶段。当前授权范围、PG 14/17/18 验证及其余未完成项见 [删除字段属性槽核验](postgres-dropped-attribute-2026-11-02.md)。

## 实施

`schemaddl.VerifyPostgreSQLUnselectedCatalog` 先执行已有独立目标声明图及原资产属性检查，再比较原/当前快照中未选入目标声明图的记录。两边按各自草稿建立目标绑定，未编辑记录必须仍以原目录类别、OID、子对象身份存在；不能通过实际归属变化或新增草稿别名取得编辑授权。

已捕获的外部关系/字段/类型/函数/枚举、入站外键及触发器、policy/rewrite/publication、默认 ACL、目录描述及其它原记录均保留完整 JSON。未知字段和精确数字不被剥离，物理字段也不被笼统忽略。缺失、新增、变化或转入选中范围返回冲突；新发现依赖上下文也不会自动成为获准基线。输入深复制/独立解码，失败不返回部分证明。

`pg_depend` 的读取容器按数字 objid 聚合，可能同时包含不同目录类。核验按 **source classid + objid** 拆分，仅目标选中来源交给独立声明图核验；外部来源指向目标的入站边继续检查。保留重复边、未知边属性及未知容器属性，按规范化 JSON 排序消除返回顺序差异。安全标签按目录类处理；投影跳过的删除字段残留和其标签继续属于严格核验范围。

记录/OID/子对象、依赖身份和属性归属有明确验证，沿用 10000 条、单记录 1 MiB、总量 16 MiB 的目录限制。错误只报告冲突类别及数值身份，不输出 SQL、标签、属性值或用户控制的字段名。

## 真实发现并修复的读取问题

首次真实服务验收中，完全不改外部对象的请求也出现“新增未编辑记录”。定位发现初始目录已经输出某些 TOAST 索引和发布成员，却没有让其身份参与同一次依赖闭包。后续按原身份补读才取得这些对象的关系、字段和依赖。复杂目录还暴露了复合类型所对应关系的依赖遗漏。

修复 `postgresDesignCatalogSQL` 的读取闭包：增加关系到索引/自动归属关系、行类型、默认值/约束/触发器/policy/rewrite/发布成员，类型到数组/复合关系/类型约束，以及继承子关系与访问方法族成员的结构读取关联；默认 ACL、全库发布和版本支持的 schema 发布成员参与初始种子。它们是 **读取关联**，不写入 PostgreSQL，也不伪造输出中的 pg_depend 边。输出仍是当前服务真实记录。

复杂真实 fixture 连续两次将捕获记录重新作为种子，修订保持一致；包含 TOAST/复合类型、默认 ACL、继承/分区、访问方法族、表/schema 发布及全量原有资产。PG 14 的 SQL 仍不引用 PG 15 才有的 pg_publication_namespace；PG 14 只有纯版本回归，没有新增真实运行结果。

## 回归范围

纯回归覆盖同数字 OID 不同目录类、入站 FK/触发器、policy/rewrite/publication、外部物理属性、掉落字段残留、标签和未知精确属性；缺失/新增/重新归属；依赖重数、顺序、未知边/容器属性；错误范围、重复/非法记录与错误外部字段归属。标签及未知字段使用纯证据 fixture，不宣称进行了真实扩展标签写入。

纯回归初次执行的重新归属 fixture 缺少触发器函数/类型属性，在绑定校验阶段被拒绝，尚未到达期望的原上下文冲突。补齐 fixture 的原生身份属性后通过，不将最初失败算作通过。

新增 `TestPostgreSQL17DesignUnselectedContextFixture` 使用真实 PostgreSQL：原目标有外部类型/cast/函数、双向外键、外部视图、RLS policy、发布及关联身份序列。目标仅请求注释修改；预先捕获的原目标声明图独立于后续外部变化，不调用新增 CREATE 探针。

11 个子场景验证未改原上下文可通过，以及实际修改/删除函数、增加枚举成员、删除入站外键、禁用入站触发器、修改 policy、视图规则、发布、外部序列配置及关系注释会冲突。这些变更先确认通过原目标属性核验，再确认被新上下文检查识别，避免用目标本身变化制造假阳性。每项均取得真实 ROLLBACK 回执，最后重读原目录并核对保留行与文本长度。

## 检查与证据

Linux amd64，Go 1.27.3 / MyGo v0.7.3-nyadb.2，HEAD `5960d26`，`GOWORK=off GOCACHE=/data/.cache/golang`。全部原有未提交成果保留。

早期失败证据保留在 `build/verify/postgres-Kx881s`、`postgres-54Xyhg`、`postgres-GlZe2O`、`postgres-3SjBrt`、`postgres-Bm9NU1`、`postgres-23gun8`：包括最初闭包误报以及复杂复合关系遗漏；不计通过。`postgres-eC4RMo` 是首轮修复后的 11 个子场景通过，23.372 秒。`postgres-HagTyG` 是最终闭包修复的四个定向主场景及 15 个子场景通过，76.537 秒；复杂目录 40.95 秒，未编辑上下文 26.07 秒。定向验收不能替代最终完整服务包。

| 最终检查 | 实际结果 |
|---|---|
| `go test ./... -timeout=20m` | 通过；schemaddl 0.427 秒、engines 0.386 秒、dbruntime 0.027 秒、Shell 75.115 秒；其它未变化包按正常 Go 缓存复用。默认跳过的集成测试不计真实服务通过。 |
| contract/schemaddl/engines/dbruntime/objectworkspace race，20 分钟超时 | 通过；schemaddl 3.806 秒、engines 4.082 秒、dbruntime 1.096 秒；未变化包使用正常缓存。此前本步首轮 objectworkspace race 18.431 秒通过。 |
| `go vet ./...` / `go tool mygo vet` | 最终通过。 |
| 翻译生成 / i18n check | 通过，生成目录已是最新，1338 条 zh-Hans/en 消息。 |
| `go tool mygo build` | 最终通过，26.7 秒；Linux amd64 程序及 deb/tar 已生成。 |
| 最终完整 PostgreSQL 17.10 服务包 `--race` | 21 个主场景及 29 个子回归通过，engines 541.335 秒；新增未编辑上下文 85.44 秒、复杂目录 48.78 秒、native plan 141.99 秒、保留属性 59.60 秒。 |

最终证据：`build/verify/postgres-YNuC7t`，包含 `tests.log`、`versions.txt`、`resource.txt`、`validation.json` 与 `cleanup-audit.json`。`test_exit=0`、`owned_container_removed=true`；独立 `docker inspect --type=container` 确认本次容器 `e9f36ebdfbe95b1932693bc721953f57bef7692ac1f9be8d866406600c9535b3` 不存在。29 个子回归包含已有 18 个及新增 11 个上下文场景。最终 `git diff --check` 通过。时间为工具/服务测试耗时，不是 UI 性能基准。

正式二进制 `build/linux-amd64/nyadb` SHA-256：`ce930034c501fe7772e6adc5607763c87e5a844089f411de3333149e6b678d53`。构建通过不计原生窗口执行通过。

上述 8 个中间运行均记录 `owned_container_removed=true`，独立 `docker inspect --type=container` 确认原容器不存在；各目录保存 `cleanup-audit.json`。未按名称清理其它资源，失败证据保留。

完整重放：

```bash
GOWORK=off GOCACHE=/data/.cache/golang PATH=/usr/local/go/bin:$PATH \
  tools/verify_postgres_fixture.sh --race
```

## 仍须完成

本检查是“已捕获且未获准改变的上下文必须不变”的保守条件。不能据此宣称一般原生 ALTER 的全部上下文副作用已获证明。合法新上下文、掉落字段残留、FK/索引/触发器重建造成的授权变化、外部缓存或派生定义变化仍需按原请求/实际 OID 和属性映射单独证明；当前保守拒绝，不能用忽略字段绕过。一般 native ALTER fixture 仍使用此前的保留目标核验，新上下文检查尚未绑定到全部类型变更/重建路径。

目标物理/TOAST 重写、保留及新字段的数据语义、序列当前值、并发依赖锁和冲突策略仍未完成；读取闭包稳定不代表任意并发 DDL 下的一致性已证明。独立 CREATE 探针的用户函数执行、非事务性序列副作用和权限策略、完整分区/继承/迁移、正式提交及 PG 表单仍待实施。

没有本步原生窗口/Python 改动或截图，没有新增其他 PG 版本/平台/大端或 Navicat O/S/V；编译和 Tester 回归不计真实窗口验收。未启动原版、连接生产或 commit/push。

PG/SQLite 对象 CRUD/导入/备份/隧道、跨库传输/数据与结构同步、调度、ClickHouse、可配置 OpenAI 兼容 AI SQL、模型/逆向、BI、监控、字典、生成、脱敏、CLI 和精细交互/原生/原版验收继续属于 [完整目标](workbench-completion-2026-11-02.md)，整体未完成。
