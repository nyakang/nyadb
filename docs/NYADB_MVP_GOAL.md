# NyaDB — MVP Implementation Goal

在本仓库实现一个基于 **Go 1.27+ / MyGo Native UI** 的原生跨平台数据库管理应用 NyaDB，首版支持 MySQL、PostgreSQL、SQLite。必须先阅读 `NYADB_TECHNICAL_DESIGN.md`，按其中约束执行。不要引入 React、Tauri、Electron、WebView、前端构建链。

## 编码原则

1. 使用独立的 `internal/db/contract` 和各数据库 `Driver` 实现，数据库核心包不得 import `mygo`。
2. UI 使用 MyGo 原生 `ui.Outline`、`ui/editor`、`ui.Table`、`ui.Split`；Widget 不直接执行 SQL。
3. 连接、元数据和 SQL 全部异步运行；通过 `Window.Update` 在主线程更新状态，使用 QueryID 防止晚到结果污染新 tab。
4. Query/Exec 有明确上下文，支持取消；所有 `RowStream` 始终 Close；结果有明确行数/内存/单元格展示限制。
5. 凭据只能存系统密钥库或内存，严禁落在 SQLite/JSON/日志；缺少密钥库时提示不持久化。
6. 所有标识符按方言转义，数据值使用绑定参数；任意查询不悄悄重写 SQL。
7. 为关键逻辑增加单元测试，集成测试以本地容器可选方式执行。提交前运行 `go test ./...`, `go vet ./...`, `go tool mygo vet`（如 CLI 可用）。
8. 固定 MyGo 与驱动版本，不允许依赖 `main` / `latest` 作为生产运行版本。
9. 优先跨平台，特别注意 Windows 11 amd64、Linux amd64、macOS arm64 的构建兼容性。
10. 源码注释精简、函数命名明确，功能按小粒度分阶段交付。每个阶段报告已完成、测试结果及剩余风险。

## 交付顺序

### M0 框架垂直验证
- 以原生模板初始化 Go 项目；设置应用名称 NyaDB、ID、icon、mygo.json，固定模块依赖。
- 显示原生窗口、侧边栏、编辑器和结果区占位组件。
- CI/本地运行 Windows/Linux 平台构建及 UI tester。

### M1 应用框架
- 实现 app State、tab workspace、持久化布局、原生命令/快捷键、SQLite schema migration（modernc.org/sqlite）、Connection Profile CRUD（初始不保存密码）。
- 将数据库核心与 UI 通过服务层隔离。

### M2 驱动和连接
- 实现 MySQL、PostgreSQL、SQLite 的最小 Driver/Session 契约；SQLite 使用可流式读取的 `modernc.org/sqlite`，不得用 MyGo SQLite 插件的缓冲查询处理任意用户 SQL。
- 实现连接建立、Ping、关闭、基本池管理、连接错误格式化。
- 连接/断开/超时不得阻塞 GUI。

### M3 Schema Explorer
- 按需获取 schema、tables、views、columns；树节点缓存、刷新和错误展示。
- 对不支持的元数据层级通过 Capability 决定 UI 展示。

### M4 SQL 编辑与执行
- 使用 MyGo ui/editor 的 SQL 高亮。选区优先执行；无选区按单条 SQL 运行，不使用分号字符串拆分。
- QueryJob 有 pending/running/completed/failed/canceled 状态，支持 Run/Cancel；错误和执行时间展示。

### M5 数据浏览
- `ui.Table` 实现 DataGrid；支持至少 100,000 行/50 列的表的服务端按页查看（无需一次性加载全部）。
- 展示 SQL 数据类型、NULL、BLOB 的安全预览，支持选区复制 TSV、调整列宽。
- 加载结果行数默认 500、可配内存上限，取消和关闭 tab 不泄漏资源。

### M6 Release
- 输出 Windows 可移植 ZIP 与安装包；Linux 标准包；macOS .app/.dmg。
- 编写 README、开发命令、测试方法、release checklist，核验附带动态库与许可证。

## 最终验收

- 使用最少两个真实数据库容器运行测试：MySQL 和 PostgreSQL；SQLite 用本地文件验证。
- 100k 行测试表读取、翻页、复制期间 GUI 保持响应。
- 各种失败模式：网络断开、认证失败、权限不足、超时、中途取消、关闭 tab，均能正确报错和清理。
- 无敏感信息泄漏；重启可恢复布局/连接档案/未执行的 SQL 草稿。
- 将执行命令、测试输出、风险与未实现功能记录到 `docs/implementation-status.md`。

**不要超前添加 ER 图、Redis、AI SQL、多语句执行、复杂数据编辑。先完成可以测试的纵向切片，再拓展。**
