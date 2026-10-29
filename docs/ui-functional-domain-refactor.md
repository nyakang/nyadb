# 完整功能域整理与验证

本次将数据页和对象工作区收回完整功能模块。工作台负责原生窗口、连接管理、分栏和文档协调；分页、编辑、检查信息、对象菜单及异步状态不再分散在工作台、会话和独立小型 viewer 包之间。没有自动提交。

## 基线与范围

开始时的提交 `dfea747` 已包含尚未接线的数据页迁移，无法作为可运行的界面基线。使用上一可运行提交 `15d264e` 的独立归档 `temp/ui_baseline/`，通过其全部 UI 测试，并用相同的合成服务回复捕获基线。主目录保留 `dfea747` 的草稿，在此基础上修复，未重置用户代码。

MyGo 仍为 `v0.6.4-nyadb.4`。配置格式、`mysql_data_layout:` 偏好键、默认布局、快捷键、翻译和 bootstrap 入口保持兼容。新增两条失效会话提示的中英文翻译，通过 MyGo 工具生成访问器。

## 状态和接口

| 模块 | 拥有的状态 | 宿主接线 |
| --- | --- | --- |
| `dataworkspace` | 不可变连接/数据库/表身份，网格、页码/游标、规则、列布局、选区、编辑/草稿、复制导出、检查面板、私有通道和取消函数 | 注入会话快照、读取/保存/核验服务、连接查询、偏好及状态/激活/面板回调 |
| `objectworkspace` | 每连接对象列表的行/滚动/列布局/筛选/工具栏分类，菜单、表操作对话框，结构请求和程序快照文档 | 注入元数据/会话快照、结构读取、表操作执行和打开/选择/完成回调 |
| `sessionbrowser` | 连接、密码草稿、单调代次、打开数据库、元数据缓存和取消任务 | 移除列表行、滚动、列布局和分类；导航目标留在 shell |
| `shell` | 原生窗口、菜单工具栏、分栏、页签容器、连接管理和模块协调 | 现有 `workspace.go` 集中身份查询、激活、草稿检查、释放和批量移除 |
| 预览/设置 | 私有预览树、对象、网格、搜索和选区；设置页的只读示例编辑器 | 位于各自的页面代码，不借用真实导航或会话展示状态 |

`datainspector` 已合入数据模块；`structureviewer`、`programviewer` 已合入对象模块，旧目录及转发层全部删除。通用无状态属性行归 `theme`，字段和对象模型转换留在功能模块。

数据模块提供 `New`、`Open`、`Load`、`Refresh`、`Cancel`、`Consume`、`View`、`InspectorView`、`Modals`、`GuardLeave`、`Close`、`CloseAll` 和偏好注入。对象模块提供文档打开/展示/检查/关闭、列表选择/筛选、菜单/表操作及消费接口。网格、草稿、通道、取消函数和浏览器展示字段均私有；宿主测试通过服务回复和真实 MyGo 输入建立状态。

页签只保留 ID、标题、类型和 SQL/数据/对象文档。复用身份包括连接、数据库、对象类型、名称及展示用途，数据页与结构页分别复用。关闭非活动页签、批量移除保留活动文档 ID。绘制帧使用页签快照，释放切片引用不会破坏正在绘制的行。数据库裁剪也经过草稿决策；保存失败不会继续移除。断连保留 SQL 文本和显式恢复，关闭/退出释放 SQL 编辑缓存、任务和物理租约。

连接选择器的目录、搜索和状态合并到同一职责文件；编辑器表单和原生文件选择入口合并。SQL 的租约/生命周期、执行消费、语句定位和展示保持清楚的职责文件，没有按行数机械拆分。

## 异步和数据安全

- 启动前捕获服务方法、连接、代次、请求号；深拷贝游标的字节切片、行身份、指纹指针、编辑映射/二进制值和核验请求。
- 私有缓冲通道只由 UI 线程消费；停放的数据读取/保存、结构和检查面板结果等待原连接激活。
- 会话代次失效会结束加载并提示明确刷新。失效写入保留草稿、阻止后续离开动作和重放；不完整提交/核验结果不能清除未知提交保护。
- 保存取消等待执行器确认。对象表操作完成事件带原连接/代次/数据库/表/目标/操作；停放完成由宿主按原连接处理。退出取消对象任务并撤销迟到事件。
- 页读取、统计和字段读取独立取消；字段段落仍最多 1024 字符/字节，完整编辑仍最多 1 MiB。
- 关闭幂等，注销文档并释放页、元数据、编辑/导出回调、通道、选区和缓存。原生文件保存对话框不能由 Go context 强行关闭；关闭文档会撤销结果消费，并在对话框返回时检查取消，跳过尚未开始的写入。

