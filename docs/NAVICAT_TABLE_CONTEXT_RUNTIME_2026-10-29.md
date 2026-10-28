# Navicat 18 表菜单运行对照与 NyaDB 修正（2026-10-29）

## 本轮结果与范围

实际操作 `temp/navicat/` 的 Navicat Premium 18 Linux 原版 GUI，采集单表、多选、五个子菜单、三级菜单和危险操作确认窗口，并在同一个可丢弃 MySQL 8.4.11 fixture 上运行 NyaDB 原生 GUI。菜单名称与复制顺序已据实调整，修复了新建表目标名、空库入口、快捷键目标以及复制生成列/不可见列的问题。

本轮获得 **O（原版运行观察）**，以及清空/截断/删除和普通表数据复制的**限定范围双边行为证据**。菜单几何、确认方式、重命名及复制命名仍有差异；不宣称完整功能或像素级 V 通过。

原始材料位于 Git 忽略目录 `temp/research/table-context-live-20261029/`，截图没有作为产品资源分发。`menu-comparison.png` 为两张原始截图的原尺寸裁剪拼接，无缩放。文件哈希见 `evidence-sha256.txt`。

## 环境与目标身份

- 原始 AppImage：`temp/navicat/navicat18-premium-cs-x86_64.AppImage`，SHA-256 `3b77fe1e3295dc3067402a0bab33a8ed1cbfbdea063d8b59e67c26a6480b6d63`。
- 复用历史正常运行的 Navicat 窗口，Xvfb `:184`，1600×960；没有另起原版实例。
- 已核对运行进程映射使用历史从原包提取的 `pristine-lib/libcc.so`，SHA-256 `6c45353fea04ba4b941aef2477fe9fa4997c77cee6380c3abc60f87f404e8e57`，避开旧解包目录中不同的同名文件。映射和可执行文件哈希见 `navicat-loaded-libcc.txt`、`artifact-sha256.txt`。
- REA MCP 在当前会话不可用；read-only doctor 检出注册与 Node 版本问题。本轮没有修改工具配置，使用 X11 正常输入、截图和服务端 SQL 日志采证，没有将这些记录伪称为 REA 返回的 Evidence ID。
- Docker fixture：`nyadb-table-context-20261029`，仅 `127.0.0.1:32788`，MySQL 8.4.11；数据库 `table_context_it`。专用测试账户仅对该库授权，空密码不保存到 Navicat 档案。
- 临时 Navicat 档案：`NyaDB Table Menu Fixture 20261029`。NyaDB 使用独立 `XDG_CONFIG_HOME`，原生窗口在 Xvfb `:197`，同为 1600×960、中文、浅色。NyaDB 初始窗口需通过 X11 map 显示；这是本轮启动条件，未据此声称一般桌面启动通过。
- `setup.sh` 保存 fixture 建立过程；`launch-nyadb.sh` 保存 NyaDB 启动过程。另一个独立 schema `nyadb_table_it` 只供自动集成测试使用。
- 报告日期按 Asia/Shanghai；MySQL 原始日志使用服务器 UTC 时间，因此日志中的 2026-10-28 17 时对应本地 2026-10-29 01 时。原始时间不改写。

## O：实际菜单及行为

