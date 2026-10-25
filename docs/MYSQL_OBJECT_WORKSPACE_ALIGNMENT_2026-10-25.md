# MySQL 连接后对象工作区：元数据、菜单、筛选首轮对齐（2026-10-25）

## 范围与证据

基于干净的 `master` / `199887dd5bd25b11` 开始。仅调整已有只读 MySQL 对象工作区，不更改连接管理、表数据查询、DDL/DML 或 MyGo fork。本轮没有运行、启动或连接 Navicat，也没有访问生产数据库；未 commit/push。

- **O（历史原版运行观察）**：`docs/NAVICAT_TABLE_DESIGN_DIFFERENTIAL_2026-10-22.md` 第 14–19 行确认展开测试 schema 后会显示表列表、双击表打开数据页、Enter 打开表与 Ctrl+D 进入设计页。这些行为已有历史截图：`temp/research/mysql-structure-diff-20261022/navicat-expanded-schema.png` 和 `navicat-table-context-menu.png`。
- **I（本轮独立实现）**：扩展真实 `information_schema.TABLES` 读取和原生 MyGo Table 绑定，修正筛选身份以及菜单操作范围；添加数据/交互单元测试。
- **V（严格同条件视觉差分）**：**BLOCKED**。缺少原版与当前 NyaDB 在同一数据库服务、fixture、对象结果指纹、字体、实际缩放和选择/展开状态下的新截图。历史截图不能替代同条件原版读取。
- **Unknown**：原版数据库节点及分类节点的精确菜单条目、视图结构菜单、行数是否带近似符号、菜单项行高、字段文本基线、Win/Linux 原生字体和当前版本的像素差异。

## 开发前差异清单

| 区域 | 现有事实 | 首轮处理 |
| --- | --- | --- |
| 对象元数据 | 表头七列；服务端仅读取名称、类型、引擎、注释 | 读取可验证的 AUTO_INCREMENT、UPDATE_TIME、DATA_LENGTH、TABLE_ROWS |
| 缺失值 | 未读取的四列原本全部空白 | SQL NULL 保持空白；返回数值 0 保持 0；视图不合成表统计 |
| 行数准确性 | 原实现没有显示服务端行数 | 使用 `≈` 标记 `TABLE_ROWS` 的估算语义，不运行 COUNT(*) |
| 列表导航同步 | 工具栏筛选重置 index，但未同步导航所指对象 | 相同身份仍可见则保留；不可见则切换到分类/数据库节点 |
| 左树右键 | 数据库、分类和对象基本共享刷新项 | 分类增加“显示全部对象”，表和视图区分只读操作；刷新禁止重复进行中 |
| 1280px 工具栏 | 未实现的创建/删除/导入/导出按钮占用横向空间 | 保留打开/设计、全部/表/视图、刷新；未实现动作不展示为可用入口 |
| 离开数据库 | 回到连接时可能留下还在执行的对象请求 | 取消对象任务并隔离迟到结果，保留原有 generation/request 检查 |

## 服务端数据语义

查询仍在既有认证会话上执行，使用参数 `TABLE_SCHEMA = ?`，按表名排序，15 秒超时，至多读取 10,000 个对象，沿用 Disconnect/Close 取消机制。新增四列如下：

| 列 | 数据源 | 显示原则 |
| --- | --- | --- |
| 自动递增值 | `AUTO_INCREMENT` | 仅服务器非 NULL 时显示下一值 |
| 修改日期 | `CAST(UPDATE_TIME AS CHAR)` | 使用服务器字段，不使用本机时间；未知留空 |
| 数据长度 | `DATA_LENGTH` | 显示服务器返回的字节数；NULL 留空 |
| 行 | `TABLE_ROWS` | 服务器统计值，并标记 `≈`；不暗示为精确总行数 |

`TABLE_ROWS` 对 InnoDB 等引擎可能是估算值，`UPDATE_TIME` 在部分引擎或版本中为 NULL，`DATA_LENGTH` 不是无条件精确的实时磁盘占用。以上均是数据库元数据，**不是对行记录的读取**。视图的表专用统计不展示。

## 原生布局参数（代码值，非实测几何）

对象命令栏高度 `30` DIP；按钮高度 `25` DIP；列顺序与默认宽度（DIP）：

| 名称 | 自增 | 修改日期 | 数据长度 | 引擎 | 行 | 注释 |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 265 | 100 | 130 | 110 | 95 | 70 | 180 |

这些数字来自 Go/MyGo 源码，不能当作原版像素测量。用户改动的列宽仍经原生 `ListState.Columns` 恢复。若无任何对象仍保留同列头，不生成假数据行。

## 测试与平台验证边界

新增：
- `internal/ui/shell/mysql_object_metadata_alignment_test.go`：七列文本、NULL/0/近似值、过滤后选中身份、数据库菜单范围、1280×800 / 840×560 筛选操作。
- `internal/services/connections/mysql_metadata_test.go`：真实 SQL 列扫描的 mock driver、绑定 schema、表/视图差异和超限回退。

未新增连接外部数据库或重启 Navicat。本轮自动化运行使用 `ui.NewTester`，属于无真实窗口 UI 验证。对于真实 GUI、截图和差分，目前仅能引用 `docs/UI_ALIGNMENT_2026-10-25.md` 已记录的**历史** Linux 实拍（原版本轮修改前），不能将它们包装为本次修改后的实拍证据。

## 尚未完成的验收

1. 使用已授权且隔离的 MySQL fixture，对当前构建采集 1280×800 / 840×560 真窗口的对象、菜单、滚动与筛选截图，记录字体和实际缩放。
2. 在合法授权且获得明确运行许可时，让 Navicat 同一只读 fixture 读取成功，获得 schema、行结果指纹、选中对象及状态；此前有无法读取的观察记录（见 `docs/MYSQL_DATA_NATIVE_DARK_MENU_LAYOUT_DIFFERENTIAL_2026-10-22.md`），不得沿用旧页假设一致。
3. 用 `tools/strict_visual_differential.py` 的 `objects` 场景，提交两端成功读取、ROI、几何和纯色样本数据；逐控件误差符合门禁后才标记 V。
4. 下一轮按优先级处理：数据标签、双行表头与底部状态、右侧字段详情、筛选排序扩展与菜单、真实桌面和 Windows 对照。

严格分级：首轮源码功能与无窗口交互标记 I；对象页真实运行新截图为 **BLOCKED**；与 Navicat 当前版本的同条件像素差分为 **BLOCKED**。
