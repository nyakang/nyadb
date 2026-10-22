# MySQL 表设计同尺寸校准与只读数据查看器验收（2026-10-22）

## 目标、边界与证据

本轮在 `kang/nyadb`（起点 `a8e3eb6`）完成：
1. 依照有授权运行的 Navicat 18，在同样的 **1280×800** 窗口尺寸下校准已实现的只读表设计页；
2. 实现真正从 MySQL 安全会话加载的只读表数据页，替换真实对象列表双击时以前错误打开结构页的行为；
3. 使用一次性本地 MySQL 8.4、MyGo 无窗口测试器和独立 Linux Xvfb 原生桌面做验证。

原版程序及解包文件未修改，没有访问生产数据库或执行保存、DDL、表数据修改操作。研究证据存放在 git 忽略的 `temp/research/`，不会把原版截图和图标提交到正式产品资产。

**证据等级**：O=原版运行观察；I=独立实现并测试；V=限定场景直接差分符合；Unknown=未验证。完整像素等价仍未宣称 V。

## 一、相同尺寸布局测量与修正

原版 Navicat 18 窗口由最初的 1066×640 客户区调整为 **1280×800**，采集研究截图：

- `temp/research/mysql-structure-pixel-20261022/navicat-foreign-1280x800.png`

NyaDB 同时使用 1280×800 的独立 Xvfb 桌面，实测真实连接后的对应界面：

- `temp/research/mysql-data-pixel-20261022/nyadb-design-calibrated-1280.png`（初步分栏）
- `temp/research/mysql-data-pixel-20261022/nyadb-final-foreign-1280.png`（最终外键列宽）
- `temp/research/mysql-data-pixel-20261022/nyadb-real-data-doubleclick-1280.png`（实际双击后的数据页）

**横向几何**：原版左对象树与中央区分隔线约在 **x251**、中央区与右信息栏分隔线约在 **x1075**；NyaDB 调整后对应边界约在 **x251**、**x1075**。针对设计 Tab 使用独立的 `structureSidebarPercent=19.6`、`structureCenterPercent=80.1`，不覆盖主工作台原有的分栏设置。两侧宽度在此场景下已基本对齐（限定 V）。

**结构页纵向**：NyaDB 原来额外的字段/索引/外键统计行已移除，表设计命令区缩到 35px；选中字段下方属性区调整为约 182px；外键列宽重新分配为 115/110/110/105/115/95/95，使外键两种动作规则都在视口内可见。

**尚有差异**：原版客户区截图与 NyaDB Xvfb 根窗口截图所包含的窗口装饰不同。Navicat 子页/表头起点约在其客户区 y184，NyaDB 根窗口约在 y235（含窗口装饰）。菜单、全局工具栏、状态栏、子 Tab 样式、字段属性详情排列及原版可编辑功能未达到逐像素一致。故本轮是有尺寸对照的区域布局校准，**不是完整像素级 V**。研究截图没有经过自动生成或插入私有资源。

## 二、真实 MySQL 只读表数据服务

新增 `internal/services/connections/mysql_data.go`：

- `MySQLSessionManager.TableDataPage(ctx, profileID, schema, table, offset, limit)` 使用当前已认证会话（包括原先验证的 TLS 配置），不会隐式发起连接；
- 表名、数据库名只能经过校验并按 MySQL 规则双写反引号后写入带反引号的 SQL 标识符；主键字段由绑定参数查询 `information_schema.KEY_COLUMN_USAGE`，并独立转义后构造 `ORDER BY`；
- `LIMIT ? OFFSET ?` 真正绑定数值分页参数，不查询 `COUNT(*)`，最多每页 200 行加 1 行判断是否还有下一页，默认 100 行；限制总偏移；调用者 Context、断开连接、20 秒超时均可取消；
- 保持字段原始顺序，保存 SQL NULL 和空字符串的区别，对二进制预览做 16 进制显示；单个单元格预览截到最多 2048 字节以免 UI 无限扩张；
- 具有主键的表按主键列排序；没有可见主键的表/视图允许只读浏览，但 UI 明确说明分页时可能存在顺序不稳定；
- 每次只读取一页，**没有表编辑、行插入、删除、更新、保存或伪造的模拟查询行**。对于非常大的 BLOB，`SELECT *` 仍可能传输单个很大的单元格，即使 UI 仅截取预览；后续需要列投影/服务端截断优化。

