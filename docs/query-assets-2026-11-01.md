# 查询资产管理与已有入口交付记录 — 2026-11-01

本次在基线 `7e71eec` 上独立实现命名查询管理和已有入口接线，工作区修改未提交。证据等级为 **I**；本阶段未启动 Navicat，未新增原版运行证据 **O** 或差分结果 **V**。

## 实现范围

- `sqlworkspace` 拥有原生查询表格、名称搜索、数据库/schema 与分组筛选、逐连接的选择/滚动/筛选状态，以及重命名、删除和一级分组弹窗。单击选择，双击或 Enter 打开，F2 重命名，Delete 先确认；右键包含打开、重命名、移动分组和删除。
- 顶部“查询”和连接下的本地查询节点在离线状态可用。导航只消费命名查询及分组快照，查询操作独立于数据库对象菜单。已连接的数据库节点次序保持兼容，本地查询节点追加在连接子项末尾。
- 打开查询按稳定查询 ID 激活已有 SQL 标签，或直接使用保存的原连接与命名空间创建文档。离线文档保留原身份，连接原配置后显式恢复物理会话，不自动执行正文。
- 分组归属连接，拥有稳定 ID；名称去除首尾空白、非空、逐连接唯一。创建、重命名、删除及移动查询通过加密目录保存。删组后组内查询变为未分组。合法名称与“全部数据库”“未分组”等默认筛选项重名时，筛选显示增加身份后缀以区分。
- “函数”进入已有函数列表；“其它”提供已有过程和事件列表。账户、备份、恢复及两种 SQL 转储复用已有服务、确认、执行和取消流程，按引擎能力和当前范围启用。
- 表右键“通过配置文件打开表”显示实际保存配置。Shell 注入列表及打开回调，`objectworkspace` 保存打开菜单时的不可变快照，`dataworkspace` 在原表上等待元数据并复用草稿、事务和字段校验。
- 主按钮及对象菜单的账户和备份可用性复用公共判断。菜单操作检查打开时的连接、命名空间、代次或资产修订；旧导航查询菜单选择不会重新激活已经离开的连接。

SQL 历史、设计器深化和更多数据库引擎未纳入本阶段。

## 持久化与文档边界

仍使用 `queries.Store` 和现有 `sql_documents` 表，无 SQLite schema 迁移。新增 `query_catalog` 加密记录以保存逐连接的分组和查询归属，旧查询没有目录时默认为未分组。既有保存、bootstrap 和布局存储接口保留；MySQL 与 PG/SQLite 的表配置键及序列化格式保持兼容。

SQLite 的条件更新按原 profile、记录 ID、kind 及预期修订原子执行，多个 Store 实例的冲突只能有一个成功。重命名、删除和保存先认证原记录，密钥不可用或密文损坏时不覆盖原记录。旧的注入仓库接口继续可用，其单 Store 回退由 Store 锁保护；跨 Store 原子性由正式 SQLite 仓库提供。

删除把原资产替换为 `query_deleted` 加密标记，标记不含 SQL 正文；迟到保存和旧 ID 的重新创建被拒绝。当前所有关联标签解除命名关联，保持正文、光标、结果和物理租约，后续保存需要重新命名。重启恢复也会解除已删除资产的命名关联。

重命名只更新资产名称，保留已打开文档正文和修改标记。只有正文保存基线与被重命名修订一致的文档才获得对应名称修订；旧正文不能通过一次重命名取得覆盖新正文的权限。失败及修订冲突保留原记录和弹窗输入，可刷新后重新选择，不自动覆盖。

后台请求捕获不可变值，结果通过私有缓冲通道由 UI 消费。关闭、连接切换和弹窗 token 检查阻止旧回执修改新弹窗范围；已完成的原连接资产操作仍更新其原连接缓存。

## 自动验证

环境为 Linux amd64、Go 1.27.3、MyGo `v0.7.3-nyadb.2`。初始检查使用已有 `go.work` 中的本地 MyGo 替换；该目录为干净的 `v0.7.3-nyadb.2` tag。交付检查显式使用 `GOWORK=off`，依赖 `go.mod` 中的发布版本。缓存位于 `/data/.cache/golang`。

