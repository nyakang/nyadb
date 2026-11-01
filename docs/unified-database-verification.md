# 三引擎统一验证记录

日期：2026-11-01。工具链为 Go 1.27.3、仓库 go.mod 固定的 MyGo fork。实施说明见 [统一架构](unified-database-architecture.md)。

## 运行与安全回归

| 范围 | 验证内容 |
| --- | --- |
| 公共契约 | NULL / 文本 NULL、空串、空 BLOB、原始二进制、int64 极值、精确 decimal、JSON、截断标记，两种行快照 JSON 往返和字节深复制 |
| 运行层 | 并发 profile、取消后迟到成功、旧句柄关闭、重连、终止、独立测试连接、能力交集和未实现功能拒绝 |
| 原会话绑定 | 管理操作公开/内部 generation 与上下文转换、旧管理句柄拒绝；SQL 租约懒预留/替换与物理连接隔离；事务 token 跨 profile/重连拒绝 |
| 编辑与事务 | 复合键、生成字段、视图和无可靠键只读、完整原行冲突、动态存储类型/大小写冲突、savepoint 请求回滚、跨请求事务、取消等待回执 |
| 不确定结果 | 改键/重复修改/删除的最终证据归并、丢失提交回执、核验不完整、原草稿保留、禁止重放和明确的只读核验 |
| UI | 三引擎共用 State/Document 网格和信息面板、能力隐藏、原子保存/事务提交/外部修改冲突、停放、非活动关闭保存、断开保留 SQL、原 profile 显式恢复 |
| 配置与兼容 | PG/SQLite 测试连接草稿快照、密码脱敏及迟到结果拒绝；完整命名空间缓存与刷新；旧 mysql_data_layout / engine_data_layout 键和格式；连接存储未迁移 |

主要回归来源为 contract/models_test、connections/descriptors_test、dbruntime/runtime_test / administration_test、engines/adapter_test、dataworkspace/engine_test / unified_test，以及原有 MySQL、SQL、信息面板和 shell 生命周期测试。公共类型原有别名与 MySQL 纯导入规则抽取由原有 MySQL/导入测试继续验证。

## 可重现的真实数据库验证

运行 `tools/verify_unified_grid_fixture.sh`。脚本创建随机唯一名称的 MySQL 8.4 与 PostgreSQL 17 容器，绑定随机回环端口并在 EXIT 清理；SQLite 使用 t.TempDir 中的文件。未复用或修改已有服务。

MySQL 显式开关包含 NYADB_MYSQL_CORE_IT、NYADB_MYSQL_DATA_EDIT_IT、NYADB_MYSQL_DATA_TX_IT。一次性夹具覆盖 DDL 往返、类型化 SQL/完整流式读取、对象管理、备份恢复、完整字段编辑、可靠键与冲突、锁等待取消、显式数据事务，以及 COMMIT 未转发/成功回执丢失的 update/insert/delete；故障注入中预期出现的 driver EOF 不代表回归失败。

PostgreSQL 使用 NYADB_POSTGRES_IT 的既有 PG17 夹具，覆盖精确 numeric、bytea、JSON、复合键、生成字段、视图只读、完整原行冲突和提交后核验。三引擎 TestUnifiedGridRepresentative 直接通过统一运行层及成熟网格完成实际加载、原子保存、显式事务、提交后独立读取与外部修改冲突；PostgreSQL 还终止测试自身的 SQL 连接，验证 schema 设置期间的连接丢失会撤销原租约并要求显式恢复。

三张 1280×800 截图由 ui.NewTester 使用实际数据库结果生成，已查看共享网格、列/选区/信息面板及功能显隐。这是可重复的原生组件渲染证据，不包含人工桌面交互或第三方软件差分验收。

| 引擎 | 本地截图 | SHA-256 |
| --- | --- | --- |
| MySQL | [mysql.png](../build/unified-grid/mysql.png) | `7b61e6cfc183662dd05593f4309cf852da09e2f35b7289782aabcb71b72424ab` |
| PostgreSQL | [postgresql.png](../build/unified-grid/postgresql.png) | `7e5d27271ef501cd4fa651a4b2560e439f54022d62f96becb462c7899b84ea8e` |
| SQLite | [sqlite.png](../build/unified-grid/sqlite.png) | `51ba9b7f82a2499a97b1566bafba8e50db29ad2e125754fa4a21abad44ecfc88` |

build 是忽略目录；重新运行脚本会生成新的唯一表名和时间，截图散列随之变化。PG/SQLite 设计、导入、备份、计数、字段分块没有在本次扩展，继续按能力关闭。

## 检查命令

完整功能测试使用 `go test ./...`。生命周期 race 覆盖 AGENTS.md 的包清单并加入 contract/dbruntime/engines，使用下面的行为名称筛选；完整功能覆盖由上一条命令承担。

```sh
go test -race -timeout 20m \
  ./internal/db/contract ./internal/services/connections ./internal/storage/localdb \
  ./internal/services/dbruntime ./internal/services/engines ./internal/services/mysql \
  ./internal/ui/shell ./internal/ui/connectioncatalog ./internal/ui/connectioneditor \
  ./internal/ui/navigation ./internal/ui/sessionbrowser ./internal/ui/sqlworkspace \
  ./internal/ui/dataworkspace ./internal/ui/objectworkspace ./internal/ui/theme \
  -run 'Session|Runtime|Adapter|Engine|Unified|Snapshot|Generation|Cancel|Park|Late|Disconnect|Reconnect|Recover|Lease|Transaction|Draft|Guard|Close|Commit|Verify|Verification|Save|Edit|Conflict|Layout|ConnectionTest|Namespace|Unsupported'

go vet ./...
go tool mygo vet
go tool mygo generate
go tool mygo i18n check
python3 -m unittest discover -s tests -v
```

Python 视觉工具测试需要 Pillow；本环境将依赖放在 /tmp/nyadb-visual-python 并设置 PYTHONPATH，33 项测试通过。翻译目录经 MyGo generate 生成，未手改 catalog_gen.go。

CGO_ENABLED=0，GOARCH=amd64 分别使用 GOOS=linux/windows/darwin 执行 `go build ./cmd/nyadb`，产物位于 build/unified-validation。静态检查、完整测试、生命周期 race、真实夹具及编译日志保存在同一忽略目录中。

## 最终结果

完整 Go 测试、上述生命周期 race、Go/MyGo vet、翻译检查、33 项 Python 视觉工具测试、三平台 CGO 禁用编译、一次性三引擎真实运行回归均为 PASS。最后一轮源码 gofmt 检查和 git diff --check 通过，生产代码中无 EngineState/EngineDocument/engineData 接线，SQL/data/session/editor 不再直接导入具体运行服务。验证日志及机器可读结果位于 build/unified-validation/verification.json。
