# NyaDB — MySQL 数据页工作区紧凑对齐与状态栏验证（2026-10-25）

## 本轮范围与基线

前置提交 `116807b` 已推送至 `origin/master`。本阶段独立完善已存在的 MySQL 只读数据页 UI：工作区 tab 标题、图标、紧凑操作栏、底部分页状态。仍复用 MyGo 原生 `ui.Table`、双行字段表头、固定行号、横向/纵向滚动与当前分页/字段分块服务，不改变 SQL 服务及数据库写入边界。

## 已实施

1. `workspace_tab_caption.go` 与 `workspace.go`：正常单库将数据页签缩短为表名（例如 `child`），结构页显示 `child · 设计` 并使用不同的原生图标；同名跨库标签自动附加 `数据库.表` 以保持区分。完整表身份与可访问 tab label、关闭操作 label 和 tooltip 仍保留；没有按标题字符串决定工作区身份。
2. `mysql_data_rule_editor.go`：去掉紧凑规则栏上重复的表图标及表名；保留“筛选 & 排序”、筛选/排序数量、返回、刷新、复制、清除及窄视口换行。
3. `mysql_data_status.go` 和 `mysql_data_view.go`：底部状态栏只在有成功页结果时显示已读取行数与读取范围；加载、出错、尚未加载分别有独立状态，不把没有加载结果的页显示为成功读取零行。
4. `mysql_data_workspace_alignment_test.go`：覆盖成功/零行/加载/失败状态、分页范围、原生网格及窄窗口底栏、数据与结构 tab 区分及跨数据库同名冲突。已有 tab 生命周期、异步请求取消、双行表头、Keyset/OFFSET、安全分块读取回归保留。

## 状态栏语义

| 条件 | 文本示例 | 说明 |
| --- | --- | --- |
| 未发起成功读取 | `尚未读取数据` | 不暗示查询完成 |
| 正在读取下一页 | `正在读取第 2 页…` | 不显示旧成功数 |
| 服务返回错误 | `读取失败 · 第 1 页` | 错误详情仍在数据工作区现有错误栏 |
| 查询成功但无行 | `已读取 0 行 · 第 1 页` | 仅在真实页对象存在后显示 |
| 查询成功，Offset 2、返回一行 | `已读取 1 行 · 3–3 · 第 2 页` | 只描述当前响应范围，不伪造总行数 |

## 本阶段 NyaDB 真实 Linux GUI（I）

使用 `temp/research/data-review-20261025/native_fixture_capture.py` 在独立 Xvfb/Openbox 中启动本轮构建的 Linux/amd64 NyaDB，连接新建的一次性回环 MySQL 8.4.11；从数据库 `nyadb_struct_a` 对象列表双击 `child` 表，显示确实读取到的三行数据，并采集：

| 场景 | 客户区 PNG（ignored） | 观察重点 |
| --- | --- | --- |
| 数据页 1280×800 | `temp/research/data-review-20261025/04-data-1280-client.png` | 真实表数据、双行字段名/类型、行号、紧凑标签与操作栏、底部 `已读取 3 行 · 1–3 · 第 1 页`、右侧字段信息 |
| 数据页 840×560 | `temp/research/data-review-20261025/05-data-840-client.png` | 操作栏响应式换行、网格裁切与横向滚动空间、底部页控件及右侧字段栏 |

每个客户端 PNG 配有原始桌面 PNG、X11 客户区实测坐标、观察 JSON（含二进制 SHA 和稳定帧）。fixture SHA-256 为 `06c2f4c0e792fb2289e47a4b9900339ab2947597ce697c5b7a26f9402e374c2c`。隔离服务仅允许测试只读用户访问 fixture，结束后清理自有容器和进程。未运行 Navicat 或访问生产数据库。

UI 截图中的表格双行表头保持在单一 `ListState` 列宽/排序/菜单布局中；并未为了压缩工具栏更换 WebView 或伪原生网格。窗口实际字体、Qt/MyGo 缩放和 Navicat 读取指纹仍未核验，因此**不能宣称严格视觉 V**，也不能根据单方图片报告 MAE/ROI/DIP 色差通过。

## 本轮验收

- `go test ./...`：PASS（含原有同名跨库设计页的完整可访问名称定位测试）。
- `go vet ./...`：PASS。
- `go tool mygo vet`：PASS。
- `go tool mygo build`：PASS，Linux/amd64 构建。
- `git diff --check`：PASS。
- `ui.NewTester`：新增状态栏、网格及标签可访问名称、1280×800/840×560 窄视口检查通过。
- Linux Xvfb 真桌面 + 真实隔离 MySQL：**I（NyaDB 原生实际运行）**，不是同条件 Navicat V。
- Navicat 同 fixture 实测、Windows 真 GUI、实测字体/缩放及严格像素差分：**BLOCKED/Unknown**。

后续继续按独立区域处理右侧字段信息、右键菜单和表头动作，各自测试并 commit/push。严格 V 需先获得经明确授权的 Navicat 同一 fixture、同状态真实读取证据。