新增回归覆盖：SQLite 关闭并重开后的分组恢复、跨连接隔离、旧查询兼容、条件修订冲突、删除标记与迟到保存、丢失密钥/损坏记录、脏文本与租约保留、关闭和停放回执、默认筛选项重名、菜单目录变化，以及表配置不可变快照与目标校验。

`ui.NewTester` 覆盖查询搜索、键盘、菜单目标、离线入口、原连接打开、查询标签复用、导航菜单切换、工具栏溢出、备份溢出后第二级菜单、能力不足时禁用，以及账户/备份注入调度和代次转换。管理入口测试使用注入运行时，没有连接真实 MySQL 或生产数据库。

| 检查 | 交付结果与日志 |
|---|---|
| `CGO_ENABLED=0 GOWORK=off go test ./...` | PASS；`build/query-assets/go-test-delivery.log` |
| 生命周期 race | queries、localdb、sqlworkspace、navigation、objectworkspace、dataworkspace 已通过；原合并运行的 Shell 超过默认 10 分钟，单独 `go test -race -timeout=20m ./internal/ui/shell` 重跑通过（682.931 秒）；`go-race.log`、`go-race-shell.log` |
| 最后入口修正后的 race | PASS：localdb、sqlworkspace、objectworkspace 全包及 Shell 查询/工具栏回归；`go-race-delivery.log`、`go-race-shell-entries-delivery.log` |
| `go vet ./...` / `go tool mygo vet` | PASS；`go-vet-delivery.log`、`mygo-vet-delivery.log` |
| `go tool mygo generate` / `go tool mygo i18n check` | PASS，zh-Hans / en 共 1125 条；`i18n-final.log` |
| `go tool mygo build` | PASS，最终 Linux executable / .deb / .tar.gz；`mygo-build-delivery.log`，产物位于 `build/linux-amd64/` |
| `git diff --check` | PASS |

## 原生窗口验证

在本任务专属 Xvfb / Openbox 桌面运行 NyaDB 的真实 MyGo Shell。配置、缓存和 SQLite fixture 位于忽略目录 `temp/research/query-assets-20261101/`，没有使用真实用户配置。测试 runner 使用注入的内存测试密钥；用户 SQLite fixture 为自有 `orders` 表及一条数据。窗口为中文、暗色、1280×800 和 840×560，采用原生软件绘制。

实际操作确认：离线查询与分组列表、名称搜索、Enter 打开、右键和 F2 重命名、连接原 SQLite 配置并显式恢复、显式执行 fixture SQL 得到 `draft_value=42`、重命名保留脏正文与结果，以及 Delete 确认后保留未命名草稿及结果。键盘探针显示该环境的 `xdotool key F2` 会携带 Alt；改用 XTest 发送无修饰的 F2 后，原生重命名弹窗验证通过。

主要截图及相应采集 JSON 位于：

| 场景 | 本地证据 |
|---|---|
| 离线查询管理 | `temp/research/query-assets-20261101/native/01-query-manager-offline-client.png` |
| 搜索 | `native/02-name-search-client.png` |
| 改名保留脏正文及结果 | `native/13-rename-preserves-draft-result-client.png` |
| 原生 F2 弹窗 | `native/15-f2-native-confirmed-client.png` |
| 删除确认 | `native/16-delete-confirmation-client.png` |
| 删除保留草稿及结果 | `native/17-deleted-query-draft-result-client.png` |
| 840 像素查询管理 | `native/19-narrow-query-manager-client.png` |
| 最终打包入口 smoke | `formal/02-final-package-query-manager-client.png` |

正式打包入口也在独立配置下启动并进入查询页面。该隔离桌面缺少系统 secret service，正式入口显示查询存储不可用；因此复杂资产操作的原生验证依赖测试密钥注入，不能据此宣称系统密钥环集成通过。420 像素工具栏的溢出调度使用 Tester 验证；正式窗口最小宽度的原生截图没有覆盖该尺寸。

这些截图是 NyaDB 原生运行证据，不能作为 Navicat 的 **O** 或差分 **V**。Windows/macOS 原生操作、系统密钥环、真实 MySQL 管理服务及其他缩放/主题组合仍未在本阶段验证。采集进程仅关闭本任务创建的实例，原版及无关数据库未操作；未执行 commit、push、reset 或历史改写。
