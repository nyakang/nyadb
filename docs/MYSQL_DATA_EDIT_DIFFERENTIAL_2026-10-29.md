# 表数据编辑流程：行为规范与验收（2026-10-29）

本报告区分 **O**（原版运行观察）、**I**（NyaDB 独立实现及测试）、**V**（相同流程的双边差分通过）和 **Unknown**（证据不足）。编译、无窗口 Tester、真实桌面和双边差分分别记录；未执行项目不标通过。

## 环境和证据边界

- 沿用仓库 Go 1.27.3、MyGo `v0.6.4-nyadb.4`，没有引入 WebView、升级依赖或复制原版私有实现。
- 原版复用 Xvfb `:184` 的历史 Navicat 18 中文 Linux 窗口。进程映射已核对加载 `temp/research/navicat-session-fixture-20261027/pristine-lib/libcc.so`。解包文件保持只读，没有授权修改或动态调试。
- 一次性 MySQL 8.4：`nyadb-table-edit-20261029`，仅 `127.0.0.1:32790`，数据库 `table_edit_it`。临时账户 `data_probe` 仅对该库有权限，`data_readonly` 仅可读取。Navicat 临时档案名为 `NyaDB Edit Fixture`，未保存密码。
- 原版截图和管理员查询结果在忽略目录 `temp/research/table-edit-live-20261029/`。服务器开启 TABLE general log，独立管理员连接读取持久化数据。原始证据含数据库值，不进入正式产品资源。
- `after-typing.tsv`、`after-tab.tsv`、`after-blur.tsv` 的管理员客户端未指定 UTF-8，中文显示 `??`；`after-enter.tsv`、`batch-before-save.tsv` 和原版 GUI 均显示正确中文。此显示问题不用于判断数据损坏。批量实验输入实际包含 `keyReturn` 字样；原始输入和日志如实保留，不影响提交时机结论。

## O：原版默认编辑模式

| 操作 | 界面状态 | 服务器及最终数据 | 证据 |
| --- | --- | --- | --- |
| 第一次单击普通 varchar 字段 | 立即出现文本编辑器并选中现有文本 | 无写入 | `first-click.png` |
| 在编辑器中右键 | Undo、Redo、Cut、Copy、Paste、Delete、Select All；初始 Undo/Redo 禁用 | 无写入 | `selection-menu.png`（文件名历史保留，内容是文本编辑菜单） |
| 将 id=42 的 note 输入为 edited42 | 行号旁出现橙色待处理标记，保存/取消启用 | 独立连接仍读取 beta | `typing.png`、`after-typing.tsv` |
| Tab 到同一记录的下一字段 | 转移编辑字段，保留当前记录草稿 | 独立连接仍读取 beta；无 UPDATE | `after-tab.tsv` |
| 点击另一条记录 | 当前行修改提交，进入另一行字段编辑 | `UPDATE ... SET note='edited42' WHERE id=42`；独立连接读取 edited42 | `after-row-switch.tsv` |
| Enter 结束单元格编辑 | 退出文本编辑，字段以草稿显示 | 当前 id=41 仍为 alpha，未产生新的 UPDATE | `after-enter.tsv`、`table-menu.png` |
| 将焦点移到菜单/工具栏 | 保留记录草稿 | 当前 id=41 仍为 alpha | `after-blur.tsv` |
| 底部 × 放弃当前待处理内容 | 恢复字段原值 | 没有提交 id=41 的草稿 | `view-menu.png`、`record-options.png` 中已恢复 alpha |
| 退出文本编辑后右键 | 记录操作菜单，而非文本菜单 | 无写入 | `grid-menu.png` |

这里观察到的是“自动应用单条记录的更改”的默认行为。它不等同于每个键入字符、单元格失焦或 Enter 都立即提交。

## O：批量编辑模式、记录菜单和底部操作

“工具 → 选项 → 记录”中 **自动应用单条记录的更改** 默认勾选（`record-options.png`）。取消该项后观察结果如下。

