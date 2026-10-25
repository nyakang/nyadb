# MySQL「保存密码」Linux 原生截图与严格视觉门禁复核（2026-10-25）

## 范围和结论

本次检验的是提交 `6282934` 后的「新建连接 → MySQL → 常规」页面。**O（既有 Navicat 原版运行观察）+ I（本轮 NyaDB 真实 Linux Xvfb 原生进程）证明：「保存密码」默认可用且勾选的局部交互已对齐。严格像素 V 仍为 BLOCKED，不宣称全页通过。**

两端截图均为深色主题、中文、900×700 **客户区**、新建 MySQL、无数据库连接、无密码录入。原版图来自已有隔离研究会话；**本轮没有重新启动或操作 Navicat 原版**。NyaDB 使用新的独立 Xvfb/Openbox 和临时 XDG 配置，操作完成后采集程序清理其启动的进程。没有连接生产数据库或读取生产凭据。

## 来源与可复核证据

- 原版 O：`temp/research/ui-alignment-20261025/original/06-mysql-general.png` 及同名 JSON；SHA-256 `88019ff6ceb0fd6e062249407197f9069c8d801b89f4be274a1c6251812a7024`。
- 旧版 NyaDB：`temp/research/ui-alignment-20261025/native/connection-02-general-client.png`，当时保存密码禁用、未勾选。作为前后对照，不用作原版。
- 新版 NyaDB I：`temp/research/save-password-verification-20261025/native/02-mysql-general.png` 与 `02-mysql-general.json`；SHA-256 `83d2a05d7ed266cc2df26f8f674fcf4851b64a75f66eb530453f1cf2b3d23155`；可执行文件 SHA-256 `1af863f51d5820be0b6b45702906c73f950baccd3aaf7c77c44de482c2ad4707`。
- NyaDB 启动与打开 MySQL 的步骤保存在 ignored 目录的 `steps-editor-active.json`。捕获脚本用 `active_window` 等待同一进程的原生子窗口获得焦点、核对窗口标题，采集四帧稳定非黑屏客户区，而非 `ui.NewTester` 截图。
- 图像诊断：`temp/research/save-password-verification-20261025/{verify.py,measurement.json,manifest.json,strict-result.json}`。研究截图、临时脚本和原版内容均在 ignored `temp/research/`，不打包、不同步到 Git。

## 已测物理像素指标（不是 V）

| 对比项 | 测量结果 |
| --- | --- |
| 客户区尺寸 | 原版 / NyaDB 均为 **900×700 px** |
| 五个输入框 x / y / 宽 / 高 | 单分量最大绝对偏差 **1 px** |
| 表面背景 RGB | 两端均 **49,54,59** |
| 输入框填充 RGB | 两端均 **35,38,41** |
| 底栏背景 RGB | 两端均 **49,54,59** |
| 常规表单指定 ROI（140,120–755,340） | 原始 RGB MAE **2.8433**，仅诊断 |
| 表单以下空白表面 ROI（150,350–750,620） | 原始 RGB MAE **0**，仅诊断 |
| 新旧 NyaDB 保存密码区域 313,312–398,336 | **537** 个像素有 RGB 差异；直接查看新图可见勾选态 |

注意：ROI 大面积空白会稀释 MAE；选用自有 NyaDB 图标，不能拿低 MAE 宣称整页字体、图标和行为完全相同。输入框几何在设备像素中接近，不自动证明 DIP 对齐。复选框方框与字体字形仍可能存在平台渲染差异。

## 严格门禁

使用既有 `tools/strict_visual_differential.py` 的 `connection` 场景和本轮采集证据，`strict-result.json` 返回：

- **BLOCKED**，不是 PASS 或 FAIL；结果 `regions: {}`，没有严格门禁的像素验收度量。
- 原版三项缺失：`font_family`、`font_size_dip`、`actual_scale`。
- NyaDB 同样缺失这三项。旧证据中仅可确认加载字体集合，不能证明每个控件实际解析字体。
- 两端 X11 DPI 差值在既有门槛内，但 DPI 一致并不能替代 Qt/MyGo 的实际应用缩放或控件字体测量。

**现阶段仅把默认勾选行为认定为 O/I 对照一致，不提升到完整 V。** 不修改门禁阈值、不推测字体或缩放、不伪造元数据。

## Windows 实机状态

本轮查询到在线 Windows 桌面 Agent（窗口 DPI 报告为 120）；其窗口列表没有 NyaDB 或 Navicat 进程/窗口，且可启动应用列表为空、远程命令不可用。本轮因此**没有采集 Windows 上的新版本 NyaDB 或 Navicat 截图**，也不能把 Windows 交叉编译结果记为桌面 GUI 验收。

后续达成严格 V 需要在同等 DPI/主题/语言下双方真实窗口、可复核的 Qt/MyGo 实际应用缩放及单控件解析字体和 DIP 字号证据，再按现有严格门禁逐区域验收；Windows 差分另需可启动的两端应用或已授权的桌面测试环境。
