# MySQL 对象工作区 — 隔离 MySQL 8.4 原生实拍复验（2026-10-25）

## 执行条件

- 初始工作区为 `master` HEAD `199887dd5bd25b11`；工作树存在上一轮尚未提交的元数据、菜单、筛选改动，均保留并参与本次构建。
- 单独启动一次性 `mysql:8.4` 容器 `nyadb-object-review-20261025`，仅映射 `127.0.0.1:34306`；创建仅对 `nyadb_struct_a` 和 `nyadb_struct_b` 有 `SELECT, SHOW VIEW` 的只读测试用户。隔离测试容器使用空口令，**仅限本轮回环专用环境，不可用于生产**。
- Fixture 文件：`internal/services/connections/testdata/mysql_structure_fixture.sql`，SHA-256 `06c2f4c0e792fb2289e47a4b9900339ab2947597ce697c5b7a26f9402e374c2c`，MySQL 实测版本 `8.4.11`。
- 将历史研究使用的**非秘密**连接档案复制到自动清理的临时 `XDG_CONFIG_HOME`，把临时 appearance 设置为 `dark`；未覆盖用户连接和配置。
- 新建独立的 Xvfb/Openbox，桌面 1600×1000、Xvfb 报告 DPI 约 96；启动 **NyaDB Linux/amd64 新构建二进制**，操作“选中已有连接 → 点击已有连接右侧连接按钮 → 选择 nyadb_struct_a”，读取实际 fixture。
- 自动化实现：被忽略的 `temp/research/object-review-20261025/native_fixture_capture.py`，复验后自动关闭自己的程序/Xvfb/Openbox，并 `docker stop` 自有一次性数据库容器。没有启动 Navicat，也没有接触生产数据库。

## 真窗口截图、记录与校验

以下文件保存在 Git 忽略的 `temp/research/object-review-20261025/` 中，不复制原版资源到正式源码。

| 场景 | NyaDB 真窗口截图 | 确认情况 |
| --- | --- | --- |
| 连接前工作台 | `00-before-connection-client.png` | 只作为开窗前状态 |
| 成功连接与数据库树 | `01-connection-attempt-client.png` | 仅验证 NyaDB 侧 |
| 1280×800 已选 nyadb_struct_a 对象列表 | `02-objects-1280-client.png` | **I：实际 GUI + 实际数据库读取**；显示 `child`、`parent`、`parent_view` |
| 840×560 同对象列表 | `03-objects-840-client.png` | **I：实际 GUI**；导航、工具栏、对象列横向溢出由原生布局管理 |

每张截图另有全桌面 PNG 及 JSON 观察记录，包括窗口客户区几何、X11 DPI、抓图稳定帧、应用二进制 SHA 和实际操作流程。采集工具不伪造 `actual_scale`、真实解析字体及字号；这些字段仍记 `unknown`。图像内容由本轮实际检查过，不能仅依赖自动化脚本设定的 `ui_state` 宣称状态成立。

**服务端元数据独立检查**：`nyadb_struct_a` 包含 `child` 和 `parent` 两张 InnoDB 基表，`parent_view` 为 VIEW；截图中两表显示 `DATA_LENGTH=16384`，行数加 `≈` 标记、视图统计留空，自增列只在返回值存在时显示。TABLE_ROWS 是引擎估算，不与固定 fixture 的 INSERT 行数画等号。

本轮首次采集误进入“新建连接”窗口；第二次采集误选了 `nyadb_struct_b`，均不作为最终对象页证据。最终采集已更正右侧“连接”按钮、选中 `nyadb_struct_a` 和深色配置，原版历史截图没有被替换。

## 几何与差分结论

当前源码默认对象命令栏高 30 DIP、按钮高 25 DIP；列宽名称 265、自增 100、修改日期 130、数据长度 110、引擎 95、行 70、注释 180 DIP。两个客户端截图实测客户区尺寸分别为 **1280×800** 与 **840×560**，后者列范围被裁切并可通过原生表格横向滚动浏览。字体、应用缩放与原版 ROI 条件不足，**不做伪造的原版同尺寸几何或 MAE 对比**。

- **I**：当前 NyaDB 新二进制、真实隔离 MySQL 8.4、深色工作区、对象浏览以及小窗口均完成实际桌面验证。
- **O（历史）**：原版表对象入口和设计行为记录见 `docs/NAVICAT_TABLE_DESIGN_DIFFERENTIAL_2026-10-22.md`，不是本次采集。
- **V**：**BLOCKED**；未获得同一轮同 fixture 的 Navicat 成功读取、对象状态和字体/缩放可验证证据。
- **Unknown**：原版表行号和统计值精确语义、数据库/分类/对象菜单全部项、两端真实字体匹配、Windows 实际 GUI。

## 自动化与下个区域

上一轮新增 MyGo `ui.NewTester` 的 1280×800 / 840×560 对象筛选、键盘、元数据与菜单测试以及四项 Go/MyGo 工程门禁已经通过；本轮实际 GUI 补足了**已实现区域**的 Linux 运行截图证据。接下来单独实施数据表标签、双行网格和底部状态栏，再复验并独立 commit/push；字段信息与原生菜单另按区域处理。
