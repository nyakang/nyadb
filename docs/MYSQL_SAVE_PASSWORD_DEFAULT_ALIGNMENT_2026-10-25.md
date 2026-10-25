# MySQL 新建连接保存密码默认勾选对齐（2026-10-25）

## 证据

- **O**：现有 Navicat 18 Linux 原版运行观察记录（`docs/MYSQL_CONNECTION_EDITOR_DIFFERENTIAL_2026-10-21.md` 第 10–12 行，以及 `docs/UI_ALIGNMENT_2026-10-25.md` 第 91–99 行）确认：MySQL 新建连接常规页的「保存密码」控件默认启用并勾选。原始观察截图保留在 ignored 的 `temp/research/`，不会作为 NyaDB 资源。
- **I**：先前 NyaDB 为避免明文凭据保存而禁用该控件；`09a1f81` 已接入操作系统密钥库。本次新建 MySQL 表单仅在凭据存储接口存在时默认勾选。
- **Unknown**：原版对系统密钥库不可用、空密码及重新编辑历史无密码资产时的默认行为尚未观察，不能推断为 V。

## 行为变化与安全边界

- 从连接类型选择器进入新的 MySQL 编辑器：有密钥库存储接口时「保存密码」**默认勾选**；用户可以主动取消勾选。
- 无密钥库接口：默认未勾选，控件禁用，绝不静默把密码写入 SQLite 或日志。
- SQLite/PostgreSQL：不套用 MySQL 默认勾选状态。
- 返回连接类型选择器、取消、关闭编辑器均清除该暂存勾选状态；重新进入新的 MySQL 表单恢复新建默认值。
- 新建 MySQL 连接密码留空时仍可保存非敏感连接信息，不向系统密钥库创建空密码记录；非空密码仍受显式勾选及密钥库可用性约束。
- 不变更 SQLite schema、服务层存储结构、Navicat 原版资源或授权文件。

## 验证级别

- **I**：`ui.NewTester` 覆盖初次默认状态、点击取消保存、返回清理并重新进入、无密钥库及非 MySQL 的默认行为、空密码正常保存且不写凭据。
- 工程检查：`go test ./...`、`go vet ./...`、`go tool mygo vet`、`go tool mygo build`、`go tool mygo i18n check` 和 `git diff --check`。
- **V / Unknown**：本次对齐的是原版已观察到的一项默认交互，不表示字体、窗口缩放、整页像素或 Linux/Windows 实机密钥库交互完成严格差分验收。