## 测试归属和替代覆盖

没有为减少文件数删除应用行为测试。迁移保留原测试名称和断言，混合测试按实际职责拆分：

| 原测试职责 | 当前位置 |
| --- | --- |
| 数据网格、规则、布局、页脚、交互、编辑安全、SQL 文本复制、实际分页/字段类型/编辑集成 | `dataworkspace/*_test.go` |
| 表/字段信息、语言、统计、独立取消、停放及字段读取 | `dataworkspace/inspector_*_test.go` |
| 结构呈现/请求生命周期、程序身份/快照/500 行上限 | `objectworkspace/structure_*_test.go`、`program_*_test.go` |
| 对象元数据单元格、过时代次及停放表完成 | `objectworkspace/browser_metadata_test.go`、`table_actions_test.go` |
| 原生窗口、对象过滤与导航同步、菜单接线、数据页完整布局、表完成协调、跨连接身份和释放 | `shell/*_test.go` |

原 `TestSessionBrowserWorkspacePresentationAndOpeningAreIsolated` 更名为运行状态专属的 `TestSessionBrowserWorkspaceOpeningIsIsolated`，保留打开数据库隔离断言；列表展示隔离改由 `TestObjectBrowserPresentationBelongsToEachProfile` 验证，并保留 shell 跨连接导航测试。关闭后的元数据保留断言改为释放断言；断连时 SQL 文本保留、退出时缓存释放分别验证。待处理栏与草稿对话框同名按钮使用真实键盘焦点操作，避免误点背景按钮。

新增生命周期测试覆盖工作者请求拷贝、停放结果、失效保存禁止重放、检查面板释放、表操作取消和迟到结果、每连接列表状态、预览隔离、数据库裁剪的取消/保存失败/放弃，以及 SQL 断连与最终关闭的区别。

## 验证结果和证据

- `go test ./... -timeout 10m`：通过。
- `go vet ./...`、`go tool mygo vet`：通过。
- `CGO_ENABLED=0 go build -o build/nyadb ./cmd/nyadb`：通过。
- `go test -race ./internal/ui/... -timeout 20m`：全部 UI 包通过；后续清理变更另跑相关生命周期和终端关闭 race 回归，通过。
- `go tool mygo generate`、`go tool mygo i18n check`：通过，546 条消息。
- `/usr/bin/python3 -m unittest discover -s tests -v`：33 项通过。
- `git diff --check`：通过。

`TestUIFunctionalDomainVisualMatrix` 是可重复运行的 opt-in 捕获夹具。对象列表、数据页、结构页、程序页覆盖 1280×800 / 840×560、中英文、深浅主题及检查面板开关；连接选择器和编辑器使用 900×700 的原生子窗口内容入口。72 张基线/当前 headless 图逐像素一致，包括导航图标、网格和底部按钮。

证据在忽略的 `build/ui-refactor/`：`baseline/`、`current/` 和 `comparison.json`。运行：

```sh
NYADB_UI_DOMAIN_CAPTURE="$PWD/build/ui-refactor/current" go test ./internal/ui/shell -run '^TestUIFunctionalDomainVisualMatrix$' -timeout 5m
```

实际 Linux GTK/Xvfb 主窗口捕获覆盖两种尺寸，中英文当前版本已捕获；中文版基线和当前客户端区域逐像素一致。当前证据位于 `native-current-main/`、`native-current-en/`，基线位于 `native-baseline-zh/`。

实际 GTK 连接子窗口自动操作未完成：基线和当前版本都没有响应捕获工具的工具栏点击，窗口定位超时；没有将 headless 子窗口测试冒充 OS 子窗口验证。Xvfb 使用无 GPU 的软件路径，系统也报告未安装所请求的 C locale。Windows/macOS、物理桌面、IME、多显示器 DPI 和 GPU 路径未执行。

真实 MySQL 集成继续保留显式 `NYADB_MYSQL_*_IT` 开关，本次未启用，不访问用户数据库。合成协议/服务接线测试仍在普通测试集中执行。
