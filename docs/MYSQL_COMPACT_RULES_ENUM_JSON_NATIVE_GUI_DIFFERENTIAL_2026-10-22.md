# MySQL 数据查看器：紧凑筛选栏、ENUM/JSON 和 Linux 原生菜单差分（2026-10-22）

## 结论与证据定义

本轮从 `9705d55` 开始，在 `master` 实施并测试：紧凑筛选/排序栏、MySQL ENUM/JSON 字段真实元数据、Linux 桌面实际弹出的 MyGo 表头右键菜单。证据沿用 **O / S / I / V / Unknown**，其中：
- **O**：已有合法授权的 Navicat 18 原版运行观察截图，**没有在本轮修改或启动 Navicat**。
- **S**：原版静态证据；本轮未新增。
- **I**：NyaDB 独立实现与真实 MySQL 或真实原生 GUI 检验；分别注明运行方式。
- **V**：相同状态和尺寸下差分达标。本轮**几何项局部接近，整体像素及菜单外观未通过 V**；不因能操作就声称像素一致。
- **Unknown**：无法同时对齐的数据快照、平台绘制环境或原版行为。

## 实现

1. `4abe896`：新建 MySQL 数据 Tab 时筛选/排序栏默认收起，显示 `筛选 N · 排序 M`，保留“清除筛选”、展开入口和折叠状态的错误提示。展开恢复全部多条件 AND、排序优先级、输入值与应用按钮。每个 Tab 独立保留草稿与表格列宽；不触碰 Keyset/OFFSET、SQL 执行或 BLOB 服务。
2. `c984b2d`：`testdata/mysql_field_types_fixture.sql` 使用一次性 MySQL 8.4.11 测试容器创建 `nyadb_types.field_types`：`enum('active','disabled','待审核')`、JSON、SQL NULL、字符串 `"NULL"`、大型 Unicode JSON。入口仅允许 opt-in `NYADB_MYSQL_TYPES_IT=1` 和固定回环端口。真实服务测试覆盖类型声明、默认值、注释、受限预览、JSON 显式分块读取、中文 ENUM Keyset 下一页、ENUM 筛选和 JSON 不允许排序。真实服务返回数据送入原生 `ui.Table` 和字段栏 `ui.NewTester`，不是伪造服务响应。
3. Linux X11 / Openbox / Xvfb `:130`，独立 HOME 和 XDG_CONFIG_HOME，启动真实 `build/linux-amd64/nyadb`。创建仅存非秘密连接字段的临时配置，使用私有文件输入一次性测试密码；应用实际连接到 **127.0.0.1:34306** 的 disposable MySQL 8.4.11 只读用户，展开 `nyadb_types.field_types` 与 `nyadb_struct_a.child` 数据页，实际鼠标右键打开 GTK 原生列头菜单并截屏；点击“显示字段类型”后，第一行字段名保持而第二行消失，再展开完整筛选编辑器验证真实窗口行为。

## 真实 GUI 截图清单（I，忽略目录，非产品素材）

研究目录：`temp/research/mysql-visual-20261022/`。

| 路径 | 证据 |
|---|---|
| `native-data-1600x960.png` | 已授权隔离 MySQL ENUM/JSON 数据页，紧凑栏默认收起 |
| `native-menu-1600x960.png` | 在 `field_types` 页真实鼠标右键原生菜单 |
| `native-child-1600x960.png` | `nyadb_struct_a.child`，同名表参考场景 |
| `native-child-menu-1600x960.png` | `child.id` 真正的 GTK 菜单弹层 |
| `native-hide-types.png` | 菜单点击后真实双行表头切换为单行 |
| `native-expanded-1600x960.png` | 再展开多条件编辑区 |
| `navicat-aligned-1280x800.png` | 原版 O 截图的仅供研究裁切 |
| `nyadb-native-aligned-1280x800.png` | NyaDB 真实 Linux 桌面同尺寸裁切 |
| `navicat-menu-aligned-1280x800.png` / `nyadb-native-menu-aligned-1280x800.png` | 两套弹出菜单的同尺寸桌面裁切 |

原版两张研究截图引用 `temp/research/mysql-keyset-diff-20261022/navicat-selected-data-1280.png` 与 `navicat-column-context-root.png`，**均为此前 O 截图**。原始画布都是 **1600×960**，本轮所有对照裁切矩形严格统一为 **(160,80)-(1440,880)**，得到 **1280×800**。这校准了画布和窗口范围，不证明 Navicat 与 NyaDB 的主题/字体/DPI/行数据快照完全相同。

## 同尺寸量化差分（仅供定位缺口，不代表 V）

