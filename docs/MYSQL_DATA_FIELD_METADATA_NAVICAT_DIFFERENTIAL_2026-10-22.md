# MySQL 数据查看器第三阶段：字段元数据、双行表头与 Navicat 18 差分（2026-10-22）

## 范围、证据等级和安全边界

本期从 `abecf6b` 出发，复用 MySQL 会话、数据页及 MyGo v0.6.0 原生 `Table`。不新增数据编辑、DDL/DML、额外 BLOB 全量读取或独立假表头。原版 Navicat 仅引用**已有的、合法授权运行观察截图**，本期未修改或再次启动原版，也未复制原版图标。

- **O**：Navicat 已有原版运行观察，限下面列出的实际截图内容。
- **S**：静态证据；本期没有新增 Navicat 二进制或静态分析。
- **I**：NyaDB 独立实现并经过注明的测试环境验证。
- **V**：同一状态、相同数据与尺寸下进行原版/新实现差分且达标；本期**尚无完整 V**。
- **Unknown**：尚无充分观测或所需执行环境不具备。

## 原版 O 观察与 1280 × 800 对照

参考截图均来自此前已授权的 Navicat 18 运行记录：`temp/research/mysql-keyset-diff-20261022/navicat-selected-data-1280.png`、`navicat-column-context-root.png`。其原始截图尺寸为 **1600 × 960**，Navicat 窗体占中部区域；本轮把第一张裁切为 `navicat-fieldmeta-crop-1280x800.png`（裁切矩形 x=160..1440、y=80..880），仅用于同画布布局比较，不改动原版文件。NyaDB 为 `nyadb-fieldmeta-1280-20261022.png`，由真实 MySQL 8.4.11 数据服务 + `ui.NewTester` 于 **1280 × 800** 输出。**两者是相同画布，不意味着真实桌面 DPI、字体渲染或数据行完全一致。**

既有原版截图可直接确认（O）：字段名在上、蓝色类别图标及基础/带参数类型在下；右侧字段信息栏对 `id` 使用金色键形标记、基本类型/NOT NULL/默认值/注释的纵向排列；右键菜单包含复制、显示/隐藏列、适应列宽及显示字段类型等功能。图中 Navicat 数据行处于 `(N/A)` / 无记录状态，而 NyaDB 测试容器读取了三条真实行。因此**数据区不能作为相同数据库快照的像素差分**。ENUM、JSON 及更长的 `COLUMN_TYPE` 原版表现仍为 Unknown。

局部人工复核尺寸（像素，基于上述截图的可见边线，约数而非设备无关尺寸）：

| 区域 | Navicat O（裁切图） | NyaDB I（测试器图） | 差异 |
|---|---:|---:|---|
| 左侧连接/对象栏宽度 | 约 251 | 251 | 接近 |
| 右侧字段信息栏左边界 | x≈1076 | x≈1075 | 接近 |
| 字段双行表头顶端 | y≈157 | y≈349 | NyaDB 下移约 192 |
| 字段双行表头高度 | 约 46 | 约 45（由约 42 调整） | 已在 MyGo 原生表头内做最小高度校准 |
| 筛选与排序交互区 | 原版紧凑工具栏 | NyaDB 默认展开多条件编辑区（约 160px） | **最主要的垂直布局差异** |

NyaDB 在真实截图中可辨认 `bigint unsigned`、`varchar(12)`、`decimal(10,2)`，而不是原先只有 `bigint`、`varchar`、`decimal`。字段信息栏按需显示更多服务器属性，内容比原版已有截图更长；尚未完成严格的行距、颜色、字形像素 V 验证。

原版菜单 O 截图 `navicat-column-context-root.png` 已核对；MyGo `ui.NewTester` 能执行右键菜单交互断言，但其本轮菜单图像输出**未画出弹出层**，故不把无弹层的截图作为 NyaDB 右键菜单视觉 V 证据。旧截图、生成的裁切图和 NyaDB 最新截图均在 Git 忽略的 `temp/research/` 中，不作为产品资源。

## 实现（I）