测试夹具 `internal/services/connections/testdata/mysql_structure_fixture.sql` 仅在具名一次性测试容器内新增 3 条 child 行、3 条 parent 行及跨 schema 行，包含真实 NULL、空字符串、汉字以及字面值 `"NULL"`。

## 三、MyGo 原生只读数据工作区

新增 `internal/ui/shell/mysql_data_state.go`、`mysql_data_view.go`：

- `tabMySQLData` 独立于之前模拟用的 `tabData` 和只读设计 `tabStructure`；数据页标题以 schema/table 明确区分并允许多表 Tab 并存；
- 对象表格双击或 Enter、数据库树表/视图双击、对象菜单“打开表（只读）”均进入真实数据页；“设计表（只读）”仍打开单独的结构 Tab；
- 使用 MyGo Table，在当前页中滚动，显示真实列名、实际值与 NULL；支持上一页、下一页、刷新、取消、异常重试与空结果；
- 每个 Tab 保存 profile/schema/table、偏移、页大小、读取缓存、请求序号及取消函数；迟到结果必须同时通过 session generation、profile ID、请求号、偏移校验；
- 关闭、断开、重连、删除活动连接或 schema 列表变化时，相关 Tab 的异步请求和缓存失效；保留未受影响的其他 schema Tab；
- 工作线程只向有缓冲通道交付结果，不直接访问 MyGo State。未支持编辑、服务器端排序/过滤、任意位置跳页、完整结果虚拟游标、后台全表计数。

## 四、验证矩阵

| 场景 | 结果 | 性质 |
|---|---|---|
| 原版双击表打开数据 Tab；设计表独立 Tab | 通过 | O（此前与本轮真实运行） |
| 本轮 NyaDB 对象表双击 child 打开实际三行数据 | 通过 | I，真实 Linux Xvfb GUI + 真 MySQL |
| 本轮 NyaDB 数据/设计作为不同 Tab | 通过 | I，真实桌面 + ui.NewTester |
| 真实 MySQL 8.4，前两行/后一行，LIMIT+OFFSET，主键稳定排序 | 通过 | I，真实服务 |
| 表/视图与跨 schema 同名表读取，NULL/空串/中文 | 通过 | I，真实服务 |
| 取消/断开查询、失败重试、过期结果忽略、schema 精确失效 | 通过 | I，模拟驱动 + ui.NewTester |
| 1280×800 同尺寸结构页左/右分栏位置 | 基本一致 | 限定 V，按截图边界校准 |
| 外键约束字段以及 CASCADE/RESTRICT 均在 UI 可见 | 通过 | 限定 V，同一隔离数据 |
| 完整窗口逐像素一致、设计编辑能力 | 未完成 | Unknown/非 V |
| 非隔离生产数据/其他 MySQL 版本/桌面平台 | 未测试 | Unknown |

本轮门禁：
`go test ./...`、`go vet ./...`、`go tool mygo vet`、`go tool mygo build` 均 **PASS**；
`go test -race` 以显式 opt-in 的真实 MySQL 环境执行服务及 UI 相关测试 **PASS**。
另外实际从构建的原生 NyaDB Linux 可执行程序在 Xvfb 操作连接、对象、双击、设计、外键子页，截图确认证据。

## 五、提交记录

- `6e9c900`：1280px 表设计专用三栏比例及上方结构页密度调整。
- `c7adb0e`：真实 MySQL 只读分页服务、数据夹具、隔离驱动与真实数据库测试。
- `cbfae93`：原生数据工作区、实际双击入口、分页/异步生命周期与集成测试。
- `8f09e25`：外键列宽/属性区细节修正及 schema 定向失效回归测试。
- 本报告单独提交推送到 `origin/master`。

## 六、下一阶段建议

1. 在授权的本地 MySQL 中增加足够多的测试记录，测性能、无主键大表、超长 BLOB 和特殊字段类型；再改进服务端投影以避免传输完整大字段。
2. 数据页补列宽调整、复制单元格、客户端筛选/排序（注明非全表）、键盘导航与选中字段右侧属性面板，并继续原版同尺寸差分。
3. 若需要完整复刻 Navicat 数据编辑行为，必须另立带权限审计、乐观锁和事务确认的设计阶段；本轮仅提供真实只读浏览。

**安全收尾**：独立测试容器和 NyaDB 的 Xvfb 测试进程在报告提交后结束；原有 Navicat 研究桌面只恢复窗口位置与尺寸，不销毁或覆盖此前的研究资源。
