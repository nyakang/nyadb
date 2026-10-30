# 表工具栏补齐：原版运行证据（2026-10-30）

本报告采用 reverse-engineer-anything 的观察/证据流程。研究产物位于忽略目录 `temp/research/table-toolbar-completion-20261030/`，不作为产品资源；尚无新增完整双边 V。

## 运行身份与边界

原版进程 `26874`（子进程 `27004`），Xvfb `:184`，1600×960，复用既有正常授权状态和隔离 MySQL 容器 `nyadb-toolbar-xml-20261030`（loopback 32790）。旧观察子任务在 17:52:08 因 API 图像数量超过 50 而退出；本任务在确认其终止后才复用该窗口。

尝试在 `:222` 及复制的现有授权配置下启动原版，进程退出且没有可见窗口，日志仅有库版本警告。它不能证明多实例支持或确定退出原因；未修改授权。该辅助 Xvfb 未用作功能证据。

只创建本任务唯一自有表 `table_edit_it.toolbar_completion_formats_20261030`。`formats-before.tsv` 确认不存在，`formats-fixture.sql`、`formats-created.tsv` 保留实际 DDL 和六行值。包含 Unicode、LF、SQL NULL、空串、空字节、VARBINARY、超过 2^53 的 DECIMAL 与微秒日期。其它测试表未更改。

## 新增 O：导出向导与 Parquet 失败边界

- `007-source-ready.png`：原版真正打开六行自有表。
- `008-export-entry.png`：导出前区分全部记录、当前六条记录和取消。
- `009-export-formats.png`：实际可见 TXT/CSV/JSON/XML/HTML/SQL/XLS/XLSX/DBF/Parquet。
- `010-parquet-destination.png`：选择唯一自有表和输出文件，列表其它表未勾选。
- `011-parquet-fields.png`：五个字段全部选择，包含 payload。
- `012-parquet-options.png`：可见“遇到错误时继续”默认勾选。
- `013-parquet-ready.png`：执行前总结。
- `014-parquet-finished.png`：真实执行后处理 6 行，但日志连续显示 StringBuilder 无效转换，随后显示 `payload expected length 6 but got length 0`。界面仍写 `Finished successfully`。

`original.parquet` 实际大小 606 B。该文件是失败路径的 O 证据，不能作为成功互通 fixture。不能仅凭 100% 或 Finished successfully 判定格式成功。仍需去掉二进制列重试、独立解析确认真实字段和行数。

## 旧六模式证据纠正

`temp/research/table-toolbar-resume-20261030/` 的 `024-update-finished.tsv`、`025-upsert-finished.tsv`、`upsert-single-finished.tsv` 和 `043-delete-result.tsv` 与原始两行一致。对应完成截图实际日志为 INSERT 遇到重复主键，Added/Updated/Deleted 均为 0；截图文件名中的 update/upsert/delete 不证明对应模式真正生效。

`045-append-success-finished.tsv` 确认无冲突追加实际新增 id=3 和 id=4，原 id=1/2 保留。它只证明该追加样本的 O；六模式匹配和继续/停止行为仍未完整验证。

## 验收状态

| 项目 | 状态 | 仍需证据 |
|---|---|---|
| 导出步骤/入口 | O | NyaDB 对应原生窗口、键盘及同条件几何 |
| 六模式 | 仅 clean append 有 O；其它未验证 | 映射/模式状态确认、执行SQL、逐行结果与双边比较 |
| 原版 Parquet | O：二进制分支失败 | 有效不含二进制样本、独立读取和 NyaDB 互通 |
| TXT/XLS/DBF | 未验收 | 原版实际文件和反向导入 |
| 统计边界 | 旧截图有 O，尚未整理完整数值 | 原 fixture 身份、完整统计及 NyaDB 同范围值 |
| 十万行三次性能 | O：三次完成时间区间和总RSS已测 | NyaDB 同fixture结果/资源测量；原版取消尚未证明 |
| 关键几何 ≤2 DIP | 未验收 | 原版与 NyaDB 同DPI/视口成对几何 |
| 普通桌面首次启动 | 未验收 | 真实桌面启动，不能用 Xvfb 替代 |

截图和 SQL 的 SHA-256 位于 `evidence-sha256.json`。后续每项以新增实际结果更新本报告，不把已有 I、截图命名或工具退出码当作 V。

## 新增 O：十万行同源分析与窗口测量

`perf-fixture.json`、`perf-fixture.sql`、`perf-created.tsv`、`perf-rows.tsv` 标识本任务唯一自有表 `table_edit_it.toolbar_completion_perf_20261030`。server UUID 为 `287d15ac-d42a-11f1-87dd-a65e7abb65ad`，MySQL 8.4.11。范围为全部 100000 行已提交记录，无筛选，无编辑草稿；字段 id BIGINT、amount DECIMAL(30,10)、note VARCHAR(32)，InnoDB/utf8mb4_0900_ai_ci。id 为 1..100000，amount 为 id−50000，note 为 group−(id mod 100)。按 id 排序的真实 TSV SHA-256 为 `d39a3a0df31a5c73d846adb9dd5efc1a1190fbe74c38713e1b311c8b9257b8d1`。

原版实际 `SELECT * FROM table_edit_it.toolbar_completion_perf_20261030` 已由逐次 PROCESSLIST 记录确认。原版迅速获取全表后在客户端继续分析；不能把服务器查询结束当作 GUI 分析完成。20 ms 采样父进程 26874 和子进程 27004 的 VmRSS，并对灰色占位变为完整青色分布条的 ROI 轮询；三次最终原生截图均显示 id/amount 的非重复与唯一数量 100000，note 的非重复 100、唯一 0，NULL 0。

| 审核轮次 | GUI 完成时间区间（秒） | 父子总 RSS 峰值（B） | 开始基线（B） | 峰值减基线（B） |
|---|---:|---:|---:|---:|
| 2 | (5.236118, 5.505514] | 1043316736 | 977989632 | 65327104 |
| 3 | (4.738007, 5.005545] | 1141014528 | 1039933440 | 101081088 |
| 4 | (5.007564, 5.259346] | 1213460480 | 1137598464 | 75862016 |

中位 GUI 完成区间为 **(5.007564, 5.259346] 秒**。原生截图、ROI、进程采样和 SQL trace 分别为 `original-perf-{2,3,4}-ready.png`、同名 ready-roi.png、`original-perf-{2,3,4}.json`；汇总 `original-performance-summary.json`。第一次探索测量只得到 (4.834,9.139] 秒，未纳入三次精细测量中位数。

以上时间包含轮询截图开销；RSS 是含已有停放文档的全应用父子进程总和，存在递增基线。不能将这些数值与 NyaDB 服务的 TotalAlloc 或单次 HeapAlloc 增量直接等量比较，也不是通用延时门槛。尚未把原版全应用与 NyaDB 服务边界的差异称为性能 V。

`original-geometry.json` 保留实际 X server 1600×960、406×244 mm（计算 DPI 约 100.10×99.93）、原生导出窗口 800×600 client geometry 及工具栏像素测点。实际 Qt 缩放尚未独立确认；该 JSON 没有声明双边 ≤2 DIP 已通过。

取消试验 `cancel-process-waiting.tsv` 确认原版 SELECT 等待本任务表的 metadata lock；但 `original-cancel-observation.json` 显示 Ctrl+W 派发之前锁已超时自动释放。Sleep 状态不能归因于取消，本次取消响应仍未验收。锁在 finally 中已释放，未修改 fixture 数据。