| 问题 | 本轮直接观察 | 证据文件 |
| --- | --- | --- |
| 单表菜单 | 25 项、四条分隔线；包含“粘贴”，没有“创建打开表快捷方式”；原版树与对象列表均采到 | `08-table-menu.png`、`28-tree-menu.png` |
| 通过配置文件打开表 | “无配置文件”禁用；“默认配置文件”下还有“快速模式” | `09-profile-submenu.png`、`14-default-profile-submenu.png` |
| 复制表 | 顺序为“结构和数据”“仅结构”；点击直接生成 `probe_copy1`，本样本没有命名对话框 | `10-copy-submenu.png`、`17-copy-data-result.png`、`22-copy-structure.png` |
| 转储 SQL 文件 | “结构和数据…”“仅结构…”；未执行导出 | `11-dump-submenu.png` |
| 维护 | 分析表、检查表、优化表、修复表、获取行的总数 | `12-maintain-submenu.png` |
| 检查表三级菜单 | 常规、快速（Quick）、快（Fast）、已更改、扩展；未执行 | `15-check-submenu.png` |
| 修复表三级菜单 | 快速（Quick）、扩展；未执行 | `16-repair-submenu.png` |
| 管理组 | 新建组；无组时“移动到”“从组中排除”禁用 | `13-group-submenu.png` |
| 清空、截断、删除确认 | 显示目标表名，要求勾选“我了解此操作是永久性的且无法撤销”；外键检查下拉为默认/启用/禁用；本轮保留默认 | `18-delete-rows-confirm.png` 至 `21-drop-confirm.png` |
| 重命名 | F2 进入对象列表行内编辑；输入后执行 RENAME TABLE | `23-rename-inline.png`、`navicat-actions-general-log.tsv` |
| 对象“复制” | Ctrl+C 后粘贴变为可用；X11 剪贴板同时提供 `PBTypeQListCMRef`、`text/plain` 等类型；文本值为 `probe`。内部格式含义及粘贴执行流程仍未知 | `24-after-object-copy.png`、`navicat-clipboard-targets.txt`、`navicat-object-copy.txt` |
| 多选 | 本样本选中 probe/second 后菜单变为 20 项；不显示表配置、设置权限、创建 BI 工作区、共享、重命名；保留粘贴。未执行批量写操作 | `25-multiselect-menu.png` |

### 原版发出的 SQL

`navicat-actions-general-log.tsv` 是仅筛选本轮 `table_probe` 用户的实际服务端日志，不是由菜单名称推测：

- 清空：`SET FOREIGN_KEY_CHECKS = 1`，然后 `DELETE FROM probe_copy1`（日志中标识符带反引号）。
- 截断：`SET FOREIGN_KEY_CHECKS = 1`，然后 `TRUNCATE probe_copy1`。
- 删除：`DROP TABLE IF EXISTS table_context_it.probe_copy1`。
- 重命名：`RENAME TABLE table_context_it.probe_copy1 TO table_context_it.renamed_probe`。
- 复制：读取 `SHOW CREATE TABLE` 和字段/触发器信息，设置 `FOREIGN_KEY_CHECKS=0`，发送重写名称后的完整 CREATE；带数据时继续 `INSERT INTO probe_copy1 SELECT * FROM table_context_it.probe`。本轮普通源表无外键/触发器，不能推论复杂对象的复制策略。

原版此处不是 `CREATE TABLE ... LIKE`。这些连接上的原始日志随后出现 Quit；本轮没有测试此路径与用户 SQL 标签事务的关系，也没有验证外键下拉选“禁用”的实际后果。

## 双边实际数据库结果

源表含 `id BIGINT AUTO_INCREMENT PRIMARY KEY`、`note VARCHAR(80)`，两行 `(41, alpha)`、`(42, beta)`。原版复制为 `probe_copy1`，NyaDB 从同一源表复制为 `nya_copy`，随后对各自副本按相同次序进行清空、插入探针行、截断、插入探针行、删除。

| 检查 | Navicat GUI | NyaDB GUI | 结论边界 |
| --- | --- | --- | --- |
| 复制结构和数据 | 两行及 id/note 保留 | 两行及 id/note 保留 | 此普通表的数据结果一致，命名交互/CREATE 策略不同 |
| 清空后 COUNT(*) | 0 | 0 | 保留结构 |
| 清空后探针 INSERT 的 LAST_INSERT_ID() | 43 | 43 | 本样本自增不重置 |
| 截断后 COUNT(*) | 0 | 0 | 保留结构 |
| 截断后探针 INSERT 的 LAST_INSERT_ID() | 1 | 1 | 本样本自增重置 |
| 删除后 information_schema 中目标表数量 | 0 | 0 | 删除结构 |

探针 INSERT 与后置状态由 fixture 管理员查询执行，不伪称应用 UI 插入。证据：`navicat-{delete,truncate,drop}-state.tsv`、`nyadb-gui-{delete,truncate,drop}-state.tsv`、`nyadb-gui-copy.tsv`、`both-gui-general-log.tsv`。NyaDB 操作截图为 `39` 至 `47`，新建 `nya_created` 另有 `SHOW CREATE TABLE` 证据 `nyadb-gui-created.tsv`。

