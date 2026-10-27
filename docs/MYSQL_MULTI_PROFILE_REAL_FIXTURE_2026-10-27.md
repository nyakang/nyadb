# NyaDB 连接资产第四阶段：真实双连接 MySQL 隔离 Fixture

日期：2026-10-27。

## 目的

验证第三阶段 `38e4c85` 的多连接工作台不仅在 mock opener 中可用，还能通过真实 `MySQLSessionManager` 同时保持两个实际数据库连接。所有操作都限制在专用、一次性 `mysql:8.4` 容器，且端口仅发布至 `127.0.0.1`。不连接生产数据库，不复用机器上正在运行的容器，不启动 Navicat 原版。

## 夹具与运行

- 脚本：`bash tools/verify_mysql_multi_profile_fixture.sh`。
- 专用容器：`nyadb-multi-asset-it-20261027`；如果同名容器已存在，**拒绝运行**。使用本机已有镜像 `mysql:8.4`，发布自动分配的本机回环高端口，脚本通过 EXIT trap 清理自身启动的容器。
- 仅在该容器内创建数据库 `nyadb_multi_shared` 及两张表 `a_orders` / `b_orders`。本机回环专用、容器删除即失效的两名用户分别仅获各自表的 SELECT 权限。
- 两个 NyaDB 档案均指向临时回环端口，数据库名称相同、账户权限不同；本轮没有生产凭据或私有配置。
- 新增可选集成测试 `TestMySQLTwoRealProfilesIsolateSameSchemaMetadata`，需要脚本设置 `NYADB_MYSQL_MULTI_IT=1` 与 `NYADB_MYSQL_MULTI_PORT` 才会实际连接。默认运行 `go test ./...` 时测试跳过，不会发起连接。
- 使用真实 `MySQLSessionManager`、权限过滤后的 SHOW DATABASES/SHOW TABLE STATUS 与原生 `ui.NewTester` 工作台。断开连接 A 时核验 B 的管理器连接仍然有效、B 的表元数据未受到影响。

## 验证结果

真实 MySQL 8.4 集成 **PASS**：

1. A 档案可以连接，读取同名 schema 中的 `a_orders`，缓存保留正确权限范围。
2. 在 A 保持连接期间打开 B，B 读取 `b_orders`；A 和 B 同时在 `MySQLSessionManager` 中处于已连接。
3. 切回 A 时不会看到 B 的表，断开 A 后 B 仍然已连接，且其 `b_orders` 缓存未丢失。
4. 首次实验自动化按相同文本点击数据库节点，命中了树中第一个同名数据库，产生非目标对象结果；修复测试改用 `databaseNode(ProfileID, database).key()` 明确目标后通过。**这是测试定位歧义，不可当作会话串用的证据。**
5. 脚本退出自动停止并删除自身创建的 MySQL 测试容器，未更改其他项目或使用已运行的共享数据库。

## 证据等级及剩余限制

- **I**：真实 MySQL 服务端读取、双连接生命周期、按档案隔离的工作台数据，已由可复现的脚本和 Go 集成测试验证。
- **O**：Navicat 原版多连接体验的历史截图仅为既有参考；本轮没有启动原版。
- **V**：尚未将 NyaDB 与 Navicat 放在相同两套 fixture、字体、DPI 和工作区视图下执行完整差分；不能声称通过 V。
- 该集成使用 `ui.NewTester`，并不等于实际 Linux/Windows 图形桌面的双连接手动/自动交互验收。后续应使用此独立 fixture 做真实 NyaDB GUI 检查，再开展经明确授权的原版同状态差分。
