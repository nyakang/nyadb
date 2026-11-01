# 对象浏览区域状态与操作入口对齐 — 2026-11-01

未连接工作台现在保留一行紧凑的禁用工具栏，下方为空白。批量操作、非表对象新建、分组筛选、权限与草稿恢复进入“更多”菜单；分组编辑通过独立弹窗完成，不再常驻两排表单。

本次以已有 Navicat 18 Linux 运行截图 `temp/research/navicat-session-fixture-20261027/profile-disconnected.png` 为布局观察依据：原版未连接时无对象列头或批量/分组表单。此次没有运行 Navicat，也不宣称完整工具栏复刻、严格像素差分或 Windows 原生验收。本记录替代历史文档中“未连接时保留对象列头”的当前行为说明；结构页和已打开空数据库仍使用原有共享空表组件。

## 显示与交互

| 浏览范围 | 内容与操作 |
| --- | --- |
| 无档案、未连接、连接中、连接失败 | 无列头或缓存对象；数据库/对象按钮禁用；刷新图标使用灰色；原连接草稿仍可显式恢复 |
| 已连接，未选数据库 | 空白；可刷新数据库列表；账户入口要求能力、读取器和执行器均可用 |
| 数据库已选但未打开 | 显示“双击打开数据库”，无列头或缓存对象；选择、绘制及对象刷新不启动读取 |
| 已打开且加载中 | 保留列头和缓存对象；加载状态及取消始终可见，不能重复刷新 |
| 已打开且读取失败 | 保留列头和缓存对象；错误与重试始终可见 |
| 已打开的空库或筛选无结果 | 保留列头及空列表说明；新建与筛选按能力启用 |
| 已打开且存在对象 | 原有原生列表、选中、列布局与打开入口 |

常用对象操作使用 MyGo 工具栏溢出机制。“更多”固定在刷新左侧，普通状态只有一排操作。菜单顺序为复制/粘贴/删除所选对象、非表对象新建、分组筛选/管理、账户、草稿恢复。已有执行器、DDL 确认、取消与结果不确定时禁止重放的流程不变。

菜单保留打开时的连接、数据库、会话代际、浏览器实例和对象选择；收到选择时再验证原范围。分组弹窗拥有该快照，改变表格当前选择不会改变提交对象。切换连接/数据库、断开、代际变化、重置浏览器或 CloseAll 撤销弹窗，不保存未提交输入。分组名属于用户数据，不经过翻译；分组保存键及版本、对象剪贴板格式和公共接口均保持兼容。

## 回归与检查

`browser_view_test.go` 覆盖空状态不派发、已打开空库、缓存加载/错误/取消、多选批量入口、原菜单选择、过期菜单、跨连接剪贴板、能力不足、非表对象新建、连接级账户入口、原连接草稿、分组提交/重命名/删除/排除/取消与关闭。Shell 原先要求未连接列头常驻的测试已改为检查内容区保持空白。

MySQL/PostgreSQL/SQLite 的浏览状态与能力组合用注入式 headless 测试验证，不等于三个引擎的原生 GUI 实测。中文/英文、亮暗主题、900/320 DIP 对象区域、100%/125%/150% 设备缩放均有 headless 检查；Shell 检查 1280×800 和 840×560。

检查日志位于 `temp/research/object-alignment-20261101/`：

| 检查 | 结果/日志 |
| --- | --- |
| `CGO_ENABLED=0 go test ./...` | PASS，`go-test.log` |
| AGENTS.md 所列 15 个相关包 `CGO_ENABLED=1 go test -race` | PASS，`go-race.log`；shell 313.814s、dataworkspace 527.865s、objectworkspace 10.381s；race 要求启用 CGO |
| `CGO_ENABLED=0 go vet ./...` | PASS，`go-vet.log` |
| `go tool mygo vet` | PASS，`mygo-vet.log` |
| `go tool mygo generate` / `i18n check` | PASS；生成目录，1099 条 zh-Hans/en，`i18n-check.log` |
| `CGO_ENABLED=0 go build -o build/object-alignment/nyadb ./cmd/nyadb` | PASS，`go-build.log` |
| `git diff --check` | PASS |

## 原生窗口证据

正式入口二进制运行在独立 Xvfb/Openbox `:224`，客户区经 Xlib 验证为 1280×800。独立 XDG 配置下创建两个自有 SQLite 文件：一个含真实 `orders` 表，一个空库。没有连接用户已有数据库。截图工具复用 `NativeCapture`，环境缺少 scrot 时通过已有 ImageMagick `import -window root` 获取桌面图，再按实测客户区裁剪；没有修改工具源码或安装依赖。

以下真实窗口截图与动作/稳定帧记录均在忽略目录 `temp/research/object-alignment-20261101/native/`：

| 状态 | 客户区截图 |
| --- | --- |
| 未选择档案 | `00-no-profile-client.png` |
| 已选档案、未连接 | `02-selected-disconnected-client.png` |
| 已连接、未选数据库 | `03-connected-profile-client.png` |
| 单击选择但未打开数据库 | `04-unopened-database-client.png` |
| Enter 显式打开真实对象列表 | `05-object-list-client.png` |
| 更多菜单及 SQLite 不支持项禁用 | `06-more-menu-client.png` |
| 管理组弹窗 | `07-group-modal-client.png` |
| 已打开空数据库 | `09-opened-empty-database-client.png` |

每张 JSON 记录二进制 SHA-256、Xlib 几何和画面稳定性；`observed-steps.json` 按实际脚本操作整理了复核步骤。原生采图未测定字体及应用缩放，不宣称上述 headless 缩放组合的原生验证。`01-disconnected` 是初次采图时错误命名的未选择档案画面，不用作已选断开状态证据；有效证据为 `02-selected-disconnected`。
