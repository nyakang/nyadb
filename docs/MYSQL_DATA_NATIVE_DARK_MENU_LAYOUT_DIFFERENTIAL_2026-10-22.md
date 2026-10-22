# MySQL 数据页深色菜单与紧凑布局差分复验（2026-10-22）

## 实施与证据等级

本轮从 c73f95f 开始。**I（独立实现及测试）**：提交 1adb614 修复 GTK 原生菜单暗色适配；提交 526730b 压缩数据页顶部布局及窄窗口操作条。**O（原版运行观察）**：研究环境中此前已运行的授权 Navicat 18 原版 GUI；本次只读观察，不修改原版程序、资源、授权或数据库。**S（静态原版证据）**：无新增。**V（同条件差分验收）**：尚未通过，同实例数据快照和 DPI 条件不足。**Unknown**：未经原版交互确认的其他状态、数据和菜单细节。

### 1. GTK 原生深色菜单（I）

MyGo v0.6.0 的 Linux Theme.SetSource(ThemeDark) 修改 gtk-application-prefer-dark-theme；原桌面 GTK 默认 Yaru 主题下，GtkMenu 实拍仍为浅色。实验指定 GTK_THEME=Adwaita:dark 后原生菜单变暗。曾试验在 MyGo 运行时修改 gtk-theme-name，实拍没有效果，**试验补丁已撤回**，third_party/mygo 保持不变。

NyaDB 的 internal/bootstrap/gtk_theme_linux.go 在 GTK 初始化前设置 GTK_THEME=Adwaita:dark，**仅在用户未显式配置 GTK_THEME 时**启用；明确设置其他主题或空值时不覆盖。非 Linux 系统保持 no-op，相关单元测试通过。使用真实 Xvfb :136 窗口、真实隔离 MySQL 8.4 数据页和鼠标右键实拍，原生菜单背景 RGB 中位数从 (255,255,255) 变为 (47,47,47)；Navicat 原版参考菜单的采样值为 (48,53,60)。**这只是背景色接近，不代表菜单像素整体等价**。GTK 的默认主题覆盖有可能改变用户原有桌面风格，可通过显式 GTK_THEME 控制；菜单仍由 GTK 原生实现而非假菜单。

### 2. 数据页紧凑布局（I）

取消 mysql_data_view.go 中重复的 35px 表名/返回/刷新/复制栏，把功能合并到默认紧凑的 mysql_data_rule_editor.go。没有数据页、加载中或失败时仍可访问导航、刷新、规则草稿。chrome.go 菜单栏从31px改为26px，主工具栏从84px改为72px，按钮/图标同步调整。保持 MyGo 原生 TableLayout、双行表头、排序、列宽、菜单、横向滚动及分页。视口宽度小于1100时，操作按钮自动到第二行，不会覆盖右侧字段信息栏。

测试 internal/ui/shell/mysql_data_compact_layout_test.go 在 1280×800 验证双行表头、折叠/展开；在 840×560 验证全部紧凑操作按钮都处于信息栏左侧。真实 Linux MySQL 数据页实拍：统一裁切后的双行表头 y≈180，旧版 y≈239，原版参考 y≈157，偏差从约82px降到约23px。剩余差异主要为原生系统标题栏以及工作区结构，不能为像素一致性去掉原生窗口机制。

### 3. 1280×800 同画布差分（非 V）

原版参考图是已有 Navicat 18 O 记录，NyaDB 是本轮新采集的真实 Xvfb :136 数据页。双方研究截图原画布1600×960，使用相同裁切 (160,80)-(1440,880)，得到1280×800。差分计算脚本和图片保留在 Git 忽略的 temp/research/mysql-visual-20261022/compact-after/compare_native.py、child.png、child-menu.png、child-aligned-1280x800.png。对齐参考使用 temp/research/mysql-visual-20261022/navicat-aligned-1280x800.png。纯 RGB 逐像素绝对差，没有平移或语义掩膜：

| ROI | 旧版 MAE | 新版 MAE | 新版至少一通道差值>32比例 |
|---|---:|---:|---:|
| 全 1280×800 | 14.27 | **14.13** | 10.46% |
| 左侧对象栏 | 15.71 | 16.62 | 13.46% |
| 数据工作区 | 6.70 | **5.72** | 2.92% |
| 右侧字段栏 | 8.43 | **7.76** | 5.26% |

左侧 MAE 增大；没有声称各区域全部改善。原版旧页为 (N/A) 行，NyaDB 页为三行真实数据，且窗口外观及操作系统标题栏不同。这些 MAE 只能作为视觉定位指标，不是 V 通过率。

### 4. 原版新观察（O），相同数据库快照阻断

发现此前已运行的 Navicat 18 实例位于独立 Xvfb :121，存在 nyadb_struct_a.child 的表数据/设计页；未重新启动 Navicat，也未修改原版。研究实拍保存在 ignored 的 compact-after/navicat-observe-121.png、navicat-data-121.png。尝试对已经打开的数据页执行一次只读刷新时，Navicat 返回 MySQL 1045：

> Access denied for user 'nyadb_navicat_observer'@'172.17.0.1' (using password: NO)

错误截图 compact-after/navicat-data-refresh-121.png。已关闭弹窗并回到原来设计页，见 navicat-restored-121.png。NyaDB 使用隔离 localhost 34306 上仅授 SELECT / SHOW VIEW 的 nyadb_it_reader，读到三行；Navicat 的旧页显示 (N/A)。**并未确认是同一数据库实例、同一时点或同一行集。**另外，尽管 Xvfb 画布可见均为1600×960×24，环境没有 xdpyinfo，DPI 未独立核验。故严格 V 明确未通过；不能将同名表/低 MAE/功能测试写成 V。原版编辑菜单含 DML 项目，NyaDB 当前保持只读，不为了复刻而启用写入。

### 5. 验证矩阵

| 验证 | 结果 |
|---|---|
| go test ./... | PASS |
| go vet ./... | PASS |
| go tool mygo vet | PASS |
| go tool mygo build（Linux/amd64） | PASS |
| go test -race ./internal/services/connections ./internal/ui/shell | PASS |
| 隔离 MySQL 8.4 服务层字段与 ENUM/JSON 测试 | PASS |
| 隔离 MySQL 8.4 + ui.NewTester 原生表头/字段栏 | PASS |
| Linux 真实 GUI + 真实 MySQL + GTK 深色菜单 | PASS（I） |
| Navicat 同数据库快照/DPI 的严格视觉 V | **Unknown / 未通过**，1045 与数据行状态不同 |

此轮保持 Keyset 可靠唯一索引边界、无唯一键 OFFSET 回退与风险提示、并发期间无跨页快照保证、BLOB 限量预览和按需分块，以及会话生命周期。没执行 DDL/DML，也未连接生产库。

### 6. 下一阶段

仅在合法授权的隔离环境里，提供 Navicat 可用的**只读测试凭据**，让两边在同一个 fixture 上可追踪地运行同样的 SELECT/分页和字段选择；独立核验相同 DPI、窗口内容区尺寸、字体和排序。采集两端同状态双行表头、字段栏与菜单并建立 ROI 阈值，重新判定 V。继续遵守不复制专有资源及不引入数据写入。