脚本 `temp/research/mysql-visual-20261022/analyze_no_numpy.py`（研究目录）使用 Pillow，对上述 `child` 页面和原版 `child` 的每个对应 RGB 像素计算绝对差，输出：

| 对齐区域 | RGB MAE（0..255） | 至少一通道绝对差 >32 的像素占比 |
|---|---:|---:|
| 完整 1280×800 | **14.27** | **10.49%** |
| 左侧对象栏 (0,140)-(251,740) | 15.71 | 13.86% |
| 工作区 (251,140)-(1075,740) | 6.70 | 3.51% |
| 字段信息栏 (1076,140)-(1280,740) | 8.43 | 5.73% |

*上述 MAE 不进行语义对齐、字体抗锯齿补偿、位移配准或差异忽略，不能作为同一数据快照的 UI 通过率。*

目测及横线坐标复核（近似像素值）：
- 左侧对象栏宽约 **251 px**，右侧字段栏起点 **x≈1075–1076**，均接近参考截图。
- Navicat 双行表头在对齐画布内约 **y≈157** 开始；本期 NyaDB 原生窗口约 **y≈239** 开始，较前轮默认展开编辑器造成的 **约 192 px** 偏移明显改善，但仍有 **约 82 px** 的顶端差距，主要来自工作区结构与工具栏。两者双行表头本体高度都约 **45 px**。
- Navicat 菜单背景样本中位数 **RGB(48,53,60)**；NyaDB 真实 Linux GTK 菜单为 **RGB(255,255,255)**。菜单标题、选项集合、内边距也不完全一致；目前 MyGo `Theme.SetSource(ThemeDark)` 不保证隔离 Linux GTK 本机菜单随应用主题变暗。
- 原版菜单包含更多原版功能（如显示注释、缩放及编辑相关项目）；NyaDB 保持**只读**，不能为截图一致性添加贴上/保存等 DML 操作。共同项如字段名复制、显隐列、调整宽度、类型显隐可在 NyaDB 真实菜单中找到。未通过菜单视觉 V。

## 验证矩阵

| 验证层次 | 结果 | 说明 |
|---|---|---|
| 服务层单元与内置回归 | PASS | 普通 Go 测试 |
| 紧凑/完整规则交互 `ui.NewTester` | PASS | 包括折叠恢复、多排序、列状态 |
| 独立真实 MySQL 8.4.11 ENUM/JSON 服务 `-race` | PASS | 回环隔离测试，无生产数据库 |
| 真实 MySQL 数据传入 `ui.NewTester` 原生表头/信息栏 `-race` | PASS | 无窗口渲染 |
| Linux 原生 GUI / GTK 菜单弹层和“显示字段类型”操作 | **PASS (I)** | Xvfb :130 实际窗口、真实鼠标与真实 MySQL |
| Navicat 菜单/工作区严格视觉 V | **FAIL / 尚未达标** | 浅色菜单、工作区顶部偏移、不同数据快照及功能集合 |
| 原版 ENUM/JSON 显示行为同场景观察 | Unknown | 本轮原版没重新运行，不能推断其完整声明和数据格式 |

## 工程门禁与边界

本轮执行 `go test ./...`、`go vet ./...`、`go tool mygo vet`、`go tool mygo build`，以及 `go test -race ./internal/services/connections ./internal/ui/shell`；具体 PASS 结果以执行日志为准。

- 密码不保存到产品配置，不写入命令行参数和 Git；只在 ignored 目录的权限为 0600 的临时文件中提供给 GUI 自动输入，并在完成时清理临时文件。独立数据库容器与独立桌面本轮结束时清理。
- Keyset 仍只针对完整、非空的可靠唯一索引。并发修改不会提供跨页事务快照一致性，无可靠唯一键仍退回 OFFSET 并告警。
- 大字段始终分页预览和显式分块读取，不新增 DDL/DML。
- 所有原版图片仅做内部研究比较，不加入产品资源或公开上传为仿制资产。

## 下一步（差分未通过项）

1. 进一步研究 MyGo v0.6.0 的原生 GTK 菜单与系统暗色偏好传递；优先通过**受支持的 GTK 原生主题机制**统一颜色，勿仿造第二套悬浮菜单。
2. 对布局的多余 **约 82 px** 垂直空间做最小改动，保留筛选条件编辑、顶部表操作以及 Tab 生命周期；加入 1280×800 视觉 ROI 回归。
3. 在同一授权 MySQL fixture 上再次观察 Navicat 18 `child` 数据页的真实数据与弹出菜单状态，统一行数据、DPI、原版分页/列显隐，再进行具可重复阈值的真正 V 验收。本轮严格 V **不通过**，不能用低全图 MAE 或单元测试代替。
