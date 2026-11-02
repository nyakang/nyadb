# PostgreSQL 隐藏探针的类型并发边界 — 2026-11-02

## 故障与修复

PG 服务内部 CREATE/ALTER 执行器通过独立 CREATE 规范化声明，再通过独立表投影核验新默认值、生成列和 identity。预检查 domain 的 I/O 和当前依赖只能证明读取时的定义；普通引用表不会锁定 domain，REPEATABLE READ 也不能保证后续解析器使用同一版本。另一物理连接可以在检查后新增 CHECK，使隐藏验证重复调用用户函数。事务回滚不能撤销 sequence 或其它外部副作用。

修复前的真实 PG 17.10 复现保存于 `build/verify/postgres-n623jq/`：最终 rolled_back，但 counter.is_called 为 true，exit 1。本阶段保留该失败记录，不将其解释为成功或删除反例。

现有私有执行器不再接纳可变用户类型用于隐藏声明或值投影。类型必须有固定系统 OID（小于 16384）、位于 pg_catalog，且不是 domain；数组、复合和 range 的嵌套类型继续递归核验。用户 enum、domain、复合、range 和其数组在 CREATE 之前返回明确的类型稳定性错误。enum 的原生 I/O 本身不能防止它在同名位置被 DROP/CREATE 为 domain，因此不能作为例外。

这是一项明确缩小支持范围的修复，不是一般类型并发锁实现。去除旧的 domain 依赖遍历和用户结构类型许可，避免后续误用一次成功读取作为执行许可。不锁全局系统目录，不使用只约束合作方的 advisory lock，不添加生产 API、迁移或服务器扩展。

正式 PG SchemaDesign 继续仅提供 Snapshot，Apply/CanApply 仍不公开。原生查询、结构读取、完整目录和已有值摘要仍可读取用户类型；本限制只作用于服务内部的 CREATE/ALTER 隐藏验证。一般表达式、其它隐式回调、外部 DDL 和正式窗口保存仍需继续验证。

完整分批验收另外发现内建函数重载的误拒绝：审查所有 `length` 重载时，系统 OID 1713 的 `length(bytea,name)` 是 stable，导致合法的 immutable `length(text)` 生成列被一起拒绝。现仅对该固定 OID、函数名、internal 实现 `length_in_encoding`、参数类型 `17 19`、返回类型 `23` 和 stable 标记形成限定许可；其它身份、配置、集合返回、用户重载及 SQL 转发体的原检查保持。不是放宽所有 stable 函数或仅按名称信任。

新增真实回归确认 Unicode 文本长度生成列可提交且值为 2；实际选择 `length(bytea,name)` 的生成列仍由 PostgreSQL 以 42P17 拒绝，回执 rolled_back，没有 Actual/Verified，原目录和行值保持。错误诊断只增加 scope、受检查的函数名、系统 OID、语言/volatility/参数类型，不输出 SQL 正文或函数体。

## 回归与真实服务

- `TestPostgreSQL17DesignConcurrentDomainCallbackFixture/captured_lookup`：真实类型名称查询读取 OID并关闭行集后，从独立物理连接新增 domain CHECK，再返回捕获 ID；执行器拒绝，counter 未调用，原表声明、行值及请求保持不变。
- 同一测试的 `hidden_create` 保留原失败注入点；现在请求更早被拒绝，不能到达该 CREATE，检查回执 rolled_back/未核验/无 Actual，并确认没有留下探针。
- `TestPostgreSQL17DesignConcurrentEnumReplacedByDomainFixture`：读取真实 enum OID 后，另一连接 DROP enum 并在同名位置 CREATE 带用户 CHECK 的 domain；无伪造目录或替换请求，仍拒绝且没有序列副作用。
- `TestPostgreSQL17DesignDomainDependencyDoesNotStabilizeTypeFixture`：真实空 guard 表仍允许另一连接 ALTER DOMAIN。PG 17 样例没有 domain object lock，PG 14/18 样例显示 AccessShareLock；后者也不能阻止新增 CHECK。这个负面平台证据保留，不当作新锁机制；独立 DDL 使用有界超时，不能因未来锁行为变化无限等待。
- 结构类型回归保留 core numeric/数组/时间/JSON/bytea/uuid/regclass/range/multirange 的规范化验证，明确拒绝用户结构类型及嵌套类型；取消和无关 cast 回归保留。

第一次修复验证 `postgres-P3U72r` 的 exit 1 来自测试工具错误：把故意修改 domain 后的整个命名空间 fingerprint 当作原表不变指标，并缺少子测试 schema 清理。已改为核验原表完整声明及行值、独立核验请求，并在每次用例退出时清理自己的 schema。该失败日志仍保留；随后的并发专项 `postgres-HxPBXD` 三项真实 race 通过，16.556 秒、exit 0。

跨版本专项首次运行 `postgres-Fng6Y1`（14）及 `postgres-nPaD02`（18）exit 1：类型拒绝及并发回归通过，但负面锁测试误要求完全没有 object lock。已根据真实 AccessShareLock 与独立 ALTER 成功行为修正验收指标为“不稳定定义”，保留原失败及实际锁状态。此修改不改变产品的拒绝策略，也不把 AccessShareLock 当作可用的锁方案。

使用唯一容器、随机 localhost 端口、自有空数据库和显式 NYADB_POSTGRES_IT，全部测试使用 GOWORK=off。以下命令可重放；测试名称沿用 PG17 前缀，版本由工具参数选择：

