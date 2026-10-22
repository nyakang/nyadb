# NyaDB MySQL Keyset / 大字段按需读取与 Navicat 数据页差分（2026-10-22）

## 范围和证据

- 基线：`1e70652`；本轮实现提交：`211dfdd`；Go 1.27+、MyGo Native UI 0.6.0。
- **O** Navicat 18 授权程序在已有隔离 Xvfb :121 中直接观察；**I** NyaDB 服务、MyGo 原生测试器实际实现与测试；**V** 相同窗口及内容的限定差分；**Unknown** 尚未验证。
- 两侧参考画布为 1280×800 的窗口/测试器画面。原版是独立的真实 GUI 窗口，NyaDB 是具有真实 MySQL 连接数据的 `ui.NewTester` 画面；两者的标题栏/窗口装饰不完全相同，**没有做全像素 V**。
- 使用仅绑定 `127.0.0.1:34306` 的一次性 MySQL 8.4 测试容器和只读帐号；研究专用临时免密 GUI 账户仅对隔离测试库授予 SELECT/SHOW VIEW，容器销毁后失效，禁止用于真实服务。没有运行 DDL/DML 修改生产数据，也没有修改 Navicat 原版文件。
- Navicat 从之前的研究 HOME 原样使用；研究截图在 Git 忽略目录中，没有提交专有 UI 资源。

## Keyset / 游标分页（I）

`internal/services/connections/mysql_data_keyset.go`、`mysql_data_query.go`：

- 查询 `information_schema.STATISTICS` 和 `COLUMNS`，识别**完整长度且每个组成列均非空**的 UNIQUE 或 PRIMARY 索引；拒绝前缀、表达式及允许 NULL 的唯一索引用作行游标。
- 对可证明稳定的排序，SQL 根据完整唯一键组成字典序 seek predicate；自定义主排序支持升序/降序，唯一键作为平局处理；键值保持原始受限数据、全部通过 SQL 参数绑定，标识符单独转义。
- 服务返回 `NextCursor` 与每行的受验证 `RowRefs`，实际下一页使用 `WHERE ...` 加 `LIMIT`，**不做深度 OFFSET 跳过**；上一页重用 UI 记录的前一页边界。对游标跨表、跨过滤/排序重用以及没有上一页锚点的非法 seek 请求返回明确错误。
- 没有可靠唯一键，或用户排序包含可空字段时，不伪造游标；保留既有的 OFFSET 机制并明确向用户警告可能重复或遗漏。分页前后发生其他连接写操作时，即使 Keyset 也不保证跨页事务快照；不能保证原始行永远不变化。
- Keyset 查询会额外取回组成游标的原始键列，但键长度有上限；无法安全生成键时不返回可继续使用的 NextCursor。
- 每次切换筛选/排序、重新刷新时从第一页重新建立游标历史，避免复用过期锚点；切断会话/关闭 Tab 时清理异步请求及其状态。

### 真实 MySQL 8.4 证据

`TestMySQLKeysetIntegration84`：在真实 child 测试表中，将 `price` 设为降序，结合复合唯一键逐页加载 3 行：没有重复、没有丢行，最后一页正确报告结束。篡改 cursor.Table 后返回 `ErrMySQLDataPage`。在 `nyadb_perf.no_key_entries`（没有唯一键）上正确回退 OFFSET。

`TestMySQLRealKeysetAndOnDemandFieldNativeViewer`：真实 MySQL → MyGo 原生数据 Tab：blob_entries 以每页 1 行翻到第二行再回到第一行，UI 的键值历史得到验证。

## 大字段按需读取（I）

`internal/services/connections/mysql_data_field_chunk.go`：

- 分页阶段仍保留之前的**服务端受限字段预览**，不传输完整 LONGBLOB/LONGTEXT。
- 用户明确点击右侧字段信息栏的“读取字段片段”后，才对当前选中的字段发起真实查询；仅适用于有经过服务端唯一键验证的行标识。
- 每次服务调用最多获取 4096 个单位（**文本按字符；二进制按字节**）；当前 UI 每次请求 1024 个单位。不在内存里自动拼接无限长度的数据；Inspector 仅保留最新片段、读取位置和总长度。
- 每次读取都验证 schema、表名、列名和完整非空唯一索引元数据；行标识值通过绑定参数查询，并用 `SUBSTRING` 在服务器端取回片段。返回值含 NULL、总单位数、下一偏移及是否还有更多片段。
- 会话取消、断线和 20s 超时生效；切换行、字段、分页或关闭 Tab 均失效旧请求。**不保证跨次读取的数据库快照一致性**，其他连接在期间修改字段，片段可能来自不同内容版本。

