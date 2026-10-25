# 2026-10-25 — MySQL 对象工作区验证结果

配套实施与证据边界：`docs/MYSQL_OBJECT_WORKSPACE_ALIGNMENT_2026-10-25.md`。

## 本次实际执行的检查

| 检查 | 结果 | 备注 |
| --- | --- | --- |
| `go test ./...` | PASS | 本次对象元数据、导航、菜单、窄窗口自动化回归包含在内 |
| `go vet ./...` | PASS | Go 静态分析 |
| `go tool mygo vet` | PASS | MyGo 静态检查 |
| `go tool mygo build` | PASS | Linux/amd64；生成 `build/linux-amd64/nyadb`、deb 与 tar.gz |
| `go tool mygo i18n check` | PASS | zh-Hans/en，共 319 条消息 |
| `git diff --check` | PASS | Git 补丁空白检查 |
| `go test -race ./internal/services/connections ./internal/ui/shell` | PASS | 两包 Race Detector |
| 单项 `TestMySQLTableAndViewContextMenusUseCorrectObjectLabels` | PASS | 中央列表表/视图只读菜单 |
| `ui.NewTester`：1280×800、840×560 | PASS | 对象筛选入口；属于无真实窗口交互测试 |

`go tool mygo generate` 已生成翻译 accessors。正式源码、测试与 JSON 中英文目录一并保持在当前未提交工作区。

## 明确未完成

- 本轮没有启动 Navicat，未采集原版新截图，也未连接生产数据库。
- 本轮没有建立新的真实 MySQL fixture；新增元数据字段使用模拟驱动进行 SQL 扫描测试，并不等于真实数据库兼容性证明。
- 本轮未采集 **新编译的 NyaDB** 真实 Linux 或 Windows 桌面对象/数据/菜单截图。过去保留在 `temp/research` 的真实桌面图属于历史版本与历史场景。
- 字体、应用实际缩放、对象列表行密度、颜色值、布局物理坐标和严格对照的 ROI 指标均 **Unknown**；依据 `docs/UI_VISUAL_GATE_2026-10-25.md` 的条件，`objects` 严格视觉 V 为 **BLOCKED**，不得计算/伪造 MAE、2 DIP 或 3 RGB 色阶的通过结论。
- 阶段二数据表、阶段三右侧字段信息和阶段四完整工作区行为还没有在本次迭代单独修改或完成实拍差分，不属于本轮完成范围。

## Git 边界

检验开始前 `master` 与 `origin/master` 同步、HEAD 为 `199887dd5bd25b11` 且工作区干净。修改只涉及元数据服务、对象 UI 与状态、导航菜单、其测试、i18n 及上述两份文档。本轮不 commit、不 push、不改变 Git 历史。

## 后续门禁

优先拿到隔离只读 MySQL 的同 fixture 实测与两端有效读取状态；明确获准运行 Navicat 后，才可采集 O 证据并运行 `tools/strict_visual_differential.py` 的 `objects` 场景。V 条件成立前，对本轮结果只能标注 I。
