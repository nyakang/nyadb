# MySQL 数据页严格视觉 V：同 DPI / 同窗口验收门禁（2026-10-22）

## 结果与证据边界

本阶段沿用授权研究环境中**已运行**的 Navicat 18（Xvfb :121），并在单独的 Xvfb :137 运行 NyaDB Native UI。复用现有的 MySQL 8.4 隔离测试 SQL，不执行生产库操作，也未修改 Navicat 程序、授权或私有资源。

- **O（原版观察）**：确认已有 Navicat 连接的地址为 `127.0.0.1:34306`，原配置用户名 `nyadb_navicat_observer`。旧连接在此前读数据时报过 1045（未携带密码）。
- **I（NyaDB/工具独立实现）**：Xlib 分辨率/DPI 探针、窗口同尺寸布置和强制前置证据检查的像素对照 CLI，6 个合成用例测试；MySQL 8.4 实测五组服务集成和三组原生 UI Tester 用例。
- **V（严格原版差分）**：**BLOCKED，不通过**。本轮原版密码输入对话框未能在受控工具边界内完成验证；取消编辑以保留原有研究连接配置。未采集两端同时成功读取并选中相同字段的真实数据页，不能将未连接对象页或旧截图当作 V。
- **Unknown**：Qt 与 MyGo 的实际字体缩放系数、可复现的同一 MySQL 查询时点，以及真实数据页菜单同状态像素差分。

## 隔离数据库与凭据保护

本轮 disposable Docker `mysql:8.4.11` 仅通过 `127.0.0.1:34306` 发布端口，复用已有的本地只读测试账户 `nyadb_it_reader`。凭据不写入仓库、命令参数、截图报告或运行日志；没有连接生产库，也没有新增弱口令账户。使用仓库现有 SQL：

- `internal/services/connections/testdata/mysql_structure_fixture.sql`
- `internal/services/connections/testdata/mysql_data_performance_fixture.sql`
- `internal/services/connections/testdata/mysql_field_types_fixture.sql`

一次性 fixture 初始化额外对 `nyadb_perf` 授予 SELECT / SHOW VIEW（只在 ignored 的本轮研究目录中）。不执行应用 DDL/DML。

尝试从 Navicat 原版现有连接编辑器切换到只读账户，发现取消“保存密码”后，测试连接要求再次输入会话口令。自动受控输入这一步受到工具限制，**没有绕过**；在弹窗中取消，并取消整个连接编辑器，原连接配置中的观察用户名保持原样。这也意味着 Navicat 的旧数据库连接页签被其“关闭并编辑”操作关闭了。研究 GUI 保持原版应用未重启。

## 已实际核验：X11 DPI 和原生窗口客户区

在两个桌面环境用 `python3 tools/x11_display_metrics.py :121` 与 `:137` 查询 libX11 的 `XDisplayWidth/MM`、`XDisplayHeight/MM`，不依赖系统缺失的 xdpyinfo；两端结果：

| 指标 | Navicat :121 | NyaDB :137 |
|---|---:|---:|
| X11 画布 | 1600×960 | 1600×960 |
| X11 物理尺寸报告 | 406×244 mm | 406×244 mm |
| Xlib 水平 DPI | 100.099 | 100.099 |
| Xlib 垂直 DPI | 99.934 | 99.934 |
| 窗口客户区 `xdotool` | 1278×775 | 1278×775 |
| 客户区起点 | (162,120) | (162,120) |

对原有 Navicat 研究窗口仅进行了常规 `windowsize`/`windowmove` 布置，用于匹配 NyaDB 的窗口外框 1280×800；没有改写原版文件。Xlib 的虚拟显示器 DPI 相同不保证应用内 Qt/MyGo 的实际缩放、字体与布局度量完全相同。

研究目录（**gitignored**）`temp/research/mysql-strict-v-20261022/`：

