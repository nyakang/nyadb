# MySQL 连接窗口：新建、编辑与配置文件侧栏差分（2026-10-21）

## 证据范围

- **O — 原版运行观察：** 使用此前已经启动、合法试用的 Navicat 18（Xvfb :121 / Openbox），只对隔离测试档案进行新建和编辑；未连接数据库、未输入真实凭据，未修改 Navicat 安装文件、二进制或解包资源。
- **I — NyaDB 独立实现：** Go + MyGo v0.6.0，独立 Xvfb :129 / Openbox，900×700 原生窗口，SQLite 本地配置（仅非敏感字段）。
- **V — 限定差分验证：** MySQL 新建/编辑窗口切换、侧栏展开/折叠、草稿切换、确认保存、取消、OS 关闭以及重新打开通过测试和 GUI 实际操作；不代表 Navicat 内部存储格式、真实数据库连接或像素级完全等价。

## 原版 UI 观察（O）

1. 新建连接：第一步先选类型，下一步进入 900×700 的 MySQL 属性窗口；没有「连接配置文件」左栏，底部有「上一步」「确定」「取消」。保存后回到主窗口并增加一个连接根项目。
2. 编辑连接：对选中的 MySQL 根连接右键「编辑连接」，标题类似 `localhost_3306 - 编辑连接 (MySQL)`，默认展开宽约 258 px 的左侧「连接配置文件」面板，顶部为同名标题及加号按钮，默认选中「主要配置文件 (Active)」。右侧标签、图和表单在剩余内容宽度内居中，**底部不再显示「上一步」**。
3. 次级配置：按加号出现 `New Profile`，可在侧栏切换；次级配置顶部增加「配置文件名称」，其连接名称输入框不可编辑，主机、端口和认证相关设置可单独编辑。切回主配置会恢复主配置的字段，切回次级会恢复次级草稿。在「高级」页切换主/次配置后，原版仍停留在「高级」，不会强制返回「常规」。
4. 右键次级项原版菜单包含「设置为活动配置文件」「复制配置文件」「删除配置文件」「重命名」；本轮没有调用真实服务器或切换活动连接目标。
5. 左下角的侧栏按钮可以折叠/重新展开配置列表；折叠后右侧表单恢复接近新建窗口的横向布局。
6. 在原版添加未确认的 `New Profile` 后点击「取消」，再次编辑该根连接时只显示主要配置；新建的次级配置没有被保留。

截图保存在 `temp/research/mysql-edit-flow-20261021/`：
`original-new-general.png`、`original-edit-default.png`、`original-edit-plus.png`、
`original-edit-switch-main.png`、`original-edit-switch-secondary.png`、
`original-edit-reopen-after-cancel.png`、`original-edit-sidebar-collapsed.png`、
`original-secondary-context-menu.png`。

## NyaDB 独立实现（I）

- MySQL 编辑窗口复用共享 Editor 结构，引入真正的 MyGo 原生侧栏（258 DIP），右侧主标签、示意图和表单缩入剩余 642 DIP 内容区；底部侧栏切换按钮有实际行为，编辑态隐藏「上一步」。新建窗口保持原有的类型选择和全宽表单。
- 侧栏默认选中「主要配置文件 (Active)」，支持添加次级配置、选择切换、在「配置文件名称」输入中重命名、右键复制和删除次级配置。主连接名称在次级视图禁用，仍可在主要配置中编辑。切换主配置和次级配置时保留当前主 Tab；临时「兼容性」子页会回到高级页。
- **每个配置使用独立窗口草稿**：保存主机/端口/高级选项及未完成的字符串输入，切换时保留；只有点击「确定」且所有配置通过校验，才一次更新同一顶层连接的设置和次级配置。点击取消、OS 关闭、回到新建向导或重新打开编辑会清理未确认草稿及会话秘密。
- 增加 `connections.ConnectionVariant`（仅非敏感字段）与 `Profile.Variants`，MySQL 专用，最多 16 个，每个配置名称独立，不能重复，校验连接字段。主连接仍只有一个根 ID；创建、修改、列举、复制连接时次级配置被保留，不会在主侧栏多出虚假连接。
- 本地 SQLite 配置 schema v3→v4 新增 `variants_json TEXT NOT NULL DEFAULT '[]'`，在单一事务内迁移；老档案原样保留，旧版数据库空配置视为无次级项。更新同一连接的主/次配置通过单个 SQL UPDATE 原子写入。
- 切换或关闭窗口时清除密码、私钥口令和 HTTP/SSH 秘密；不读取、不存储私钥文件内容，不向 SQLite 写入密码和令牌。
- **不声称完成活动配置功能**：原版可将次级配置设为 Active，NyaDB 目前仅将主要配置标记为活动，真实 MySQL 会话调度尚未实现。此项会在数据库连接执行模型就绪后实现，避免用户误以为实际生效。

NyaDB 真实 GUI 截图：
`nyadb-new-mysql.png`、`nyadb-edit-sidebar-fixed.png`、
`nyadb-edit-secondary.png`、`nyadb-edit-main-return.png`、
`nyadb-edit-sidebar-collapsed.png`。

## 验证及剩余差异

- `internal/services/connections/connection_variants_test.go`：多配置创建/读取/更新/复制、重复名称和非法参数校验。
- `internal/storage/localdb/variants_migration_test.go`：旧 v3 档案升级 v4、字段完整、重复打开不会重复迁移；之前 v2→v3 测试也验证升级后最终 v4。
- `internal/ui/shell/connection_editor_profiles*_test.go`：侧栏显隐、默认主配置、配置输入草稿切换、保存后恢复、取消和 OS 关闭防止修改、右键复制/删除/重命名、验证失败保留窗口。
- 真实 Xvfb NyaDB GUI：新建后保存非敏感测试连接、重新编辑、添加次级、切换和折叠侧栏；发现起初侧栏垂直居中，已调整高度约束并重新截图确认对齐。
- 仍有视觉差异：侧栏字体、列表按钮悬停/选中、高光颜色、顶部「配置文件名称」编辑方式，以及窗口装饰。功能差异：Active 配置切换、Navicat 实际数据库驱动行为以及安全密钥库未实现，均属于 **Unknown / Not implemented**，不标完整 V。

下一步优先研究并实现「设置为活动配置文件」与数据库驱动选择规则，同时继续配置管理的键盘交互和像素差分；本轮不复制原版专有实现或品牌素材。
