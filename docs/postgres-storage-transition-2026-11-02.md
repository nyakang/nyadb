# PostgreSQL 原生结构编辑的物理存储及新引用证据 — 2026-11-02

继续完整工作台目标，补充 PG 正式 Apply 所需的验证材料。实现 **I**；公开 `Session.SchemaDesign()` 仍只提供 PG Snapshot，正式 Apply、引擎专属表单和完整执行策略尚未交付。本步不启动 Navicat，不新增原版 O/S/V，也不将 SQL 验收器当作正式功能。

## 实现与边界

`schemaddl.PostgreSQLStorageTransition` 保存私有、不可变且与原快照/草稿/结果签名绑定的证据。它只供未编辑目录比较使用；其规范化目录不写回数据库、不交给工作台作为新基线，不能独立授权 COMMIT。

- 复核原 heap relation OID、文件地址、物理属性槽和保守 index/rule/trigger 标记。仅声明中类型/生成、新字段及持久性/访问方法/表空间变化可能许可相应重写或移动；这是一份变化许可分类，不是服务器是否必然重写的预测。
- 全部 heap 属性必须有完整、唯一的原关系/物理位置和缓存证据。保留字段的 `atthasmissing/attmissingval` 精确不变；只有已许可且实际发生的 heap 重写可以清除原缓存。新字段必须追加到正确物理槽；无默认值、身份或生成字段不许可缓存。默认表达式的具体新值仍需要独立的新字段语义证明。
- 通过真实 `reltoastrelid`、`indrelid`、pg_class 类型化所有权边识别 TOAST，不按生成名称猜测 OID。要求完整三列、单个有效唯一 chunk 索引、原 heap 所有者及内部所有权依赖。chunk 索引拥有两条指向 chunk 属性 1/2 的 automatic 边，TOAST 表拥有一条指向 heap 的 internal 边。
- 保留/重建 TOAST 时，比较全部记录和依赖边的多重性，保留未知 JSON 属性及超过 2^53 的整数；只调整已复核的名称、文件、所有者/表空间/持久性及索引 horizon 等限定变化。索引定义仅重绑 CREATE 标头，主体仍严格比较。相同 OID 的任意改名没有许可。
- 首次生成 TOAST 必须与独立 desired CREATE 探针的完整存储比较。类型重写导致 TOAST 不再必要时，独立 desired 探针必须确认没有 TOAST，原文件必须实际重写，原生成资产及类型化依赖必须全部消失。额外引用、标签、未知依赖容器或边属性拒绝该移除；这是限定的整个生成资产消失许可，不能许可保留资产的未知属性修改。
- OID 别名仅作用于 pg_class 关系/索引/字段/安全标签及相应类型化依赖端点。其它 catalog 的相同数值 OID 保持不变。严格比较目录时保留原数字/字符串形式，已知数值字段的单独比较不使用浮点。
- 依赖边重组保留未受影响容器的未知属性。需要移动/过滤边而容器有未知属性时拒绝猜测重绑；旧属性的消失或变化仍由原上下文 guard 拒绝。

`PostgreSQLReferenceBaseline` 在 desired 探针回滚之后、DDL 之前保存补读的新外部类型/cast/routine 等引用。原记录必须全部精确保留；新增记录必须已经存在于独立探针闭包并精确一致，拒绝 probe 的 table/type/default/index/trigger/TOAST 和其它临时依赖。新记录只成为不可编辑上下文，不产生新的编辑权限。请求、引擎、命名空间、修订或规范表与目录不一致时拒绝，证据与原始缓冲区分离。

私有原生 SQL 验收器现在对每个 existing-table ALTER 同时执行独立声明/原属性/物理存储/未编辑上下文/保留值/原序列核验；原来根据 helper 选择是否核验上下文的绕开路径已移除。返回结果仍是普通当前目录闭包。

## 回归与真实数据库

纯回归覆盖：重写、精确缓存值保留/清除、所有物理槽、新字段非法缓存、OID catalog 碰撞、未知属性修改、TOAST 所有者/索引/定义/边、同 OID 改名、依赖容器保留及禁止猜测重绑；独立完整消失、缺失探针、探针仍需存储、残留资产/引用、未知边/容器和标签。引用基线覆盖缓冲隔离、请求/范围/引擎/修订/规范快照、原记录丢失、新引用修改、重复边及临时 probe 对象/端点。

新增四个真实服务 fixture：

1. 普通默认修改及新增无默认字段不重写 heap；已有精确 numeric 快速默认缓存不变；随后明确删除该字段验证 native dropped-slot 语义。
2. 原 integer-only heap 新增 text 和精确 numeric 快速默认，创建 TOAST 而不重写 heap。数据库复核 NULL 和精确值。
3. integer PK 扩大到 bigint，允许原 heap/TOAST/chunk index/普通 PK index 的合法物理重建，保持关系 OID、保留行及精确缓存值。
4. LOGGED → UNLOGGED → LOGGED，复核原 heap 和其 TOAST 表/索引持久性一致，并保留行数据。

已有 retained-catalog/native-properties fixture 另覆盖 text → varchar(100) 后完整移除 TOAST、原 ACL/注释/字段 options/存储/压缩/统计值及 identity 序列不变。

## 验证记录

Linux amd64，Go 1.27.3、MyGo v0.7.3-nyadb.2，基线 `dc4d04d`；`GOWORK=off GOCACHE=/data/.cache/golang`。数据库均为工具创建的独立带 label 容器、随机 localhost 端口，不复用现有服务。

已完成的真实运行：

