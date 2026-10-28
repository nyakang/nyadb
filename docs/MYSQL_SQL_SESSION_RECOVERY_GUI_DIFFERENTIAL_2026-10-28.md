# NyaDB × Navicat 18：MySQL SQL 标签断线恢复真实 GUI 差分（2026-10-28）

## 实验范围

本报告对照原版 Navicat 18 与 NyaDB 最新 \`95f6fb1\` 基线，在**同一个全新隔离 MySQL 8.4.11 容器**中观察 SQL 标签、用户变量、服务器主动断线及后续恢复路径。这是**同条件功能差分**；不把有意不同的 UI 恢复策略写成严格行为等价，更不把 Linux 实验写成 Windows 验收。

- 原版：此前已启动且目前 GUI 正常运行的 Navicat Premium 18，已有 Xvfb \`:184\`、1600×960。此次只添加并最终删除名为 \`NyaDB V Navicat 20261028\` 的临时档案，本轮不检查或修改授权状态，也不修改原始二进制。
- NyaDB：从本轮最新 Linux/amd64 构建启动 \`build/linux-amd64/nyadb\`，使用专用新 Xvfb \`:195\`、1600×960 和独立 HOME/XDG 配置；临时档案 \`NyaDB V SQL 20261028\`。本次没有修改 Go 源码。
- MySQL：容器 \`nyadb-v-session-20261028\`（\`mysql:8.4\`、实际 \`8.4.11\`），仅映射 \`127.0.0.1:32787\`，库 \`nyadb_v_sql\`、表 \`probe\`，低权限测试账户 \`nyadb_v\`，空的测试专用密码。没有生产凭据。MySQL \`general_log=ON\`，日志为 TABLE。
- 初始 \`wait_timeout=8\` 秒：用于诱发 SQL 标签预连接后首次 SQL 前的真实空闲超时。由于这会让 GUI 人工操作窗口产生额外超时，后续对照将 fixture 的全局 \`wait_timeout\` / \`interactive_timeout\` 改为 **300 秒**。新建 SQL 物理连接均实测 \`@@wait_timeout=300\`。后续仅用管理员 \`KILL CONNECTION <ID>\` 主动终止**该测试账户下的已知 SQL 会话**；没有触碰无关容器。

## 实测步骤及结果

### 1. 原生连接与 SQL 标签

- NyaDB Linux GUI 原生“新建连接 (MySQL)”窗口：填写单独的档案、127.0.0.1:32787、用户 nyadb_v，取消密码保存；**测试连接成功**，UI 显示连接后出现三座数据库的资产树。展开/选择 nyadb_v_sql 并创建 SQL 标签，工作区顶部显示当前数据库。见 \`07-nyadb-connect-test.png\`、\`09-nyadb-connected.png\`、\`10-nyadb-sql-tab.png\`。
- Navicat GUI 使用同一 MySQL fixture、新建独立档案：**测试连接成功，服务器版本 8.4.11**。连接后创建真正 SQL 标签。见 \`21-navicat-connection-success.png\`、\`25-navicat-sql-editor.png\`。
- **差分结论：V（功能）**，双方都建立了真实连接并打开 SQL 标签。但两端桌面外观主题不同（Navicat 亮色、NyaDB 暗色），主窗口尺寸/内容布局也不同；不声明像素 V。

### 2. MySQL 空闲超时（NyaDB 单边 I）

在 \`wait_timeout=8\` 秒时，NyaDB SQL 标签打开后等待过久才首次运行。已预建立的物理 SQL 连接被服务端超时关闭，首次执行 \`SET @nyadb_v_marker = 777;\` 时直接显示“原查询会话已失效”与原生按钮“以新会话继续”；没有执行旧 SQL，编辑文本仍留在原标签。见 \`11-set-marker.png\`。本项没有在 Navicat 的相同初始等待时长下复测，因此**不是双端 V**。

之后将全局 wait_timeout 调到 300 秒，NyaDB 用户显式点击按钮，按钮仅重建租约/异步连接，再次手动点击“运行”，SET 成功，见 \`12-recovered-manual-set.png\`。这个序列证明用户点击恢复本身没有重放 SET。

### 3. 同条件连接 ID 与变量对照（V 数据结果）

两端在原 SQL 标签分别执行：

\`\`\`sql
SET @nyadb_v_marker = 777;
SELECT CONNECTION_ID() AS cid, @nyadb_v_marker AS marker, @@wait_timeout AS wt;
\`\`\`

| 应用 | 断线前 SQL 标签连接 ID | marker | wt |
| --- | ---: | ---: | ---: |
| NyaDB | 16 | 777 | 300 |
| Navicat 18 | 20 | 777 | 300 |

证据：\`13-nyadb-session-before-kill.png\`、\`27-navicat-query-result.png\`。**V：同一测试库、同一测试用户和 SQL，得到等价的会话状态。** 连接 ID 不应数值相同，因为两个 GUI 使用独立物理连接。

### 4. 同条件主动断线与恢复（有意交互差异）

在两端分别执行完上述 SELECT 后，管理员只对本轮 SQL 标签连接运行：

\`\`\`sql
KILL CONNECTION 16; -- NyaDB
KILL CONNECTION 20; -- Navicat
\`\`\`

每次都在**原 SQL 标签**再次运行：

\`\`\`sql
SELECT CONNECTION_ID() AS cid, @nyadb_v_marker AS marker, @@wait_timeout AS wt;
\`\`\`

| 项目 | NyaDB | Navicat 18 |
| --- | --- | --- |
| 旧连接失效 | 16 被 KILL | 20 被 KILL |
| 用户下一次直接“运行” | **显示原会话失效、禁用继续直接运行** | **成功运行 SELECT，换新连接** |
| 是否可继续保留原 SQL 标签 | 是 | 是 |
| 是否要求显式用户确认 | **是，点击“以新会话继续”** | 此只读 SELECT 场景不需要 |
| 恢复后的连接 ID | 18 | 22 |
| 恢复后 marker | NULL | NULL |
| 恢复后 wt | 300 | 300 |

证据：NyaDB \`14-nyadb-after-kill.png\`（错误及按钮）、\`15-nyadb-recovered-no-run.png\`（用户点击恢复，仅清除错误并保留 SQL）、\`16-nyadb-recovered-query.png\`（用户**再次手动点击运行**后返回 18 / NULL / 300）；Navicat \`28-navicat-after-kill.png\`（在原 SQL 标签直接再次执行后返回 22 / NULL / 300）。

- **V（会话状态结果）：通过。** 两端都在服务器主动终止旧连接后使用新物理会话，旧用户变量不会恢复。
- **V（交互/操作次数）：不等价，差异已实测。** Navicat 在此次 SELECT 查询中允许静默取得新连接；NyaDB 明确阻止可能对 DML 不安全的自动重放，要求用户确认后才重建会话。该差异是当前设计的**有意安全策略**，不是通过了逐项行为等价。
- 未在本次 fixture 进行正在执行的长事务、DML 中途断线、COMMIT 结果不确定及自动重放行为实验；不声明这些场景 V。恢复后的 NULL 不代表断线事务在所有场景必然回滚；不能从一个变量推断事务最终提交状态。

## Windows 原生 GUI 验收状态

Repo Code Computer Agents 显示一台 Windows x86_64 主机 \`Kang\` 在线，支持 observe/control/launch，但 Windows Agent **command=false**；\`computer_list_windows\` 返回的当前窗口中**没有 NyaDB 进程或窗口**，\`apps=[]\` 也没有注册的 NyaDB launch app。因此本轮无法从受控入口在 Windows 运行/构建或采集 NyaDB 真实 GUI。**Windows GUI 验收：未完成**，不能以 Linux 构建/GUI、Windows 主机在线或 Go 测试代替。

后续要完成 Windows 验收，需要由主机端启动与当前提交一致的 NyaDB 构建（或在 Agent TOML 中注册可启动的 NyaDB 应用），再以同等 fixture 对 SQL 恢复按钮、字体、布局和菜单进行 UIA / 截图验证。不可因缺少命令权限尝试绕过 Agent 限制。

## 证据保存、清理、版本

所有原始截图、服务器 general_log、端口和服务端参数、每项 SHA-256 位于 Git 忽略目录 \`temp/research/mysql-session-differential-20261028/\`，包含 \`evidence-sha256.txt\`（39 项）。原版资产未修改。完成实验后：

- 已关闭并丢弃 Navicat 本轮临时 SQL 查询，不保存；删除**仅本轮新增**的 Navicat 测试连接，保留历史 \`NyaDBAlignment Fixture\` 与原版现有 GUI/Xvfb \`:184\`。
- 本轮启动的 NyaDB 进程、Openbox 和 Xvfb \`:195\` 按保存的 PID 与进程参数匹配后停止；独立 HOME/XDG 配置、截图仍在忽略目录供复核。
- Docker 容器 \`nyadb-v-session-20261028\` 已停止并因 \`--rm\` 自动移除；其它容器未更改。
- 本阶段只新增研究报告，未修改 NyaDB Go 源码，因此没有新的编译/测试结果可宣称；上一阶段的代码测试成果继续有效，但不自动升级为 Windows 实机结论。

**总结：Linux 同条件“建立独立会话—执行变量查询—服务端 KILL—换新连接且变量丢失”已取得双方真实 GUI 的 O 和有范围的 V；“显式恢复”交互策略与 Navicat 静默继续 SELECT 不一致，需保留设计选择；Windows GUI 和严格像素差分仍是 Unknown / 未验收。**
