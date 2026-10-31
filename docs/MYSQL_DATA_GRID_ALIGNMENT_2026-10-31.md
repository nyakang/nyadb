# 数据网格 Navicat 行为对齐验收（2026-10-31）

本轮以行为与主要布局为验收标准，保留 NyaDB 自有主题与图标。证据保存在忽略目录 `temp/research/grid-alignment-20261031/`。以下区分原版运行观察 O、NyaDB 独立实现与自动化验证 I、同一夹具两边实际操作所得的有限行为对照 V。截图不作逐像素等价证明，编译不作目标平台桌面证明。

## 夹具与运行身份

复用已有一次性 MySQL 8.4.11 容器 `nyadb-toolbar-xml-20261030` 的 loopback 32790 端口、`table_edit_it` 数据库；本轮仅创建 `grid_align_20261031` 和 `grid_align_empty_20261031` 两个表。前者有 180 条确定性记录，后者无记录。fixture 包含中文及多行 COMMENT、空 COMMENT、LONGTEXT、DECIMAL(30,10)、超过 JavaScript 安全整数的精确金额、NULL、空串、DATETIME(6) 与二进制字节。SQL、归属、服务器 UUID 和清理语句保存在 `fixture.sql`、`fixture-manifest.json`。

原版使用既有 Navicat 进程及 display :222，未修改可执行文件或已加载库。AppImage SHA-256 为 `3b77fe1e3295dc3067402a0bab33a8ed1cbfbdea063d8b59e67c26a6480b6d63`；加载的 pristine `libcc.so` SHA-256 为 `6c45353fea04ba4b941aef2477fe9fa4997c77cee6380c3abc60f87f404e8e57`，见 `original-identity.json`。本轮采用 Linux GTK/X11、Xvfb/Openbox 图形桌面执行真实鼠标/键盘事件，不冒充物理显示器的视觉验收。

NyaDB 使用本轮独立的 display :224 与私有 XDG_CONFIG_HOME；仅保存一次性 fixture 连接，不保存密码。历史 `nyadb-final` 二进制只用于准备导航坐标，其 `nyadb-baseline-*` 截图均不作为候选实现的 I/V。

## 原版观察 O

| 行为 | 实际观察 | 证据 |
| --- | --- | --- |
| 单击表头 | 选整列 | `original-header-select.png` |
| COMMENT 悬浮 | name 提示显示中文 COMMENT；多行注释完整保留；无 COMMENT 的字段不弹提示 | `original-hover-name-1.1.png`、`original-hover-multiline-comment.png`、`original-hover-empty-comment.png` |
| 悬浮按钮 | 指针停留表头后出现 `…`；菜单只有升序、降序、移除所有排序、添加筛选 | `original-hover-name-0.1.png`、`original-header-more.png` |
| 显示注释 | 增加第三行；空 COMMENT 显示 `--`，多行内容在固定高度可截断 | `original-show-comments.png` |
| 非连续列 | 把 name 拖到 comment_text 后，Ctrl 选择 id 与 name，中间 comment_text 不选中；复制只输出两列 | `original-column-reorder.png`、`original-discrete-columns.png`、`original-discrete-copy.tsv` |
| 拖选边缘滚动 | 按住拖出底边后首行由 2 移至 5，并扩展到第 23 条；拖出右边后 payload 进入视口且矩形扩展 | `original-edge-drag-outside-held.png`、`original-horizontal-edge-drag-held.png` |
| 空表 | count 为 0，表头右键、`…` 及 COMMENT 提示可用；原版显示 `(N/A)` 占位行 | `original-empty-open-attempt.png`、`original-empty-header-context.png`、`original-empty-header-more.png`、`original-empty-tooltip-after-reenter.png` |

悬浮采样文件名中的 0.1/0.6/1.1 秒是请求等待值，`original-hover-times.json` 记录的实际截屏完成时间还包含捕获开销。此样本只能证明延迟出现，不能据此推断原版精确阈值。MyGo 的 600ms 是采用的默认值，不能写作原版已验证的精确时间。点击后首次空表截图没有提示；指针离开并重新进入后提示恢复，保留了两份证据以防误读。