| 操作 | 界面状态 | 服务器及最终数据 | 证据 |
| --- | --- | --- | --- |
| 修改 id=41，再切换 id=42 并修改 | 两行橙色标记；底部橙色条显示两条待处理记录及应用/放弃 | 独立连接仍为原值 alpha、edited42 | `batch-pending.png`、`batch-before-save.tsv` |
| 点击底部 ✓ | 两条草稿全部应用，待处理条消失 | id=41/42 分别保存为 batch41keyReturn、batch42keyReturn | `batch-save-dialog.png`（实际是应用后的界面）、`batch-after-save.tsv` |
| 批量删除已有记录 | 原记录仍在网格中，以待删除状态显示；待处理条显示删除一条 | 应用前仍能从独立连接读到该记录 | `delete-pending.png`、`delete-before-save.tsv` |
| 有待删除记录时刷新 | 与关闭相同的应用/放弃/取消提示 | 提示期间不提交 | `refresh-dirty.png` |
| 放弃删除草稿 | 恢复原记录正常状态 | 没有执行该记录 DELETE | `delete-cancel.png` |
| 有待处理记录时关闭页签 | 弹出应用更改、放弃、取消三项提示；取消保留页签 | 提示期间不提交 | `close-dirty.png` |
| 底部 + | 新增行各字段显示 `(Default)`，橙色条显示插入一条 | 尚无新记录 | `add-record.png` |
| 新行输入 inserted，点击 ✓ | 应用新记录并取得自增键 | 新记录 id=46、note=inserted | `insert-after-save.tsv` |

普通记录菜单顺序由 `grid-menu.png` 确认：设置为空白字符串、设置为 NULL、生成 UUID、在单元格编辑器中编辑、删除记录、复制、复制字段名称、复制为、粘贴、保存数据为、显示/隐藏筛选排序、筛选、排序、移除所有筛选排序、缩放、刷新。

展开子菜单的证据：

- `uuid-menu.png`：默认带连字符小写、带连字符大写、无连字符小写、无连字符大写。
- `copy-as-menu.png`：Insert/Update 语句；TSV/CSV 各有数据、字段名称、字段名称和数据；JSON。
- `zoom-menu.png`：50%、75%、90%、100%、110%、125%、150%、200%，默认 100%。

这些截图确认菜单项与顺序。对中文单元格执行 LIKE 筛选时，服务器收到 `WHERE note LIKE '%中文%' LIMIT 0,1000`，GUI 仅显示 id=43（`like-filter.png`、`like-filter-sql.tsv`）。每种 UUID、每种 SQL/导出格式的实际输出、删除提交后的最终数据及其他筛选排序谓词不能仅凭菜单截图标为 V。

## I：实现与自动测试（最终源树）

继续使用 Go 1.27.3、MyGo `v0.6.4-nyadb.4` 和原生虚拟表格，没有升级依赖或引入 WebView。表数据编辑不复用 SQL 页签事务。草稿按唯一行引用和字段名保存；默认跨行提交、批量多行保存、取消、离开提示和停止使用同一动作状态。保存失败保留草稿；COMMIT 结果不确定时锁定写入重放并提供只读核验。连接代次与请求编号隔离迟到异步结果，草稿不写入配置数据库。

服务重新验证字段、完整非空唯一键和 InnoDB，排除生成列，参数化增删改。服务器原始字节指纹检测未编辑字段的并发修改；严格模式与警告检查保护精确数值，区分 NULL、空字符串、DEFAULT 和自增。长文本/二进制显式完整读取上限为 1 MiB，截断预览不能直接写回。提交返回服务器预览、自增键、规范化后的完整行身份。

本轮复查修复了两个实际缺陷：

- 数字主键输入为 `00042`、DECIMAL 键输入为 `008` 时，保存结果现在读取服务器规范化键字节，避免后续 SQL 复制的字节谓词匹配不到记录。真实集成覆盖显式插入、修改键、再次查询和删除。
- 保存后保留服务器返回的预览；二进制键继续显示十六进制，长文本键保留截断标记和大小边界，完整定位字节单独保留。

前轮实测还已对齐单格选择的 SQL 复制：INSERT/UPDATE 使用整条可写记录，UPDATE 排除主键；JSON 使用原版观察到的两空格缩进。网格 Ctrl+V 复用粘贴动作，Enter/Escape 后恢复表格键盘焦点；无效编辑不留下空草稿，待处理行标记保持单行。

