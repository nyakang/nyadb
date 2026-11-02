# PostgreSQL 原目录上下文补读 — 2026-11-02

继续 [保留资产属性核验](postgres-retained-catalog-2026-11-02.md)，为改表后的原上下文核验提供按原身份补读的服务路径。实现 I；正式 PG `SchemaDesign` 仍只开放 Snapshot，Apply/CanApply 未接通。

## 实施结果

新增私有 `postgresCatalogSnapshotPreserving`。普通快照仍消费当前命名空间及其依赖闭包；补读快照把原来捕获的目录类/对象身份加入读取种子，随后读取**服务当前记录**。原表删除字段/外键后退出当前闭包的外部函数、类型、枚举、cast、关联表/序列、发布及其他已支持原目录对象，仍可以取得核验材料。原记录真正被删除时不返回旧内容，也不重新创建对象。

种子参数只包含受固定映射约束的目录名、目录类 OID 和对象 OID，不包含 SQL、名称标签、owner、注释、正文或凭据。字段/索引/序列使用所属 pg_class 身份；枚举使用 enumtypid、range 使用 rngtypid、aggregate 使用 pg_proc 身份，不把子记录的数字猜成父对象身份。依赖和安全标签保留原目录类，数值 OID 重号不会混为同一类对象。参数去重、稳定排序，原输入深复制且不被修改。

读取之前验证原完整目录格式与支持范围；参数限 10000 个身份、1 MiB，结果仍受原有 10000 条、单记录 1 MiB、全体 16 MiB 限制。未知记录类别、无效 OID、错误版本的类别及超限明确报错，不静默丢弃。取消和失败均返回空结果，不发布部分目录，也不回显种子记录中的私有文本。

版本化 SQL 扩展原种子及直接 required 对象选择，覆盖 default ACL、访问方法族成员、发布/发布成员等退出当前关联范围的已捕获记录。PG 14 生成的 SQL 不引用 PG 15 新增的 pg_publication_namespace；没有声称 PG 14 真实运行通过。

独立读取头部现在包含服务实际返回的数据库/schema 名称，不能直接使用调用者标签作为读取结果。原数据库/schema 名称、OID、服务器版本和原生字节序必须与独立头部相符，错误原范围在主目录查询之前拒绝。仍由原 Session/运行时边界绑定 profile 和 generation，没有引入跨连接自动恢复或新生产 API。

原生 CREATE/ALTER fixture harness 在原表 alter 后读取补充上下文，并用它执行已有保留属性检查。返回给后续 fixture/workbench 的基线继续使用普通当前闭包及修订，补充旧上下文不成为新的浏览范围或编辑授权。

## 真实服务验证

新增 `TestPostgreSQL17DesignOriginalContextFixture`：原表含外部枚举和外键，关联外部函数/cast、表、身份序列与发布。删除本地字段和外键后，普通快照不再包含上述外部身份；补读快照仍能读到它们。随后修改外部函数正文、表注释并增加枚举值，补读显示当前变更；再删除 cast/函数，补读不返回已删除函数。原本地表仍只有当前保留字段，旧请求及旧目录缓冲不被改变。

外部表的触发器/缓存标记可能随 DROP FK 改变，本测试不把整行旧 JSON 当成应恢复的行；仅检查其未修改的 owner/ACL/注释/名称及身份，其余仍取当前服务记录。没有因此授予忽略这类状态变化的提交权限。

新增 `TestPostgreSQL17DesignOriginalContextScopeFixture`：错误数据库/schema OID、版本、字节序和调用者数据库标签返回冲突；取消及未知记录种子不发布部分结果，随后在原连接上的只读重读仍可正常执行。四个原范围变异子项使用真实服务；无 mock 替代通过结果。

纯回归覆盖目录类重号、子记录父身份、重复/乱序、参数中不出现正文、无效精确数字、未知类别及版本、错误原范围和预先取消。首次新增测试误用了 ObjectRef 的构造字段，已按实际中立模型修正；该编译失败不计通过。

## 检查与证据

Linux amd64，Go 1.27.3 / MyGo v0.7.3-nyadb.2，HEAD `5960d26`，`GOWORK=off GOCACHE=/data/.cache/golang`。原有未提交成果保留，无 commit/push。

