# Navicat 18 ↔ NyaDB 导航与菜单差分验证（2026-10-23）

## 边界与环境

- 原版：`temp/navicat/squashfs-root/AppRun`，Linux/Xvfb `:121`，使用**既有隔离 HOME**（不是用户正常档案），未连接生产数据库。Windows 版原版菜单亦经桌面观察。
- NyaDB：本地构建的 Linux/amd64 MyGo v0.6.2 (fork) 程序，Xvfb `:130`，独立配置目录和测试数据库。
- **O** 原版运行观察，**I** NyaDB 独立实现并测试，**V** 表示经过双方运行对照的有限范围验证，**Unknown** 表示未验证。原版二进制/图片始终未复制到正式产品源码或资源中。
- 研究截图见 `temp/research/navicat-menu-diff-20261023/`，仅作研究证据，不打包。

## 菜单行为的原版证据（O）

| 入口 | Navicat 18 的实际行为 | 原版截图 |
| --- | --- | --- |
| 顶部「工具」 | 数据传输、数据生成、数据脱敏、数据字典、数据同步、结构同步、命令列界面、服务器监控、查找、历史日志、选项。按功能分组；仅有 3 个明显的分隔区域。 | `navicat-tools.png` |
| 「我的连接」标题 | 新建连接、关闭所有连接、管理连接、新建组、刷新；选项分组。 | `navicat-header-context.png` |
| 左侧空白区域 | 右键**不弹出**菜单。 | `navicat-blank.png`, `navicat-blank-near.png` |
| 连接目录 | 新建连接、新建组、删除组、重命名、刷新；与连接菜单不是同一套操作。 | `navicat-group-final.png` |
| MySQL 连接资产 | 打开/关闭、切换配置、编辑、新建、删除、复制，数据库/查询、命令行/SQL 文件/重载、星标/颜色、管理组/共享、刷新。多级子菜单，未连接时部分操作禁用。 | `navicat-connection.png`, `navicat-manage-group-submenu.png` |

截图约为 96dpi 环境：原版「工具」菜单总宽约 258px，行高约 25px；连接右键菜单宽约 193px，真实高度与内容、分隔、字体相关。**这些仅作为环境相关的 UI 几何参考，不应视为各平台统一像素规格**。

## 本轮独立实现（I）

- Linux 原生 GTK3 菜单统一使用 CSS provider 调整菜单内边距、菜单项高度、字体和分隔线间距。纯原生 GtkMenu/GtkMenuBar，既不覆盖 MyGo 菜单交互，也不强制写死主题颜色。实现：`internal/bootstrap/gtk_menu_density_linux.go`。
- NyaDB 顶部「工具」采用观测到的条目、分组及 F6、Ctrl+Shift+F、Ctrl+L 快捷键展示；未支持的业务命令保持 disabled，无伪行为。实现：`internal/ui/shell/chrome.go`。
- 导航标题、目录、连接采用三份差异化菜单。原版右键未出现的「连接列表空白处菜单」已删除，不把右键空白误认为列表标题右键。
- 目录右键和连接资产右键现在会先切换真实选中目标，再执行菜单动作；连接目标的星标和组管理依旧持久化。并保留头部悬浮齿轮、底部搜索筛选/折叠功能。
- 尚未实现的跨数据库工具、共享/颜色、连接配置切换、删除/执行类操作不会被错误启用；根菜单「关闭所有连接」等仍需后续业务实现。

## 差分截图验证

| 条件 | 原版 | NyaDB | 结论 |
| --- | --- | --- | --- |
| 顶部工具菜单 | `navicat-tools.png` | `nyadb-tools-final-menu.png` | V：分组/命令结构已对齐；部分菜单灰显，字体及尺寸仍有平台差 |
| 根标题右键 | `navicat-header-context.png` | `nyadb-header-final.png` | V：主要命令和分组一致；快捷键提示细节尚待微调 |
| 连接目录右键 | `navicat-group-final.png` | `nyadb-group-precise.png` | V：命令集合/顺序一致；GTK 内部边距与 Qt 略异 |
| 连接资产右键 | `navicat-connection.png` | `nyadb-connection-precise.png` | V：层级、排列和菜单密度明显靠近；权限、配置、颜色等业务尚未等价 |
| 左侧空白右键 | `navicat-blank.png` | `nyadb-blank-after2.png` | V：两者均无菜单 |

注意：Xvfb 的 GTK/Qt 菜单字体字形、OS theme 和标题栏不同，因此 **V 指特定菜单的差分交互/结构验收，不等价于 Navicat 18 的严格同尺寸像素级视觉 V**。跨平台（特别是 Windows）菜单渲染仍属 Unknown。

## 自动化验证

- MyGo `ui.NewTester`：检查空白区右键无弹窗，根/目录/资产菜单作用域，选中目标准确，持久化星标，目录重命名，组管理及底栏筛选和折叠。
- `go test ./...`
- `go vet ./...`
- `go tool mygo vet`
- `go tool mygo build`
- GUI 截图手动验证：已运行 Linux 两应用、实际触发菜单，截图保存在上述研究目录。
- 测试配置中的 MySQL 连接仅用于列表显示，未执行连接。此次未运行原版 ELF 调试、未复制原版私有资源、未 commit/push。

## 遗留项（Unknown / 下一阶段）

1. 现有原生菜单在 Windows/MyGo backend 上的实际行高、左右留白和字号仍需采集同 DPI 的 NyaDB 运行截图与 Navicat Windows 截图才能标为 V。
2. Navicat 菜单对不同连接状态、TLS 配置、对象类型、资产配置、已星标状态的全部动态规则还需分条件观察。
3. 业务能力缺口（数据工具、命令行、SQL 文件、配置切换、共享、颜色）应单独设计实现，不因菜单已存在而视为功能完成。