`ui.NewTester` 覆盖第一次单击、文本/记录右键切换、文本撤销与 Unicode 粘贴、Enter/Tab/Shift+Tab/Escape、失焦和跨行提交、批量增删保存取消、NULL/空字符串、保存失败、输入法期间三键隔离、Shift/拖选/整行选区、停止防重复、迟到读取、翻页/刷新/筛选/排序/页大小离开提示、关闭页签三项决策、断连/关窗口提示与停放连接异步结果。93 行重排/隐藏/滚动场景验证保存仍写回原行原字段。新增类型预览回归覆盖二进制及多字节长文本键。

真实 MySQL 最终通过：13 个编辑子场景（含完整长字段、NULL/空串/精度/默认、自增、约束整批回滚、生成列/舍入拒绝、只读账户、并发修改、锁等待取消、无唯一键/大小限制、复合键、非事务引擎拒绝、SQL 页签事务隔离和规范化身份），7 组字面/LIKE/精确数值筛选，6 个 TCP COMMIT 故障场景，以及 SQL 剪贴板实际执行。

网络测试使用真实 TCP 代理对增、删、改分别注入两种故障：COMMIT 请求未到服务器；服务器已提交但确认回复被丢弃。两种情况下都进入不确定状态，独立连接与只读核验区分最终数据，检查没有自动重放 DML。此项已从驱动模拟补成真实服务器网络证据。详见 `mysql_data_commit_network_integration_test.go` 和忽略目录的网络日志。

## 最终真实桌面与双边差分

以下文件名相对于 `temp/research/table-edit-live-20261029/completion/`，旧原版证据仍在其父目录。V 只代表表中指定数据、操作和 Linux 环境；I 是 NyaDB 独立实现及实际验证。

| 场景 | 结论与数据库结果 | 最终证据 |
| --- | --- | --- |
| 默认单击、同记录 Tab、跨行提交 | V：单击进入编辑；同记录字段切换不写入，跨行后写入 | 父目录 `first-click.png`、`after-tab.tsv`、`after-row-switch.tsv`、`nyadb-default-result.tsv`；Tester 导航回归 |
| Enter 和工具栏失焦 | V：两边 `timing41` 保持草稿，管理员仍读取 `alpha` | 双方 `*-enter.png/.tsv`、`*-blur.png/.tsv` |
| 批量保存两行 | V：保存前 `alpha/edited42`，保存后 `batch41/batch42` | `navicat-batch-before/after-final.tsv`；`nyadb-batch-before/after-verified.tsv`、`nyadb-batch-pending/saved-verified.png` |
| Escape、取消删除、离开提示 | V（限定流程）：Escape 恢复 `batch41`；取消删除未写入；刷新/关闭提供应用、放弃、取消 | `navicat-escape.png/.tsv`、`nyadb-escape-verified.png/.tsv`；双方 `delete-pending/cancelled`；`nyadb-refresh-dirty.png`、`nyadb-close-dirty.png`；父目录原版 `refresh-dirty.png`、`close-dirty.png` |
| 新增后保存，再删除保存 | V：保存前新记录不存在，保存后为 `gui-inserted`，随后删除成功 | 双方 `insert-before/after`、`delete-commit-before/after`；Navicat 使用 `-final` 后缀；自增键分别 48/47，比较记录值与生命周期 |
| NULL 与空字符串 | O + I：独立查询分别为 NULL/空串，保存前不写入 | `nyadb-null-before-save.tsv`、`nyadb-null-saved.tsv`、`nyadb-empty-before-save.tsv`、`nyadb-empty-saved.tsv`；原版 NULL/空串观察与服务集成 |
| UUID 四种格式 | V（格式语义）：双方均满足连字符/大小写四组合，值随机生成 | `original-ime/17-uuid-{0..3}-saved.tsv`、`uuid-assertions.json`；`nyadb-uuid-{0..3}-saved.tsv` |
| TSV/CSV 六种复制与 JSON | V（单个中文 note 单元格）：六种输出及格式化 JSON 逐字节相同 | `navicat-copy-*.txt`；`final-nyadb-copy-{tsv,csv}-*.txt`、`final-nyadb-copy-json-verified.txt`、`final-copy-assertions.json` |
| SQL 复制整条记录 | V（执行语义）：双方复制出的 INSERT、UPDATE 实际执行得到 id=43、note=中文、其他字段 NULL；验证事务回滚 | `final-nyadb-copy-insert/update.txt`、`navicat-copy-insert/update.txt`、`final-*-sql-executed.tsv`；NyaDB 使用 UTF-8 十六进制字面量和精确原始键字节谓词，SQL 文本不逐字相同 |
| 网格/文本快捷键作用域 | I（最终真实桌面）+ O：网格 Ctrl+V 仅暂存；文本 Ctrl+C/X/Z 恢复 `native-paste`；中文 emoji 粘贴保存仅修改目标记录；Shift 范围 Ctrl+C 保留选区语义 | `final-grid-paste-pending.png`、`final-text-cut.png`、`final-text-copy.txt`、`final-text-undo-copy.txt`、`final-text-before/after-save.tsv`、`final-shortcut-assertions.json`、`final-grid-range-copy-verified.txt` |
| 专用长文本编辑 | I（真实桌面）：读取与原文逐字节相同（6000 字/18000 bytes），取消 hash 不变；保存后 6011 字且 SHA256 与预期一致 | `nyadb-long-complete-verified.txt`、`long-original.txt`、`long-expected.txt`、`long-assertions.txt`、`nyadb-long-{cancel,before-save,after-save}.tsv` |
| 二进制、忙碌按钮、停止 | I（真实桌面）：停止前保存等禁用，停止可用；取消保留草稿且服务器仍 `000141`；再次保存为 `000141FF00` | `nyadb-commit-busy-stop.png`、`nyadb-stop-retains-draft.png`、`nyadb-stop-before/after.tsv`、`nyadb-binary-saved.tsv/.png` |
| 保存数据为 CSV/TSV/JSON | I（最终真实桌面）：文件实际生成，内容与选区预览一致，权限 0600 | `final-nyadb-export.csv/.tsv/.json`、`final-export-json-dialog.png`；长字段导出明确标识为预览 |
| 筛选与排序 | V（原版 6 个普通谓词及 NULL 两项、降序）；NyaDB 10 种谓词全部经菜单执行并核验选中 ID | `final-nyadb-filter-results.json`、`final-nyadb-all-filters-server.tsv`、`final-nyadb-sort-desc-page.txt`；`original-ime/19-filter-server.tsv`、`21-sort-server.tsv`；原版 `original-ime/root-filter-results.json`、`root-null-filter-results.json` 和相邻 `root-filter-*-sql.tsv` / `*-rows.txt` |
| 缩放 50/75/90/100/110/125/150/200% | I（真实桌面）：逐项执行，字形与行测量变化，恢复 100%；不修改数据库 | `final-nyadb-zoom-*.png`、`final-nyadb-zoom-restored-100.png` |
| 浅色/深色、宽/窄窗口 | O + I：同库同表四状态真实截图，窄窗口操作区换行、数据可见 | `original-ime/09-light-wide-clean.png`、`10-light-narrow.png`、`41-dark-wide-clean.png`、`42-dark-narrow.png`；`final-nyadb-{light,dark}-{wide,narrow}-verified.png`；[对照图](../temp/research/table-edit-live-20261029/completion/final-theme-comparison.png) |

