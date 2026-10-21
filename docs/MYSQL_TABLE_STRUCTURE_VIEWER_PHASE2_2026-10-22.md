# MySQL 表结构查看器第二阶段：行为规格、实现与验证（2026-10-22）

## 1. 目标与范围

本阶段在原有 `MySQLSessionManager`、MySQL TLS 安全直连、数据库对象浏览及 MyGo 工作台基础上，实现**只读**表/视图结构查看。功能路径：

1. 由已保存的 MySQL 活动配置明确发起连接；不在查看结构时自动连接。
2. 展开真实会话返回的数据库树，按需读取表、视图元数据。
3. 双击数据库树叶节点、通过右键“打开只读结构”，或在中心对象表选择一行按“打开结构”/双击以打开工作区 Tab。
4. 在“字段、索引、外键、表信息”之间切换，多个数据库/表可保留独立 Tab。
5. 刷新结构、重试失败、取消加载，或者返回对象页；断开连接、修改正在使用的活动档案或删除连接档案时失效结构页及缓存。

**实现策略（I，不声称 Navicat 源码或行为原理）：** Go + MyGo v0.6.0 原生 UI，不使用 WebView；结构服务只读 `information_schema`，不加载数据行，不执行 DDL、`USE`、保存或修改结构的操作。

## 2. 原版证据与行为规格

- **O（此前原版运行观察）**：`docs/NAVICAT_GUI_DIFFERENTIAL_2026-10-20.md` 记录了 Navicat 18 的主窗口及连接管理界面，研究环境是隔离 Linux Xvfb。该观察**没有进入实际表结构页面**。
- **S（现存资料）**：本阶段没有建立可以证明 Navicat 表结构布局、页签生命周期或查询语义的静态代码证据；不以行业习惯充当原版证据。
- **I（已独立实现并测试）**：见下表及第 3–5 节。
- **V（原版与本项目差分验证）**：**尚未完成**，不得当作原版交互或视觉等价。
- **Unknown（无证据）**：Navicat 18 双击树/右键菜单确切入口、结构页行高与各列列宽、字段属性呈现方式、页签关闭策略、权限失败文本、加载时动画，以及断开或切换活动库后的具体页面状态。需要通过有授权的隔离数据库驱动的原版实际运行观察进一步确认。

| 场景 | NyaDB 当前规格（I） | Navicat 18 证据 |
|---|---|---|
| 打开表/视图 | 对象表双击或“打开结构”；数据库树双击/右键 | Unknown |
| 工作区 | 复用 TabsBase；标题以 `database.table` 区分同名对象；对象 Tab 常驻 | Unknown |
| 字段列表 | MyGo 原生虚拟化 Table；字段、MySQL 完整类型、NULL、键、默认值、额外属性、注释 | Unknown |
| 索引 | 分索引及索引列展示唯一性、类型、列序、前缀及方向 | Unknown |
| 外键 | 分约束及列展示引用 schema/表/列与更新、删除动作 | Unknown |
| 表信息 | 对象类型、引擎、排序规则、注释、服务端估算行数 | Unknown |
| 刷新/取消/错误 | 同一 Tab 按需读取；主动刷新；取消和错误重试 | Unknown |
| 多表与失效 | 结果按会话代际和请求号核验；切换 Tab 不丢其他有效缓存 | Unknown |

具体的像素尺度、禁用状态、快捷键和排序能力不以本期实现推定为 Navicat 行为。

## 3. 服务设计与安全约束

服务层：`internal/services/connections/mysql_structure.go`

入口：`(*MySQLSessionManager).TableStructure(ctx, profileID, database, table) (*MySQLTableStructure, error)`

- `information_schema.TABLES`：对象类型、存储引擎、备注、排序规则、估算行数。
- `information_schema.COLUMNS`：按 `ORDINAL_POSITION` 读取名称、`COLUMN_TYPE`、`DATA_TYPE`、数值精度/标度与字符长度、`IS_NULLABLE`、`COLUMN_DEFAULT`、`COLUMN_KEY`、`EXTRA`、备注。使用 `sql.NullString` 表达 SQL NULL 默认值和空字符串的差别。
- `information_schema.STATISTICS`：按索引名和 `SEQ_IN_INDEX` 组装复合索引；保留 `NON_UNIQUE`、`INDEX_TYPE`、`SUB_PART` 和 `COLLATION`。
- `information_schema.KEY_COLUMN_USAGE` 与 `REFERENTIAL_CONSTRAINTS`：读取引用 schema、引用表、复合外键列配对，以及 `UPDATE_RULE`/`DELETE_RULE`。

**安全性：** SQL 文本是固定的，所有目标 schema 和 table 都通过两个 `?` 谓词绑定，不进行标识符拼接；名称校验拒绝空名、NUL、超限输入。操作重用管理器已建立的认证会话及其 TLS 设置，不打开额外网络连接。应用只读取服务器元数据，没有生产数据库端点。超时上限为 20 秒，每项结果最多 10,000 条，超限返回显式错误。调用者取消、断线及管理器关闭能够取消后续查询并关闭 `rows`。

**局限：** 四条读取语句不是同一快照，若其他客户端在读取期间变更 DDL，字段、索引和外键可能出现短暂不一致。当前未实现一致性快照和跨版本能力探测。服务器可能出于权限策略隐藏某些元数据，缺失结果不能被直接当成“该对象绝对不存在”；`TABLE_ROWS` 对 InnoDB 可能是估算值。

## 4. UI 状态、缓存与异步约束

