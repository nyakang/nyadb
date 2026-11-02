# PostgreSQL 删除字段的原属性槽核验 — 2026-11-02

继续 [未编辑目录核验](postgres-unselected-context-2026-11-02.md)，按明确字段删除请求推导原生残留，并在原生验收提交前比较。实现 I；正式 PG `SchemaDesign` 仍只开放 Snapshot，Apply/CanApply 和正式设计表单未接通。

## 实施

`VerifyPostgreSQLUnselectedCatalog` 在已有规划、独立目标声明图和保留资产检查之后，按**原稳定字段 ID**找出确实被删除的原 live 属性。原属性位置来自目录绑定，不从名称、ID 字符串或实际改表结果猜测。相同名称的新 ID 不能复用原物理槽；保留 ID 的改名不会被当成删除。

新增私有 `pgExpectedDroppedAttributes` / `pgNativeDroppedAttribute`。预期只消费原记录和版本，不消费实际结果或新 CREATE 默认值。它给未编辑上下文的原预期集合增加独立推导的 dropped 记录，然后仍执行完整比较；没有把字段放入“忽略变化”清单。

共同规则包括保留原关系/attnum 身份，置 dropped、清零类型 OID、采用原物理位置的 dropped 名称，清除 NOT NULL、默认/生成及其 SQL 展示、原字段注释和被删除身份序列的展示。原长度、对齐、by-value、typmod、维数、collation OID、storage/compression、identity 模式、继承标记及未知字段仍按原完整记录保留。已有 dropped 槽不属于本次删除许可，必须保持不变。

| 版本 | 统计/ACL/选项 | missing 状态 | cache offset |
|---|---|---|---|
| PG 14–16 | 统计置 0，原 nullable ACL/options/fdwoptions 保留。 | 原 hasmissing 为 true 时清为 false/NULL，其余原状态保留。 | 要求原记录包含 attcacheoff，完整保留。 |
| PG 17 | 统计及 nullable ACL/options/fdwoptions 清 NULL。 | false/NULL。 | 要求原记录包含 attcacheoff，完整保留。 |
| PG 18 | 同 PG 17。 | false/NULL。 | 公开目录已移除此字段，允许缺失且不补造；若捕获到字段则仍完整保留。 |

缺失/非法原证据、错误属性归属、错误原生长度/对齐和标记明确拒绝，失败不返回部分残留。零 OID 保留原 native JSON 的字符串/数字表示；未知精确值不经过 float64。用户没有确认数据损失时，原规划器先拒绝删除，不能用残留核验授权绕过确认。

原生 fixture 验收器的所有含字段删除的计划，现在都会在提交前执行新原上下文检查；新删除 fixture 也显式调用检查。一般类型转换、对象重建及其它上下文副作用仍须完整证明，正式生产 Apply 不因此开放。

## 参考依据

参考公开、许可可用的 PostgreSQL `RemoveAttributeById` 与 `format_type_extended`，并结合本项目目录读取 SQL 独立编写 Go 预期转换。原始参考保存在忽略目录 `temp/research/postgres-dropped-attribute`，与正式实现分离；`provenance.json` 记录 PG 14–18 的 URL/内容 SHA-256，附原 `COPYRIGHT`。PG 18 `pg_attribute.h` 的布局变化也记录来源与哈希。