中间真实验收：

- `build/verify/postgres-Of7q94`：2 项原上下文真实场景及 4 个范围子回归通过，9.983 秒。
- `build/verify/postgres-70GezE`：加入原生验收接线后的 5 项定向场景通过，204.805 秒；native plan 127.17 秒、原属性 53.50 秒。这是中间验证，不能替代完整服务包。

| 检查 | 实际结果 |
|---|---|
| engines 默认回归 | 通过，0.396 秒；默认跳过的集成测试不计真实服务通过。 |
| contract/schemaddl/engines/dbruntime/objectworkspace race，20 分钟超时 | 通过；engines 3.945 秒、dbruntime 1.086 秒，其余未修改包复用正常测试缓存。 |
| `go vet ./...` / `go tool mygo vet` | 通过。 |
| 翻译生成 / i18n check | 通过，生成目录已是最新，1338 条 zh-Hans/en 消息。 |
| `go test ./... -timeout=20m` | 通过；engines 0.473 秒、dataworkspace 49.749 秒、Shell 77.837 秒、sqlworkspace 3.174 秒，其余未变化包按正常 Go 缓存机制复用。 |
| `go tool mygo build` | 通过，22.1 秒；正式 Linux amd64 程序及 deb/tar 已生成。 |
| 最终完整 PostgreSQL 17.10 服务包 `--race` | 20 个主场景及 18 个子回归通过，engines 408.337 秒；原上下文 6.36 秒、范围 2.44 秒、native plan 132.42 秒、原保留属性 52.06 秒。 |

正式二进制 `build/linux-amd64/nyadb` SHA-256：`6c2fb94201a169f20a14035149a355cd72591b504edd9605983d5a8de51ed094`。构建通过不计原生窗口执行通过。本步没有 UI/Python 改动，没有重跑无关窗口或截图工具。

两个中间运行均记录 `owned_container_removed=true`，独立 `docker inspect --type=container` 确认原容器不存在；各证据目录保存 `cleanup-audit.json`。

最终证据：`build/verify/postgres-soKbJ1`，包含 `tests.log`、`versions.txt`、`resource.txt`、`validation.json` 与 `cleanup-audit.json`。`test_exit=0`、`owned_container_removed=true`；独立 `docker inspect --type=container` 确认本次容器 `63259f90487ae4fc3366e97603b852358a46ddb4c32285e699b1d62fc1199e43` 不存在。18 个子回归包含已有 7 项声明图、7 项保留属性变异及新增 4 项范围变异。最终 `git diff --check` 通过。上述时间为该环境下工具/服务验收耗时，不是 UI 性能基准。

完整重放：

```bash
GOWORK=off GOCACHE=/data/.cache/golang PATH=/usr/local/go/bin:$PATH \
  tools/verify_postgres_fixture.sh --race
```

新定向场景可加 `--run 'TestPostgreSQL17DesignOriginalContext.*Fixture'`，不能代替完整服务包验收。

## 继续实施的证明

补读是核验输入，不是原上下文保留通过。还须按原资产、实际重建 OID/字段映射及明确请求检查完整未选入目标的记录/依赖集合；区分真实删除、闭包退出、新建对象和允许的原生副作用。入站外键/触发器、policy/rewrite/publication、物理/TOAST 重写、掉落字段残留、新资产属性及序列当前值仍需逐项证明和回归。

完整依赖锁定/并发冲突、独立 CREATE 探针的用户函数及非事务性序列副作用、权限要求、创建/分区/继承迁移、正式事务提交确认和 PG 表单仍未完成。不能用“原记录现在可读”开放一般 Apply，或宣称任意并发 DDL 下的全部派生定义已经验证。

本步没有 UI/Python 改动、原生窗口截图、多平台/其他 PG 版本/大端运行结果或 Navicat O/S/V；未启动原版或连接生产。

PG/SQLite 对象 CRUD/导入/备份/隧道、跨库传输/数据与结构同步、自动运行、ClickHouse、可配置 OpenAI 兼容 AI SQL、模型/逆向、BI、监控、字典、生成、脱敏、CLI 及精细交互/原生与原版验收继续属于 [完整目标](workbench-completion-2026-11-02.md)，整体未完成。