## NyaDB 本轮修改

1. 新建表：原 UI 将已选中表放进 `Table`，输入仅在 `NewName`，但 Create 服务读取 `Table`，导致创建原名称失败。现在映射到输入的目标名称；补完整点击/输入/确认回归测试。
2. 空库入口：数据库、表分类右键及对象工具栏可新建基础表；不再要求先存在一张表。
3. 快捷键：树与对象列表处理 Ctrl+N、Del、F2、F5，Del/F2 打开表确认流程。修正表节点 Del 误打开删除连接的问题，连接删除限定连接节点；输入框不会触发表快捷键。
4. 菜单：复制顺序/名称据实调整；四个尚未实现的高级子菜单保留正确箭头和观察到的结构，全部禁用、无数据库回调；加入禁用粘贴，去掉本 Linux 样本不存在的打开表快捷方式。补中文/英文翻译并生成 catalog。
5. 复制数据：取消 `SELECT *`。先读取绑定 schema/table 的 `information_schema.COLUMNS`，按 ordinal 构造转义后的显式列清单，保留 INVISIBLE、普通 DEFAULT_GENERATED 字段，依据 `GENERATION_EXPRESSION` 排除生成列。元数据失败发生在建表前；CREATE 成功而 INSERT 失败仍明确报告并保留新表。

## 验证与仍有差异的部分

- `go test ./...`、`go vet ./...`、`go tool mygo vet`、`go tool mygo i18n check`、`go build ./cmd/nyadb`（指定研究输出文件）、`go tool mygo build` 均通过。Linux 可执行程序及安装包位于 `build/linux-amd64/`。
- 新增 `mysql_table_change_integration_test.go`，默认跳过；启用方式：`NYADB_MYSQL_TABLE_IT=1 NYADB_MYSQL_TABLE_PORT=<fixture-port> go test ./internal/services/connections -run '^TestMySQLTableChangesIntegration$' -count=1 -v`。所需独立 fixture 账户和 schema 约定见测试文件，不可对生产库启用。
- 真实 MySQL 8.4.11 覆盖七个写操作、只读账户拒绝、外键拒绝、复制部分失败、生成列、不可见列及默认表达式。生成列修复前报 3105；不可见列修复前从 99 静默变为默认值 17，修复后保留 99。复现/修复日志为 `nyadb-copy-columns-{before,after}.log`；相关 SQL 与最终零测试表状态也已保存。
- 原生菜单对照：本样本 Navicat 主菜单约 206px 宽、25px 行高；NyaDB 约 205px 宽、22px 行高。两者均显示完整菜单和向右的复制子菜单。只做原尺寸截图人工比较，未报告像素差分通过。
- NyaDB 原生 Linux 菜单截图没有显示 `.Shortcut` 的提示文字，实际 Ctrl+N/Del 已通过原生操作，F2/F5 通过 Tester。行高、字体、快捷键提示、边缘展开方向和悬停延迟尚未全部对齐；没有本轮 Windows 证据。
- NyaDB 保留输入表名的危险操作确认，没有原版的外键检查覆盖选项；重命名使用对话框；复制要求输入目标名。基础新建仍为单字段模板；打开/设计仍只读。
- NyaDB 没有声称实现批量写、表配置、虚拟表分组、对象剪贴板、导入导出、维护等工作流。权限是否可执行以服务器返回为准，原版只读账户下的菜单禁用规则未观察。
- 并发 ALTER 时没有锁定一致的结构快照，复制仍不是原子操作；断线、复杂触发器/外键复制、事务回滚不在本轮双边验证范围。

## 清理

原版本轮临时连接已关闭并删除（`50-navicat-restored.png`），历史 `NyaDBAlignment Fixture` 与原版进程保留。NyaDB 的本轮隔离实例、显示服务和一次性 MySQL 容器在采证后关闭；独立集成测试表自动清理。研究证据与隔离配置留在忽略目录。未修改原版程序、授权或正式用户连接，未提交或推送代码。
