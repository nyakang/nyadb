# PostgreSQL 新字段值的独立核验 — 2026-11-02

从 `09f6343` 接续 PostgreSQL 服务内部 CREATE/ALTER 执行器，补齐新默认值、存储生成列及新 identity 分配的限定独立核验。证据为独立实现 **I** 和自有 PostgreSQL 服务结果。正式 `SchemaDesign()` 仍只提供 Snapshot；本阶段没有发布通用 Apply、PG 表单或实际窗口保存流程，没有新增 Navicat O/S/V。

## 缺陷与实现

基线真实故障注入在 ADD COLUMN 成功后改变新默认值或生成列的行值，同时保留最终声明。旧执行器仍返回 committed；原保留字段和声明核验没有覆盖新生成的数据。失败证据为 `build/verify/postgres-e6l0v3/`，两个子场景均失败，22.409 秒。注入只修改独立 fixture；生成列注入暂时修改自有数据库的属性目录以保留最终声明，不是正式应用提供的操作。

新执行器在原范围、完整修订和 ACCESS EXCLUSIVE 锁内捕获不可变请求及值证明，然后执行原生 DDL。全部独立核验成功才发送 COMMIT。

- 在 SAVEPOINT 中创建唯一随机名称的回滚专用投影表，保留真实字段类型、默认值、生成表达式和 identity 选项。省略 CHECK、FK、索引和触发器；NOT VALID 约束不要求已有行满足，投影也不应执行原表的 DML 触发器。
- 通过原稳定字段 ID 映射输入，用 `INSERT ... SELECT` 传递真实值，不将单元格渲染为 SQL。新增普通默认值和缺省 NULL 在独立表内计算；原普通值与新默认值作为完整行向量比较，保留重复行及字段关联。
- 新 identity 分配分别比较单列多重集；每个新 sequence 的 last_value/is_called 按稳定 owner ID 绑定核验。独立 ALTER 不承诺相同物理扫描顺序，因此不猜测不同 identity 或原行之间的分配顺序。原 identity/serial 仍使用既有原 OID 和当前状态保留核验。
- 在普通字段及 identity 已通过检查后，将真实普通输入提供给另一个独立投影，重新计算存储生成列，并比较所有字段的行向量。生成值从不作为输入提供给该投影；支持新生成表达式引用新 identity，及 PG 17 对已有生成表达式的显式修改。
- 全部旧普通输入被删除时，使用有限 DEFAULT 批次保留行数，不占用额外载体列。NULL、精确数值、二进制和数组沿用原生 record_send 和流式多重集摘要；单格 1 MiB、单行 16 MiB 上限保持不变。
- 投影表及其新 sequence 归原 SAVEPOINT 所有。错误、取消或会话关闭等待驱动回执后回滚；清理失败撤销物理连接并返回 uncertain。没有成功回执时不返回 Verified/Actual，也不允许自动重放。

## 可重复性与兼容性

需要填充已有行的新普通默认值，先读取独立声明的真实 pg_attrdef 树，收集嵌套 routine dispatch 并核对 pg_catalog volatility。random、gen_random_uuid、clock_timestamp 和 statement_timestamp 不能取得可重复的行值证明，明确拒绝；不会将任意生成结果当作已独立验证。CURRENT_TIMESTAMP 的事务时间和代表性精度、普通常量、二进制 decode 及数组默认值通过。

此检查仅提供可重复性证据，不授予用户函数或任意隐式回调执行权限；独立声明、类型、运算符和函数探针检查仍执行。空表不填充旧行、已有字段 SET DEFAULT 的范围继续使用原规则。此前新增 nextval 在填充旧行前拒绝的检查保持有效。

PG 14 的真实数组默认值使用 `{ARRAY ...}`，较新版本使用 `{ARRAYEXPR ...}`。首次 PG 14 组合默认值测试被保守拒绝，141.385 秒的整轮 exit 1 保留于 `postgres-dtB7xZ`；补充诊断 `postgres-jzEiec` 同样 exit 1。独立诊断容器捕获原节点格式，证据为 `build/verify/nyadb-value-node-88f8b0a56d2b/`。现在对两种数组节点执行相同递归，测试确认其中的用户 OID 仍拒绝，未知节点没有获得许可。错误只指出不能核验的字段名称，不包含默认值或 SQL 正文。

## 回归与检查

Linux amd64 / Go 1.27.3 / MyGo v0.7.3-nyadb.2，使用 `GOWORK=off`、`GOCACHE=/data/.cache/golang` 和 20 分钟测试超时。