1. **统一字段元数据服务**（`844ca0d`）：将现有结构查看器的 `MySQLStructureColumn` 和 `information_schema.COLUMNS` 查询共用给数据页，返回 `ColumnMetadata`，包含完整类型、基础类型、字段序号、是否可空、默认值、键、其他属性、注释、字符长度、数值精度/小数位、时间精度、字符集和排序规则。字段名和目标库表均经过安全处理。原有请求 Context、会话失效和 128 列限制仍有效；不查询整列 BLOB。
2. **原生双行表头**（`af65bbb`）：继续使用单一 MyGo `TableColumn.Title/Subtitle` 和 `ListState.Columns`，根据真实 `COLUMN_TYPE` 显示副标题，添加自主文本类型字形 `SubtitleIcon` 和简短 `PK/UQ/IDX` 标记。普通 `MUL` 不推断为外键。字段类型可隐藏，保留列宽调整、横向滚动、拖动重排、排序和原生右键菜单；最小化 MyGo 本地补丁并登记 `PATCHES.md`。
3. **字段信息面板**（`c2c260f`）：无行选中也可显示首个可见字段属性；选中其他单元格按字段 ID 变更；对无元数据、载入失败、隐藏字段、断线和标签页生命周期分别展示对应状态。显示 SQL `NULL` / 未设置和真实字符串默认值 `"NULL"`、`""` 的区别；其余键、字符集、排序规则、长度精度和 `EXTRA` 也取真实元数据。值预览和按需分块读取仍沿原只读接口。

数据页的异步结果继续通过已有请求标识 / 会话 generation 检查后才落地；刷新重新请求元数据，关闭/断线释放数据页。未引入跨页快照承诺：只有具备完整可靠唯一键时才使用 Keyset，其余 OFFSET 并保留风险提示。

## 测试矩阵

| 检查 | 结果 | 分类 |
|---|---|---|
| `go test ./...` | PASS | I：单元/无窗口 |
| `go vet ./...` | PASS | I：静态检查 |
| `go tool mygo vet` | PASS | I：MyGo 工具检查 |
| `go tool mygo build` | PASS，Linux/amd64 原生构建 | I：编译，不等于桌面交互 |
| `go test -race ./internal/services/connections ./internal/ui/shell` | PASS | I：并发/生命周期回归 |
| 服务层 `TestMySQLTableDataIntegration84`、`TestMySQLStructureIntegrationMySQL84`（隔离、opt-in、`-race`） | PASS | I：真实 MySQL 8.4.11 |
| `TestMySQLRealDataNativeViewer`、`TestMySQLStructureRealDatabaseNativeViewer`（opt-in、`-race`） | PASS | I：真实 MySQL + MyGo `ui.NewTester`，不是桌面窗口 |
| 独立性能夹具的 `TestMySQLFieldChunkIntegration84`、`TestMySQLKeysetIntegration84` | 首次 FAIL：本次新测试容器缺少 `nyadb_perf`；补导入操作受工具安全检查阻止，未复测 PASS | **Unknown：真实 DB 扩展复测** |
| `ui.NewTester` 原生表头尺寸/列宽/滚动/拖动/菜单与新 inspector 测试 | PASS | I：无窗口原生 |
| 本轮 Linux 真实桌面窗口/原版交互再运行/相同快照像素 V | 未执行 | Unknown / 非 V |

本轮创建**专用一次性** `mysql:8.4.11` 容器 `nyadb-mysql-fieldmeta-it-20261022`；映射仅 `127.0.0.1:34306`，凭据从 `temp/research/mysql-structure-20261022/reader-password`（0600，Git 忽略）读取，专用用户 `nyadb_it_reader` 只授予 fixture 两个 schema 的 `SELECT, SHOW VIEW`。导入了现成结构/数据测试夹具，没有连接其他项目容器或生产服务器。为复测性能表而执行的额外导入命令被安全检查拦截，未继续规避限制。**测试完成后已停止并自动删除该专用容器。**

## 剩余差异与下一阶段

本轮仅完成可证实的 **I + O 布局对照**，不声称 V。最大可测差异为默认展开的筛选编辑器导致网格表头垂直位置偏下，且主工作台上方工具栏、局部字体、原生菜单弹层和字段属性排版仍与 Navicat 有别。下一轮优先：让筛选/排序编辑器默认紧凑、展开后保持全部编辑能力；补 MySQL ENUM/JSON/长类型的隔离真实数据样例；在合法授权原版与 NyaDB 均能交互的同尺寸桌面上采集相同数据状态和菜单弹层，建立可复算的 ROI/尺寸差分。不可为了视觉一致性重写 MyGo 表格或牺牲 Keyset、分页、BLOB 安全边界。
