# PostgreSQL 图形设计正式保存

基线 `d44b392`。按执行器、正式绑定、窗口与交付验收三个阶段本地提交，不推送，不改写历史；不新增持久化迁移。证据均为 NyaDB 实现与运行验证 I，不构成 Navicat 行为、视觉或性能差分。

## 第一阶段：执行前检查与版本兼容

正式 Apply 在此阶段仍关闭。检查闭包包含受支持核心函数全部重载的参数及返回类型、类型回调、转换身份、运算符返回类型、访问方法和默认 opclass。子查询等未支持构造在独立 CREATE 或数据投影前拒绝；回滚后比较不能授予执行许可。PG 15 核心 SQL 包装节点仅允许惰性的 `mergeUseOuterJoin=false`。

原表在原物理连接、原事务内先取得 ACCESS EXCLUSIVE 锁，再读取完整修订和重新规划。旧、新声明的外键引用按 schema/name 排序取得 SHARE 锁；新增引用在独立 FK 探针前捕获目录，避免探针内部触发器成为原始基线。保留完整声明图、外部依赖、存储、行关联、精确值与序列核验。内部 FK 触发器引起的引用表 `relhastriggers` 变化只允许由已绑定核心触发器证明的 false→true。

PG 18 的 `contype=n` 绑定稳定字段身份，不重复生成字段 NOT NULL 和约束条目。已有名称、注释和目录属性独立核验；未验证/继承属性不可编辑，未修改的 NOT VALID 属性从原始声明进入预期证据并原样保留。PG 14 publication、PG 17 生成表达式替换及 statistics 默认目录值按版本分支执行。

验证记录：`build/verify/design-stage1-unit-final.log`、`design-stage1-race-final.log`；PG 14–18 定向服务清单均为 `TestPostgreSQL17Design{DeclarationCoreRoutines,ProbeReturnTypeClosure,ReferenceLock,ColumnNotNull}Fixture`（包含子场景），记录于 `design14-stage1-final.log` 至 `design18-stage1-final.log`。PG 18 另有未修改 NOT VALID 保留场景 `design18-notnull-retained.log`。失败探索记录保留，不能计为通过。最终矩阵与资源清理审计在第三阶段补全。

## 第二阶段：正式绑定与工作台

PG `SchemaDesign()` 公开非阻塞 `CanApply`、Snapshot 和 Apply，通过现有事务执行器保存；服务器版本、权限和声明由 worker 核验。公开代次转换仍在 dbruntime。核心类型范围同样约束改名和元数据路径，PG Drop/Copy 与程序/数据库/账户管理保持各自绑定。

正式工作台贯通新建、设计、单独改名、六页表单及 SQL 预览确认。回执同时核对原 profile/catalog/schema/generation、预览后的目标名称和表属性；不完整或不一致回执保留草稿、禁止重放并撤销原句柄。晚到的回执不能关闭替代连接。成功后撤销旧 inspector 读取并刷新已核验对象；停放回执按原连接消费。

公开服务测试通过真实 provider 完成 CREATE/ALTER/改名并独立读取精确 numeric、bytea、NULL、数组与 identity；工作台 Tester 从新建入口、Ctrl+S、确认保存走到真实 runtime 回读。`design-shell-receipt-final.log` 覆盖公开工作台与替代连接；PG 17/18 的设计和只读程序工作台 race 见 `design17-shell-race-settled.log`、`design18-shell-race-settled.log`，替代连接 race 另有记录。

## 第三阶段：版本矩阵与交付检查

`tools/verify_postgres_fixture.sh` 用独立、带所有权标签的 PG 14、15、16、17、18 disposable containers 执行设计用例。每版本按完整清单拆为六批，共 78 个顶层服务测试，其中 70 个设计测试；另八个覆盖数据/只读程序/字典回归。PG 17、18 另将全部 70 个设计测试各跑一次 race。通用测试无跳过，版本专属子场景在 fixture 内显式分支；最终 `build/verify/design-release-audit.json` 已将实际顶层结果与每批 selected-tests、全量 test-list 逐项比对，证明无遗漏。五版本共 390 项、两版本设计 race 共 140 项全部通过，42 个最终批次的自有容器独立确认已经删除。

目录版本差异包含 PG 14 的 ARRAY/CASE/WHEN 表达式节点、PG 14/15 与 PG 16+ 关联数组类型改名命名算法、PG 14 publication 限制、PG 17 生成列 DDL 与 PG 18 NOT NULL。完整服务用例覆盖 CREATE/ALTER、精确 numeric/bytea/NULL/数组、identity、生成列、约束/索引、表/字段改名、依赖改变、同名对象、权限、原物理连接、并发 DDL、锁等待取消、回滚与丢失提交回执；拒绝路径独立核对数据及序列状态。未知节点/用户回调在隐藏 CREATE/数据投影之前拒绝。