| 检查 | 实际结果 |
|---|---|
| 全量 Go | 最终 `go test ./... -timeout=20m` exit 0，schemaddl/engines 1.027/0.883 秒。未修改的 UI 包使用有效缓存；本轮较早全量运行的 Shell/dataworkspace 为 95.239/46.981 秒。 |
| 相关 race | 最终 schemaddl/engines/dbruntime/objectworkspace exit 0，前两包 7.795/6.706 秒，其余使用有效缓存。未重新运行整个 Shell race，本轮没有 UI 生命周期变更。 |
| PostgreSQL 17.10 | `postgres-1irGDZ` 十项顶层真实 race 通过，342.898 秒，包含三项 DDL 事务/生命周期/丢失提交回执及七项新值回归。其后字段错误信息和数组遍历修复的两项 race 在 `postgres-hCHTIv` 通过，89.835 秒；最终 PG 14 声明归一化修复后，PG 17 组合默认值在 `postgres-dm5CoZ` 再次真实 race 通过，28.972 秒。没有把较早十项运行声称为最后归一化版本的十项全量重放。 |
| PostgreSQL 14.24 | 最终 `postgres-fwCaXy` 七项顶层回归通过，154.375 秒，包含 NULL/精确数值/二进制/数组/重复行、identity/序列篡改、全部输入删除及真实取消/关闭。已有生成表达式修改用例在该版本验证规划器明确拒绝，不宣称执行了 PG 17 SET EXPRESSION。 |
| 静态、翻译、打包 | 最终 `go vet ./...`、`go tool mygo vet`、翻译生成、i18n 检查（1456 条）及 `go tool mygo build` 全部 exit 0；最终打包 28.2 秒。 |

最终二进制 `build/linux-amd64/nyadb` SHA-256 为 `d28385aee846701b411eec0c929e9f1e860aabdfb14c2b2df080ba5aa2e90101`。本轮没有 UI 变更或实际窗口执行，打包不计为 GUI 验收。自动 Go 测试未启用集成旗标时会跳过真实服务 fixture。

日志位于忽略目录 `build/verify/postgres-new-values-*.log`；版本、退出码与资源身份汇总为 `postgres-new-values-runs.json`。最终容器清理审计和检查身份另保存于同一目录。

本阶段十四个成功/失败 fixture 容器及一个数组节点诊断容器均已清理。独立核对成功的 Docker 全量容器列表和 inspect 明确不存在响应，最终审计为 `postgres-new-values-cleanup-audit.json`；未操作其它资源。首次审计因 Docker 返回小写 `no such object` 而误判为未确认，exit 1 保留于 initial 审计；修正响应大小写处理后重新读取实际状态，没有通过删除其它容器解决审计问题。

早期失败单独保留：`postgres-pPgADW` 的 NULL/二进制核对 SQL 未引用 binary 字段，identity 投影也被规划器拒绝；后续定位投影拒绝的 `postgres-80bCTA` exit 1。修正核对 SQL 及独立投影声明后 `postgres-GkuvC4` 三项通过。`postgres-eST2oF` 与 `postgres-DmU0Hr` 的取消测试在到达 INSERT 门控前耗尽 5 秒预算；后者普通用例通过、整轮仍 exit 1。最终测试将完整准备预算改为 30 秒并检查提前回执，只有数据库侧实际观察到 advisory lock 等待后才取消或关闭，不能用等待时间推断执行状态。

PG 14 首次数组格式修复只接通重复性遍历，后验声明图仍保留 ARRAY 的 parser location，导致 adbin 比较拒绝。后续让声明图归一化同时识别 ARRAY/ARRAYEXPR、绑定类型并排除已知 parser location；真实值、multidims 和未知属性变化仍保留在比较中。第一次修复后的失败整轮日志 `postgres-new-values-final14-corrected.log` 保留，最终结果另列，不将被拒绝的组合默认值算作通过。

## 重放及限制

```bash
PATH=/usr/local/go/bin:$PATH GOWORK=off GOCACHE=/data/.cache/golang \
  tools/verify_postgres_fixture.sh --race \
  --run 'TestPostgreSQL17Design(NewValue|DDLTransaction).*Fixture'
PATH=/usr/local/go/bin:$PATH GOWORK=off GOCACHE=/data/.cache/golang \
  tools/verify_postgres_fixture.sh --postgres-version 14 \
  --run 'TestPostgreSQL17DesignNewValue.*Fixture'
```

工具使用唯一自有容器、随机 localhost 端口、空数据库和显式 NYADB_POSTGRES_IT；只清理本轮资源。未修改凭据、公开接口、数据库迁移或 Python 工具。本阶段不运行 Navicat、不连接生产。验收时保留工作树；随后按用户恢复的“每完成一项请提交一次”要求本地归档，不 push 或改写历史。

该核验不是任意默认/生成表达式的正确性或并发副作用证明。identity 只有分配多重集及状态证明，不承诺扫描顺序；摘要保留现有有界二进制和哈希比较规则。大表投影增加完整扫描、临时关系及 I/O，本阶段没有大数据性能验收。PG 18 独立 NOT NULL 约束没有适配，本阶段没有 PG 18 新值全范围结果。

下一步仍需并发/依赖和其它隐式回调范围、正式 Apply binding、引擎专属表单及实际窗口保存验收。[完整剩余清单](reconstruction-remaining-2026-11-02.md) 的 PG/SQLite 文件与对象工具、大数据、传输/同步、调度及高级功能继续有效。ClickHouse 和可配置 OpenAI 兼容 AI SQL 已有基础实现，其深化和商业/多平台验收仍待完成。
