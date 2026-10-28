# NyaDB — MySQL 表右键菜单与基础表管理（2026-10-29）

## 目标与范围

基于 Navicat 18 用户提供的表右键截图（O：菜单项、分组与顺序），独立实施 NyaDB 表对象菜单和真实 MySQL 操作接口。原版各二级菜单内容及运行后的细节仍为 Unknown；不得以 NyaDB 行为推断 Navicat 原版实现。

## 实施（I：独立实现和自动化测试）

- `internal/ui/shell/mysql_table_actions.go`：左侧树与中央数据库对象表共用 `mysqlObjectContextMenu`，表/视图/程序采用不同范围；已有只读“打开表”和“设计表”行为保留。
- `internal/services/connections/mysql_table_change.go`：封装 `CREATE TABLE`（当前为明确告知用户的 InnoDB 单字段基础模板）、`RENAME TABLE`、`DROP TABLE`、`DELETE FROM`、`TRUNCATE TABLE`、`CREATE TABLE ... LIKE`、`INSERT ... SELECT`。
- 对已有表操作先从 `INFORMATION_SCHEMA` 查询确认 `BASE TABLE`，schema/table 名以参数绑定读取，DDL 使用受控反引号转义且枚举操作，不接受任意 SQL 字符串。
- 新建/重命名/复制提供目标表名称输入；删除/清空/截断必须再次输入原表名以确认；操作异步运行并绑定已有会话的生命周期，超时 45 秒。
- 成功后缓存失效、按需刷新对象列表；删除/重命名关闭原名称下已打开的数据和结构标签；清空/截断的数据页回到第一页加载。来自旧会话代际的完成结果不会应用到新会话或其他工作区。
- 高级入口（表配置、AI 建表、权限、导入导出、数据字典、脱敏等）目前明确禁用，无假动作。完整表结构设计器未实现。

## 安全/语义说明

- `DELETE FROM` 由 metadata pool 在默认自动提交配置下执行；它不是对 SQL 编辑器中现有事务的操作，UI 不声称事务会被保留或自动恢复。
- MySQL DDL 可能发生隐式提交；`TRUNCATE` 通常重置自增值，`DROP` 删除结构及数据。
- 复制结构使用 `CREATE TABLE ... LIKE`，并不等价于完整复制所有外键定义。结构+数据复制不是原子事务；`INSERT` 失败时保留已创建的新表，明确反馈并刷新列表。
- 服务端权限、并发 DDL、外键限制及 SQL 执行超时会在 UI 展示错误。网络断开时 DDL 是否已执行成功可能不确定，提示重新核对状态。
- 不自动重试、重放修改或创建 SQL 标签会话。

## 验证

| 项目 | 结果 | 证据级别 |
| --- | --- | --- |
| DDL 枚举 SQL、反引号转义、表类型检查、失败路径 | 单元测试通过 | I |
| 右键菜单、危险操作确认、错误反馈 | MyGo `ui.NewTester` 通过 | I（无窗口测试） |
| `go test ./...` | 通过 | I |
| `go vet ./...` | 通过 | I |
| `go tool mygo vet` | 通过 | I |
| `go tool mygo build`（Linux/amd64） | 通过 | I（构建） |
| 隔离 MySQL 真实 DDL 与权限/外键/回滚行为 | 尚未进行 | Unknown |
| Linux 真桌面菜单显示、Windows 菜单几何与字体 | 尚未进行 | Unknown |
| 相同 fixture 的 Navicat 18 双方差分 | 尚未进行 | V 未取得 |

## 下一轮验收

1. 仅在显式准备的 **可丢弃** MySQL 8.x fixture 上测试全部表操作、权限不足、外键、部分复制失败及断线/重连；不得使用生产库。
2. 完成原版各二级菜单的只读观察，严格区分 O 与未知行为。
3. 真实 Linux GUI 与 Windows GUI 检查菜单展开方向、宽度、行高、禁用态、确认窗口和刷新结果；形成双边同条件 V。
