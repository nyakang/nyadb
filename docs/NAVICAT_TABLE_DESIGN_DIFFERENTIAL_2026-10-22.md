# Navicat 18 MySQL 表设计：运行观察与 NyaDB 真实桌面差分（2026-10-22）

## 实验边界与判定

- 仓库：`kang/nyadb`，基线 `46d4e29`，MyGo v0.6.0，Go 1.27+。
- 仅使用本机已存在的 `mysql:8.4` 镜像启动 **具名、一次性** 测试容器 `nyadb-mysql-structure-it-20261022`，只发布到 `127.0.0.1:34306`；由 Navicat 原版测试连接页面实测服务端版本 **8.4.11**。
- 测试数据库：`nyadb_struct_a`、`nyadb_struct_b`；使用仓库 `internal/services/connections/testdata/mysql_structure_fixture.sql`。两款 GUI 均使用专用 `nyadb_navicat_observer` 测试账户，该账户仅有两库的 `SELECT, SHOW VIEW`。为避免在 UI 自动化中处理明文口令，研究容器内该**临时本机账户**不设置密码；此设置**严禁**用于生产或网络可访问服务器。只在隔离回环端口有效，停用测试容器即失效。NyaDB 的其他集成测试仍使用独立的有密码只读账户。
- Navicat 18 的研究环境是此前独立 Xvfb `:121`（1600×960 画布）、原始 Navicat AppImage 的授权 GUI 和独立研究 HOME；本轮没有修改其程序、授权或专有资源。
- NyaDB 是本轮实际编译的 `build/linux-amd64/nyadb`，在新的隔离 Xvfb `:159`（1280×800 画布）中运行，使用单独的 HOME / XDG_CONFIG_HOME。**两边都完成了真实服务端连接及真实 GUI 页面操作。**
- **O** 原版直接运行观察；**I** NyaDB 独立实现并测试；**V（限定范围）** 两边在相同隔离数据上直接核验了结构内容与部分操作结果；**Unknown** 无证据。**本轮没有像素级 V**，也不将 NyaDB 的只读实现等同于 Navicat 的可编辑设计器。

## 原版观察（O）

### 连接和对象入口

1. 原版“新建连接 (MySQL)”设置 `127.0.0.1:34306`，测试连接显示绿色状态 **连接成功，服务器版本 8.4.11**。
2. 保存研究用连接，在左侧双击展开，可见 `nyadb_struct_a`、`nyadb_struct_b` 两个隔离 schema 及获准访问的系统 schema。
3. 双击 `nyadb_struct_a` 后，对象列表列出 `child`、`parent`。**双击 child 在工作区新增的是表数据页，不是表结构页**。即使数据表无行，也有字段列、右侧字段详情与底部数据查询状态。
4. 在对象列表选中 child，点击上方 **设计表** 或右键菜单的 **设计表（Ctrl+D）**，可打开独立的表设计工作区 Tab。右键菜单“打开表（Enter）”与“设计表（Ctrl+D）”明确分开。此次使用只读账户，没有修改字段、执行 DDL 或保存设计。

### 设计工作区及子页

原版打开 child 数据 Tab 和 child 设计 Tab 后，又可以打开 parent 设计 Tab，三者同时保留；设计 Tab 选择 **外键** 后切换到 parent 再切回来，原版仍停留在 **外键**，即子页选择按表独立持久。

原版设计页有工具栏与以下子页：**字段、索引、外键、检查、触发器、选项、注释、SQL 预览**。当前 NyaDB 阶段只复刻其中的已取证、已提供真实元数据的字段/索引/外键，不复制原版的保存、添加、删除、修改字段等可编辑操作。

| 实测子页 | Navicat 原版 O | NyaDB I 和定向 V |
|---|---|---|
| 字段 | 8 行，按顺序列出 id、tenant_id、parent_code、price、nullable_note、blank_note、created_at、remark；列头主要为“名称、类型、长度、小数点、不是 null、虚拟”；id 选中时，下方单独显示默认值、自增、无符号、填充零等字段属性 | 真实 GUI 同样显示 8 行、类型、长度和小数位等；本轮重新组织只读字段表及下方属性区；行数/名称/类型/精度/非空属性在本夹具下对照一致（限定 V） |
| 索引 | **2 行**：uk_tenant_code（UNIQUE/BTREE）、ix_note_prefix（NORMAL/BTREE）；一条索引为一行，主键不作为单独次级索引行 | 本轮保留服务层 PRIMARY 元数据，只在 UI 排除它；两条次级索引作为两行显示，复合字段用逗号组合。索引类型、方法与数量对照一致（限定 V） |
| 外键 | **1 行**：fk_child_parent；显示完整的源字段组合、目标 schema、目标表、目标字段组合，以及删除行为（RESTRICT）；单约束不拆成多行 | 本轮 UI 一条外键一行；源列/目标列/目标库表匹配。更新 CASCADE、删除 RESTRICT 来自真实服务端。对象数量和关系映射对照一致（限定 V） |

### 界面结构与尺寸（观察值）

原版采集画布 1600×960，观察时 Navicat 主窗口约位于画布 **x267–1333、y161–823**，总宽约 1066px，高约 662px。工作区三栏大致为左对象树约 254px、中央表设计约 608px、右信息栏约 204px。表设计页自上而下为工作区 Tab（约 27px）、命令工具栏（约 35px）、子页标签（约 26px）、属性表（约 25–27px 行高）、字段页下方属性编辑区域（约 190px）；索引和外键页没有显示同样密度的字段属性编辑区。

