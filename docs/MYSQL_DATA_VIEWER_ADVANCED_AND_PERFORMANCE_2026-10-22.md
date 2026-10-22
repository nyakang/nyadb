# NyaDB MySQL 数据表交互与大表性能验证（2026-10-22）

## 目标与安全边界

仓库：`kang/nyadb`，Go 1.27+ / MyGo v0.6.0。基线：`669620b`。
仅增强正式 MySQL 只读数据页；模拟数据页保持分离。未执行数据编辑或 DDL，未连接生产数据库。测试 DDL 只在明确创建的本地回环 `mysql:8.4` 一次性容器内，由隔离夹具脚本载入。

O=Navicat 原版实际观察，S=原版静态证据，I=NyaDB 独立实现并验证，V=原版与 NyaDB 差分通过，Unknown=缺少证据。本轮主要是 I 和真实 MySQL 性能测量，不宣称新增 Navicat 差分 V。

## 实现

### 数据服务

`internal/services/connections/mysql_data_query.go` 新增 `TableDataPageWithOptions`，仍复用已建立的 `MySQLSessionManager`、TLS、安全凭据及 Disconnect 取消链路。原 `TableDataPage` 已统一委托给增强版查询，不再存在会拉取完整大字段的 `SELECT *` 后备路径。

- **有限投影**：服务器对 BLOB/二进制列使用 `SUBSTRING(column,1,1025)`，文本和 JSON 类使用 `SUBSTRING(column,1,2049)`（字符计数，utf8mb4 最多约 8 KiB/字段），然后在本地把单元格预览限定到 2048 字节。长预览和二进制值不会将完整 1 MiB+ 字段加载到 UI。二进制预览使用十六进制编码。
- **元数据校验**：数据库与表标识符经验证和反引号转义；字段均来自绑定参数的 `information_schema.COLUMNS`；筛选值、页大小和偏移量都使用 SQL 绑定变量。列数量上限 128，行数量上限 200 + 1 行前瞻。
- **无主键**：读取 `information_schema.STATISTICS` 并校验列是否全长度、非空，优先使用主键，其次使用完整且非空的唯一索引；没有可靠唯一键的表/视图继续提供只读 OFFSET 分页，同时明确警告结果顺序不稳定，不伪称唯一性。
- **服务端筛选**：允许精确匹配或文本 `LOCATE` 包含匹配；字段名校验后转义，实际值绑定。筛选没有全表计数，不假称只筛当前页。
- **服务端排序**：只允许元数据可验证且可排序的标量字段；指定排序列后追加完整唯一索引作为平局排序依据。没有完整唯一键时仍显示分页不稳定的提示。
- **生命周期**：Context/会话断开/20 秒超时可取消查询；失败返回错误，不回退到未筛选结果。

局限：OFFSET 深分页仍为线性扫描；表在多次分页间发生并发写入时，即使有唯一索引也不提供跨页快照一致性；TEXT/JSON 以字符裁剪，服务器仍可能为表达式扫描大字段；带表达式或前缀索引不会误标为稳定键；空间列等特殊类型需要后续兼容性验证。这些是可描述的限制，不应标为性能问题已全部解决。

### MyGo Native UI

`internal/ui/shell/mysql_data_view.go`、`mysql_data_state.go`、`mysql_data_interactions.go` 和 `mysql_data_inspector.go`：

- 使用 **MyGo 原生 Table 的列头拖动、宽度调整、列重排与排序状态**，在翻页时保留 `TableLayout.Widths`，不会为了刷新将表格状态丢弃。
- 页面提供字段选择、包含/精确模式、筛选值和应用/清除按钮；应用后回到第一页发起服务器查询。
- 原生 Table 可排序列头触发远程排序并保留当前过滤条件；不将当前页本地排序冒充全表排序。
- 单元格行列选择、右键菜单复制与顶部“复制预览”，调用 MyGo `WriteClipboard`；NULL 显示为 SQL NULL，截断预览在复制文本中注明“不是完整字段内容”。不发起隐式完整大字段读取。
- 右侧信息栏改为跟随活跃 MySQL 数据 Tab 与选中单元格：字段名、原始数据类型、页号、行号、NULL/截断状态及当前值预览。关闭数据 Tab 后返回既有档案信息栏。
- 异步请求仍通过会话 generation、请求序号与分页 offset 过滤迟到结果。数据服务、UI 与表结构模块未合并成新的架构。

## 隔离测试与性能数据

在 `internal/services/connections/testdata/mysql_data_performance_fixture.sql` 创建**独立** `nyadb_perf` schema（不污染原有 `nyadb_struct_a` 结构验证），包含：10,004 条无主键行、只有完整非空唯一索引的表、以及包含 1 MiB LONGBLOB + 约 1.2 MiB LONGTEXT 的表。

在本地一次性 MySQL 8.4 容器内，测试 `TestMySQLAdvancedRealPerformanceFixture` 的实际结果：
- 无主键表 OFFSET=9000、LIMIT=100：读取 100 行，用时约 **15.44 ms**，且 `StableOrder=false`；
- 含 1 MiB BLOB 的受限读取连续执行 15 次：总计约 **101.63 ms**；长字段可见预览均不超过 2048 字节；
- 完整唯一索引表：在不含 PRIMARY KEY 的情况下按完整唯一索引正确稳定排序；
- 二进制 NULL、空字符串、中文、包含/精确筛选与字段名注入防护测试均通过。

以上时间受本机缓存、容器配置和数据量影响，是一次实验观测值，**不是 SLA、正式基准数据库性能或 10 万/100 万行规模验证**。生产数据未参与。

## 验证等级

| 内容 | 结果 | 等级 |
|---|---|---|
| SQL 生成、注入防护、NULL/空值、大文本受限传输 | 驱动替身与真实 MySQL 通过 | I |
| 10k 无主键 OFFSET + 1 MiB BLOB 受限分页 | 真实隔离 MySQL 8.4 通过 | I |
| UI 应用/清除筛选、排序请求、复制预览、列宽跨页保留 | `ui.NewTester` 通过 | I |
| 真 MySQL → `ui.NewTester` 数据页的精确筛选、排序、复制 | 通过；实测 `parent_code=P2` 返回 id=2 | I |
| 1280×800 新数据页视觉截图 | `temp/research/mysql-data-enhance-20261022/native-filter-sort-1280.png` | I（MyGo Tester） |
| 最新本机 Linux 原生 NyaDB 可执行程序正常启动 | Xvfb :159 上确认 | I（窗口启动） |
| 最新版 Linux 桌面连接及筛选栏全程鼠标操作 | 自动化 GUI 输入被工具安全检查阻止，未继续绕过 | Unknown |
| Navicat 原版对应筛选、排序、复制逐项操作 | 本轮未追加原版观察 | Unknown，非 V |

全量门禁（显式 opt-in 的真实隔离 MySQL 8.4）：
`go test ./...`、`go vet ./...`、`go tool mygo vet`、`go tool mygo build`。
Race Detector：`go test -race` 使用真实 MySQL 服务及数据页关键交互测试运行。

## 下一阶段

1. 通过独立 100k/1M 行夹具观察超深 OFFSET 的实际成本，再对有完整唯一键的表研究 keyset/cursor 分页；不能在没有可靠排序键时承诺可重复。
2. 将长字段读取策略发展为可检查字段总大小、必要时手动分块的安全预览，尤其是多字节字符与 JSON/空间类型。
3. 在合法授权的原版 Navicat GUI 中研究原生数据页的筛选/排序栏、列宽、右侧属性、单元格复制行为后再做同尺寸差分 V。此阶段只读，不提前加入保存或 SQL DML。
