# PostgreSQL 运算符、操作符类与访问方法探针 — 2026-11-02

从 `0f1e14e` 接续未完成补丁。真实 PostgreSQL 已复现新运算符及内建符号用户重载在空验证表索引声明中执行 IMMUTABLE 用户函数并消费序列；事务回滚不能撤销 nextval。此步在创建独立验证表之前读取目录并拒绝这些调用。仅新增 NyaDB 实现 **I** 和真实服务回归，不新增 Navicat **O/S/V**，不启用 PostgreSQL 正式 SchemaDesign Apply。

## 实施与限制

- 纯 `schemaddl` 词法读取字段默认值/生成表达式、约束、索引及触发器声明中的运算符；识别 OPERATOR 限定符、比较语法和索引方法/操作符类。注释、字符串、命名参数与索引配置语法不误记为调用；不能确定的表达式类型保持未知。
- 请求私有的目录检查读取全部同名 core 候选，拒绝用户运算符/重载，即使位于 pg_catalog 或自称 IMMUTABLE。显式和相关默认操作符类及 family 支持函数沿用类型回调检查。普通 heap 和 btree/hash/gin/gist/spgist/brin 要求真实 core OID、handler、语言及返回类型，用户访问方法别名不能借核心 handler 获得支持。
- 部分 core `+` 运算符使用 SQL 函数反转参数。直接拒绝所有 SQL 语言会误伤普通数值表达式；新检查只接受已保存 prosqlbody 中一个无来源、无条件的 SELECT/RETURN，其唯一表达式为作用于两个反转外部参数的 OPEXPR。逐项核对签名、真实 operator/function OID 和终端 C/internal core 实现；不对用户 SQL 执行 PREPARE/EXPLAIN 来获取解析证据。
- PG 18 新增 hasGroupRTE、returningOldAlias/returningNewAlias 的无作用值已明确适配；未知属性继续拒绝。被替换为用户函数调用的内建 SQL 转发体亦拒绝。
- 检查发生在 SAVEPOINT/CREATE 之前；失败保留调用方事务、原表行、请求值和默认访问方法。请求之间不缓存许可，不改变凭据、持久化格式或公开设计能力。

这是保守的候选检查，没有完整 SQL 类型解析；不相关的同名重载或候选默认类也可能使声明不可用。目录读取不是并发定义锁定或一般副作用证明。普通表达式的隐式函数调用、所有动态 dispatch、并发 DDL、完整新字段语义及正式事务 Apply/PG 表单继续未完成。核心原生运行库真实性仍由部署环境保证。

## 回归与失败记录

验收使用自有一次性 PostgreSQL fixture、随机 localhost 端口、明确集成开关和资源身份；不连接已有服务。

- `postgres-2YpY1W` 是补丁接入前的真实反例：两个运算符样例消费序列；显式/默认操作符类与访问方法样例被接受，但没有观察到序列变化，不能写成这些样例已执行副作用。
- `postgres-4SQ4SI` 拒绝危险样例，但误拒普通 core `+` 及已有目录表达式 fixture；`postgres-8LhSSS` 定位到 SQL 语言内建转发，后续按解析体限定处理。
- 初次 PG 18 专项因新 QUERY 属性拒绝 core 转发，失败日志保留为 `postgres-probe-dispatch-pg18.log`；明确适配后使用独立 fixture 重跑。
- 早期 `postgres-RthZ3l` 不作为被替换内建转发体的有效证明：该新增样例先有多余括号。最终修正 SQL，并要求错误匹配 dispatch preflight，避免其它拒绝原因被算作该回归通过。

Go 1.27.3 / MyGo v0.7.3-nyadb.2 / Linux amd64，`GOWORK=off`，SDK `/usr/local/go`，缓存 `/data/.cache/golang`。本步未启动原生窗口或 Navicat。

| 检查 | 实际结果与范围 |
|---|---|
| 全量 Go | `go test ./... -timeout 20m` 通过；最终 Shell 98.349 秒。相关 schemaddl/engines/dbruntime/objectworkspace race 通过，最终 schemaddl/engines 为 8.286/7.178 秒；其余包使用有效缓存。 |
| 静态/翻译/构建 | `go vet ./...`、`go tool mygo vet`、翻译生成/检查（1456 条）和 `go tool mygo build` 全部 exit 0；原生运行没有在本步执行。 |
| PostgreSQL 17.10 全量 fixture | `postgres-ZrLiSf` 49 个顶层 fixture 全部通过，593.400 秒；原生连续 DDL 规划及原保留属性均通过。此 run 编译早于只增加 PG 18 无作用 QUERY 字段的兼容修正；最终源码的 PG 17 新增 race、PG 18 专项、全量 Go/race/检查与构建均在该修正后运行。 |
| PostgreSQL 17.10 新增专项 race | `postgres-tXD31m` 8 个顶层 fixture 通过，45.624 秒；包含被替换的内建 SQL 体的有效拒绝回归。 |
| PostgreSQL 14.24 专项 | `postgres-uQX2rL` 18 个顶层 fixture 通过，101.342 秒，含既有函数/类型、目录表达式与新增 dispatch，不等于 PG 14 完整设计验收。 |
| PostgreSQL 18.6 修正专项 | `postgres-GAAfzb` 8 个顶层 fixture 通过，40.743 秒。初次失败保留，不把专项等同于 PG 18 完整设计验收。 |
| GUI/原版 | 没有新增实际窗口或 Navicat O/S/V；全量 Go 内的 Tester 结果不替代原生及差分验收。 |

最终二进制 `build/linux-amd64/nyadb` SHA-256：`0c3585c90e9d43a8223d77b03d969f5435655c769960a019917e92a14c7b6b2d`。完整日志在忽略目录 `build/verify/postgres-probe-dispatch-*.log`；退出码与范围汇总为 `postgres-probe-dispatch-checks.json`。

最终全量容器 `1af8048bed4c34dc8ca648c8c0123957f1391bc7dd2e64a3b0e3ffa15dc33492` 已确认不存在。包括前置反例、失败、通过和两次 core 目录诊断，本步全部 11 个独立容器均通过 Docker daemon 可用检查、明确的不存在响应与全量容器 ID 列表独立核对。审计 `build/verify/postgres-probe-dispatch-cleanup-audit.json`，未修改其它资源。首次审计对小写 Docker 错误做了区分大小写匹配，产生假阴性；保留初次审计，修正匹配后重新读取服务确认，而不是将失败查询当作清理成功。

## 重放

```bash
PATH=/usr/local/go/bin:$PATH GOWORK=off GOCACHE=/data/.cache/golang \
  bash tools/verify_postgres_fixture.sh

PATH=/usr/local/go/bin:$PATH GOWORK=off GOCACHE=/data/.cache/golang \
  bash tools/verify_postgres_fixture.sh --race \
  --run 'TestPostgreSQL17DesignProbe(Operator.*|IndexClass|DefaultIndexClass|AccessMethod|CoreDispatch|CoreOperatorCatalog|ChangedCoreForwarder)Fixture'
```

使用同一专项模式加 `--postgres-version 14` 或 `--postgres-version 18` 重放版本验收。工具记录退出码和本轮容器身份，退出时只清理该容器。验收时变更保留于工作树；后续用户恢复“每完成一项请提交一次”的要求，本项按该更新要求本地归档，不 push 或改写历史。其它完整待办见 [剩余复刻清单](reconstruction-remaining-2026-11-02.md)。
