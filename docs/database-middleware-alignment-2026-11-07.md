# 数据库与中间件能力对齐清单 — 2026-11-07

目标保持为继续接入数据库和中间件能力，逐项提交，最终对齐 Navicat 和
本地 `temp/dbx` 的对应能力。连接成功、增加图标或复用协议均不足以证明
产品或整体对齐完成。

## 基线与范围

NyaDB 基线为 `c4cf751`，已有 13 个独立产品连接，能力边界见 README。
CockroachDB 已提交，无需重复提交。本地 DBX 的参考提交为
`a3add88da1c44fc97d233f90977f67dc8c277fc1`。

KingbaseES 核心接入后，当前已注册 16 个独立产品：MySQL、MariaDB、
TiDB、OceanBase、PostgreSQL、CockroachDB、openGauss、GaussDB、KingbaseES、SQLite、DuckDB、
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
- [x] openGauss 原生事务表重命名、完整目录/物理身份核验及工作台草稿处理，见 [重命名验证](opengauss-table-rename-2026-11-07.md)。
- [x] GaussDB 原生连接基础：华为 Go 驱动、独立多端点/TLS、公开配置、真实 Kernel 503.1.0 Docker 认证/类型/事务/取消清理验证；基础阶段记录见 [传输基础与保留范围](gaussdb-native-transport-2026-11-07.md)。
- [x] GaussDB 工作台核心：独立多端点配置、逐物理连接原生身份校验、SQL/过程脚本/文件、真实 USTORE/ubtree 和 ASTORE/btree 共享数据编辑/事务/导入导出、取消恢复及不确定回执验证，见 [核心验证](gaussdb-2026-11-07.md)。
- [ ] GaussDB 设计/对象/账户管理、备份维护、主备/分布式/DWS 验证及其它完整参考功能。
- [x] KingbaseES 原生连接基础：独立公开配置、原生 gokb 驱动环境隔离、真实 V8R6 PG 模式 SCRAM/TLS/类型/事务/取消验收和未知写入禁止重放，见 [传输基础与保留范围](kingbase-native-transport-2026-11-07.md)。
- [x] KingbaseES 工作台核心：独立原生配置和入口、逐物理连接产品/版本/模式校验、限定元数据、SQL/脚本/文件与共享精确网格、编辑/事务/导入导出、取消恢复和不确定回执验证，见 [核心验证](kingbase-2026-11-07.md)。
- [ ] KingbaseES 设计/对象/账户管理、备份维护、其它版本/兼容模式/HA 和其它完整参考功能。
- [x] 达梦 DM8 接入前置验收：自有 Docker 实例、原生版本/模式/编码检查、未修改候选驱动的精度损失与已写入但回复丢失后的重复写入复现，以及显式配置规避证据；仅为研究工具，见 [前置报告](dameng-readiness-2026-11-07.md)。
- [x] 达梦私有 loopback DPI 会话边界：外部原厂客户端、精确 NUMERIC、普通认证/拒绝、预处理/临时会话隔离、原始 I/O 取消与原句柄等待、真实 INSERT/COMMIT 回执丢失禁止重放；远程/TLS 和工作台尚未注册，见 [边界验证](dameng-dpi-boundary-2026-11-07.md)。
- [ ] 达梦原生传输和工作台接入：驱动来源/发布边界、精确绑定、认证/TLS、取消/未知提交、独立配置/provider/元数据/共享 SQL 和数据工作区；清单仍为 missing，完整管理范围继续保留。
- [x] 达梦原生 TLS 传输基础：显式远程策略、Go 服务器身份校验、每会话私有 TLS、原厂超时毫秒单位、延迟握手及加密取消/回执丢失验证；固定服务器没有强制客户端证书信任，未声称完整双向认证，工作台仍未注册，见 [传输验证与限制](dameng-native-tls-2026-11-07.md)。
- [x] 达梦独立公开配置与逐物理产品/Schema 校验基础：限定系统视图、原生内建身份、真实初始 Schema 设置/读取、普通账户和两物理会话隔离、同名用户对象及拒绝/取消回归；完整原厂环境隔离和工作台注册仍待完成，见 [会话校验与保留范围](dameng-native-session-gate-2026-11-07.md)。
- [x] openGauss 私有表设计验证基础：原生规划、完整值/目录证明、事务执行、self/outgoing/incoming 与循环外键、注释保留和取消回滚；公开能力仍关闭，见 [基础阶段与验收边界](opengauss-table-design-2026-11-07.md)。
- [x] openGauss 表设计表单阶段：原生字段/约束/索引表单、可空注释、触发器状态、存储选项、独立规划与小窗口中英文检查；通过注入测试绑定验证，公开保存仍未开放，见 [表单阶段](opengauss-table-design-2026-11-07.md#原生表单与独立规划阶段)。
- [x] openGauss 原生 SQL 转换预检：32 条原生转换及嵌套实现检查、真实值/typmod、独立声明回滚和已提交替换拒绝；不代表检查到执行之间的并发稳定性，公开表设计保存仍关闭，见 [转换阶段](opengauss-table-design-2026-11-07.md#原生-sql-转换预检阶段)。
- [x] openGauss 新增默认值的原生重写证明：独立空堆观察、TOAST 身份与实际 chunk、完整值/回滚、混合原生转换的私有提交；公开表设计仍关闭，见 [重写阶段](opengauss-table-design-2026-11-07.md#新增默认值的原生重写证明)。
- [x] openGauss 隐式类型与数组元素闭包：隐式转换目标、递归数组元素及原生回调检查，替换/缺失类型回调拒绝和原生数组声明验证；完整目录并发与公开保存仍待完成，见 [隐式类型阶段](opengauss-table-design-2026-11-07.md#隐式类型与数组元素闭包阶段)。
- [x] openGauss 索引访问方法绑定：9 种原生方法和 106 个固定回调、实际存储映射、目录替换拒绝及原生索引读写验证；目录并发、其它调度绑定和公开表设计仍待完成，见 [索引绑定阶段](opengauss-table-design-2026-11-07.md#原生索引访问方法绑定阶段)。
- [x] openGauss 全部字段替换的原生存储证明：独立空载体证明 TOAST 重建及先删后增的移除，绑定堆所有者、认证账户和完整物理目录；完整值、保留用户索引、不同所有者及取消/回执丢失的真实回归通过，公开表设计仍关闭，见 [字段替换阶段](opengauss-table-design-2026-11-07.md#固定宽度字段替换的原生存储证明)。
- [ ] openGauss 表设计、其它对象/账户管理、备份恢复和维护能力补齐；[表设计开发证据](opengauss-table-design-2026-11-07.md)记录私有事务、自引用/传入引用与循环外键组合验证；公开表设计入口和剩余范围仍未完成。
- [ ] 其它参考连接、现有产品缺口和跨产品工具。
- [ ] 按完整清单逐项审计所有用户流程和验证范围，证明最终对齐。