- `internal/ui/shell/mysql_structure_state.go` 定义每个表工作区的独立读取状态、请求序号、错误、取消函数及缓存对象。
- `internal/ui/shell/mysql_structure_view.go` 为结构字段、索引、外键和表信息提供原生 MyGo 表格及子页。沿用 `theme.App/Header/Text/Muted/Blue/Gold` 和现有工作台布局。
- 结构异步加载采用缓冲的一次性结果通道；工作线程不写 UI State。每次 UI 帧消费时验证**会话 generation、profile ID、Tab 请求号**，忽略失效请求，即使底层模拟驱动不理会取消。
- 同一 `profileID + schema + table` 再次打开时聚焦已有 Tab，并复用成功加载结果。强制刷新可再次从会话读取；取消/关闭 Tab 会撤销其等待结果。
- 断线/重连/保存活动配置/删除活动连接都使用既有 `disconnectMySQLSession` 入口清理结构 Tab。成功刷新数据库列表且某 schema 已删除，则只清理该 schema 结构页，保留其他有效缓存。
- 本期对象页/结构页所有展示的真实模式数据均来自元数据服务；研究测试中的固定数据只注入测试替身，不作为正式 UI 的结果来源。
- 尚未支持多连接**同时在线**的独立结构工作区；已有会话管理由主窗口当前选择控制。

## 5. 验证记录

| 验证项 | 结果 | 性质 |
|---|---|---|
| 隔离 `database/sql/driver`：绑定参数、特殊名称与解析结果 | 通过 | I，服务模拟 |
| 隔离驱动：断开会话中止元数据查询、拒绝未连接/无效目标 | 通过 | I，服务模拟 |
| `ui.NewTester`：打开/多表/切换、字段/索引/外键/信息子页 | 通过 | I，无窗口 GUI |
| `ui.NewTester`：缓存、刷新、晚到结果、错误/重试/断线关闭 | 通过 | I，无窗口 GUI |
| `ui.NewTester`：不同库同名表、schema 被删除后的精确清理 | 通过 | I，无窗口 GUI |
| `go test ./...` | 通过 | 全量单元测试 |
| `go vet ./...` | 通过 | 静态检查 |
| `go tool mygo vet` | 通过 | MyGo 静态检查 |
| `go tool mygo build` | 通过 | Linux/amd64 原生可执行与包构建 |
| `go test -race ./internal/ui/shell ./internal/services/connections` | 通过 | 相关包数据竞争检查 |
| Linux Xvfb/Openbox 启动 NyaDB 构建 | 通过：程序保持运行，窗口出现，主工作台及空状态正常 | I，**真实 GUI 进程** |
| 已连接状态下真实 Linux GUI 表结构 | 未验证 | 无隔离 MySQL 服务端 |
| 真实 MySQL/MariaDB 服务器元数据正确性 | 未验证 | 不连接生产库，当前无隔离测试实例 |
| Navicat 原版结构页与 NyaDB 同环境截图差分 | 未验证 | 原版表结构行为 Unknown，非 V |
| Windows/macOS/Linux 物理桌面操作 | 未验证 | 未建立相应测试记录 |

截图证据放置在**默认忽略、不进入产品源码**的 `temp/research/mysql-structure-20261022/`：

- `mygo-headless-structure.png`：基于**模拟元数据源**的 MyGo `ui.NewTester` 结构 Tab，仅证明 UI 组件显示。
- `nyadb-desktop3.png`：本次 **Linux Xvfb 运行的真实 NyaDB 进程** 的未连接主工作台；不代表已连接的结构页。
- 前两次尝试的 `nyadb-desktop.png`/ `nyadb-desktop2.png` 首次采样存在黑屏/启动时序问题，不能当作 UI 通过证据；以第三次成功截图为准。

若需要重新生成测试器截图，在仓库根目录执行：

```bash
NYADB_MYSQL_STRUCTURE_CAPTURE=/data/kang/nyadb/temp/research/mysql-structure-20261022/mygo-headless-structure.png \
  go test ./internal/ui/shell -run TestMySQLStructureNativeTabsSwitchRefreshAndDisconnect -count=1
```

## 6. 变更提交

- `952327c`：真实 MySQL 表结构元数据服务与隔离驱动测试。
- `6ad13c2`：MyGo 原生结构工作区、工作台 Tabs、异步加载与交互测试。
- `ea7cfb0`：schema-qualified Tab、连接状态显示、额外状态测试及研究截图开关。

三个提交均独立推送到 `origin/master`，不包含 Navicat 文件或生产数据。

## 7. 剩余差异与下一阶段

1. 首先准备**明确授权、隔离的本地 MySQL 8 测试库**，包含复杂默认值、字符字段、复合索引、外键、多张同名 schema 表、视图和权限受限账户。验证端到端结构加载、TLS 会话取消、MySQL 错误传播和查询超时；再评估 MySQL 5.7/MariaDB 差异。
2. 在相同隔离测试库中通过有授权的 Navicat 18 运行观察真实结构页，记录操作步骤、屏幕尺寸、Tab 管理及错误和刷新行为，才可能给出 **O** 证据和 **V** 差分结论。
3. 优化结构表列的视觉密度/交互、单个 schema 内的元数据变化检测、较大对象的结果性能。仍应保持原生 MyGo 和只读操作边界。
4. 完成以上验证后，再推进 MySQL 表数据分页/虚拟滚动、SQL 执行和显式事务操作；勿将数据预览的模拟内容接入生产工作区。
