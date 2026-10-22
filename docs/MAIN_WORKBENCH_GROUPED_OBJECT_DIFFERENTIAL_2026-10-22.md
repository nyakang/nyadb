# NyaDB 主工作台：虚拟连接组与对象区统一（2026-10-22）

## 目的与依据

依据用户提供的 Navicat 18 与 NyaDB 主工作台截图进行静态可视观察；这两张图**尺寸和选中状态不同**，不得作为严格像素 V 验收。研究对象：菜单与工具栏、左侧连接分组、中央对象工作区、右侧信息面板。NyaDB 使用独立 Go/MyGo 代码和授权图标，不导入 Navicat 资源。

证据：
- **O（截图观察）**：Navicat 的左侧树为虚拟组及连接层级，选中虚拟组使右侧显示文件夹名称、组成员计数；中央对象列表的列头在空列表仍可见。截图未证明数据库实际连接或完整菜单语义。
- **I（独立实现）**：本轮代码和测试所完成的内容，见下表。
- **V（差分）**：尚未达到同窗口、同 DPI、同选中状态、同数据库快照条件，不宣称 V。

## 分阶段设计与结果

| 阶段 | 设计 / 验收 | 本轮状态 |
| --- | --- | --- |
| P0 统一连接导航 | 非敏感持久化虚拟组；连接归组、创建、重命名、删除、跨组移动；按组/连接区分选择状态；删除组不得删除连接 | **I**：schema v6、服务 API、原生 Outline、右键菜单与原生 UI 测试 |
| P1 统一对象工作区 | 不因数据库会话状态切换不同的页眉；空状态保留列头；保留表结构/数据入口及真实元数据异步生命周期 | **I**：同一页签、紧凑操作栏、MyGo Table 的真实列表；未连接时静态列头 |
| P2 主窗口 Chrome | 紧凑工具栏、页签、左侧搜索区；选中虚拟组显示对应信息 | **I**：工具栏 72→64，页签 32→27，取消默认按钮外框，左下 CRUD 变紧凑按钮 |
| P3 图标与色板 | 原生/独立图标、统一中心和侧栏底色；不复制私有资源 | **部分 I**：根据 MySQL/PG/SQLite 使用独立图标，中央区域与侧栏底色统一；细节逐像素调整待 V |
| P4 严格视觉门禁 | 同尺寸、DPI、字体、同展开状态、同选中节点及相同数据库快照，逐 ROI 像素差分 | **BLOCKED**：本轮没有授权原版实时同状态截图，不给出伪造的像素差值 |

## 重要行为决策

1. 分组是本地导航容器，不代表真实 MySQL schema，也不影响现有会话或表数据。删除组事务性解除连接归组，组内连接保留，密码不持久化。
2. 旧档案迁移默认未分组；支持空文件夹，虚拟组名称独立存储并防止重名。建/改档案要求组仍存在；旧版本的迁移自动升级到 v6。
3. 左树使用 MyGo `ui.Outline`。组与连接拥有不同的选择上下文；已连接数据库继续通过现有 `ui.Tree` 读取，保持按需加载及取消机制。
4. 真实对象的 `ui.Table` 保留原生排序/列表能力。未连接时不构造零行 `Table`，因为当前 MyGo 运行时在该场景触发 ListState 冲突；仅绘制匹配列定义的静态列头。
5. 原始对象元数据目前只有名称、对象类型、引擎、注释；新增的自动递增值、修改时间、数据长度、行数列**暂时留空**，不伪造服务端数据或谎称已实现。
6. 菜单和按钮中尚未实现的 DDL / 导入导出保持禁用；已实现的打开数据、设计结构、切换数据库、刷新元数据保留可访问标签，避免 UI 重构造成原测试退化。
7. P4 将使用相同的组、组成员、选中状态和窗口及 DPI，分别衡量菜单工具栏、树、对象列表、信息面板、状态栏。不把无窗口 Tester 渲染等同于真实 OS GUI。

## 改动范围

- `internal/storage/localdb/sqlite.go`、`internal/services/connections/groups*.go`、`group_integrity.go`、`profiles.go`：schema v6 / 分组 CRUD / 档案归组。
- `internal/ui/shell/profile_groups.go`、`profile_view.go`、`profile_state.go`、`model.go`、`connection_dialog.go`：原生两级导航、分组菜单、选择状态、连接继承组。
- `internal/ui/shell/chrome.go`、`workspace.go`、`mysql_session_browser.go`、`mysql_object_view.go`：紧凑 Chrome、对象页统一及无数据列头。
- 测试：分组完整生命周期、迁移幂等、原生 Tester 分组与信息面板，并保留现有 MySQL 数据、结构与查询交互测试。

## 测试与视觉边界

本轮必须执行 `go test ./...`、`go vet ./...`、`go tool mygo vet`、`go tool mygo build`。若有失败记录确切失败项及解决结果。GUI 与 Navicat 原版同尺寸差分不在此次代码测试中通过。


## MyGo 无窗口视觉自检（不是 Navicat V）

新增可选 `TestCaptureGroupedWorkbenchPreview`（`NYADB_GROUPED_CAPTURE=1`），在临时配置数据库中创建「开发库」10 个 `example.invalid` 的模拟 MySQL 档案和空组「生产库」，截取 MyGo 1280×800 渲染帧：

- `temp/research/main-workbench-grouped-20261022/nyadb-headless-groups.png`（gitignored，研究证据，不属于产品资源）。
- 第一轮自检发现虚拟组经程序预选时高亮缺失：补充 `outlineRowForSelection`，在 `Outline` 首次绘制前按节点身份计算选中行。
- 第二轮自检确认选中「开发库」有蓝色行高亮、右侧显示「虚拟组 (10 对象)」，中央显示对象列头，左下搜索和快捷按钮合并到单行。
- 未进行 Navicat 原版程序操作，也未连接 MySQL 或生产服务；不可据此声称同尺寸 V 或真 GUI 验收。

## 本轮最终验证结果

- `go test ./...`：**PASS**（分组生命周期、迁移、MyGo Tester 与现有 MySQL 服务/UI 回归）。
- `go vet ./...`：**PASS**。
- `go tool mygo vet`：**PASS**。
- `go tool mygo build`：**PASS**，构建产物 `build/linux-amd64/`。
- `git diff --check`：**PASS**。
- 研究截图：MyGo 无窗口渲染已采集并审阅；原版实时 GUI 同状态差分与 Windows/Linux 真实桌面运行：**未执行，不计 V**。
- Git 状态：变更保留工作区，未 commit、push 或修改历史。
