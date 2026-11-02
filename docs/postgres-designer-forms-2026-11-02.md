# PostgreSQL 原生设计表单准备 — 2026-11-02

本阶段补齐 sqlworkspace 以外、由 objectworkspace 所有的引擎原生设计表单及预览路径。准确状态是组件实现 **I** 和 ui.NewTester 验证；正式 PG `SchemaDesign()` 仍只提供 Snapshot，尚未开放 Apply。不能将注入组件截图当作已经启用的正式 PG 图形设计器。

## 表单与生命周期

- 共享设计文档新增 PostgreSQLField 的独立表单及基线，保持 SQLiteField、完整 catalog、稳定 clause ID 和字段属性分离。按真实 Snapshot.Engine 选择 PG/SQLite 规划器，不将 PG 类型、identity 或生成列套用 SQLite SQL。
- PG 字段提供名称、完整类型、非空、默认值、collation、identity 模式及完整 sequence 选项、STORED/VIRTUAL 生成式和注释。VIRTUAL 按服务器版本提供；选项互斥由表单及原纯规划器共同校验。仅改名称保留原声明原文、ACL、存储参数及目录属性。
- 约束名称修改通过原稳定 ID 和限定目标重绑完整定义，新增 CHECK 使用 PostgreSQL boolean。索引和触发器模板限定原 schema/table，触发器使用 EXECUTE FUNCTION；不会将 SQLite 的 BEGIN SELECT 触发器作为 PG 输入。
- 表属性提供注释、访问方法、表空间、持久性及逐项存储参数，保留已有 owner/ACL/RLS/继承数据。输入、选项有可见标题和可访问名称；权限和未实现属性不会因局部表单获得重写许可。
- 缺失表使用 PG 原生字段/主键模板。主键名称保留 `_pkey` 后缀并按 UTF-8 字符边界限制为 63 字节。新表名称可以在预览前修改，随后新增的索引/触发器引用最终名称；既有表仍引用原捕获目标。相应内部值回执修复见 [新表名称记录](postgres-create-chosen-name-2026-11-02.md)。
- 未应用的 PG 字段、名称、原始定义及表属性参与脏文本守卫和预览修订检查。预览后的 identity 修改不能借旧预览执行，非法默认值/identity 组合保持原草稿。既有原范围、generation、私有缓冲回执、停放、取消和不确定操作禁重放规则保留。

## 真实并发失败与入口状态

`postgres-n623jq` 从独立物理连接，在类型预检查结束后、独立 CREATE 到达服务器前，对原 domain 增加用户函数 CHECK。服务最终 rolled_back，但隐藏投影调用了该函数并消费 sequence；主回归 **exit 1**，13.521 秒。回滚没有撤销该副作用，故正式 Apply 没有开放。该未解决回归保留于工作树，后续必须修复，不能通过默认测试跳过集成旗标宣称它通过。

初次尝试以普通空表的类型依赖作为锁，`postgres-iDwWBx` **exit 1**。实际 pg_locks 没有 domain object lock，另一连接 ALTER DOMAIN 成功。后续把此实验保存为明确的负面平台证据 `TestPostgreSQL17DesignDomainDependencyDoesNotLockTypeFixture`；在 `postgres-zRaVw0` 真实 race 中通过，不作为并发缺口修复结果。

同时读取了公开 PostgreSQL 17 [typecmds.c](https://github.com/postgres/postgres/blob/REL_17_STABLE/src/backend/commands/typecmds.c) 和 [lockcmds.c](https://github.com/postgres/postgres/blob/REL_17_STABLE/src/backend/commands/lockcmds.c)。前者 get_rels_with_domain 的注释指出类型新引用无法被该关系扫描稳定锁住；后者表锁权限随锁级别检查，普通 SELECT 不授予 SHARE 权限。这是公开数据库实现背景，不是 Navicat **S**。原始证据保留于忽略目录 `build/verify/postgres-typecmds-17.c` / `postgres-lockcmds-17.c`。

下一步需要验证类型/约束稳定性和权限方案，以及避免隐藏探针重复执行动态 domain CHECK 的路径；之后再公开 Apply、补实际窗口保存及更多引擎范围。完整 [剩余清单](reconstruction-remaining-2026-11-02.md) 和 [工作台目标](workbench-completion-2026-11-02.md) 保持有效。

## 组件证据与检查

五项 PG Designer Tester 用例覆盖名称/原文/属性保留、identity/旧预览、非法字段保留草稿、PG 约束和触发器模板、缺失表原生模板/新名称/程序目标，以及三个尺寸的固定应用按钮。另加 ASCII/多字节长名称的约束名回归；最后与已有 SQLite Designer 回归合跑 1.472 秒通过。没有使用真实 PG 服务渲染这些组件图片。

已查看注入组件的 [1280×800](../build/verify/postgres-designer-components/fields-1280x800.png)、[840×560](../build/verify/postgres-designer-components/fields-840x560.png)、[530×430](../build/verify/postgres-designer-components/fields-530x430.png) 图片。默认 Tester 浅色，530×430 是组件范围，不改变正式窗口最小尺寸；小尺寸下字段区滚动，应用按钮保留。没有正式 NyaDB 原生 PG 保存窗口、Windows/macOS 或 Navicat O/V 结果。

```bash
GOWORK=off GOCACHE=/data/.cache/golang \
NYADB_POSTGRES_FORM_CAPTURE=/data/kang/nyadb/build/verify/postgres-designer-components \
  go test ./internal/ui/objectworkspace \
  -run 'Test(PostgreSQL|SQLite)Designer' -timeout=20m
```

Go 1.27.3 / MyGo v0.7.3-nyadb.2 / Linux amd64；GOWORK=off。最终全量 Go、schemaddl/engines/dbruntime/objectworkspace race、Go/MyGo vet、翻译生成和检查（1466 条）通过。静态及默认 Go 通过不替代上述真实并发失败；没有执行完整 Shell race或真实 PG 全范围集成。日志为 `build/verify/postgres-designer-form-*.log`。Python 工具未修改。

最终全量源码测试 objectworkspace 6.258 秒、Shell 110.737 秒；最终 objectworkspace race 29.679 秒，其余包使用有效缓存。最终 MyGo 打包 exit 0、28.1 秒，提交前二进制 SHA-256 为 `e60c44e40734308951a278cd45e4531718053f520044972c97e9e356cba906aa`。打包不算实际窗口验收。

三个成功/失败 runs 的自有容器均通过成功 Docker 列表和 inspect 明确不存在响应独立核对，身份/版本/退出码/日志哈希见 `build/verify/postgres-designer-form-evidence.json`。并发回归的失败状态也记录在该文件中，未操作其它资源。

本阶段只创建及清理自己的独立 PostgreSQL fixture，不连接生产、运行 Navicat、push 或改写历史。按用户逐项要求归档已验证的服务名称修复和组件准备；尚未解决的并发用例继续保留工作树，不将其视为已交付。