- `navicat-aligned-window.png`、`nyadb-aligned-window.png`：同外框尺寸的**未连接对象页**对照，不能用来给数据查看器打 V。
- `native/x11-nyadb-137.json`、此前 `temp/research/mysql-visual-20261022/compact-after/x11-navicat-121.json`：Xlib 虚拟尺寸和 DPI 输出。
- `blocked-differential.json`、`blocked-result.json`：真实证据缺失时严格工具返回 BLOCKED 的记录。

## 强制前置门禁的视觉对照

新增 `tools/strict_visual_differential.py`（Pillow），从 manifest 读取两张原始 X11 PNG、双方元数据及 1280×800 ROI；**不自行修改画布或数据库**。脚本在计算像素指标以前先核验：

1. 两端实时数据库读取均成功（`read_status=success`），且有真实观察证据文件。
2. 服务实例身份、fixture 指纹、行集指纹、库/表一致；`unknown` 不视为相等。
3. Xlib DPI 的两轴差值均不超过 0.5、客户区尺寸相同。
4. 选中字段、可见列、排序规则和菜单展开状态完全一致。
5. 截图和证据文件存在、画布尺寸一致，固定裁切为 `(160,80)-(1440,880)`，每个 ROI 有显式的 MAE 验收阈值。

退出码：0=PASS（上述声明与证据门禁通过、像素达阈值）；1=FAIL（前提齐备但像素未达阈值）；2=BLOCKED（前提不足，**不会计算像素差分冒充 V**）。这里的 PASS 是**按提供的证据 manifest 的验收结果**，实际 V 仍须人工验证证据真实性、时点及授权。

使用方式：

```bash
python3 tools/x11_display_metrics.py :121
python3 tools/x11_display_metrics.py :137
python3 -m unittest discover -s tests -p test_strict_visual_differential.py -v
python3 tools/strict_visual_differential.py \
  temp/research/mysql-strict-v-20261022/blocked-differential.json \
  --output temp/research/mysql-strict-v-20261022/blocked-result.json
```

本轮门禁实跑结果为 **BLOCKED，退出码 2**，原因包括双方无已确认的实时数据页与字段选择、无法证明行集与服务实例身份相同；没有输出虚假的 RGB MAE。6 个 Python 测试都通过（包括“截图完全相同但数据读取未成功必须 BLOCKED”和“状态相同但像素差异超阈值必须 FAIL”）。

## 测试

- `go test ./...`：PASS。
- `go vet ./...`：PASS。
- `go tool mygo vet`、`go tool mygo build`：PASS。
- `go test -race ./internal/services/connections ./internal/ui/shell`：PASS。
- 真实 MySQL 8.4：表结构、Keyset、数据分页、ENUM/JSON、大字段分块 5 组 PASS；真实 MySQL + `ui.NewTester` 的 3 组界面测试 PASS。
- 实测暴露原性能 fixture 在导入 Unicode LONGTEXT 时未设置 utf8mb4，测试误把 400,000 汉字读成 1,200,000 字符单位。补充 `SET NAMES utf8mb4` 后在全新隔离容器中五组 PASS；作为独立提交 `ead84d1`。这是测试数据编码修正，不修改服务分页、BLOB 逻辑或原版。
- Linux 真 GUI：NyaDB 运行于匹配 DPI 的 Xvfb :137，窗口位置和客户区实测；**原版数据页 V 未执行**，无密码时不伪造其行数据。

## 恢复 V 的条件

下次仅在已授权的隔离研究环境里，通过正常 Navicat 界面由操作者完成一次性只读测试凭据输入（建议不保存凭据），同时让 NyaDB 连接相同 disposable MySQL 8.4 实例。两端执行只读页加载，记录服务实例身份、行集指纹、当前字段、顺序、列宽、菜单状态与独立 UI 截图；确认真实字体/DPI 和客户区一致后，填充 manifest 并运行 V 门禁。若条件缺失应继续显示 BLOCKED，不允许使用模拟字段或旧的 `(N/A)` 行替代真实服务响应。
