# MySQL 表结构查看器：真实数据库兼容性与原版差分验证进度（2026-10-22）

## 范围与证据分级

本轮延续 `docs/MYSQL_TABLE_STRUCTURE_VIEWER_PHASE2_2026-10-22.md`，核对 *真正的 MySQL 8.4* 元数据查询，并尝试在合法隔离研究环境对照 Navicat 18 的表结构交互。**没有改动 Navicat 原始程序及其私有资源，没有连接生产数据库。**

- **O**：Navicat 18 的原版 GUI 实际运行观察。
- **S**：原版静态证据。本轮无新增。
- **I**：NyaDB 独立实现或实际运行验证，需标明是真实服务端、驱动替身、测试器还是原生桌面。
- **V**：同一测试场景下 Navicat 与 NyaDB 差分确认。本轮表结构功能 **V 未通过、也未声称通过**。
- **Unknown**：证据不足或没有执行。

## 真实 MySQL 8.4 隔离实验

- 使用本机已有 `mysql:8.4` 镜像，以专用一次性容器 `nyadb-mysql-structure-it-20261022` 运行；端口映射仅是 `127.0.0.1:34306 -> 3306`，不暴露到公共网卡。
- `internal/services/connections/testdata/mysql_structure_fixture.sql` 仅用于专用容器内的 **nyadb_struct_a / nyadb_struct_b**；包含 InnoDB、视图、UTF-8 中文字段注释、整数自增、DECIMAL 精度、NULL/空字符串/表达式默认值、复合主键/唯一索引、前缀索引、复合外键及相同表名。
- 运行时单独创建只读 `nyadb_it_reader` 账户，只授予两个测试 schema 的 `SELECT, SHOW VIEW`；临时随机口令仅存储在被忽略的 `temp/research/mysql-structure-20261022/reader-password`，**未加入 Git**。
- 测试通过 `MySQLSessionManager.Connect` 建立的真实 `database/sql` 连接查询服务端，而非 `sqlmock` 或预置静态结果；SQL 仍是应用生产代码内的只读固定 `information_schema` 查询。
- 首次执行发现中文 `COLUMN_COMMENT` 为乱码，定位到 **MySQL 命令行导入测试 SQL 的客户端字符集**，修正 `SET NAMES utf8mb4` 并以 `--default-character-set=utf8mb4` 重新导入隔离测试库后复测通过。该问题不属于 NyaDB 元数据解析错误。

### 已通过的真实服务验证（I）

`TestMySQLStructureIntegrationMySQL84`：实际建立会话、读取两个数据库及对象列表，识别真实表/视图；字段 `COLUMN_TYPE`、精度/标度、SQL NULL/空字符串默认值、中文备注；复合 UNIQUE、索引前缀 `SUB_PART`；外键列次序和 CASCADE/RESTRICT；同名表按 schema 隔离；VIEW 的结构字段；不存在的对象、已取消 Context、已断开会话的显式错误。**复测 PASS。**

`go test ./...`、`go vet ./...`、`go tool mygo vet`、`go tool mygo build` 均通过，MyGo v0.6.0 Linux/amd64 构建成功。续轮启用 `NYADB_MYSQL_STRUCTURE_IT=1` 后，以真实 MySQL 服务端同时执行了 `go test -race` 的服务测试和 UI 测试，结果通过。

### 真实数据库 → MyGo 查看器验证（I，已执行）

`internal/ui/shell/mysql_structure_integration_test.go` 新增 `TestMySQLStructureRealDatabaseNativeViewer`，将真实会话中的数据库对象、字段、索引、外键送到 `ui.NewTester` 原生页面，测试加载、刷新、断开后的缓存清理；可选将截图写到研究目录。此用例有双重保护：必须显式设置 `NYADB_MYSQL_STRUCTURE_IT=1` 且提供测试账户口令，目标主机与端口写死为 `127.0.0.1:34306`，默认安全跳过。

**续轮在具名一次性 MySQL 8.4 容器内执行 PASS。** 采用 `NYADB_MYSQL_IT_PASSWORD_FILE` 从 Git 忽略的权限为 `0600` 的临时文件读取只读账户口令，测试命令与日志不包含密码；只在显式 opt-in 和固定回环目标下启用。MyGo `ui.NewTester` 真实读取并渲染了 8 个字段、复合索引、前缀索引及双列外键；测试还覆盖强制刷新、跨 schema 同名表打开/切换、缓存复用、VIEW 单独打开和断线清理。**这仍是无窗口 MyGo 原生渲染测试，不等于在实际 Linux 桌面与真实数据库交互的差分通过。**

真实 MySQL 渲染截图：`temp/research/mysql-structure-diff-20261022/nyadb-live-mysql-viewer.png`（项目忽略目录，不作为生产 UI 资源）。首次真实页面测试暴露了数据库对象缓存未初始化的 panic，现已在加载路径补齐空 map 的防御性初始化并回归验证。

## Navicat 18 运行观察（O）

采用 1600 × 960 的原有隔离 Linux Xvfb `:121` 研究桌面，该原版程序已由之前的研究运行并使用研究目录中的独立 `HOME` / `XDG_CONFIG_HOME`。原版程序和授权机制未被修改或绕过。

实际观察：