`original-discrete-copy.tsv` 为 180 行、2056 字节，无末尾换行；其第二行保留 `中文客户`。此对照特意选择 id/name，避免多行文本、NULL、二进制与不同剪贴板编码解释掩盖字段选择问题。它不证明所有类型的跨应用复制格式一致。

## 候选实现与有限行为对照

两次候选均从本轮真实源码工作区构建，未冒充已发布 `.2` 的远端下载产物。完整依赖、补丁、自动化及打包状态见 [MyGo 发布候选记录](MYGO_DATA_GRID_RELEASE_2026-10-31.md)。

广泛桌面操作使用 `nyadb-candidate`，SHA-256 `1b91eaf0ffde7ce42677e927d0420163e39bb9976d2140b551285d869dcd604b`。最终候选 `nyadb-candidate-final`，SHA-256 `7bd521bc8acc76acda7fbd9739a3e858539e7cc265e49b931296199f45564c31`，包含最后的 native Shift 状态清理、可选回调 thunk 与暗色提示对比修正；关闭前一候选原生窗口后才启动最终程序。最终重跑单列/Ctrl 选列、180 行复制、空表和单/多行提示，并确认重启后的布局偏好。两轮不能混用二进制身份。

| 验收项 | NyaDB 桌面实际结果 | 分类与证据 |
| --- | --- | --- |
| 表头单击 | name 的表头与全部已加载真实记录高亮，记录次序保持，读取时间不变 | 有限行为 V；`nyadb-candidate-header-select.png` |
| COMMENT 提示 | 中文、两行 COMMENT 可见，空 COMMENT 无提示，空表也可提示 | 有限行为 V；`nyadb-final-comment-tooltip.png`、`nyadb-final-empty-tooltip.png`、`nyadb-final-empty-multiline-tooltip.png`、`nyadb-candidate-empty-comment.png` |
| `…` 与右键 | `…` 只有四项操作且保留原选区；右键已选列保留集合，未选列改为单列；右键含复制、列宽、注释、缩放等 | 有限行为 V；`nyadb-candidate-more.png`、`nyadb-candidate-more-preserves-selection.png`、`nyadb-candidate-right-selected.png`、`nyadb-candidate-right-unselected.png`、`nyadb-candidate-context.png` |
| 显示注释 | 第三行注释、空值 `--` 与字段类型第二行独立；重启仍恢复注释开关 | 有限行为 V；`nyadb-candidate-show-comments.png`、`nyadb-final-grid.png` |
| 重排及非连续复制 | name 拖到 comment_text 后，Ctrl 选择 id/name；中间字段无高亮、不进入剪贴板 | V；`nyadb-candidate-column-reorder.png`、`nyadb-final-discrete-columns.png`、`final-discrete-copy-comparison.json` |
| 原生边缘滚动 | 按住拖出底边后首条可见记录 1→26、选区到 44；拖出右边后 payload 进入视口，矩形扩展 | 有限行为 V；`nyadb-candidate-edge-drag-held.png`、`nyadb-candidate-horizontal-edge-drag-held.png`；未声称两边滚动速度相同 |
| 空表与筛选零结果 | 零行表具备表头选择、菜单、滚动条和提示；显式筛选无匹配值后仍保留原生表头，不生成记录 | 空表交互有限 V；筛选零结果 I；`nyadb-candidate-empty-grid.png`、`nyadb-candidate-empty-more.png`、`nyadb-candidate-empty-context.png`、`nyadb-candidate-filter-zero.png` |
| 添加筛选 | 展开并聚焦 name 的“精确匹配”空值草稿，保留 180 行和原读取时间；等待用户应用 | I；`nyadb-candidate-add-filter-draft.png`；查询次数的严格断言来自注入 loader 的自动化回归 |
| 显式排序 | 菜单选择 id 降序后发生新读取，首行 180，并显示降序箭头 | I；`nyadb-candidate-explicit-sort-desc.png` |
| 精确列宽 | 对话框显示当前 155；输入 0 被拒绝；输入 230 后原生 name 宽度更新，重启恢复 | I；`nyadb-candidate-width-dialog.png`、`nyadb-candidate-width-invalid.png`、`nyadb-candidate-width-applied.png`、`nyadb-final-empty-grid.png` |
| 缩放与隔离 | 菜单显示批准的八档；空表 150% 时表头标题/类型放大，切回另一个表仍为 100%；重启恢复该表 150% | I；`nyadb-candidate-zoom-menu.png`、`nyadb-candidate-empty-zoom150.png`、`nyadb-candidate-populated-zoom100.png`、`nyadb-final-empty-grid.png` |

