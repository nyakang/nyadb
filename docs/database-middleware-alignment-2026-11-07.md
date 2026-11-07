# 数据库与中间件能力对齐清单 — 2026-11-07

目标保持为继续接入数据库和中间件能力，逐项提交，最终对齐 Navicat 和
本地 `temp/dbx` 的对应能力。连接成功、增加图标或复用协议均不足以证明
产品或整体对齐完成。

## 基线与范围

NyaDB 基线为 `c4cf751`，已有 13 个独立产品连接，能力边界见 README。
CockroachDB 已提交，无需重复提交。本地 DBX 的参考提交为
`a3add88da1c44fc97d233f90977f67dc8c277fc1`。

openGauss 核心接入后，当前已注册 14 个独立产品：MySQL、MariaDB、
TiDB、OceanBase、PostgreSQL、CockroachDB、openGauss、SQLite、DuckDB、
SQL Server、Oracle、ClickHouse、MongoDB 和 Redis。注册连接与管理功能
完整度分别验收；openGauss 表设计仍在开发，没有开启公开 Apply。

[机器可读清单](database-middleware-alignment-2026-11-07.json) 保留 DBX 的
88 个稳定连接类型、110 个产品配置，以及每个类型声明的能力。来源为
`plugins/connection-types/*.yaml`、`profiles/catalog.yaml` 和生成的
`crates/dbx-core/assets/database-drivers.manifest.json`。声明仅确定待验收目标，
运行时实现、真实服务器和用户流程证据才证明能力完成。

现有产品记为 `implemented_with_gaps`，尚未接入的产品记为 `missing`。
同协议的云服务或兼容产品仍需确认独立默认值、认证、元数据和能力边界。
DBX 的单个连接类型可能含多个产品；NyaDB 的独立 MariaDB/TiDB/OceanBase/
CockroachDB 身份不应为追求类型数量而合并。

Navicat 用户流程以已记录的原生证据和源码审计为参考：
[现有差距审计](navicat-ui-ux-gap-audit-2026-11-05.md)、
[已完成高频体验改进](navicat-ux-implementation-2026-11-05.md)。
DBX 的能力参考还包含其 SQL/schema/transfer 模块和
`crates/dbx-core/src/admin/{mq,mqtt,nacos,consul}`。不以 README 中的宣传
或静态能力标记代替实际流程验收。

## 实施顺序

| 批次 | 产品或能力 | 完成要求 |
| --- | --- | --- |
| A | openGauss，随后 GaussDB、KingbaseES/达梦，以及其它关系型产品 | 独立产品配置、真实认证/TLS、命名空间和对象结构、查询诊断与计划、共享数据编辑/事务/导入导出；设计与管理能力逐项验证 |
| B | Doris/SelectDB/StarRocks、Redshift/Snowflake/Trino，以及清单中的其它分析和时序数据库 | 原生类型与方言、正确只读/写入语义、查询/分页/计划与导入导出，不继承不适用的 OLTP 管理工具 |
| C | Elasticsearch/Easysearch/Solr、CouchDB/Meilisearch、图和向量数据库 | 原生命令或请求执行、对象/文档/索引浏览、分页、编辑并发控制、导入导出；按实际协议表达能力 |
| D | etcd、Consul、Nacos、ZooKeeper | KV/配置和服务发现浏览、查询/编辑、版本比较、ACL/认证/TLS、取消、会话替换及管理工具 |
| E | Kafka、RabbitMQ、RocketMQ、Pulsar、MQTT | 主题/队列、消费组、消息读取/发布与追踪、Broker/策略/权限；专属管理界面及明确的副作用边界 |
| F | 清单剩余数据库、泛型 JDBC/驱动与扩展 | 实际可安装连接与可运行查询、兼容配置持久化、隔离驱动生命周期、原生能力和认证边界；覆盖全部参考配置 |
| G | 补齐现有数据库管理与跨数据库工具 | 表/视图/程序/账户管理，备份恢复，结构/数据对比与同步，数据传输、生成/脱敏，ER 建模和字段血缘，自动运行/定时备份及服务器监控 |
| H | 连接迁移与协作入口 | Navicat/DBeaver 配置导入、加密跨设备迁移、连接拓扑/代理/隧道和云认证、可控的 CLI/MCP 及扩展入口 |

