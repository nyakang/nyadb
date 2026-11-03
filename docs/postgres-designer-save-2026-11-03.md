# PostgreSQL 图形设计正式保存

基线 `d44b392`。按执行器、正式绑定、窗口与交付验收三个阶段本地提交，不推送，不改写历史；不新增持久化迁移。证据均为 NyaDB 实现与运行验证 I，不构成 Navicat 行为、视觉或性能差分。

## 第一阶段：执行前检查与版本兼容

正式 Apply 在此阶段仍关闭。检查闭包包含受支持核心函数全部重载的参数及返回类型、类型回调、转换身份、运算符返回类型、访问方法和默认 opclass。子查询等未支持构造在独立 CREATE 或数据投影前拒绝；回滚后比较不能授予执行许可。PG 15 核心 SQL 包装节点仅允许惰性的 `mergeUseOuterJoin=false`。

原表在原物理连接、原事务内先取得 ACCESS EXCLUSIVE 锁，再读取完整修订和重新规划。旧、新声明的外键引用按 schema/name 排序取得 SHARE 锁；新增引用在独立 FK 探针前捕获目录，避免探针内部触发器成为原始基线。保留完整声明图、外部依赖、存储、行关联、精确值与序列核验。内部 FK 触发器引起的引用表 `relhastriggers` 变化只允许由已绑定核心触发器证明的 false→true。

PG 18 的 `contype=n` 绑定稳定字段身份，不重复生成字段 NOT NULL 和约束条目。已有名称、注释和目录属性独立核验；未验证/继承属性不可编辑，未修改的 NOT VALID 属性从原始声明进入预期证据并原样保留。PG 14 publication、PG 17 生成表达式替换及 statistics 默认目录值按版本分支执行。

验证记录：`build/verify/design-stage1-unit-final.log`、`design-stage1-race-final.log`；PG 14–18 定向服务清单均为 `TestPostgreSQL17Design{DeclarationCoreRoutines,ProbeReturnTypeClosure,ReferenceLock,ColumnNotNull}Fixture`（包含子场景），记录于 `design14-stage1-final.log` 至 `design18-stage1-final.log`。PG 18 另有未修改 NOT VALID 保留场景 `design18-notnull-retained.log`。失败探索记录保留，不能计为通过。最终矩阵与资源清理审计在第三阶段补全。

## 后续验收

正式 provider/runtime/工作台绑定、版本全量服务矩阵、生命周期与窗口采集、全量交付检查待后续阶段完成。支持普通核心 PostgreSQL 14–18 表；用户类型、分区/继承编辑、物理字段重排、身份序列完整选项修改和未支持表达式保持拒绝。Windows/macOS 实机和 Navicat 同条件差分仍待验证。