```bash
PATH=/usr/local/go/bin:$PATH GOCACHE=/data/.cache/golang \
  tools/verify_postgres_fixture.sh --race --shard 1/3

# 同样运行 --shard 2/3 和 --shard 3/3；每批独立容器和 20 分钟超时。

PATH=/usr/local/go/bin:$PATH GOCACHE=/data/.cache/golang \
  tools/verify_postgres_fixture.sh --race --postgres-version 14 \
  --run '^TestPostgreSQL17Design(Concurrent|DomainDependency|ProbeStructural|ProbeNested|ProbeTypeCallbacks|ProbeUnrelatedCast|CreateChosenName).*Fixture$'
```

Go 1.27.3、MyGo v0.7.3-nyadb.2、Linux amd64；生命周期与真实服务 race 使用 20 分钟超时。

完整 PG 17 单次运行 `postgres-OgN7kw` 目录快照成本随 fixture 对象累积增长，后续改为三个独立批次。切换时编辑了仍在运行的脚本，旧批次收到终止信号后收尾解析错误，runner exit 2，没有完整 test_exit；按中断记录，绝不当作完整通过。原 resource.txt 记录清理成功，并以成功 Docker 列表和明确 no such object 响应核对自有容器已删除。后续批次使用最终脚本，运行期间不再修改它。

`--shard i/n` 在创建服务前用 go test -list 取得指定模式的完整清单，按固定索引分配，并保存 test-list.txt/selected-tests.txt。无匹配或无效编号在创建容器前退出；手动验证五种无效编号均为 exit 2，bash -n 通过。验证工具不会新增集成测试跳过规则。最终审计须验证三个清单相同、选中项无重复、并集完整，以及每个选中项都有实际 PASS。

初始三个批次分别选择 23/23/22 项，完整并集为 68，没有重复和遗漏。第一、第三批通过；第二批 `postgres-nzOmkN` exit 1，两项失败：默认 enum 索引类用例仍期待旧 dispatch 错误（实际被类型稳定性检查更早拒绝），以及上述原生设计样例的 length 重载误拒绝。前者改为检查更严格的提前拒绝，同时保留无序列副作用和原表不变断言；显式 core 类型/用户索引类回归仍验证 dispatch 检查。

定位复测 `postgres-YRqcql`、`postgres-LLR3qe` 的 exit 1 保留。新增生成列重载回归后，当前测试清单为 69 项；按初始通过项和修复后的针对性复测核对完整 PASS 并集，不将第二批原 exit 1 改写为成功，不把补测称为重新运行全部三个批次。工具重放时会自动取得当前 69 项清单。

## 最终结果与资源审计

- PG 17.10 初始分批：`postgres-jLzcD3` 23 项通过、311.162 秒；`postgres-nzOmkN` 21 项通过/2 项失败、380.587 秒；`postgres-t2GC40` 22 项通过、520.422 秒。
- PG 17.10 修复专项：`postgres-GW64am` 六项全部通过、308.962 秒。包括新增重载回归、原两项失败和用户/core 转发体/范围回归；原生设计全流程样例本身 266.87 秒。PASS 并集覆盖当前全部 69 项，无遗漏。这里只是服务及验收 harness 的真实 PostgreSQL 运行，正式 PG Apply 仍不公开。
- PG 14.24 类型并发专项 `postgres-dMnata` 通过、62.909 秒，新增重载/拒绝专项 `postgres-fSbwnn` 通过、38.437 秒。
- PG 18.6 类型并发专项 `postgres-lFYvUY` 通过、71.806 秒，新增重载/拒绝专项 `postgres-9JZiw0` 通过、43.726 秒。不是 PG 18 完整原生设计器验收，独立 NOT NULL 约束等限制保持。
- 最终 `go test ./...`、objectworkspace/schemaddl/engines/dbruntime race、`go vet ./...`、`go tool mygo vet`、翻译生成/检查（1466 条）及 MyGo 打包 exit 0。命令统一 GOWORK=off、GOCACHE=/data/.cache/golang；日志为 `build/verify/postgres-type-stability-*-final.log`。最终部分包使用有效缓存；之前 objectworkspace race 已实际执行 28.670 秒，最终 engines race 7.615 秒。没有完整 Shell race或新的原生窗口/Python 工具修改。

最终二进制 `build/linux-amd64/nyadb` SHA-256：`449bbfc5bab0f4a7de7525da75dbf7d8fd269eb54a6cf92de57f06f208a2e2f7`。打包通过不替代实际窗口结果；表单组件图片继续以[表单准备记录](postgres-designer-forms-2026-11-02.md) 中的限定结果为准。

最终以成功的 Docker 全 ID 列表及每个容器明确 no such object 的 inspect 响应，确认上述成功、失败、中断及原故障复现共 17 次运行的自有容器全部不存在。版本、原退出码、资源身份、日志/源文件哈希、初始清单和最终 PASS 并集审计保留于 `build/verify/postgres-type-stability-evidence.json`；没有操作其它已有资源。按逐项要求本地提交本项修复和分批验证工具，完整[剩余目标](reconstruction-remaining-2026-11-02.md) 保持进行中。

新增证据属于 NyaDB 实现及真实自有 PostgreSQL 服务运行。没有新增原生 PG 保存窗口、Windows/macOS 或 Navicat O/S/V，没有连接生产、运行 Navicat、push 或改写历史。完整目标及逐项提交要求保持有效。
