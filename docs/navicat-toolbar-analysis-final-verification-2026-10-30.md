# 服务统计数值验收（2026-10-30）

本轮只运行独立服务和外部 SQL，不操作 GUI。研究产物位于忽略目录 `temp/research/toolbar-analysis-final-20261030/`。统计数值符合独立参考只标 I；原版数值、实际窗口和全应用性能 V 仍由主报告单独记录。

## 同源十万行

复用现有只读 fixture `table_edit_it.toolbar_completion_perf_20261030`，MySQL 8.4.11，server UUID `287d15ac-d42a-11f1-87dd-a65e7abb65ad`。范围是全部 100000 行已提交记录，无筛选。通过 `data_probe` 会话读取；未修改或锁定该表。独立 SELECT 按 id 排序，带字段头 TSV 的 SHA-256 与原版既有证据相同：`d39a3a0df31a5c73d846adb9dd5efc1a1190fbe74c38713e1b311c8b9257b8d1`。

| 原始服务字段 | id | amount | note |
| --- | ---: | ---: | ---: |
| 行数 | 100000 | 100000 | 100000 |
| NULL | 0 | 0 | 0 |
| 非 NULL 非重复组 | 100000 | 100000 | 100 |
| 非 NULL 单次组 | 100000 | 100000 | 0 |
| 数值行数 | 100000 | 100000 | 0 |
| 零值 | 0 | 1 | 0 |
| 最小值 | 1 | -49999.0000000000 | 不适用 |
| 最大值 | 100000 | 50000.0000000000 | 不适用 |
| 平均值 | 50000.5000 | 0.50000000000000 | 不适用 |
| 中值 | 50000.5000 | 0.50000000000000 | 不适用 |
| 总体标准差 | 28867.513458037913 | 28867.513458037913 | 不适用 |
| 最大频次 | 1 | 1 | 1000 |
| 完整众数数量 | 100000 | 100000 | 100 |

服务只保存每列最多 10 个非 NULL 分布值；id/amount 的 100000 个并列众数、note 的 100 个并列众数计数完整，没有被预览上限截为 10。原始全属性及分布样本保存在 `perf-service.json`，独立 SQL 结果在 `perf-external-sql.json`。

Python Decimal 90 位计算的参考总体标准差为 `sqrt((100000²−1)/12)`，即 `28867.5134580379125524472902219988450012520417813674169995949125456036017618451591886225152`。MySQL 标准差是浮点结果，按绝对 `1e-6`／相对 `1e-14` 误差核对；整数/DECIMAL 的极值、平均值和中值按精确 Decimal 核对。这里核验的是 `STDDEV_POP`，没有把总体标准差与样本标准差等同。

## 私有边界 fixture

`verify_analysis.go` 另创建唯一自有 `table_edit_it.analysis_final_*` 表，完成后删除。全部列结果与独立 SELECT 原始行、Python Decimal／计数组合比较，共 28 个列或场景检查通过：

- 超过 2^53 的两个接近值 `9007199254740992`、`9007199254740993`：DECIMAL(65,30) 平均值和偶数中值精确为 `9007199254740992.5`，总体标准差为 `0.5`；两个 NULL 构成频次为 2 的唯一众数组。
- `Alpha`／`alpha`／空串／NULL：`utf8mb4_0900_ai_ci` 非 NULL 非重复组为 2、单次组为 1；`utf8mb4_bin` 为 3／3。大小写无关筛选得到两行，二进制筛选得到一行。
- 空筛选：零行、零众数，数值极值／平均值／中值／标准差均为空；全 NULL 范围同样不产生数值统计，NULL 构成唯一众数组。
- 奇数非 NULL 数值 `0/12.5/25`：均值、中值为 12.5，总体标准差 `10.206207261596576`；加上一次 NULL 后完整众数数量为 4。
- 偶数 `0/12.5/25/50`：均值 21.875，中值 18.75，总体标准差 `18.4877493221863`。
- `10/10/20/20`：两个并列众数，每组频次 2，平均值／中值为 15，总体标准差为 5。
- DECIMAL(65,30) 的正负最大值：65 位极值字符串保持精确，平均值／中值为 0；总体标准差返回浮点 `1e35`，与精确独立值在声明误差内一致。

完整数值在 `boundary-service.json`、`boundary-external-rows.json` 和 `extreme-decimal.json`；比较规则和 28 项结果在 `compare_analysis.py`、`analysis-comparison.json`。服务的 Distinct/Singletons 排除 NULL；Modes 会在频次相同或更高时纳入 NULL。界面与原版的计数展示口径必须另行核对，不能直接把原始服务字段当作 GUI 标签数值。

## 回归及证据

显式运行 `NYADB_MYSQL_ANALYSIS_IT=1 NYADB_MYSQL_ANALYSIS_PORT=32790 go test -race ./internal/services/mysql -run '^TestMySQLDataAnalysis(Integration|MaximumPrecisionIntegration)$' -count=1 -v`，两项真实 MySQL 测试通过，日志为 `native-analysis-final-race.log`。现有集成测试覆盖真实锁等待期间取消及驱动回执、完整文本／JSON／二进制分组、29 个并列众数；新增极限精度回归固定 DECIMAL(65,30) 正负极值和 NULL 众数行为。所有锁仅限该测试创建的私有表。

本轮未发现生产统计服务缺陷，未修改 `mysql_data_analysis.go`。统计源码及测试子集指纹在 `source-fingerprint.json`，研究证据哈希在 `evidence-sha256.json`。本报告不代表 GUI 时间、全应用 RSS、普通桌面首次启动、Windows/macOS 运行，或与 Navicat 全统计数值已完成双边验收。