NyaDB 真桌面截图是在 **1280×800** 的不同窗口尺寸下采集：左栏约 282px、中央约 748px、右栏约 250px；表格行高约 24px，字段下方只读属性区约 141px。该差异**没有归一化为同画布同尺寸比对**，不能宣称精准像素、列宽和位置一致。

## NyaDB 独立实施（I）

新增/修改：

- `internal/ui/shell/mysql_structure_presentation.go`：索引过滤与汇总、复合索引前缀长度、复合外键字段映射、字段类型长度/小数位与默认值格式化；
- `internal/ui/shell/mysql_structure_view.go`：字段表列头、下方选中字段的只读属性区、索引/外键各自单行展示及状态计数，全部使用原生 MyGo Table/Tabs 和项目统一主题；
- `internal/ui/shell/mysql_object_view.go`：显式 **设计表（只读）** 入口以及对象列表右键菜单。由于真实表数据查看器**尚未实现**，原有双击表打开只读结构的临时行为仍存在：这是有意保留的**未完成差异**，绝不使用静态模拟表行冒充真实数据；
- `internal/ui/shell/model.go`：记录每个结构 Tab 的字段选择；
- `internal/ui/shell/mysql_structure_presentation_test.go` 和已有状态测试：覆盖索引/外键聚合及 NULL、空字符串、数值长度和精度格式，确保主键仍保留在服务元数据而不重复于次级索引表。

本轮又根据截图修复默认值语义：MySQL `COLUMN_DEFAULT IS NULL` 无法分辨“无默认值”和“DEFAULT NULL”，因此对**非空、服务端无默认值**的字段属性不再错误显示文字 `NULL`；对于允许 NULL 的字段继续显示 `NULL`。这只是展示策略，服务原始元数据保持不变。

## 真实 Linux GUI 验证（I）及对照范围

完整 NyaDB 物理程序流程：新建 MySQL 研究档案 → 连接回环的 MySQL 8.4.11 → 展开/选择 `nyadb_struct_a` → 列出 child/parent/parent_view → 选择 child → **设计表（只读）** → 依次查看字段/索引/外键。全部在 **Linux Xvfb 真实进程**中执行，不是 `ui.NewTester` 的模拟画面。

图片仍在 **Git 忽略的研究路径** `temp/research/mysql-structure-diff-20261022/`，不提交 Navicat 原版素材：

| 原版 O 证据文件 | NyaDB I 证据文件 |
|---|---|
| `navicat-no-password-connect-result.png`（连接成功） | `nyadb-gui-connected-live.png` |
| `navicat-expanded-schema.png`（真实对象列表） | `nyadb-gui-objects-live.png` |
| `navicat-child-open-default.png`、`navicat-child-data-tab.png`（双击默认数据 Tab） | 未实现真实表数据页，**未对齐** |
| `navicat-child-design-fields-clean.png` | `nyadb-gui-real-design-fields.png` |
| `navicat-child-design-indexes.png` | `nyadb-gui-real-design-indexes.png` |
| `navicat-child-design-foreignkeys.png` | `nyadb-gui-real-design-foreignkeys.png` |
| `navicat-multitable-tabs-clean.png`、`navicat-child-design-tab-restore.png` | 多表 Tab 与页面恢复已在 `ui.NewTester` + 真实 DB 测试中验证；本次 Linux 真桌面仅打开了 child |
| `navicat-table-context-menu.png`（打开表 Enter / 设计表 Ctrl+D） | 显式入口与右键操作已实现，但桌面右键菜单的实际点击尚未单独截图验证 |

### 测试门禁

在代码 UI 结构调整后执行：

- `go test ./...`：PASS（含显式 opt-in 的 MySQL 8.4 集成与真实 metadata→MyGo 测试）。
- `go vet ./...`：PASS。
- `go tool mygo vet`：PASS。
- `go tool mygo build`：PASS，MyGo 0.6.0 Linux/amd64。
- `go test -race ./internal/services/connections ./internal/ui/shell` 结合真实 MySQL 集成用例：PASS。
- `ui.NewTester` 结构交互与展示用例：PASS。
- 原版 O 与真实 Linux NyaDB 实际显示：字段 8 行、次级索引 2 行、外键 1 行、字段和关系内容符合相同夹具，**内容与部分交互限定 V**。

新增非空无默认值显示修复后已复测目标单元/集成门禁；**真桌面截图在该微调之前采集**，不要把截图里的 `id: NULL` 误当成最终的修复后画面。

## 未完成差异及优先顺序

1. **高优先级**：原版双击表打开真实数据页，而 NyaDB 暂时打开结构页；等只读表数据分页查看器准备好后调整默认双击行为，不允许静态模拟数据代替。
2. **中优先级**：两款应用在**同一窗口、同一分辨率**下逐像素对齐表设计的行高、列宽、滚动条、下方属性区的大小与状态；目前只有粗略尺寸和内容/行数核验，不是像素级 V。
3. **中优先级**：NyaDB 只读模式仍缺少原版的检查、触发器、选项、注释、SQL 预览等子页；新增前应先建立真实元数据/行为规格，不应返回假的空结果。
4. **中优先级**：原版次级索引顺序（该夹具中唯一索引在前）与目前 `information_schema.STATISTICS ORDER BY INDEX_NAME` 的展示顺序可能不同；暂不臆测跨版本稳定规则。
5. **低优先级**：快捷键 Ctrl+D、正式的“打开表”入口、设计 Tab 标题、禁用/失败/重连场景原版行为，以及更多数据库版本的差分。

**证据分级结论**：本轮取得了缺失的原版设计页 O 证据、真实 Linux GUI NyaDB I 证据，并对**相同隔离夹具的字段、次级索引、外键内容与数量**作了限定 V；**没有达成整体像素级 V、完整菜单快捷键 V 或整个表设计编辑器 V**。
