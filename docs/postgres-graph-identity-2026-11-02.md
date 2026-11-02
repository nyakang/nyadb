# PostgreSQL 目标目录身份与表达式核验 — 2026-11-02

延续 [改名目录保真](postgres-catalog-rename-proof-2026-11-02.md)，补齐通用 CREATE/ALTER 目录比较所需的稳定对象绑定及表达式树规范化。当前交付是身份与表达式证据；完整声明图投影、原目录保留证明和正式 Apply 尚未完成，PG `SchemaDesign` 继续只公开 Snapshot。

## 已实现 I

- `schemaddl/postgres_graph.go` 按真实 `pg_catalog` 目录类与 OID 组成对象键。目标表、行/数组类型、默认值、约束、约束索引、用户索引/触发器、用户约束触发器所属约束、外键内部触发器和列归属序列获得明确的逻辑身份。原表及入站外键等上下文记录不获得独立探针目标的身份。
- 显式名称别名连接探针中的随机名称与原草稿的稳定字段/约束 ID、定义身份；不按表名或 OID 数值猜测关联。`postgresDesiredBaseline.GraphNames` 保留这些独立映射。所有输入及原 JSON 保持不变；失败不返回部分绑定。
- 读取并核对表/类型/默认/约束/触发器的原始 OID、索引 OID、属性归属及序列归属；缺少必要行、别名覆盖不完整、重复逻辑 ID、错误归属或模糊内部触发器明确报错。目录 `to_jsonb` 的 OID 十进制字符串和显式 bigint 数字均精确解析，拒绝小数、指数表示及溢出。
- `schemaddl/postgres_node.go` 解析公开 `pg_node_tree` 的节点、列表、Datum 字节数组和转义原子；上限为 1 MiB、65536 tokens、128 层，拒绝无效 UTF-8/NUL、截断及重复/空字段。已知类型/关系引用、明确绑定的本层 VAR 和复合属性按稳定身份规范化；OLD/NEW 分别绑定，未知关系、外层 VAR 或缺失字段不能借用目标字段映射。
- 只对已知节点的已知字段去除 token location 和 CoercionForm 显示差异。函数、类型、参数、字段、排序规则、普通数字/字符串、外部 OID 常量及未知字段保留。FIELDSELECT/FIELDSTORE 使用复合类型属性绑定；NEXTVALUEEXPR 使用关系和 `typeId` 绑定。
- regclass/regtype 的原生 Datum 按捕获的服务器字节序解析，客户端架构不参与推测；支持 4/8 字节承载及有符号字节。未知服务器目标保留未知，涉及非 NULL OID Datum 时明确拒绝规范化。NULL 常量不需要字节序。目录快照新增 `NativeByteOrder` 和五种目录类记录，无 SQLite 迁移。