- [PG 14 heap.c](https://github.com/postgres/postgres/blob/REL_14_STABLE/src/backend/catalog/heap.c)
- [PG 17 heap.c](https://github.com/postgres/postgres/blob/REL_17_STABLE/src/backend/catalog/heap.c)
- [PG 18 pg_attribute.h](https://github.com/postgres/postgres/blob/REL_18_STABLE/src/include/catalog/pg_attribute.h)

这些是 PostgreSQL 公开行为参考，不计为 Navicat 原版静态/运行/差分证据，没有复制 Navicat 私有实现。

## 回归与真实服务

纯回归覆盖五个版本的清理区别、未知精确属性、原物理属性不变、无部分结果、非法原长度/对齐/标记、版本字段缺失、稳定 ID 与改名/同名新 ID，以及没有删除许可时的严格比较。真实 fixture 在确认后的原生 SQL 提交前核验，也分别核对保留 UUID/BLOB、精确 bigint、全部 attnum 槽及已删除身份序列。

新普通删除 fixture 含原 text 默认/注释/ACL/options、快速添加列的精确 missing 值及此前已删除的槽，删除后仍须有 5 个属性位置、3 个 dropped 槽及两列有效声明。新身份/生成 fixture 核对自动删除拥有的序列、生成属性槽以及保留 bigint 数据。

六个负回归修改**真实快照的证据副本**，验证错误长度/对齐/attnum、missing 值未清除、旧 dropped 槽变化和未知状态不会取得许可。这六项不是实际执行了错误 catalog UPDATE，也不声称进行了低级物理篡改。

工具新增受固定版本白名单约束的 `--postgres-version 14|15|16|17|18`，默认仍为 17；仍创建唯一命名、随机 localhost 端口及原所有权 label 的空容器，按捕获身份清理。测试名称沿用 `PostgreSQL17`，实际服务版本须以每次 `versions.txt` 为准，名称不能替代版本证据。

中间证据：

| 证据目录 | 实际结果与范围 |
|---|---|
| `build/verify/postgres-tnOXlz` | 首轮 PG 17.10 两个删除主场景及六个证据子项通过，18.781 秒。 |
| `build/verify/postgres-tUsJGT` | 首轮 PG 14.24 同范围通过，16.618 秒。 |
| `build/verify/postgres-SK57Fg` | PG 18.6 首次失败，9.524 秒：旧版必填 attcacheoff 要求不适用于 PG 18，不计通过。 |
| `build/verify/postgres-HTYawv` | 修复 PG 18 字段布局后，普通删除主场景及六个子项通过，10.733 秒。 |
| `build/verify/postgres-YpezuX` | 最终代码 PG 14.24 两个删除主场景及六个子项通过，16.038 秒。 |
| `build/verify/postgres-HxiWSI` | 最终代码 PG 18.6 普通删除主场景及六个子项通过，10.078 秒。 |

PG 18 本次仅运行上述普通删除 fixture，没有运行身份/生成 fixture、完整 PG 服务包或正式表单，不能由此宣称 PG 18 全部设计行为通过。PG 15/16 的本步规则有公开参考与纯回归，没有新增真实服务结果。

## 检查与交付

Linux amd64，Go 1.27.3 / MyGo v0.7.3-nyadb.2，HEAD `5960d26`，`GOWORK=off GOCACHE=/data/.cache/golang`。保留全部原有未提交成果，无 commit/push。

| 最终检查 | 实际结果 |
|---|---|
| `go test ./... -timeout=20m` | 通过；schemaddl 0.271 秒、engines 0.376 秒、dbruntime 0.016 秒、objectworkspace 3.536 秒、Shell 76.788 秒；其它未变化包按正常缓存复用。默认跳过的集成不计真实服务通过。 |
| contract/schemaddl/engines/dbruntime/objectworkspace race，20 分钟超时 | 通过；schemaddl 3.576 秒、engines 3.541 秒、dbruntime 1.069 秒、objectworkspace 17.634 秒。 |
| `go vet ./...` / `go tool mygo vet` | 通过。 |
| 翻译生成 / i18n check | 通过，生成目录已是最新，1338 条 zh-Hans/en 消息。 |
| `bash -n tools/verify_postgres_fixture.sh` | 通过。没有本步 Python 源码修改。 |
| `go tool mygo build` | 通过，26.5 秒；Linux amd64 程序及 deb/tar 已生成。 |

完整 PG 17.10 `--race` 通过：23 个主场景、35 个子测试，engines 包 567.804 秒。其中普通字段删除 26.08 秒、身份/生成字段删除 9.65 秒、原生规划 142.72 秒、保留目录 58.23 秒。最终证据为 `build/verify/postgres-EZuspE`，包含 `tests.log`、版本/资源日志、`validation.json` 和 `cleanup-audit.json`。

本阶段 Linux amd64 二进制 `build/linux-amd64/nyadb` 的 SHA-256 为 `c87f1848956d4f45d91e1a04b210a8dcd577cd425cda063fb3b9c50715b9a107`。最终容器 `68f766a2788f99353c3bbc19e70dcb8873e5ca765b08cc0579f2f5e80a1acdbe` 按本轮捕获身份清理；测试退出码为 0，工具记录删除成功，另以 `docker inspect --type=container` 独立确认不存在。上述六个中间目录也各含独立清理审计，确认对应容器均已删除；PG 18 首次失败仍作为失败保留，不计通过。

重放：

```bash
GOWORK=off GOCACHE=/data/.cache/golang PATH=/usr/local/go/bin:$PATH \
  tools/verify_postgres_fixture.sh --race
GOWORK=off GOCACHE=/data/.cache/golang PATH=/usr/local/go/bin:$PATH \
  tools/verify_postgres_fixture.sh --postgres-version 14 --run 'TestPostgreSQL17DesignDropped.*Fixture'
GOWORK=off GOCACHE=/data/.cache/golang PATH=/usr/local/go/bin:$PATH \
  tools/verify_postgres_fixture.sh --postgres-version 18 --run 'TestPostgreSQL17DesignDroppedAttributeFixture'
```

## 剩余范围

这一步验证普通原生字段删除的原属性槽及已捕获未编辑上下文。不是一般堆/TOAST 重写、表迁移、合法新上下文、FK/索引/触发器重建或入站定义变更的完整许可。新字段数据语义、保留序列当前值、依赖/并发锁、独立 CREATE 探针的用户函数/非事务性副作用与权限、完整分区/继承以及正式提交/PG 表单仍未完成。

本步没有新原生窗口/Python 改动或截图，没有新增 Windows/macOS、大端或 Navicat O/S/V；编译与 Tester 回归不计真实窗口验收。未启动原版或连接生产。

PG/SQLite 对象 CRUD/导入/备份/隧道、跨库传输/数据与结构同步、调度、ClickHouse、可配置 OpenAI 兼容 AI SQL、模型/逆向、BI、监控、字典、生成、脱敏、CLI 和精细交互/原生/原版验收继续属于 [完整目标](workbench-completion-2026-11-02.md)，整体未完成。
