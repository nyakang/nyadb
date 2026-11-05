本轮按 [全局 UI 审查](ui-global-audit-2026-11-05.md) 的 UI01–UI15 实施修复，保留默认字号下的紧凑工作台。使用 Go 1.27.3 和 go.mod 固定的 MyGo `v0.7.4-nyadb.1`，未修改依赖版本。

| 审查项 | 实施结果 | 主要验收 |
| --- | --- | --- |
| UI01 | 选择器树、过滤器、搜索条按文字缩放调整高度；卡片保留缩放后的 116×94 DIP 最小尺寸，列数按内容区计算；搜索条可换行。宿主页脚同步缩放。 | 选择器网格／树布局矩阵，原生选择器截图 |
| UI02 | SQL 运行、范围、取消优先排列；上下文限宽、省略并带完整 Tooltip，窄宽度时另起一行；次要命令继续使用原生溢出菜单。 | 长连接名 SQL 布局矩阵，运行按钮真实执行测试 |
| UI03 | 共享子页和现有原生页签统一 Selection/SelectionText；焦点颜色不再代替活动背景。 | 所有主题深浅模式与多种高对比度强调色的最终控件栅格及 4.5:1 色对测试 |
| UI04 | 静态字段、账户操作、错误、删除／确认说明补入 catalog；高级连接选项、账户操作和结果状态使用稳定值与显示标签映射。保留数据库和用户输入的原始值。 | MyGo generate、i18n check，中英文矩阵及原生高级页 |
| UI05 | 普通文字按钮自然测量；主工具栏项目以 64 DIP 为下限；图标按钮、信息栏和底部操作按文字缩放保持命中区域。DPI 仍由 MyGo 处理。 | 默认紧凑高度回归，大字号页脚及工具栏检查 |
| UI06 | 类型选择树改用 theme.Outline；节点身份含分区／供应商／条目，最近使用重复条目保持独立身份；搜索临时展开，清空后恢复用户展开。 | 方向键选择、供应商披露、搜索展开恢复及 recents 测试 |
| UI07 | 连接、设计、导入导出和账户字段使用原生 Form/Field/Fieldset；窄表单堆叠标签；过滤图标与文字纳入 CheckboxBase.Children。连接字段错误通过 Field.Error 显示并聚焦。 | 标签点击、字段错误位置、输入清除错误及原生复选框测试 |
| UI08 | 缓存导出显示格式／范围；账户显示账户、主机、权限及授权目标；对象搜索使用带提示文字的 SearchField。 | 大字号缓存导出／账户弹窗测试，原生导出截图 |
| UI09 | 设计子页、SQL 结果、数据编辑器和信息面板使用 TabsBase；对象过滤使用 Segmented，工具状态使用 Toggle。设计页和数据编辑器先校验候选页，失败时恢复原选择、文本和输入焦点并滚动到该字段。 | 原生页签方向键，非法索引前缀与非法十六进制草稿切页守卫、焦点恢复及无数据库写入回归 |
| UI10 | 普通对象／数据工具按钮改用 Toolbar 内 styled Button；compact checkbox 的特殊绘制与状态集中到 theme。移除 Toggle 的重复文字，保留能力控制和溢出行为。 | 紧凑工具栏、三引擎发布能力与导入命令一致性 |
| UI11 | 树和卡片共用支持状态说明；两个视图都有无匹配提示；未支持条目仍可选看，下一步依能力禁用。 | 筛选、无结果、未支持类型及选择回调测试 |
| UI12 | 分页、撤销／重做、停止、关闭、展开和菜单箭头使用现有 Lucide SVG 系统；新增 16 个固定版本资产。最终截图发现并修复 5 处资产文件名与 theme 注册名混用导致的空图标，补齐相关 Tooltip。 | SVG 解析／资产测试；全局静态图标引用回归；原生截图；资产来源见 internal/assets/lucide/README.md |
| UI13 | 业务弹窗使用 DialogBase 薄包装，单层 14 DIP 内边距与 Panel 表面、20 DIP 外边距；大型弹窗独立滚动正文，页脚固定。取消／关闭优先排列，主操作在右。 | 导入导出各步骤及滚动页脚回归，大字号账户／缓存导出弹窗 |
| UI14 | 设置页暂时隐藏对象 inspector，卡片宽度计算也采用当前页展示策略；返回对象页按原偏好恢复；长对象标题省略并带提示。 | 设置往返偏好恢复，工作台矩阵 |
| UI15 | ColumnMenu 接收 Context，按真实字体测量表头、类型及最多均匀采样 200 行缓存值；64–800 DIP 边界与超长文本提前停止。历史显示连接名称（#ID），删除连接保留 ID；SQLite 根页面移入对象属性。 | 比例字体／缩放／测量预算，历史同名消歧／删除连接，旧列布局解码 |

共享薄包装位于 `internal/ui/theme/controls.go`；特殊复选框位于 `compact_checkbox.go`。objectworkspace 增加可选 ProfileName 注入，SQL 沿用原有回调。未修改布局存储键和格式、数据库访问边界、会话 generation、SQL 租约／显式恢复、取消回执和数据草稿提交契约。对象关闭会释放新增的表单焦点句柄。