CoercionForm 和各节点字段经 PostgreSQL 17.10 [公开 `primnodes.h`](https://github.com/postgres/postgres/blob/REL_17_10/src/include/nodes/primnodes.h) 核对；其中 `funcformat` 明确是调用显示形式，NEXTVALUEEXPR 使用 `typeId`。这属于 PostgreSQL 依赖核对，不是 Navicat 原版 S/O/V。规范化仍保留完整已解析运算内容，不能用显示形式变化掩盖不同的函数或类型。

## 验证与失败记录

Go 1.27.3 / MyGo v0.7.3-nyadb.2，Linux amd64，`GOWORK=off GOCACHE=/data/.cache/golang`，HEAD `5960d26`。现有未提交成果保留。

纯回归覆盖 OID 重号、生成资产、用户约束触发器、外键目标/外部触发器区分、入站/上下文排除、字段编号空洞、无效/模糊身份、原生 OID 字符串、精确数字及未修改调用方输入。表达式回归覆盖 OLD/NEW、外层变量拒绝、复合属性、显示转换、函数变化、未知属性/原始常量保真、两种字节序和尺寸/编码边界；另有 fuzz 回归。

真实 PostgreSQL fixture 对原表和 rollback-only 独立 CREATE 探针比较逻辑对象集合及 17 组默认值、CHECK、索引表达式/谓词、用户/内部触发器树。原表存在删除字段留下的 attnum 空洞，探针字段连续。规范化后的树一致；替换 CHECK 树后仍能识别实质变化。探针清理后原目录修订保持一致，测试明确断言 PG Apply/CanApply 仍未开放。

失败运行原始记录保留：

- `build/verify/postgres-dlld1D`：未接受 PostgreSQL 原生 OID 字符串；修复解析并补回归。
- `build/verify/postgres-ICk2PT` 和 `postgres-dxkfsc`：重新解析服务器反解 SQL 后，数值隐式转换变为显式转换，导致五组树差异；核对公开 CoercionForm 字段后修复，其他解析内容保持完整。
- `build/verify/postgres-Q21zBW`：首次定向真实 race 验证通过；后续增加目录身份/序列记录校验，以最终全包运行作为最终证据。

最终全量检查、服务结果、二进制身份与独立清理审计见下方最终验收记录；失败运行不计通过。

## 最终验收记录

| 检查 | 实际结果 |
|---|---|
| `go test ./... -timeout=20m` | 通过；Shell 75.273 秒、dataworkspace 46.591 秒、objectworkspace 3.476 秒、engines 0.409 秒。默认跳过的集成测试不计真实服务证据。 |
| contract/schemaddl/engines/dbruntime/objectworkspace race，20 分钟超时 | 通过；schemaddl 2.088 秒、engines 3.612 秒、objectworkspace 18.484 秒。没有重跑未修改的 Shell/dataworkspace 全包 race。 |
| `go vet ./...` / `go tool mygo vet` | 通过。 |
| 翻译生成 / `go tool mygo i18n check` | 通过，1338 条 zh-Hans/en 消息，生成目录已是最新。 |
| `go tool mygo build` | 通过，25.9 秒；正式 Linux amd64 程序、deb/tar。 |
| 最终表达式 fuzz，15 秒，4 workers | 通过，42863 次执行；总工具时间 16.037 秒。此前初始版本的 152863 次执行单独通过，不代替最终版本结果。 |
| 完整自有 PostgreSQL 17.10 fixture `--race` | 14 个场景全部通过，engines 212.440 秒；新增 graph 场景 6.55 秒、native plan 69.85 秒。 |
| `git diff --check` | 通过。 |

最终正式二进制 SHA-256：`9b5b80c0cd0654ec40f7eee998012eb18c68d90572a43fe54064d624d4e2389c`。这些结果是本次环境的验证耗时，不是 UI 响应或性能基准。

最终服务证据位于 `build/verify/postgres-s3TkzY`：`tests.log`、`versions.txt`、`resource.txt`、`validation.json` 和 `cleanup-audit.json`。`test_exit=0`，`owned_container_removed=true`，独立 `docker inspect --type=container` 确认本次容器 `56427eec39207b37b87665e42a9da4ccb994169f1b58c4a728102ec3d83d9b9d` 不存在。前述四个失败/定向运行的自有容器也分别保存了独立清理审计。

完整重放：

```bash
GOWORK=off GOCACHE=/data/.cache/golang PATH=/usr/local/go/bin:$PATH \
  tools/verify_postgres_fixture.sh --race
```

`--race --run 'TestPostgreSQL17DesignGraphIdentityExpressionFixture'` 仅重放本轮新增场景。真实大端服务器、PG 14/18、网络故障、多平台原生窗口没有新增验收；大端 OID Datum 的结果来自纯回归，不声称真实大端运行通过。

## 未完成的必要工作

目标绑定尚未完成所有原始目录属性、属性向量和依赖边的声明图投影/比较；仍需处理探针对原行类型的仅引用映射、所有新建/保留/重建资产的原目录与未知属性保留、入站依赖、序列设置/当前值和冲突/提交核验。不能把本次单独表达式一致性当成完整类型变化或数据迁移证明。

独立 CREATE 探针仍可能执行用户类型输入函数或折叠用户声明的 IMMUTABLE 表达式，非事务性序列副作用无法靠 SAVEPOINT 回滚；还需要原 schema/tablespace CREATE 权限。本轮没有解决这些生产执行问题，探针继续限于私有验收/后端工作，不能据此开放通用 Apply。需为不变复杂表达式复用原权威树，并为完整类型/默认变更建立适当策略。

PG 原生表单、PG/SQLite 对象/导入/备份/隧道、跨库传输/同步、调度、ClickHouse、可配置 OpenAI 兼容 AI SQL、模型/逆向、BI、监控、字典、生成、脱敏和 CLI 仍属于 [完整工作清单](workbench-completion-2026-11-02.md)。本轮没有 UI/Python 变更、真实窗口截图、多平台原生或 Navicat O/V；未启动 Navicat、连接生产、提交或推送。

后续 PG 工作按以下依赖推进：先以本轮显式别名投影独立目标完整声明图，使用目录类/OID 而不是全局数值替换解析 typed OID、属性向量和依赖边；再证明原记录的保留/重建映射、未知属性、权限/RLS/标签、入站依赖和序列状态；解决探针的用户代码/非事务副作用后，接入原锁/修订/值核验与提交确认；最后开放正式 Apply、引擎表单和原生验收。单独的绑定与表达式测试不能跳过中间步骤，也不将尚未证明的复杂定义静默降为可编辑。
