# PostgreSQL / SQLite 共享网格大数据能力 — 2026-11-03

接续完整工作台目标，交付 PG/SQLite 的稳定游标分页、显式精确计数、限量字段读取和全表分析。使用原有 dataworkspace State/Document 和网格，未增加引擎专用 UI。证据属于独立实现 I 和 NyaDB 运行；不新增 Navicat O/S/V。

## 实现范围

- 产品描述及实际 DataRuntime/DataReads/DataAnalysisOperations 接通 Keyset、Count、FieldChunks、Analysis；分页、计数、字段和分析继续各自拥有请求与取消范围。
- 每次读取在原物理会话的单个事务中取得结构与数据；PG 使用 repeatable read 及原限定关系的 ACCESS SHARE 锁。已有编辑事务中的页/字段读取沿用原事务。翻页之间不是长时间数据库快照。
- 游标绑定物理会话随机 scope、generation、ProfileID/Catalog/Schema、真实表结构、筛选、排序和对象身份。规则改变、重连或对象变化拒绝旧游标。唯一键参与稳定排序；SQLite BINARY / PG C 文本比较作为键的最后决胜条件。可空排序、缺失稳定键或不匹配的 PG 索引操作符类不承诺稳定 seek。
- 网格投影在数据库内截取 1024 字符/字节并返回截断标记，页最多 1000 行、512 字段和 16 MiB 结果预算。截断行仍保留完整类型化主键供显式字段读取；不能把截断预览作为完整编辑值。显式整行编辑保留 1 MiB 边界。
- 字段读取最多 1024 字符/字节，复核完整原对象及唯一键；支持文本、JSON、二进制、NULL、空二进制和 Unicode。SQLite 含 NUL 文本通过该用户会话专属驱动函数计算字符边界，不向配置库或全局 driver 注册函数。UI 在派发时深拷贝类型化身份及 bytes。
- 分组、计数、排序、频率和中位数计算在引擎读取快照中完成，只返回统计、两项中位值和最多十项有界频率预览。SQLite 数值聚合保持整数及实际浮点值的有理数累计，避免 int64 经 float64 丢失精度；PG 保留 numeric 文本。显示总体标准差；不支持的非有限值明确拒绝。
- PG/SQLite 正式窗口的失效分析提示改用“原数据库会话”，翻译重新生成。布局存储键/格式、原连接、停放、代次核验和取消确认机制保持兼容。

## 验证

当前源码重跑全量 Go 测试通过；Go vet、MyGo vet、翻译生成/检查（1466 条）、正式 MyGo 打包和 Python 62 项检查通过。

真实自有 PostgreSQL 17 的 `--race --run '^TestPostgreSQL17Advanced.*Fixture$'` 通过：游标范围、预览/整行边界、字段分块、计数/事务、精确分析与锁等待取消。运行 `build/verify/postgres-tY2XTs/`，服务测试 1.900 秒。另一个独立 PG fixture 的共享网格 Tester race 通过，SQLite/PG 均实际操作上一页、下一页、精确计数、8192 字节字段分块和三行全表分析，核对大整数均值 `9223372036854775806`；运行 `build/verify/postgres-z8KySr/`，4.204 秒。

SQLite 十万行回归核对全表计数、seek、均值、中位数和单例数；它是正确性测试，不等同于同条件峰值 RSS 或 Navicat 性能对照。Shell 原连接/断开/独立取消回归 race 通过，42.091 秒；engines/dbruntime race 通过（48.785 秒/缓存）。第一次完整 dataworkspace race 收到 SIGTERM，exit 143，不计为通过；随后按当前 271 个顶层测试的完整清单分为四批，记录实际 PASS/SKIP 并核验无遗漏。

分批验证定位旧 `TestSelectedNullCellKeepsReadableInkAfterColumnReorder` 的重复整帧 RGBA 转换：每个像素都调用 Tester.Image。改为每次颜色断言捕获一次图像，仍逐像素检查同一范围、颜色、最少像素数、两种主题及列重排后的选择，不缩减断言。

四批分别 exit 0，16.807 / 293.069 / 143.423 / 54.878 秒；顶层回执并集精确覆盖原清单 271 项，无重复或遗漏，262 PASS / 9 原有显式环境 SKIP。SKIP 属于 MySQL/外部读器集成与显式视觉/规模产物测试，不作为它们通过的证据；PG 和 SQLite 本阶段的真实用例另行执行。像素检查优化后的同一用例 race 重跑通过，测试体 0.53 秒；没有通过删去慢测试满足 race 检查。相关日志为 `build/verify/portable-data-race-part-{1,2,3,4}.log`。

最后英文文案及像素测试修正后，再运行当前完整 dataworkspace 单次 race，通过，247.649 秒；最终 `go test ./...` 通过（dataworkspace 31.752 秒、Shell 102.225 秒），Go/MyGo vet 再检查 exit 0。原完整 race 的 exit 143 保留，不改写为成功；最终无需用分批结果替代完整单次结果。

## Linux 正式窗口

复用上轮拥有且仍存活的独立运行 `build/entry-acceptance/r-e45d3c3572f0/`，不复用生产库或系统密钥环。自有 SQLite 附加文件包含 250 行、三个接近 int64 上限的整数值、Unicode 长文本及 8192 字节二进制。

上一轮最后一次采集 `capture-b5c3ecd7db9d` 的实际图片复核确认全表分析 250 行（网格一页为 100 行）、二进制字段读取 1024/8192、非重复数和单例数。此轮重建正式程序，新增所选整数列的统计图片 `capture-c362af9d4245/analysis-numeric-1280-client.png`，实际查看确认 250 行、三个非重复值、最小值 `9223372036854775805`、众数 `9223372036854775806` 及右栏所选值；均值/中位数由真实服务及 Tester 核对，该图片没有显示所有统计行。

`custom-9115f82e` exit 0，正式运行二进制 SHA-256 `b888609b3c974530018632924df6ae0594bf4558146cf6f48e44f25a012d61d1`；实际 client 1280×800 / 840×560。读取前后源数据指纹相同，为 250 行、SHA-256 `06faf198efa7a4d38b45377cd55a90a96b83c0e6798c24419670cc528ad1f325`。采集 metadata 和脚本退出码不单独作为内容通过的证据。采集后补齐三条英文失效提示，未改变截图的中文界面；图片二进制身份与最终打包身份分别记录。

通过拥有者工具停止该运行，独立 `ps` 检查四个记录 PID 不存在，Docker 对本次 entry 容器及两个 PG fixture 容器均返回明确 no such object，`secrets.json` 已删除。仅清理这些记录身份的本阶段资源，保留公开图片、日志及密文配置。Windows/macOS amd64 的 CGO_ENABLED=0 编译通过；不当作对应平台的实际窗口验收。

最后英文提示修正后的正式 MyGo 打包 exit 0，二进制 SHA-256 `8771658a6950afcf39490632d05b0ac19317ab253dfb746bfdc23c5b4393a149`，与采集时的二进制身份分开记录。

## 尚需完成的完整目标

正式 PG 设计 Apply、PG 程序/序列目录和对象管理、多引擎导入/多格式向导及备份、非 MySQL 隧道、完整命名工作区、跨库传输/同步、自动运行、模型/BI/监控/生成/脱敏/CLI、ClickHouse/AI/字典深化继续保留。Windows/macOS、物理桌面 IME/缩放/多显示器和 Navicat 同条件差分不由本阶段替代。