验证日志与截图集中在新的忽略目录 [build/ui-optimization-20261105](../build/ui-optimization-20261105/)。原审查目录保持不变。baseline 从 HEAD `db5fc6b` 构建；验收时实现尚未提交，随后按共享控件与资源、连接界面、查询与数据工作区、对象与宿主四组拆分提交。实现文件与验收清单中的文件哈希一致。

可先查看 [修复验收截图拼图](../build/ui-optimization-20261105/optimization-evidence-sheet.png)，包含 24 DIP 文字的选择树、窄窗口 SQL、中英文导出字段、设计器操作图标及正式程序大字号连接窗口。逐项检查、二进制与修改文件哈希、截图来源和 race 分组覆盖记录在 [验收清单](../build/ui-optimization-20261105/verification.json)。拼图第一项属于 headless 组件，其余为 Linux 原生窗口客户区。

| 检查 | 结果与证据 |
| --- | --- |
| `CGO_ENABLED=0 GOWORK=off GOFLAGS=-mod=readonly go test ./...` | 通过；go-test-final.log |
| `go vet ./...` | 通过；go-vet-final.log |
| `go tool mygo vet` | 通过；mygo-vet-final.log |
| `go tool mygo generate` / `go tool mygo i18n check` | 通过；1909 条消息，zh-Hans / en；i18n-check.log |
| 提交拆分验证 | 四个实现提交的独立暂存快照均通过 `go test -run '^$' ./...` 和相关包回归；commit-*-compile.log、commit-*-tests.log。实现文件与原验收哈希一致。 |
| `/usr/bin/python3 -m unittest discover -s tests -v` | 64 项通过；python-tests.log |
| Linux amd64 / Windows amd64 / Darwin arm64 编译 | 通过；nyadb-linux-amd64、nyadb-windows-amd64.exe、nyadb-darwin-arm64 |
| 选择器、连接表单、SQL 命令矩阵 | 每模块 48 组组合，全部通过；中英文、1280×800／840×560、字号 12／16、文字缩放 1／1.5、浅色／深色／高对比度。headless-matrix/ 共 240 张图片，acceptance-matrix.log、host-chooser-final.log。 |
| 全宿主对象／数据／结构／程序矩阵 | 64 场景及 8 张连接窗口图；host-matrix/、host-matrix.log。使用合成元数据与独立存储，属于 headless 证据。 |
| MySQL 8.4 / PostgreSQL 17 / SQLite 功能夹具 | 通过；unified-grid.log、unified-grid/。脚本创建并清理唯一容器／临时 SQLite 文件，覆盖共享网格读取、类型值、编辑写入、能力发布及服务功能。 |
| Linux 原生工作流 | before/ 与 accepted/；每语言 14 张场景截图，包含真实 SQLite 数据、记录表单、SQL、多结果、快照、字段查看、缓存导出及设计器。verification.json 记录数据库哈希不变和进程清理。 |
| Linux 正式程序启动与连接窗口 | production-validated/；无 fixture 环境变量，独立 XDG 配置，字号 12／16 的工作台、选择器、连接表单及高级页。两轮正常关闭 exit 0，进程清理通过；verification.json。 |
| 生命周期 race | 规定包列表的完整 race 中，shell 超过默认 10 分钟包时限；其他包通过，原失败日志保留在 race.log。shell 的 168 项已枚举测试分成四组，各 42 项，全部补验通过；shell-race-shard-0.log 至 -3.log、race-coverage.json。定向生命周期回归通过；lifecycle-race-final.log；最后的数据编辑器焦点回归通过 data-race-final.log。显式集成／截图测试仍遵循各自环境标志和原有跳过规则。 |

原生工作流使用仓库 fixture 命令打开真实工作区，再通过普通鼠标／菜单交互验证。中文设计器的索引页坐标在新版原生页签下重新校准，校准后的脚本保存在 `verify_final_native.py`。早期坐标脚本误拍到触发器页的图片仍留在中间目录，最终索引验收仅引用 accepted/。正式程序的独立配置启动／关闭及连接窗口捕获另存 production-validated/；较早生产捕获中的误标场景不作为最终验收证据。最后一次图标引用修正后重跑全量测试、静态检查、布局矩阵及三平台编译，并补跑主题、表单、设计器和宿主的定向 race（icon-migration-race-final.log）；最终截图对应重新构建的二进制。

矩阵对可见控件检查尺寸、重叠及主操作；滚动列表最后一个被视口部分裁切的条目允许通过滚动显示，不把它当作行高不足。截图中的原生字体、系统实际缩放等无法从 Xvfb 可靠获知的指标保留 unknown；字号偏好和 headless TextScale 单独记录。

Windows 与 macOS 只完成交叉编译，未做实机界面验收。原生捕获平台为 Linux X11/Xvfb/Openbox，不能替代真实显示器的 DPI／辅助技术检查。MySQL／PostgreSQL 的功能夹具和网格图片为实际服务加 headless UI，未记为原生服务器界面验收。此报告不将未执行的平台或操作标为通过。