1. 原版主工作台为空连接列表，工具栏“连接”打开 **新建连接** 类型选择器；左侧类型/厂商分类及右侧 MySQL 卡片区域可见。
2. 选择 MySQL 后进入 **新建连接 (MySQL)**，具有常规、高级、数据库、SSL、SSH、HTTP、备注等配置页。
3. 常规页中配置一次性本地测试库的地址、端口与专用测试用户名；**取消保存密码**后点击“测试连接”，原版显示独立的 **连接密码** 提示窗口，要求再次输入凭据，提示中显示测试主机/端口及用户名。
4. 自动化输入该测试凭据遭遇工具安全检查阻止，故取消连接测试及新建连接操作。原版连接列表恢复为空，没有完成认证、数据库展开或表结构页面观察。

对应的忽略目录 `temp/research/mysql-structure-diff-20261022/`：

- `existing-navicat-121.png`：原版主工作台空连接。
- `navicat-connect-menu.png`：连接类型选择。
- `navicat-mysql-editor.png`：MySQL 常规连接表单。
- `navicat-test-connect.png`：不保存密码时的连接密码提示。
- `navicat-after-cancel.png`：取消后回到空连接工作台。
- `navicat-initial.png`：新的 Xvfb 启动尝试产生的无效黑色帧，**不能作为 GUI 成功证据**。

这些图像仅是**原版研究证据**，不复制进 NyaDB 的产品资源或公开项目资产。

## 当前差分矩阵

| 场景 | 原版 Navicat 18 | NyaDB | 结论 |
|---|---|---|---|
| 主工作台 / 新建 MySQL 连接入口 | 运行观察已确认 O | 既有原生组件与之前测试 I | 未进行本期像素差分，非 V |
| 不保存密码后测试连接 | 原版提示输入连接密码 O | NyaDB 提供一次性会话密码 I | 流程不同；未完成等价性验证 |
| 真实 MySQL 表/视图元数据 | 本次未连接到测试库，Unknown | 真实 MySQL 8.4 查询 PASS，I | 无从比较，非 V |
| 字段、索引、外键结构布局 | Unknown | 真实 MySQL + 原生 MyGo 测试器通过 I | **差分未完成** |
| 多表页签、刷新、错误、断线 | Unknown | 真实 MySQL 多表/视图/刷新/断线测试通过 I；错误状态由隔离替身验证 | **差分未完成** |
| 真实 MySQL 元数据在 MyGo 结构页显示 | Unknown | **已执行并 PASS（真实 MySQL + ui.NewTester）** | I，**尚非桌面 GUI 或 V** |

不因外观相似就声称 Navicat 的结构页内容、尺寸、工具栏、页面入口或关闭策略已复刻。

## 复现实验（需操作者明确选择测试环境）

**仅限一次性本地容器；严禁指向生产或共享数据库。** 脚本/命令不在测试之外运行，也不将真实口令加入源码。若已自行启动隔离实例，在专用 `mysql:8.4` 容器内通过：

```bash
# 确保连接目标确实是本地一次性 mysql:8.4 容器
docker ps --filter name=nyadb-mysql-structure-it-20261022

# 只在新建的空容器中导入一次；操作者以受保护的环境变量提供临时管理员口令。
docker exec -i -e MYSQL_PWD="$NYADB_FIXTURE_ROOT_PASSWORD" nyadb-mysql-structure-it-20261022 \
  mysql --default-character-set=utf8mb4 -uroot \
  < internal/services/connections/testdata/mysql_structure_fixture.sql

# 通过受信本地流程创建 nyadb_it_reader 并赋予两个测试库的 SELECT, SHOW VIEW。
# 在受保护的私有文件中提供一次性只读口令，不将密码写进命令行。
export NYADB_MYSQL_IT_PASSWORD_FILE=/data/kang/nyadb/temp/research/mysql-structure-20261022/reader-password
NYADB_MYSQL_STRUCTURE_IT=1 go test ./internal/services/connections \
  -run TestMySQLStructureIntegrationMySQL84 -count=1 -v
NYADB_MYSQL_STRUCTURE_IT=1 go test ./internal/ui/shell \
  -run TestMySQLStructureRealDatabaseNativeViewer -count=1 -v
```

测试环境必须确保该只读账户只对 `nyadb_struct_a` / `nyadb_struct_b` 有权限。 不要将密码写到 shell 历史或 Git。测试完成后可执行 `docker stop nyadb-mysql-structure-it-20261022` 清理独立临时容器，原有用户服务保持不动。

## 提交与下一步

- `0dbe253`：新增实际 MySQL 8.4 的只读元数据集成测试及安全隔离夹具，服务层真实验证通过。
- `19fec5e`：增加显式 opt-in 的真实 MySQL + 原生 MyGo 查看器用例；续轮完成真实执行 PASS。
- `cd405d9`：补齐首次对象异步加载的 nil 缓存防护，并允许测试从专用私有文件读取凭据，已独立提交并推送。
- `4d5c73b`：同名跨库表、多工作区、VIEW、缓存复用和断线清理的真实 MySQL 测试已通过并推送。

下一步应在**同一个隔离测试库**上取得 Navicat 18 实际表结构窗口的合法运行截图与交互步骤，再对字段/索引/外键三个页面逐项差分并修复。原版研究仍止于连接密码提示窗口：没有表结构页面的 O 证据，所以**无法宣称 V**。真实 Linux 图形桌面中运行 NyaDB 并连接隔离 MySQL 的手工操作仍待验证；本轮通过的是具真实服务端数据的 `ui.NewTester`。
