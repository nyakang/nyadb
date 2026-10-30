# 外部数据源与流式导入验收记录

日期：2026-10-30。源码：`internal/services/datasources`、MySQL 的 `mysql_data_import_stream*.go`。完整服务源码 SHA-256 记录于忽略目录 `temp/research/toolbar-datasources-20261030/source-fingerprint.json`。本记录不替代主报告的原版操作与 GUI 差分验收。

## 实现与状态

| 项目 | 独立实现 I | 实际验证 | 原版观察 O / 双边差分 V |
| --- | --- | --- | --- |
| ODBC | purego 动态加载驱动管理器；驱动枚举；只读连接设置；限定标识符查询；完整分块字段读取；取消等待 SQLCancel 和原调用回执 | Linux unixODBC 2.3.14、SQLite ODBC 0.99991；中文路径、中文表名、Unicode/换行、精确数值字符串、NULL/空字符串、非空/空 BLOB | 此服务测试未操作 Navicat，保留主报告的 O/V 状态 |
| Access | 可用 ODBC 优先，否则通过 MDBTools 的 tables/schema/JSON 只读子进程；关闭和取消等待进程退出；诊断不含密码或源值 | Linux MDBTools 1.0.1；实际 ACCDB 的 `Asset Items` 表，13 字段、65 行 | 同上；未完成 Navicat 与同一 ACCDB 的逐字段差分 |
| Paradox | 可选 libpx 动态加载；先复制 DB 与 memo 文件，再由本机库读取私有副本；原文件仅只读打开 | Linux pxlib 0.6.9；自建 2 行 DB 的整型、文本、数值、日期、字节、NULL；原始 DB 字节一致 | 同上；memo/blob 边界尚未实际验收 |
| 真正批量 INSERT | append/replace 共用一次事务；按 10,000 行、16 MiB 批次、65,535 参数和配置语句大小分组；字段集合变化拆组 | 驱动回归测试检查实际 SQL 为多个 VALUES 元组；10,001 行拆组后只提交一次；参数、字段和语句边界通过 race | 执行器结果真实核验已通过；原版批量参数差分另由主报告记录 |
| 批次错误隔离 | 仅已知服务器错误且保存点回滚及释放均确认后才逐行执行；未知连接错误、取消和丢失保存点终止，不重放 | 确定性驱动测试验证等待回执、连续 1-based 日志、终止失败计数；真实 MySQL 重复键继续通过 | 终止/不确定提交的网络故障验证是注入测试，不能宣称真实故障现场已验收 |
| 六种模式、跨批次事务 | append/update/upsert/insert_missing/delete/replace；停止模式整次回滚；replace 使用 DELETE 保持事务 | 真实 MySQL 六模式、跨批次 replace 最后一组冲突后恢复全部原记录；完成计数与独立 SQL 一致 | 原版流程和 GUI 差分见主报告 |
| 会话设置 | 专用物理连接上加强严格 SQL 模式；按选项关闭外键检查；所有出口恢复，失败则丢弃连接 | 真实 MySQL 验证 sql_mode 和 foreign_key_checks 恢复；之后孤儿行插入确实失败 | 设置与原版 UI 参数差分见主报告 |

正常构建与启动不要求安装 unixODBC、MDBTools 或 pxlib；仅相关来源功能按需检测。Windows AMD64、macOS ARM64 已执行 `CGO_ENABLED=0` 测试二进制交叉编译；未在这些系统执行原生驱动测试，继续标为**未验收**。

## 实际数据与证据

所有研究 fixture、编译产物、日志留在忽略目录 `temp/research/toolbar-datasources-20261030/`：

- `native-race.log`：三个实际来源及单元测试的 race 结果。
- `mysql-stream-native-race.log`：实际 MySQL 流式导入 race 结果。使用现有隔离容器 `nyadb-toolbar-xml-20261030`、loopback 端口 32790、隔离库 `nyadb_toolbar_tx_it`；只新建并清理 `import_probe_stream_*` 表，未改账号或既有业务表。
- `create_paradox.c` / `source.db`：由 pxlib 公共 API 自建的 Paradox fixture。
- `ASampleDatabase.accdb` / `access-provenance.json`：MDBTools 公共测试仓库样本，URL 为 `https://raw.githubusercontent.com/mdbtools/mdbtestdata/master/data/ASampleDatabase.accdb`，SHA-256 `3ee857f318f953758970e2981ba6435d0240212365d1f2110ccd571d6db6a9fb`。未将样本加入发行资源。
- `windows-cross-compile.log` / `darwin-cross-compile.log` 与对应目录：两服务的无 CGO 交叉编译证据。

真实流式大输入为 10,001 行，每行 2,048 字节文本，文本总计 20,482,048 字节。外部 SQL 对 COUNT 与 SUM(OCTET_LENGTH) 的核验通过。精度 fixture 验证 BIGINT UNSIGNED 最大值、DECIMAL(65,30)、中文换行、空串、NULL、`00FF` 字节及空 BLOB。完整行结果由回调写出，服务返回的诊断样本最多 100 行。此规模测试证明总输入限制已移除；内存随规模变化的实测曲线仍属于主任务的性能验收。

## 回归与限制

已执行流式 focused race、两服务 `CGO_ENABLED=0 go vet` 和上述原生测试。回归覆盖未知连接错误不重放、已知服务器错误必须等待保存点回滚、取消等待原写入和整次事务回滚、失败行在停止前写入结果、结果日志失败回滚，以及既有不确定提交只保留状态不重放。

实际 Linux 测试发现 SQLite ODBC 的 SQLDriverConnectW / SQLExecDirectW 损坏中文路径/标识符，Unix 使用进程 UTF-8 编码的对应 API；Windows 保留宽字符 API。字段和元数据仍使用宽字符读取，非法 UTF-16/UTF-32 直接拒绝，不替换为字符。其它 Unix 本机驱动与非 UTF-8 进程 locale 未实际验收。

MDBTools 的部分版本对普通文本/memo 内部缓冲为 200,000 字节，读取器保守拒绝接近该边界的值，避免把本机工具截断值当完整字段。该情形需改用支持完整值的 ODBC 驱动。Paradox 复杂 memo/blob 编码、Windows/macOS 原生读取、全部 Access 类型，以及与 Navicat 同 fixture 的外部来源双边差分，均保留为未验收。
