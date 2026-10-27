# NyaDB — 阶段 7 SQL 执行与阶段 8 原生 GUI 差分进展（2026-10-27）

## 验证边界与证据等级

- **I（NyaDB 独立实现）**：真实 MySQL 8.4 隔离 fixture 上执行查询/DML、取消执行、错误反馈；Go/MyGo UI Tester 中验证选中或光标所在 SQL、结果展示、行数/耗时、多连接作用域、关闭标签与断连资源清理。
- **I（原生 GUI）**：2026-10-27 在隔离 Linux Xvfb/Openbox 桌面上启动 `build/linux-amd64/nyadb`，采集稳定的 1280×800 原生窗口；无真实数据库连接。
- **O（历史原版观察）**：既有 `temp/research/gui-compare-20261020/`、`temp/research/ui-alignment-20261025/original/` 和既有工作区差分文档。历史截图可作设计参考，但不与本轮隔离 SQL fixture 同数据、窗口和选择状态。
- **V（严格双方差分）**：**未达到**。本轮没有新启动 Navicat 原版、没有同 fixture 的双方真实数据库读取，也没有 Windows 上本轮二进制的实际窗口验证。不得把单端截图或 Windows 交叉编译标成 V。

## 阶段 7：已实施与测试

| 项目 | 证据 | 结果 |
| --- | --- | --- |
| SQL 编辑器与标签 | `mysql_sql_workspace.go`、`model.go` | I：ProfileID/Database 与标签绑定，选中/光标所在 SQL 分发 |
| 服务层真实执行 | `mysql_sql.go` | I：每次预留会话连接，按 schema 执行，SELECT/SHOW 等结果读取、DML/DDL 影响行数、耗时 |
| 异步/取消 | UI RequestID + context + buffer channel | I：取消请求等待工作线程响应，明确写入操作可能已经提交；断连/关闭标签取消并丢弃迟到结果 |
| 数据量保护 | 服务层每次结果最大 1,000 行、8 MiB、单字段 64 KiB、512 列 | I：单行不突破总字节预算、边界单测通过；这不是数据库网络传输量上限 |
| 隔离 MySQL 8.4 | `TestMySQLExecuteSQLIntegrationMySQL84`，`NYADB_MYSQL_SQL_IT=1` | I：127.0.0.1:34307 一次性 Docker 测试，CREATE/INSERT/UPDATE/SELECT、错误、SLEEP 取消和恢复查询 PASS；容器已清理 |
| 原生 Tester | `TestSQLQueryUI*`、`TestSQLTabsProfileIsolationAndCloseCancel` | I：UI 运行/取消/结果/错误/断连/多连接身份及关闭标签 PASS |

SQLite/MySQL 真实生产环境未用于测试。SQL 结果是客户端有界物化，不宣称无限结果集、并发事务快照一致性或服务器端修改自动回滚。

SQL Tester 截图（非真实窗口）：`temp/research/sql-stage7-workspace-20261027.png`。

## 阶段 8：本轮可重复的新证据

| 目标 | 执行方法 | 结论 |
| --- | --- | --- |
| Linux 原生窗口启动、截图 | `python3 tools/capture_alignment_gui_linux.py --binary build/linux-amd64/nyadb --output temp/research/stage8-linux-20261027 --dpi 96` | **I PASS**：已读取稳定非黑帧；客户区 1280×800，虚拟屏约 96 DPI |
| Windows/amd64 构建 | `go tool mygo build -platform windows/amd64 -o temp/research/stage8-windows-20261027` | **编译 PASS**：生成 `NyaDB.exe`；当前 Linux 无 NSIS，未生成安装程序 |
| Windows 原生 GUI、菜单 | 需要 Windows 上运行本轮构建并抓取窗口 | **Unknown**：Windows Agent 在线，但采集时未运行 NyaDB；交叉编译不构成运行验收 |
| 资产树字距/行距 | `TestNavigationAssetVisualFixture`（MyGo UI Tester） | **I PASS**：最新字体 12.5 DIP、21 DIP 行距、层级缩进和右键菜单回归 |
| Navicat 严格像素差分 | 相同 MySQL fixture、DPI、字体、缩放、窗口、展开/选中/hover/菜单状态 | **Unknown / 未完成 V**：历史原版图片不能替代本轮双端观测 |

Linux 原生图片与元数据：
- `temp/research/stage8-linux-20261027/main.png`（1600×1000 虚拟桌面原图）
- `temp/research/stage8-linux-20261027/main-client.png`（1280×800 原生窗口客户区裁剪图）
- `temp/research/stage8-linux-20261027/main.json`
- binary SHA256：`f58af4b3c502d572cc0d601d78d740e8275c96a041c667fd9eed3e2736b37ea5`
- capture SHA256：`7587a83c747df3bf9b049937052f9cc4fd99e207e73e940260e1b784d1111f25`
- 实际字体/文字缩放在该截图元数据中为 **Unknown**，不能据此计算可跨设备复现的像素误差。

Windows 可运行文件：
- `temp/research/stage8-windows-20261027/windows-amd64/NyaDB.exe`
- SHA256：`3d413d43a7623888ebf4f589e679d3f184e5acc4017299e1bc0a612463f10c95`

## 下一步 V 门禁

1. 对 Navicat 和 NyaDB 配置**同一隔离 MySQL fixture**；不连接生产数据库，也不绕过任何软件授权。原版执行需用户明确授权。
2. 固定两端实际窗口 1280×800、缩放、字体/DPI、locale、暗色主题、数据库树和 SQL 标签选择状态；记录来源截图。
3. 双端各自真实执行只读 SQL，验证菜单/树展开/hover、SQL 工具栏、双行表头、结果网格和错误状态；记录像素 ROI/几何误差及复现步骤。
4. Windows 上对本轮 NyaDB exe 进行真实窗口操作，验证菜单字号行高、三角/选中、表格和 SQL 取消。编译通过不代表通过该项。
5. 对差异逐个创建 I 修正记录，只有双端同状态复验通过后方可标 V。

## 完整构建检查

2026-10-27：`go test ./...`、`go vet ./...`、`go tool mygo vet`、`go tool mygo build`、`git diff --check` 全部 PASS；Windows/amd64 MyGo build PASS。