最终 clipboard 的 180 行、2056 字节与原版完全相等，SHA-256 都是 `23d4a84b35c2343ab0f4a6088cf4b78ad144304a75c11e870c38d4658c899109`。独立 MySQL CLI 使用 `--default-character-set=utf8mb4 --batch --raw --skip-column-names` 读取 `SELECT id,name ... ORDER BY id`，只移除 CLI 固有的最后一个换行后，结果也逐字节相等，见 `runtime-final-identity.json`。两边都实际加载全部 180 行：原版 LIMIT 1000，NyaDB 页大小 200，未把 100 行与 180 行的不同分页结果强行比较。

首轮暗色单行 tooltip 使用与工具栏相同的颜色，背景边界难辨；native Bounds 检查确认尺寸正常。这是对比问题，最终使用已有 SurfaceHover 主题色。最终单行与多行截图确认背景可辨，未把先前现象误记作几何尺寸错误。

本轮源码审计与自动化覆盖还包括原生 Ctrl/Shift、右键内外、`…` 不干扰拖列/缩放、零行注册及边缘滚动；数据工作区覆盖 loader 查询计数、排序/草稿边界、非连续 CSV/JSON 导出及填充、连续可见列粘贴、统计行排除、隐藏列身份清理、宽度校验、偏好兼容及关闭/占位页回调释放。相应回归位于 MyGo `ui/grid_header_test.go` 与 NyaDB `internal/ui/dataworkspace/column_header_test.go`；600ms 测试推进 Tester 时钟，不用真实等待。自动化覆盖属于 I，不能扩大成所有功能已完成 Navicat 双边 GUI V。

## 验证边界与清理

没有 Windows/macOS GUI 实机结果；它们的 Go 编译结果由发布记录单独报告。未在物理显示器或真实桌面用户会话中复查截图；Linux X11 图形栈与原生事件的结果有明确环境边界。原版精确 tooltip 时间、像素等价、所有平台菜单绘制、跨应用全部复制格式、自动适配全部复杂字形及缩放内嵌编辑器的原版 GUI 对照，均未获得本轮双边运行证据。NULL、空串、大数、截断预览、隐藏列、草稿/刷新取消与关闭生命周期依赖已有和新增自动化回归，不能仅凭本轮 id/name 剪贴板 V 宣称全部类型与生命周期已双边验收。

空表模型遵循批准方案：NyaDB 保持零条真实记录，不生成原版 `(N/A)` 占位记录；表头照常交互。这是已接受的数据模型差异。

广泛候选与最终候选均从原生窗口关闭，退出码为 0；自有 :224 Openbox/Xvfb 已终止，见 `runtime-cleanup.json`。两个一次性表按复查要求保留，manifest 提供仅删除自有表的清理语句。私有配置与研究证据全部位于忽略目录，没有写入共享用户配置。

原版在本轮清理检查时 PID 1381940 已不存在，:222 无可见窗口；本轮未发出原版关闭或终止操作，因此无法另行确认其退出时的临时表布局保存行为。清理检查重新计算 AppImage 与 pristine libcc 的 SHA-256，与观察前一致。历史 :222 Xvfb 和 MySQL 容器保留，未终止或重建。

重现实验可复用 manifest 的一次性数据库及私有配置，从原生工作区构建候选后，依次打开有记录/空表；设页大小 200，拖动 name 到 comment_text 后，普通选 id、Ctrl 选 name、Ctrl+C。`actions-original.jsonl` 与 `actions-nyadb.jsonl` 保存实际 GUI 坐标动作；坐标依赖 1600×960 display、1280×800 窗口，换尺寸需重新定位。最终产物/截图/操作记录的哈希见 `runtime-evidence-sha256.json`。
