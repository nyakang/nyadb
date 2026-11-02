# URI、对象查找和收藏夹交付记录 — 2026-11-02

本阶段补齐完整任务清单中的常用入口。代码为 NyaDB 独立实现 **I**；没有启动 Navicat、连接生产数据库或新增 **O/V**。ClickHouse 优先新引擎、可配置 OpenAI 兼容 AI SQL，以及 PG/SQLite 深化、跨库工具和高级功能继续在整体范围中。

## 实现

- `connections.ParseURI` 支持 MySQL/MariaDB、PostgreSQL 和本地 SQLite/file URI。只接受当前实际执行的 TLS 参数，拒绝模糊端口、未括号 IPv6、控制字符、fragment、重复和未知选项。公开配置与临时密码分离；错误和格式化结果不回显 URI。Windows 本地文件路径按平台转换。
- URI 页面归 `connectioneditor`，Shell 承载连接类型页和原生子窗口标题。整个 URI 输入隐藏；验证成功后清除原文，将密码送入现有临时密码状态，默认不保存密码。导入只准备表单，不保存连接或自动连接；取消和原生窗口关闭清理输入。连接表单内原有独立 `URI` 按钮仍禁用，其导出/分享用途不在本次 URI 导入交付中。
- 全局数据库/模式查找归 `objectworkspace`，接通工具菜单及 Ctrl+Shift+F。支持名称/注释、字段名称/注释、已有程序及视图完整定义的搜索；可选范围、类型、包含/前缀/完全匹配和大小写。初始范围来自已加载的只读命名空间快照。缺少运行读取器时禁用入口，缺少字段读取器时禁用字段选项。
- 查找请求捕获完整条件及绑定原会话方法，三分钟超时；最多 50000 项和 16 MiB 的保留结果预算。显示片段最多 512 字符，完整定义保留在对象快照供只读打开。结果限额不限制驱动单次元数据读取或整份定义的峰值分配；跨大量表的字段查找仍可能较慢。
- 修改查找条件清除旧结果；打开结果验证原 profile、generation 和完整 namespace/ObjectRef。Shell 激活原连接并通过 `sessionbrowser` 打开数据库、加载完整元数据，独立创建/复用 qualified 数据或程序文档。搜索不把单个结果伪装成完整会话缓存，也不执行编辑器 SQL。
- 新增 `favoritesworkspace` 管理全局收藏：已有连接星标、对象引用、命名查询 ID 引用；搜索、打开、改收藏名称、移除确认、一级分组、移动和分组删除。对象/查询收藏名称独立于资产名称；连接星标仍使用原 profile 存储，移除走取消星标，不复制连接配置。连接星标不加入对象/查询的收藏分组。
- `favorites.Store` 保存公开导航名称和引用，不保存 SQL 正文或凭据。复用 `app_preferences`，没有数据库迁移。独立读取 token 与 SQLite 条件更新防止旧进程覆盖；外部删除后迟到发布不能重新插入。未知格式、未知字段、重复分组名、失效归属或超限目录拒绝写入，原记录保留。最多 10000 项、512 分组、8 MiB 编码目录。
- 分组名称去除首尾空白、不能为空且唯一；收藏别名允许同名。稳定 ID 保持选择，内置筛选同名分组仍可区分。分组删除后对象/查询转为未分组；删除收藏不删除数据库对象、命名查询或已打开文档。
- 离线可浏览所有收藏；查询打开重新读取最新加密资产，删除资产不能靠收藏重新创建。对象必须显式连上原连接后打开；通过绑定元数据读取重新解析完整定义，连接代次、活动范围变化和关闭撤销迟到回执。离线对象显示“离线”，不以未连接推断已删除。
- 顶层收藏夹菜单、对象右键、命名查询右键接通；菜单捕获原标签、连接、generation、目标 ID 和目录/资产修订。工作线程使用私有缓冲通道，由 UI 消费；关闭面板、替换存储或退出后旧回执不操作新范围。失败保留当前列表及编辑框，用户取消操作后可刷新并重新选择，不自动覆盖冲突。

## 验证

Go 1.27.3 / MyGo v0.7.3-nyadb.2，Linux amd64，`GOWORK=off`；HEAD `5960d26`，变更未提交。

