# MySQL 真实数据库对象浏览第一阶段（2026-10-22）

## 范围和证据等级

- **I — NyaDB 独立实现**：Go/MyGo 原生 UI，复用已经建立的 MySQL TLS 会话和生命周期管理器。
- **I — 服务层测试**：以隔离的 `database/sql/driver` 本地协议替身验证元数据 SQL、绑定参数、表/视图区分、超大结果上限；这些测试不代表真实 MySQL 服务器的跨版本行为验证。
- **I — MyGo 无窗口 UI 验证**：使用 `ui.NewTester` 注入隔离的数据库名称和对象列表，验证懒加载、树、表格、筛选、切换、缓存、刷新、错误重试、晚到请求取消与断开清理。可选环境变量 `NYADB_MYSQL_METADATA_CAPTURE` 可生成 PNG。
- **I — 真实 Linux GUI 验证**：隔离 Xvfb :135 实际启动 NyaDB，使用临时连接档案验证未连接空状态、连接入口和界面可用性。**没有把模拟测试器的已连接截图冒充为真实数据库的结果**。
- **V/Original Unknown**：本轮未启动 Navicat 做原版对象树差分，也未连接真实 MySQL/MariaDB 服务；不声称像素等价或端到端数据库兼容验证。

## 服务层：真实元数据 SQL

`internal/services/connections/mysql_metadata.go` 在 `MySQLSessionManager` 内新增：

`Objects(ctx, profileID, databaseName) ([]MySQLDBObject, error)`

该接口在已认证的会话上使用固定的 `information_schema.TABLES` 查询和绑定的 `TABLE_SCHEMA = ?` 参数，返回实际服务端提供的对象名称、类型（`BASE TABLE` → 表；`VIEW` → 视图）、引擎、注释。没有动态拼接数据库名或对象标识符，也没有读写任何表记录。

- 每次查询有 15 秒超时；会话 Disconnect/Close 时取消请求及释放 rows。
- 对非法数据库名称、未连接会话返回明确错误。
- 一次读取最多 10,000 个对象；超限返回 `ErrMySQLMetadataLimit` 而不是返回伪装成完整列表的部分数据。中心对象表格由 MyGo 原生虚拟化 `Table` 渲染。
- 这轮只读取**元数据**，不执行数据库切换的 `USE`，不影响其他请求的连接默认 schema；对象的实际查询始终通过数据库名参数限定。

## MyGo 交互与状态

- 主连接仍由已保存的 Active 配置建立会话，**用户必须主动连接**；未连接时对象面板保持空状态，完全不显示模拟表数据。
- 左侧使用原生 `ui.Tree` / `ui.TreeItem` 显示当前真实会话返回的数据库，支持展开收起及内建键盘操作；展开某个数据库后才加载该库的表/视图元数据，类别节点可继续展开对象名称。
- 中心区域提供数据库切换、全部/表/视图筛选以及“刷新当前数据库”；服务器级“刷新数据库列表”继续保留。数据库列表与对象元数据各有单独的刷新生命周期。
- 数据库级元数据按数据库名在当前会话内缓存；切换回来不会重复发起请求，除非用户点击“刷新当前数据库”。
- 只允许一个对象元数据请求处于当前 UI 的活跃状态：切换数据库时取消旧请求，以当前会话 generation、请求序号和数据库名共同验证结果，忽略已取消的迟到返回。
- 断开、重连、删除或修改正在使用的连接档案后，清空对象树展开状态、选中数据库和缓存，防止旧服务器对象误留在新会话中。
- 数据库列表刷新**失败**保留原先已加载的数据库名称和对象；成功且某数据库已消失，才清理对应缓存与选择。
- 左侧树展开渲染每类最多前 100 个叶节点，大量对象仍可在中央原生 Table 列表中查看，以免一次绘制过多树节点。

## 自动化测试

- `internal/services/connections/mysql_metadata_test.go`：使用实际 SQL 绑定参数验证特殊数据库名称不能注入、正确返回表和视图元数据、拒绝非法数据库名/未连接、超过 10,000 条时明确报错。
- `internal/ui/shell/mysql_object_state_test.go`：按需加载、双库切换与缓存、表/视图筛选、强制刷新、异步迟到结果隔离、读元数据报错后的重试、断开清理。
- `internal/ui/shell/mysql_object_refresh_test.go`：服务端数据库列表刷新失败时保留缓存；刷新成功后清理服务端不再存在的数据库。
- 工程门禁：`go test ./...`、`go vet ./...`、`go tool mygo vet`、`go tool mygo build`、Race Detector 和 Git 格式检查。

## 研究与 GUI 证据

`temp/research/mysql-object-browser-20261022/`（默认不提交临时截图）：

- `mygo-headless-objects.png`：**本地模拟元数据源**，已连接后的 MyGo 树、类别节点、对象表格与字段布局。
- `desktop-initial.png`、`desktop-selected-disconnected.png`、`desktop-failed-connection.png`：**真实桌面进程**，验证空状态、选择连接与无可用本机服务器时的失败表现。

## 未实现 / 下一步

1. 对真实授权的隔离 MySQL/MariaDB 服务器运行兼容性测试并进行 Navicat 原版对象树的差分观察。
2. Schema 下的列、索引、外键、触发器和视图定义；表结构与对象详情。
3. 表数据按需分页、SQL 编辑器实际执行/取消、schema 树多层级导航与权限不足状态的细化 UI。
4. 多连接同时在线的会话与缓存隔离、元数据大结果分页，以及不同 DPI/字体的视觉收敛。
