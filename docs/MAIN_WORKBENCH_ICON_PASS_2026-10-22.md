# 主工作台图标收敛 — 2026-10-22

## 目标与资源边界

参考用户提供的 Navicat 18 主工作台截图，改善 NyaDB 图标识别性、层级和颜色分布；**研究的是交互与视觉组织，不复制原版私有图标**。

核对了以下可独立使用的图标来源：

- **Lucide Icons v0.468.0**（ISC，包含继承的 Feather/MIT 声明）。沿用仓库固定版本，新增 11 个 SVG：`bot`、`binoculars`、`folder-plus`、`folder-open`、`pencil`、`copy`、`trash-2`、`file-plus-2`、`file-down`、`file-up`、`table-properties`。资源直接来自该版本公开源码，继续随仓库 `internal/assets/lucide/LICENSE` 保存许可信息。
- **现有数据库品牌 PNG**：`internal/assets/connectionicons/`，带独立来源、SHA-256 清单和回退逻辑；已有 MySQL（Devicon MIT）、PostgreSQL 和 SQLite（Simple Icons 集合）等标识，不另外抓取 Navicat 私有素材。商标权和品牌政策仍归权利人；Simple Icons 的集合许可不保证每个品牌标识均为 CC0。
- **NyaDB 独立绘制实心文件夹**：用于紧凑连接树和组信息面板；不是 Navicat 文件资源。
- 比较过开源 [Tabler Icons](https://github.com/tabler/tabler-icons)（MIT），但为避免项目通用图标同时依赖两套线框风格，本次**不引入** Tabler。

## 已应用位置（I）

| 主工作台位置 | 图标策略 | 功能/行为边界 |
| --- | --- | --- |
| 顶部「新建查询」 | Lucide `file-plus-2` | 区别于一般「查询」 |
| 顶部「视图」 | `binoculars` | 视觉上区别于普通眼睛 |
| 顶部「自动运行」 | `bot` | 只是图标升级，不表示自动化功能完成 |
| 顶部「备份」 | `database-backup` 蓝色 | 继续沿用旧资源 |
| 虚拟连接组 | NyaDB 自绘黄色实心文件夹 | 14–45 DIP 统一视觉语义 |
| 左侧 MySQL / PostgreSQL / SQLite | 现有 embedded 品牌 PNG（16 DIP） | 图片缺失时按数据库类型回退到原有 SVG |
| 新建组与连接 | `folder-plus` / `plus` | 原生 MyGo 按钮及可访问标签 |
| 左下编辑 / 复制 / 删除 / 刷新 | `pencil`、`copy`、`trash-2`、`refresh-cw` | 保留原生按钮禁用态与快捷操作 |
| 对象栏：打开 / 设计 / 新建 / 删除 | `folder-open`、`table-properties`、`file-plus-2`、`trash-2` | 不改变现有只读限制；未实现的创建删除继续禁用 |
| 对象栏：导入 / 导出 | `file-down`、`file-up` | 按钮继续禁用，不能仅因有图标就宣称功能可用 |
| 对象表名称列 | `table-2` / `binoculars` | 沿用 MyGo 原生表格与列表状态 |
| 右侧信息 | 类型品牌图标或黄色实心文件夹 | 始终根据当前选择上下文显示 |

SVG 均由 MyGo 原生 `ui.Icon` 解析、主题色渲染，PNG 继续由现有 Go embed 图片缓存加载。没有新增 WebView、网络请求、字体依赖或运行时图标包。

## 验证与证据

- 使用 `NYADB_GROUPED_CAPTURE=1 go test ./internal/ui/shell -run TestCaptureGroupedWorkbenchPreview -count=1` 重新生成无窗口 1280×800 工作台截图：
  `temp/research/main-workbench-grouped-20261022/nyadb-headless-groups.png`。使用本地临时档案数据，并不连接任何数据库。
- 从图片观察到顶部新图标、实心文件夹、嵌入式数据库品牌 PNG、紧凑工具条均能在 MyGo 渲染中出现。
- `internal/ui/theme/icons_test.go` 检查通用语义映射可解析。
- `internal/assets/lucide/assets_test.go` 验证 31 个 embedded SVG。
- `internal/ui/shell/profile_icons_test.go` 验证三种主要数据库图标与回退逻辑。

此证据等级为 **I（实现与 MyGo 无窗口视觉验证）**。尚未完成真实 Linux/Windows GUI 菜单及 Navicat 相同 DPI、窗口、树展开状态的 V 差分，不可称为像素等价。

## 本轮实跑结果（最终）

- `go test ./...` — **PASS**。
- `go vet ./...` — **PASS**。
- `go tool mygo vet` — **PASS**。
- `go tool mygo build` — **PASS**（MyGo 0.6.0、NyaDB 0.1.0、Linux amd64，输出在 `build/linux-amd64/`）。
- `git diff --check` — **PASS**。
- 在更新后的 1280×800 MyGo 无窗口截图中确认树上的 PNG 品牌标识、黄色实心组图标、顶部双筒镜及机器人图标、紧凑对象操作图标和底部编辑/复制/删除/刷新图标。
- 本轮未运行 Navicat、未连接生产数据库、未 commit/push，也未修改原版可执行文件。
