# 第三阶段：MySQL 对象管理与连接选项

证据标记：本阶段新增行为属于独立实现 I。运行测试使用 NyaDB 和一次性 MySQL fixture；未运行 Navicat，原版行为观察 O、差分 V 和真实桌面验收仍为 Unknown。

## 对象定义与安全执行

`mysql_object_management.go` 从公开 SHOW CREATE 获取完整数据库、视图、函数、存储过程和事件定义，同时保留程序 SQL mode。对象计划绑定数据库、类型和名称；非创建操作要求完整原始定义和结构指纹。执行使用原 runtime generation 与一个 pinned `sql.Conn`，不进入 database/sql 的坏连接重试路径。预检发现已发生的外部 DDL，不能消除预检与执行之间的外部竞态。

视图修改使用 CREATE OR REPLACE；函数和存储过程修改明确分为 DROP 与完整 CREATE，按顺序执行，第二步失败时报告 partial。数据库与事件修改使用完整 ALTER 输入。所有成功操作重新读取定义；失败、断线或取消保留草稿及 not_executed / executed / partial / uncertain 回执，不自动重放。表复制保留全部公开字段、生成列、约束、选项及触发器；重命名 FK/CHECK/触发器以避免同库冲突，自引用外键指向目标。数据先复制、触发器后创建。无法确认触发器可见性或不支持的引号模式时阻止复制。触发器按服务器 ACTION_ORDER 读取和创建，结构指纹保留执行顺序；设计器当前无法保证的触发器重排在 Build 阶段阻止，不执行后才反馈不支持。

`mysql_object_verify.go` 对执行后完整公开定义做保守比较，而非仅核对对象存在。比较保留正文、注释、字符串字面值与标识符大小写，只接受已识别的关键字大小写、空白与反引号变化。程序 SQL mode 用独立 `SQLModeKnown` 标记区分「未知」和明确空值；复制/重建明确空值时在原 pinned 连接上设置空 mode，不套用新连接默认值。视图核对查询、别名、显式列名、ALGORITHM 与 SQL SECURITY；函数/过程核对全部参数、返回类型、特征及正文；事件核对 schedule、边界、状态、完成策略、注释及正文，局部 ALTER 保留原定义中未修改选项。DEFINER 必须匹配显式指定账户或原执行连接读取的 CURRENT_USER；不忽略服务器插入的定义者。数据库字符集、排序规则和加密选项按计划/原定义及服务器公开默认元数据独立解析，不能用重读的实际定义自证相等。事件缺省 STARTS 只在事件和执行连接时区一致，且符合公开 CREATED/LAST_ALTERED 时间证据时接受。

DDL 已确认执行但定义不符时，回执保持 executed，`Verified=false`，保留实际完整定义、错误和草稿；绝不自动重放。无法解释的服务器改写（如视图星号展开、额外 CAST/字符集修饰）、未知 executable comment、ANSI_QUOTES/NO_BACKSLASH_ESCAPES 或无法证明的事件时区转换保持未核验。结构预检和重读都不能消除外部 DDL 竞态。恢复模块的严格完整 DDL 比较保持独立，不使用此对象编辑规范化规则。

`objectworkspace` 的完整定义模态界面提供名称输入、SQL 预览和确认执行；改名只改声明头，保留已编辑正文。可以保留草稿、继续草稿或显式放弃。UI session generation 与数据库 runtime generation 分别保存；完成事件携带原 UI generation，Shell 依原身份刷新缓存和页签。多选、批量复制/删除顺序执行，逐项展示状态，遇错停止；执行前逐一协调所有关联数据页。

## 菜单、分组与账户

对象列表 F2 使用原生 EditableText 行内编辑；Escape 取消，Enter 保留名字并提交原目标。复制自动生成不冲突且不超出 64 字符的名字。剪贴板采用 `nyadb-objects` / version 1，自带原 profile 与限定对象身份；其他格式和其他连接来源不会隐式执行。分组支持赋组、筛选、改名和删除，通过独立 `objects/groups/v1/{profile}/{database}` 偏好记录恢复；不修改服务器对象。

维护菜单运行 ANALYZE / CHECK / OPTIMIZE / REPAIR，展示服务器返回的每项消息。用户、角色与权限界面读取账户和 SHOW GRANTS，支持创建、删除、密码、锁定、授权/撤权和角色分配。权限名采用白名单，账户 DDL 的密码不支持服务器预备参数；预览使用占位符，执行时按实际 SQL mode 编码瞬时字符串字面值，不写入预览或回执。服务器版本、引擎和权限决定实际能力。仅 MySQL 能力开放这些入口，其他引擎不显示伪造的 MySQL 编辑能力。

## 高级连接设置

压缩使用驱动的 EnableCompression 选项；字符集使用公开 Charset 选项。自定义数据库列表只过滤实际可见的 SHOW DATABASES 结果，不创建数据库或提升权限。自动连接由 Shell 启动协调器执行。不支持的兼容性覆盖控件保持禁用；旧值不隐式清除，用户可以显式清除覆盖标志。

初始 SQL 的 profile 字段只保存随机加密文档引用。`queries.Store` 使用独立工作区密钥、随机 AES-256-GCM nonce 与带文档/格式/profile/revision 的认证数据保存 SQL。编辑器通过私有缓冲通道异步加载并校验 profile、配置与请求版本。密钥丢失或密文认证失败不替换旧记录。每次修改写入新引用；后续配置保存失败保留旧密文，可能留下不可见的加密孤立记录。物理 connector 在认证后执行一次初始 SQL；SQL 租约复用时不再次执行，初始化失败关闭连接且不重放已执行语句。SQL mode 变化同步到词法扫描器。测试连接仅认证，不执行初始 SQL。

## 验证范围

- 模型/服务/UI 回归：完整定义绑定、约束/触发器复制、账户输入、安全的密码绑定、名称改变保留正文、UI/runtime generation、加密记录与身份认证、丢失密钥、旧加载回执、分组限定身份及剪贴板版本。执行后 fake driver 返回外部改动时核验失败并保持一次执行；正文/注释/字面值、mode、definer、数据库默认值、事件局部 ALTER 与未知规范化均有独立回归。
- 生命周期 race：MySQL 服务、objectworkspace、connectioneditor 和 queries 的相关测试通过。
- Shell Tester：F2 行内目标/Escape/Enter 与 Delete/Ctrl-N 作用域测试通过；不等同真实桌面验收。
- 一次性 MySQL 8.0 与 8.4：数据库/视图/函数/过程/事件创建、修改、复制、删除及外部冲突；带自引用外键、检查约束、生成列和反向命名的两个触发器的完整表复制、数据及触发器 ACTION_ORDER 核对通过。
- 扩展 fixture 核验压缩、字符集、自定义列表、物理初始 SQL 与账户/角色权限操作；同一脚本的 SQL、多结果/流式导出、设计器及备份恢复也通过。最终源码版本和安装包指纹以整仓验收报告为准。

## 备份恢复补充

`mysql_restore_nontransactional.go` 为非 InnoDB 恢复提供独立入口，复用类型化 `backupRowSource` 和受限字段赋值生成器，在原 pinned 连接上逐行写入并立即检查服务器警告。它不启动或声称回滚事务，返回已确认行数、失败行及 partial/uncertain 状态，关闭输入而不重试。一次性 MySQL 8.0/8.4 的 MyISAM 测试核验无符号大整数、DECIMAL(65,30)、二进制、空二进制与 NULL；截断警告留下实际已写入行并报告 partial。
