# PostgreSQL 新表名称的回执核验 — 2026-11-02

从 `a321e32` 接续 PG 原生设计表单准备。新表请求捕获最初占位名称后，用户可以在表选项中选择最终名称。原规划器及运行时都接受 Desired.Name，值核验却仍要求 Actual.Target 等于未改名的 Original.Target，导致合法 CREATE 在提交前回滚。

现在只按独立规范化目标表的名称调整预期 Object.Name；原 profile、catalog、schema、kind 和 generation 保持捕获值，完整请求签名及实际结构名称仍核验。不能借新表改名改变连接或命名空间，也不改变既有表的独立改名路径。

真实 PostgreSQL 17.10 `TestPostgreSQL17DesignCreateChosenNameFixture` 通过，7.43 秒；确认占位表没有创建，回执使用 chosen_name，请求及稳定字段 ID 不变，随后 DEFAULT VALUES 插入保留 numeric(40,20) 的 40 位精确值。与 domain 锁负面证据合并的真实 race 为 `build/verify/postgres-zRaVw0/`，总计 8.518 秒、exit 0。后者只证明普通依赖不能作为类型锁，不证明 domain 并发缺口已修复。

本轮最终全量 Go、相关 race、go vet、MyGo vet、翻译生成及检查均通过；执行命令使用 Go 1.27.3 / MyGo v0.7.3-nyadb.2、GOWORK=off、GOCACHE=/data/.cache/golang 和 20 分钟超时。相关日志为忽略目录 `postgres-designer-form-*.log`。此项只修改服务内部核验和回归，没有新增公开 API、迁移或实际窗口结果。

```bash
PATH=/usr/local/go/bin:$PATH GOCACHE=/data/.cache/golang \
  tools/verify_postgres_fixture.sh --race \
  --run '^TestPostgreSQL17DesignCreateChosenNameFixture$'
```

正式 PG SchemaDesign 仍只暴露 Snapshot；后续公开 Apply、domain/隐式回调并发、原生窗口保存和完整剩余目标继续待完成。本项按用户逐项要求本地归档，不 push、改写历史、运行 Navicat 或连接生产数据库。