| 检查 | 记录 |
|---|---|
| 全量 Go | `build/verify/design-all-go.log`；正式绑定最后定向验证 `design-stage2-final-unit.log` |
| 相关生命周期 race | `design-release-lifecycle-race.log` 的 contract、schemaddl、connections、localdb、dbruntime、engines、mysql、connectioncatalog、connectioneditor、navigation、sessionbrowser、sqlworkspace、dataworkspace、objectworkspace、theme 全部通过；Shell 整包触发默认十分钟超时，按清单分八批重跑全部 exit 0，159 项无遗漏（145 PASS、14 按显式集成或截图开关 SKIP，PG 专项已在真实 fixture 单独通过），见 `design-shell-race-shards/audit.json` |
| 真实工作台 race | PG 17/18：`design17-shell-race-settled.log`、`design18-shell-race-settled.log`；替代连接：`design17-replacement-race-final.log`、`design18-replacement-race.log` |
| 中英文 Tester | `design-bilingual-tester.log`；限制原因、非空只读开关及保存状态使用生成后的翻译目录 |
| Go / MyGo vet | `design-go-vet-final.log`、`design-mygo-vet-final.log` |
| 翻译生成/检查 | `design-generate-final.log`、`design-i18n-final.log`；未手工改 catalog_gen.go |
| Python 工具 | `design-python-release.log`，64 项通过 |
| 正式 Linux 打包 | `design-package-release.log`；deb/tar.gz 和正式二进制位于 `build/linux-amd64/` |
| Linux/Windows/macOS 无 CGO 编译 | `design-cross-build.json`，amd64；没有据此声称 Windows/macOS 原生窗口验证 |

第一阶段为 `c611be9`，第二阶段为 `2fc63d0`。失败探索、包装脚本中断与整包超时日志均保留，不计为通过；以最终清单核验及明确通过的结果归档。资源审计只检查本任务创建的资源，不删除其它已有容器或窗口。

## Linux 正式窗口

使用 `go tool mygo build` 生成的 `build/linux-amd64/nyadb`，SHA-256 `d3d69690d800f15bd9c92c249e5fc499d12444f52d2337c0a94a0e3f891ad160`。三组正式采集均保存 `app-identity.json`、容器身份、`source-check.json`、截图操作/窗口几何、独立目录与数据 `result.json`、逐图 `review.json` 和清理记录。源码摘要包含已跟踪及未跟踪文件；最终采集期间未修改源码，随后仅完成 Git 归档和交付文档。

- 新建：`build/entry-acceptance/r-dd9db41d914a/evidence/postgres-designer-native/capture-c23b05f53d93`。
- 修改：`build/entry-acceptance/r-8c430eddc216/evidence/postgres-designer-native/capture-770d1829f446`。
- 失败保留草稿：`build/entry-acceptance/r-94bece0415bf/evidence/postgres-designer-native/capture-38ff210c6f48`。

共 43 张 client 图逐图复核。1280×800 和 840×560 覆盖新建名称确认、未保存字段、SQL 预览及显式确认、成功回读、并发冲突错误与草稿保留。窄窗口字段表单和列表分别滚动；完整名称可在主区域读取，右侧窄信息栏会折行。初始成功图的新增字段在列表下方，补采普通滚动后的回读，两个尺寸直接显示 `native_added text`。最终画面没有加载/等待状态，保存后脏标记清除，失败后仍保留脏标记及 `native_failed` 表单。

独立 SQL 在确认前证明字段尚不存在；修改后证明新字段为 NULL，精确 numeric、bytea、NULL、数组与原 label 完全相同。失败场景在预览后由独立连接增加 `concurrent_marker`，保存因修订冲突拒绝；原业务数据不变，`native_failed` 没有落库。汇总见 `build/verify/design-native-release/captures.json` 和 `review.json`。

代表图：

- [窄窗口新建预览](../build/entry-acceptance/r-dd9db41d914a/evidence/postgres-designer-native/capture-c23b05f53d93/designer-preview-840-client.png)
- [窄窗口修改成功回读](../build/entry-acceptance/r-8c430eddc216/evidence/postgres-designer-native/capture-770d1829f446/designer-applied-new-field-840-client.png)
- [失败保留草稿](../build/entry-acceptance/r-94bece0415bf/evidence/postgres-designer-native/capture-38ff210c6f48/designer-failed-draft-retained-840-client.png)

`design-native-release/resource-audit.json` 独立复查本阶段 17 次窗口运行，只有上述三组作为正式验收，其余探索和未接受采集保留。检查原 PID/出生时间已经终止、各自容器不存在、私有 display/dbus/keyring 停止及临时 secrets 文件删除。服务 fixtures 的原始资源/清理记录与版本清单审计分别保存。

## 支持边界与剩余目标

支持标准核心 PostgreSQL 14–18 普通表。用户类型、分区/继承编辑、物理字段重排、身份序列完整选项修改及未支持表达式明确拒绝；PG 18 的 NOT VALID、继承及独立约束改名保持只读。完整图形设计与其它工作台目标见 [剩余清单](reconstruction-remaining-2026-11-02.md)。管理员并发替换核心系统对象不纳入保证范围。

不新增持久化迁移，不推送或改写历史。测试数据库、截图和构建均留在忽略目录。Windows/macOS 实机、IME/缩放/多显示器、Navicat 行为及同条件性能差分继续待验证；本记录仅为 NyaDB 实现及运行验证 I。