### 真实 MySQL 8.4 证据

`TestMySQLFieldChunkIntegration84`：1 MiB 二进制字段首次请求 64 字节正确得到 128 个十六进制字符；中文长文本按 64 个字符连续请求，偏移 64→128、总长 400000 个字符；篡改 RowRef.Table 返回错误。

`TestMySQLRealKeysetAndOnDemandFieldNativeViewer`：在原生 MyGo 测试器中，点击两次“读取所选字段下一段”，获得前后 1024 字节片段；显示偏移 2048 / 1048576。未自动拉取剩余约 1 MiB。

## Navicat 18 原版运行观察（O）与同尺寸差分

研究截图（均仅留在 Git 忽略目录 `temp/research/mysql-keyset-diff-20261022/`）：

- `navicat-selected-data-1280.png`：原版表数据视图，展示独立工具栏、表头字段名及下方类型标注，右侧为字段属性。
- `navicat-filter-toggle-root.png`：点击“筛选 & 排序”后，数据表上方展开**筛选规则区 + 排序规则区 + 应用按钮**，而不是始终显示单行筛选控件。
- `navicat-filter-rule-root.png`：新增筛选规则后出现选择字段、关系/运算符和输入值及新增/删除规则等交互。
- `navicat-column-context-root.png`：列头右键菜单包含复制字段名称、复制列、显示/隐藏列、列宽调整、字段类型与注释显示开关、缩放等选项；部分操作因只读账户或当前表状态禁用。
- `navicat-designer-1280.png`：在同一画布下的独立表设计工作区。

NyaDB 对照画面：
- `temp/research/mysql-keyset-diff-20261022/nyadb-keyset-inspector-1280.png`：连接真实 MySQL `nyadb_perf.blob_entries`，Keyset 数据页 + 右侧字段属性及分段读取状态。

| 观察点 | 原版 O | NyaDB I | 差分结论 |
|---|---|---|---|
| 1280 宽主内容三栏 | 左栏约 251px、右信息区约 205px | 数据工作区独立设置 19.6% / 80.1%，左右宽度接近原版 | 有尺寸证据的局部对齐；非完整 V |
| 数据页筛选入口 | 工具栏按钮展开上下规则编辑区，支持增删规则 | 单行筛选栏，支持单列包含/等于和显式应用 | **未对齐**，规则编辑器尚待实现 |
| 表头 | 列名与类型上下呈现 | 原生 MyGo Table，列名优先，数据类型在 Inspector 中 | **未对齐** |
| 列头右键 | 多项复制、隐藏、宽度、类型/注释菜单 | 增加原生“复制字段名称”和“重置列宽”；拖动调宽已有 MyGo 支持 | 部分功能对齐，**非 V** |
| 右侧字段信息 | 类型、非空、默认值、注释等元数据 | 字段名、数据类型、NULL/截断状态、有限预览、显式大字段片段 | 核心信息已存在，属性布局及内容**未对齐** |
| 原版数据内容 | 研究窗口曾有历史连接失败后的 N/A 画面；未把它当真实行数据证据 | NyaDB 真实 MySQL 数据页与受限字段预览均可检验 | 内容 V 尚未完成 |

## 工程验证

在隔离 MySQL 8.4 明确 opt-in 条件下：
- `go test ./...`：PASS；
- `go vet ./...`：PASS；
- `go tool mygo vet`：PASS；
- `go tool mygo build`：PASS，Linux/amd64 MyGo v0.6.0；
- `go test -race`（Keyset、字段片段与真实 MyGo 数据页专项）：PASS。
- `ui.NewTester` 在 1280×800 分辨率模拟 native 行/列交互、分页和按需读取：PASS。
- 最新原生 NyaDB 可执行程序的**真实 GUI 完整人工操作**本轮未执行；不能把 `ui.NewTester` 冒称为桌面 V。

## 后续

1. 用 Navicat 运行观察补齐规则编辑器的运算符、复杂组合、禁用状态及实际应用结果，再使用 MyGo 原生组件逐项还原。
2. 数据表表头增加双行字段名/类型与字段属性，细化列头菜单的显示/隐藏列、宽度拟合等行为。
3. 在 >=100k/1M 行、不同 MySQL 版本、可空排序列、并发 DML 变动下专项测量 Keyset、行标识和大字段分段一致性；不足以验证时明确 Unknown。
4. 后续仍保持只读，不能以预览复制来隐式读取整个 BLOB 或执行 SQL 更新。
