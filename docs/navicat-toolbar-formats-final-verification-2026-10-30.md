# 格式、外部来源与流式规模验收（2026-10-30）

本轮研究证据位于忽略目录 `temp/research/toolbar-formats-final-20261030/`。本报告记录独立读取和实际服务验证 I；未操作原版 GUI，不据此声明 Navicat 双边 V。原版结果由主报告分别记录。

## 文件格式与数据库

- `external-formats.log`：实际 xlrd 2.0.2、pyarrow 26.0.0 和 go-dbase v1.10.2 分别读取 NyaDB XLS、Parquet、DBF。两行 fixture 核验超过 2^53 的整数文本、精确 DECIMAL、中文换行、二进制、空串及 NULL；DBF 使用 VFP V/Q 与 nullable flags，不能推断原版支持该分支。
- `nyadb-six-rows.*` 与 `nyadb-six-rows-values.json`：所有 11 种导出格式的同一份六行、五字段 fixture，供原版实际反向导入。含 NULL、空串、空 BLOB、非空二进制、中文、换行、公式样文本、DECIMAL(30,10) 和微秒日期。文件写入成功只标 I。
- `independent-six-row-readers.json`：Python csv/json/XML/HTML、xlrd、pyarrow 和 XLSX ZIP/XML 包检查分别核验六行输出。XLS/XLSX 的标准文本单元格读取不解释 NyaDB 的 NULL/binary 辅助元数据，不能把这项检查称为跨应用完整类型保真。
- `sql-external-verification.json`：通过独立 MySQL driver 执行六条导出 SQL，仅把原 fixture 限定目标替换为本轮唯一自有表。独立 SELECT 核验全部 30 个值，包含 NULL/空串、HEX(binary)、精确 DECIMAL 和 DATETIME(6)；表在 finally/defer 中删除。
- `mysql-stream-modes-native-race.log`：真实 MySQL 8.4.11 六种模式及批量六种模式全部通过；继续重复键、其它唯一键冲突、完整匹配键、跨批次清空回滚、精确数值/二进制、外键会话恢复和超过 10000 行实际写入均通过。

数据库验证仅使用现有 loopback 32790 隔离容器、`nyadb_toolbar_tx_it` 与唯一自有 `import_probe_*` 表，未修改业务数据或账户。

## 外部来源

`native-final-race.log` 记录 Linux 实际 unixODBC/SQLite ODBC、MDBTools Access、pxlib Paradox 读取，以及取消/关闭生命周期回归。

| 来源 | 本轮实际结果 | 验证边界 |
| --- | --- | --- |
| ODBC | 中文路径/表名、中文 CRLF、精确数字字符串、NULL/空串、非空/空 BLOB 通过 | SQLite ODBC 驱动；其它本机驱动和原版同源比较未验收 |
| Access | 公共 ACCDB 的 Asset Items：13 字段、65 行；逐项与 mdb-json 核对 845 个值、67 个 NULL，通过 | mdb-json 是本服务的同一本机生产者，这证明包装不改值，不能视作独立数据库引擎或 Navicat V |
| Paradox 基础字段 | 自建两行整型、文本、数值、日期和字节；原 DB 字节一致 | 原有 fixture，不涵盖所有字段类型/编码 |
| Paradox memo/blob | 新自建五行 fixture，inline 5 字节、MB sidecar 2000/65536 字节完整读取；DB 和 MB 均未改变；缺失/截断 MB 被拒绝 | pxlib 的零长度 blob 与未设置值均编码为 NULL；不能宣称空 BLOB/NULL 的原版区分已验收，复杂 memo 编码仍未验收 |

新 Paradox fixture 由 `create_memo.c` 使用 pxlib 公共 API 生成。Access 公共样本来源与 SHA-256 复制到 `access-provenance.json`；未加入产品资源。MDBTools 接近其 200000 字节内部文本缓冲边界仍保守拒绝，需完整值 ODBC 驱动处理，不能当作支持全部 Access 值。

## 大文件与内存

新增显式开关测试 `TestDataTransferScaleVerification`，每个规模在独立冷进程内运行：合成逐行来源 → 私有暂存 → SHA-256 校验 → 完整逐行重读，固定 128 字节 note，id 递增。Python 每 5 ms 采样该测试进程 RSS；全部运行退出码为 0。

| 行数 | note 总字节 | 暂存字节 | 缓存预览行 | 峰值 RSS 字节 | GC 后保留堆增量字节 |
| ---: | ---: | ---: | ---: | ---: | ---: |
| 10000 | 1280000 | 2718894 | 100 | 25997312 | 83944 |
| 100000 | 12800000 | 27288895 | 100 | 26812416 | 97584 |
| 1000000 | 128000000 | 273888896 | 100 | 27824128 | 108488 |

输入增长 100 倍时，进程峰值 RSS 从约 26.0 MB 增至 27.8 MB，预览固定 100 行，完整重读全部记录。此结果验证当前暂存/重读路径内存有界；不是全应用 GUI 峰值，也没有测量数据库导入总耗时。完整样本、耗时及命令保存在 `scale-{行数}.json/log` 和 `scale-summary.json`。

`large-formats.log` 另记录真正文件读取：JSON/XML/HTML/XLSX/DBF/Parquet 分别超过 16 MiB、11001 行，全部完整读取。JSON/XML/HTML/XLSX 从第 1000 行至末行的保留堆增量分别为 8424/6024/336/464 字节。取消、字段超过完整值上限、Parquet 大字段的磁盘页缓冲和取消清理专项也通过。XLS 大 SST/工作表与独立 fixture 检查保留现有回归，BIFF8 自身的行数/单元格上限仍按格式拒绝，不能宣称 XLS 支持任意百万行。

## 复现与证据绑定

- 原生来源：显式设置 `NYADB_DATASOURCES_ODBC_IT=1`，以及 `NYADB_DATASOURCES_ACCESS_FILE`、`NYADB_DATASOURCES_PARADOX_FILE`、`NYADB_DATASOURCES_PARADOX_MEMO_FILE` 到上述自有/公共 fixture，然后执行 `go test -race ./internal/services/datasources -count=1 -v`。
- 独立格式：`NYADB_TRANSFER_FORMAT_IT=1 NYADB_TRANSFER_DBF_PROBE_IT=1`，执行现有 `TestDataTransferAdditionalExternalReadersIntegration`；独立工具已保留在忽略目录，不增加正常构建依赖。
- 六行产物：设置 `NYADB_TRANSFER_VERIFICATION_ARTIFACT_DIR` 到隔离目录，执行 `TestDataTransferVerificationArtifacts`，再运行该目录的 `verify_artifacts.py` 与 `verify_sql.go`。
- 规模：先 `go test -c ./internal/ui/dataworkspace`，再运行 `measure_scale.py`；其为每次独立进程设置 `NYADB_TRANSFER_SCALE_ROWS`。普通 `go test ./...` 不运行百万行研究测试。

本轮 feature 源码/测试子集的 SHA-256 位于 `source-fingerprint.json`，研究文件哈希位于 `evidence-sha256.json`。它们不替代主任务最后冻结的整仓源码指纹。Windows/macOS 实际组件、原版全格式双向导入、全 GUI 性能和普通桌面首次启动仍由主清单单独验收。