批次是执行顺序，不缩减机器清单或既有产品的缺口。每个独立接入和可交付
功能验证后立即提交；一个产品的核心接入提交后，其尚未完成的管理能力
仍留在清单中。中间件复用连接、凭据和生命周期基础设施，但不强制伪装
为 SQL 数据库或关系表。

## 每项验收与提交

1. 配置、URI、凭据保存/回滚、重启和旧配置兼容已验证。
2. 真实服务器证明产品和版本、认证/TLS、对象身份、数据类型与操作语义。
3. 请求取消、断开/替换、原连接结果归属、不确定提交及显式恢复均有证据；
   不自动重放用户命令或不确定写入。
4. 工作台入口、导航、对象/数据或专属管理页面、翻译、错误反馈和能力开关
   均可操作；共享 SQL/数据模块继续遵守 AGENTS.md 的边界。
5. 完成相关回归、必要生命周期 race、Go/MyGo 检查、CGO=0 三平台构建；
   可运行的临时集成 fixtures 仅清理自己的资源，保留可检查的界面证据。
6. 文档准确区分完成、部分完成、未实现与未验证，再提交该项代码和证据。

## 进度

- [x] 核对当前提交，建立完整参考类型/产品清单和验收范围。
- [x] openGauss 核心接入与真实验证，见 [验证与保留缺口](opengauss-2026-11-07.md)。
- [x] openGauss 原生过程脚本切分、光标执行及有界 SQL 文件执行，见 [脚本验证](opengauss-native-scripts-2026-11-07.md)。
- [x] openGauss 普通视图管理及原生选项/元数据保留，见 [视图验证](opengauss-views-2026-11-07.md)。
- [x] openGauss 原生事务删除表及结构/依赖校验，见 [删除表验证](opengauss-table-drop-2026-11-07.md)。
- [x] openGauss 原生事务表复制、独立普通/LARGE 序列及完整数据核验，见 [表复制验证](opengauss-table-copy-2026-11-07.md)。
- [x] openGauss 私有表设计验证基础：原生规划、完整值/目录证明、事务执行、self/outgoing/incoming 与循环外键、注释保留和取消回滚；公开能力仍关闭，见 [基础阶段与验收边界](opengauss-table-design-2026-11-07.md)。
- [x] openGauss 表设计表单阶段：原生字段/约束/索引表单、可空注释、触发器状态、存储选项、独立规划与小窗口中英文检查；通过注入测试绑定验证，公开保存仍未开放，见 [表单阶段](opengauss-table-design-2026-11-07.md#原生表单与独立规划阶段)。
- [x] openGauss 原生 SQL 转换预检：32 条原生转换及嵌套实现检查、真实值/typmod、独立声明回滚和已提交替换拒绝；不代表检查到执行之间的并发稳定性，公开表设计保存仍关闭，见 [转换阶段](opengauss-table-design-2026-11-07.md#原生-sql-转换预检阶段)。
- [x] openGauss 新增默认值的原生重写证明：独立空堆观察、TOAST 身份与实际 chunk、完整值/回滚、混合原生转换的私有提交；公开表设计仍关闭，见 [重写阶段](opengauss-table-design-2026-11-07.md#新增默认值的原生重写证明)。
- [x] openGauss 隐式类型与数组元素闭包：隐式转换目标、递归数组元素及原生回调检查，替换/缺失类型回调拒绝和原生数组声明验证；完整目录并发与公开保存仍待完成，见 [隐式类型阶段](opengauss-table-design-2026-11-07.md#隐式类型与数组元素闭包阶段)。
- [ ] openGauss 表设计、其它对象/账户管理、备份恢复和维护能力补齐；[表设计开发证据](opengauss-table-design-2026-11-07.md)记录私有事务、自引用/传入引用与循环外键组合验证；公开表设计入口和剩余范围仍未完成。
- [ ] 其它参考连接、现有产品缺口和跨产品工具。
- [ ] 按完整清单逐项审计所有用户流程和验证范围，证明最终对齐。