NyaDB 10 种菜单筛选在同一 `imeoriginal` 样本（before、second、NULL、空串）下得到：= / LIKE / 包含 → id=1；!= / NOT LIKE / 不包含 → 2、4；< → 4；> → 2；IS NULL → 3；IS NOT NULL → 1、2、4。独立管理员读取实际 prepared Execute SQL，UI 的选区复制验证实际显示 ID。note 降序为 2、1、4、3，清除后恢复主键顺序。原版六个普通谓词与 NULL 两项重放得到相同 ID，逐项保留实际 SQL 和选区复制结果。原版首次 `audit-filter-*` 重放误用了整行选区的简化菜单，只有等于筛选真实执行，其余旧剪贴板未更新；采用主执行者重新采集的 `root-filter-*` 结果。

## 真实中文输入法及平台边界

两款应用均使用 Linux Xvfb、Openbox、IBus 1.5.29-rc2/libpinyin 1.15.7 和同一 MySQL 8.4。NyaDB 的真实候选、三键隔离、Unicode 与范围复制记录在 `completion/ime/runtime-report.md`，完整断言和服务器日志同目录；该独立窗口及临时表已清理。

原版候选验证补在 `completion/original-ime/audit-ime-final-*`：

- 输入 zhongwen，显示“中文”候选；组合期间 Enter 提交拼音，仍在同 note 编辑器；Tab 改变 IME 组合模式，不转到下一字段；Escape 收起候选，保留编辑器。这三步管理员均读取 before。
- Space 选择中文；组合结束后 Enter 退出字段编辑，管理员仍读取 before；底部保存后服务器才为中文，文本 Ctrl+C 得到完整中文。
- 以上提交时机及 Enter/Tab/Escape 作用域与 NyaDB 实际候选流程一致，标 V（限定 Linux、note 字段和该 IME）。