- 全量 `go test ./... -timeout 20m` 通过；后续展示修正后重跑全量通过；最终工具及翻译变更后全量再次通过，Shell 76.430 秒、dataworkspace 60.136 秒。
- Shell 完整生命周期 race 通过，705.076 秒。queries 所属 SQL 工作区、objectworkspace、localdb、连接服务/编辑器及收藏工作区 race 通过；展示修正及新增边界后补相关定向 race：Shell 12.947 秒、收藏 5.019 秒、objectworkspace 1.848 秒、localdb 1.299 秒。数据工作区本阶段未修改，沿用上一阶段完整 race 证据，不把它称为本阶段重跑。
- `ui.NewTester` 覆盖 URI 入口/标题/无自动保存连接、隐藏输入及密码生命周期、离线查询收藏读取最新正文及删除资产、搜索匹配、字段能力不足、Enter 打开、取消/关闭/连接切换/代次、收藏 F2/Delete/右键分组、条件冲突与替换存储后的旧回执、同名内置筛选分组。
- 真实自有 SQLite + dbruntime Shell 测试覆盖视图 qualified 身份、完整会话缓存和文档复用；对象引用测试覆盖新定义与原/活动会话撤销。单元注入回归不当作原生桌面结果。
- `go vet ./...`、`go tool mygo vet`、翻译生成/检查（1282 条）及正式 MyGo 构建通过。最终工具、展示和翻译变更后的两项静态检查亦通过；locale 与工具包编译检查通过。
- `/usr/bin/python3 -m unittest discover -s tests -v`：47 项通过。新增采集确认按钮和目录身份校验回归；最初确认按钮识别把输入边框当候选，增加填充密度校验后通过。

## 原生证据与限制

`tools/capture_common_entries_acceptance.py` 只接受 `verify_entry_acceptance.py` 创建的隔离运行；使用正式二进制、私有 Secret Service 和自有 SQLite，记录 `/proc/<pid>/exe` 哈希及实际窗口几何。脚本核对 URI 没有新增 profile、收藏引用/分组稳定身份、重启恢复、分组删除后的未分组、收藏移除后的原表行数据；`entryfixture native-common` 用正式 WorkspaceKey 读取加密查询及脏 SQL 草稿。确认按钮只能在中部弹窗找到唯一填充按钮时操作，检测失败停止并记录失败。

原生 run `r-4dc2a2f9031a` 首次完整重放 exit 0，并在最终翻译构建后再次重放。截图和结果在忽略目录 `build/entry-acceptance/r-4dc2a2f9031a/evidence/`；最终正式二进制 SHA-256 为 `1ed975e1ee290682d821a9e2832846e00c981c61f9beaee7fd0e553299c377c5`，再次重放 `custom-df5f6282` exit 0。探索采集中曾使用错误的子窗口标题正则，超时退出；该次不计通过。字体、实际缩放、Windows/macOS 原生及 Navicat 原版差分仍 **Unknown**。对象查找只搜索中立元数据提供者当前返回的对象类型；没有据菜单名称宣称 PG 高级程序目录或新的对象编辑能力。


最终原生重放完整通过，分别采集 URI 隐藏输入/导入表单、真实 SQLite 搜索与打开、收藏分组、空/有匹配搜索、840×560、重启离线恢复、移除确认和移除后的页面。后端数据与加密草稿检查通过；原生阶段没有执行命名 SQL 查询。重启通过按进程身份 SIGTERM 后重新启动实现，不把它称为人工关闭守卫验收。

代表性证据：

- [URI 导入表单](../build/entry-acceptance/r-4dc2a2f9031a/evidence/common-native/uri-imported-client.png)。
- [全局查找结果](../build/entry-acceptance/r-4dc2a2f9031a/evidence/common-native/search-result-1280-client.png)、[打开真实数据](../build/entry-acceptance/r-4dc2a2f9031a/evidence/common-native/search-open-data-client.png)。
- [1280×800 收藏分组](../build/entry-acceptance/r-4dc2a2f9031a/evidence/common-native/favorites-grouped-1280-client.png)、[840×560 收藏列表](../build/entry-acceptance/r-4dc2a2f9031a/evidence/common-native/favorites-grouped-840-client.png)、[重启离线恢复](../build/entry-acceptance/r-4dc2a2f9031a/evidence/common-native/favorites-restart-offline-client.png)。
- [移除确认](../build/entry-acceptance/r-4dc2a2f9031a/evidence/common-native/favorites-remove-confirmation-client.png)、[验收结果](../build/entry-acceptance/r-4dc2a2f9031a/evidence/common-native-result.json)。

840×560 保留可操作底栏和列表，固定列宽使最后的分组列需要横向滚动/悬浮提示；不以该截图宣称所有列完整显示。原生本轮覆盖对象收藏；命名查询收藏最新正文/删除保护和菜单撤销由真实存储/Tester 回归覆盖，不把两者合并为同一真实桌面结论。

探索 run `r-87ecbb079d57` 和正式 run `r-4dc2a2f9031a` 均已按记录的 PID/birth 和容器身份清理。复核没有本次运行的活动进程或容器，临时秘密文件已删除；忽略目录中的截图、配置密文及运行清单保留。没有修改系统配置或用户密钥环，没有 commit/push。