| 证据目录（忽略目录 build/verify 下） | 范围 | 结果 |
|---|---|---|
| `postgres-GufuH6` | 最初 PG 17 重写及 header/name 回归 | exit 0，21.053 秒；编译于最终补强前，不作为最终结果 |
| `postgres-PN5lfC` | PG 17 无重写、新建及重建 TOAST | exit 0，41.978 秒；编译于移除修复前 |
| `postgres-IHDJIQ` | PG 17 前三项及原目录属性 | exit 0，93.561 秒；编译于最终 chunk 所有权补强前 |
| `postgres-1z1qWO` | PG 17 持久性切换 | exit 0，19.872 秒；编译于最终 chunk 所有权补强前 |
| `postgres-surUil` | PG 14 四项、dropped 属性、原目录属性 | exit 0，130.168 秒；测试名仍为 PostgreSQL17，实际服务版本由 versions.txt 记录 |
| `postgres-gHEMet` | PG 18 相同扩大范围 | exit 1，77.215 秒；无重写/新建/持久性/删除字段通过，带 PK 重写及原目录场景被既有模型拒绝，不能计整次通过 |
| `postgres-gboKEl` | PG 18.6 已支持的删除字段/无重写/新建 TOAST/持久性 | exit 0，72.822 秒，4 个主测试、6 个子测试；不包括上述拒绝场景 |
| `postgres-1bEvz8` | 最终 PG 17.10 完整真实服务 race | exit 0，906.851 秒，32 个主测试、37 个子测试；原生连续计划 266.80 秒、native-properties 35.35 秒，两个原失败路径均通过 |

最初完整 PG 17 race `postgres-u888US` exit 1，764.187 秒：原目录属性及 native-properties 的 text → varchar 移除 TOAST 被过严规则拒绝，已增加独立完整移除证明；其余测试不替代最终代码复验。更早探索的 `postgres-xrruqr/iU5xmY/B0O7lT/gwOneP/gmQ9Lb` 保留 OID 衍生字段/所有权边/新 cast 闭包及测试编译失败日志，不算通过。`postgres-6m8k5j` 是直接 DDL 的观察成功，不是完整证明验收。

最终完整 PG 17 服务 race 与独立资源审计均已完成。完整服务运行编译后只有签名专项测试和说明注释新增，没有生产逻辑变更；新增签名测试另外验证。已完成的最终检查如下：

- `go test ./... -timeout=20m` 通过：Shell 101.901 秒、dataworkspace 49.788 秒，其它正常/缓存；日志 `postgres-storage-final-all-tests.log`。最后新增引用签名绑定回归另经下面的 race 和 schemaddl/engines 普通回归（0.934/0.965 秒，`postgres-storage-final-comment-unit.log`）验证；之后只有说明文档更新。
- contract/schemaddl/engines/dbruntime/objectworkspace race 通过，schemaddl 6.522 秒、engines 6.690 秒，其它正常/缓存；最后补齐的全部 guard race 9.382 秒，引用/存储签名专项 race 5.590 秒；分别为 `postgres-storage-final-feature-race.log`、`postgres-storage-final-guards-race.log`、`postgres-storage-final-signatures-race.log`。
- `go vet ./...`、`go tool mygo vet`、`go tool mygo generate`、`go tool mygo i18n check` 通过；1456 条 zh-Hans/en 消息，没有手改生成目录。`go tool mygo build` 通过，最终打包 32.9 秒，生成 Linux 正式二进制及 deb/tar 包。日志为 `postgres-storage-final-{vet,mygo-vet,generate,i18n,packaged-build}.log`；首次 `build.log` 编译于最后的签名回归/说明注释之前，保留但不作为最终二进制身份。
- 最终正式二进制 SHA-256：`d0db9e84e207cee75ba44a86355130dc47c81a6fdea2e9e163d852dcb132e7a8`。本步未启动该窗口；该哈希不能替代之前版本的窗口证据。
- 独立 Docker 审计确认全部 15 次运行的原容器 ID 均已移除，Docker 服务可用，各次 inspect 均明确报告原容器不存在。各目录保存独立 `cleanup-audit.json`，汇总在 `build/verify/postgres-storage-cleanup-audit.json`。最后追加的活动状态采样因容器已自动移除没有执行，不作为运行证据；清理结论来自随后独立 inspect。

重放（工具创建和清理自己的独立 fixture）：

```bash
PATH=/usr/local/go/bin:$PATH GOWORK=off GOCACHE=/data/.cache/golang \
  tools/verify_postgres_fixture.sh --race
```

PG 14 定向追加 `--postgres-version 14 --run 'TestPostgreSQL17Design(Physical.*|RetainedCatalog|DroppedAttribute)Fixture'`；PG 18 已支持范围追加 `--postgres-version 18 --run 'TestPostgreSQL17Design(Physical(NoRewrite|FreshToast|Persistence)|DroppedAttribute)Fixture'`。扩大 PG 18 范围仍会出现上述既有 NOT NULL 模型拒绝，不将它隐藏为跳过或计为通过。

## 尚需完成

声明变化许可与上述验证共同覆盖代表性原生物理变化，仍不构成生产 Apply 的全部条件。并发目录/序列及未捕获依赖、函数/自定义类型/发送函数或探针的非事务性副作用、新字段值/生成表达式及转换语义、完整取消/不确定提交、正式事务执行绑定和 PG 原生表单继续待实现。PG 18 独立 NOT NULL 约束的建模、规划、探针及图绑定也未适配；本轮保留其真实拒绝记录。

本步未改 UI/Python，没有新增原生窗口截图；编译/服务结果不计实际窗口验收。旧窗口截图按各自报告中的二进制哈希解读。PG/SQLite 文件工具、传输/同步/任务、模型/BI/监控/生成/脱敏/CLI、ClickHouse/AI 深化、多平台及原版观察/差分继续属于 [完整目标](reconstruction-remaining-2026-11-02.md)，不以完成本证明组件替代最终范围。