原版旧 Qt 的 IBus 启动需要短 UNIX socket 路径；107 字符的原 cache 路径未建立 Qt 客户端，改用隔离短 cache 后日志出现 bus connected，才采最终候选证据。早期 ASCII 直通尝试未作为候选通过记录。Qt 与 IBus 使用相同的 bus 配置目录。原版可执行文件与 pristine libcc 的哈希保持原值，不修改其实现或进行动态调试。REA MCP 当前不可用；本轮使用现有桌面工具采集运行证据，没有改写客户端注册。

原版 Qt Linux 文本菜单提供 Undo/Redo；MyGo Linux 文本菜单提供剪切、复制、粘贴、删除、全选，撤销通过 Ctrl+Z 实际验证。Linux 实测没有 Unicode、阅读方向或输入法子菜单，不添加无效项。Windows/macOS 的原生菜单和输入法未在本宿主运行，此处不外推通过。

NyaDB 记录菜单另外保留有效的“添加记录”“查看字段信息”及包含/不包含筛选扩展；原版 note 普通筛选菜单直接提供 =、!=、LIKE、NOT LIKE、<、>，NULL 单元格提供 NULL 谓词。这些是明确的独立界面差异，不标菜单或整页像素完全一致。长字段、事务安全及未知提交防重放按独立安全实现验收。

## 证据纠正

`nyadb-batch-*-final` 的早期点击实际把 id=41 改为 batch42，不能用于两行保存通过；采用 `*-verified`。`nyadb-close-dirty-verified.png` 实际已关闭页签，正确提示截图是 `nyadb-close-dirty.png`。旧 `nyadb-copy-insert/update/json.txt` 来自修复前，只表示早期输出，采用 `final-nyadb-copy-*`。`original-ime/13-quote-*`、`14-quote-retry-*` 实际保存为空串，不能作为引号粘贴通过。原始动作、截图及失败尝试保留，便于复核。

## 最终检查

最终源树及产物结果在 `completion/resume-verification/final/checks.json`、相邻日志和 `artifact-sha256.json`。初次完整 `-count=1` 检查通过，两个主键复查修复后再次执行最终检查：

| 检查 | 最终结果 |
| --- | --- |
| `go test ./...` | 通过；总命令 101.851s，shell 95.085s、connections 24.451s 实际重跑 |
| `go vet ./...` | 通过 |
| `go tool mygo vet` | 通过 |
| `go tool mygo i18n check` | 通过，zh-Hans/en 共 542 条消息 |
| `go tool mygo build` | 通过；Linux amd64 可执行文件、deb、tar.gz |
| `/usr/bin/python3 -m unittest discover -s tests -v` | 33 个测试通过；使用已有 Pillow 的系统 Python |
| 真实 MySQL 集成 | 13 编辑子场景、7 筛选组、6 TCP 提交故障场景、SQL 复制执行全部通过 |
| `git diff --check` | 通过 |

真实服务器命令仅对一次性 loopback 端口启用：

```bash
NYADB_MYSQL_DATA_EDIT_IT=1 NYADB_MYSQL_DATA_EDIT_PORT=32790 /usr/local/go/bin/go test ./internal/services/connections ./internal/ui/shell -run 'TestMySQLDataEditingIntegration|TestMySQLLiteralAndComparisonFiltersIntegration|TestMySQLDataUnknownCommitNetworkIntegration|TestMySQLDataSQLCopyIntegration' -count=1 -v
```

最终 GUI 绑定 `build/linux-amd64/nyadb`，SHA256 `27f103c5cdadec6183a053d184cd5f485d74c7141c2b37fab612ae7a3c306116`。构建、Tester、真实服务和真实窗口证据分开记录，不互相代替。

## 清理

清理结果由最终退出后的进程/配置/容器检查补入。范围为本轮 NyaDB、原版 IBus 研究会话、临时连接、一次性容器及匿名卷；历史 Alignment 档案、原版二进制、历史 :184 显示服务和其他任务保持保留。代码仅留在工作树，不 commit、push、reset 或改写历史。
