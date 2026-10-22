# MyGo Native Table 双行表头扩展与 NyaDB 接入验证（2026-10-22）

## 范围、基线与安全边界

- NyaDB `kang/nyadb`，Git 基线 `f51f0b6`。实现提交 `bcaa6f7`，Go 1.27.1+，MyGo v0.6.0。
- 原版 Navicat 18 的同尺寸研究资料沿用 `temp/research/mysql-keyset-diff-20261022/navicat-selected-data-1280.png`（用户已授权的隔离研究运行观察）；没有重新运行或修改 Navicat 文件。
- 本轮**仅扩展 Go/MyGo 原生表格控件及其 NyaDB UI 集成**；未修改 MySQL 会话、Keyset/Offset 逻辑、数据读取/分页、字段片段、TLS、数据库驱动或凭据实现。不执行表结构或数据写入操作。
- 证据：O=原版运行观察；I=独立实现与验证；V=直接差分通过；Unknown=缺少验证。当前不宣称完整像素 V。

## MyGo 原生 Table 扩展（I）

### 原组件证据和实施

MyGo v0.6.0 上游 `ui/table.go` 的 `TableColumn` 只有 `Title`/稳定 `ID`，表头固定为 `t.Space(8)`，主标题由 `coreText(...).SingleLine()` 绘制；列调整、排序、拖动和菜单都绑定到同一个以列 `ID` 为键的 header cell，横向滚动由 `head.followX = list` 同步。直接在 Table 外层覆盖一个独立表头会破坏此约束。

NyaDB 使用 `go.mod` 的相对替换 `replace github.com/egoist/mygo => ./third_party/mygo`，对 MyGo v0.6.0 的**本地 MIT 许可模块快照**仅在 `ui/table.go` 添加：

1. `TableColumn.Subtitle string`，不改变旧构造与 API；为空时维持原来的单行表头高度。
2. 在任何列提供副标题时，把**原生同一表头行**高度扩大至 `max(t.Space(8),t.Space(12))`，而数据行仍保留原有最低行高。
3. 原生列头内用 `coreColumn` 绘制一行较重的标题及一行较小的副标题，两个文本元素仍是同一个 keyed header cell 的子元素；排序箭头、右键菜单、拖动手柄、宽度调整与表头随表格水平滚动均继续使用**原来的 TableLayout/ListState 和节点**。
4. 标题仍用作可访问列标识，不把副标题混入列 ID 或 SQL 字段名。

镜像仅包含 `go mod vendor` 实际导入的 Go 源码（约 8 MiB、578 个文件）、原有 `go.mod` 和 MIT `LICENSE`。未纳入无关的官网、示例与上游测试。使用 `cmp` 对全部其他 575 个快照源文件对照本地原版模块缓存，结果 **0 个差异**，除 `ui/table.go` 的本地补丁以外未擅改依赖源码。说明位于 `third_party/mygo/PATCHES.md`。未来上游支持可复用的 Subtitle API 后应切回官方发布版本，以减少源代码维护成本。

## NyaDB 接入（I）

`internal/ui/shell/mysql_data_columns.go` 不再将列名和类型拼成 `id · bigint`；改为 `Title="id"`、`Subtitle="bigint"`、稳定的 `ID="id"`。开关“显示字段类型”直接控制副标题是否存在，自动返回紧凑单行高度。列宽和顺序、显示/隐藏列、筛选/排序、Keyset/Offset 翻页、上下文菜单与字段信息栏保持现有结构。

## 验证矩阵

| 场景 | 结果 | 证据 |
|---|---|---|
| 字段名第一行，字段类型第二行 | PASS | I，`ui.NewTester` 原生渲染；`native-subtitle-component-480.png` |
| 第二行点击仍触发相应列排序 | PASS | I，组件交互回归 |
| 副标题右键准确返回源列编号 | PASS | I，组件及工作区菜单测试 |
| 调宽后使用同一 `TableLayout.Widths` | PASS | I，原生拖动测试 |
| 横向滚动时标题、类型及行同步 | PASS | I，组件截图位置偏移回归 |
| 拖动列重排，排序 ID 稳定 | PASS | I，原生拖动重排测试 |
| 隐藏/显示字段类型后恢复单行/双行 | PASS | I，组件及 NyaDB 工作区测试 |
| 隐藏列、恢复列、复制字段名 | PASS | I，已有多规则工作区测试 |
| 真实 MySQL 8.4 的 `child` 三条数据双行表头 | PASS | I，`ui.NewTester` 真数据库 `mysql-real-subtitle-1280.png` |
| 本轮 Linux 原生可执行程序启动、连接配置窗口显示 | PASS | I，隔离 Xvfb 运行截图 `native-gui-editor.png` |
| Linux 原生 GUI 进一步输入、连接、数据页与列头操作 | Unknown | 自动化输入受到工具安全检查阻止；没有绕过 |
| 与原版 Navicat 18 完整像素级比较 | Unknown | 两边窗口装饰/状态栏及字段图标、字体和高度尚不同；不能声明 V |

截图均放在被 Git 忽略的 `temp/research/mygo-subtitle-20261022/`，未将原版截图、私有图标或任何字体文件纳入用户分发内容。

工程门禁：`go test ./...`、`go vet ./...`、`go tool mygo vet`、`go tool mygo build` 全 PASS；针对组件与 NyaDB 列头操作的 `go test -race` PASS。NyaDB 构建版本在 Linux/amd64 生成成功。

## 尚未解决与下一步

1. 本次解决的是**真正使用一个原生表头节点的两行文字**，不是整个数据页逐像素复刻。原版表头下方还有字段分类图标、完整声明如 `varchar(12)`、不同字体颜色和对齐；NyaDB 当前只展示从 `information_schema.COLUMNS.DATA_TYPE` 获得的 `varchar`、`decimal` 等基础类型。可在下一阶段依赖真实字段元数据扩充副标题为完整类型显示，但不能伪造长度。
2. 对 Navicat 字段信息栏中的非空、默认值、注释继续差分；该次没有修改其数据库服务。
3. 若 MyGo 上游增加正式 `TableColumn.Subtitle`，应把 `third_party/mygo` 替换为新的公共发布版本并运行跨平台构建；目前的本地快照增加维护开销。
4. Keyset 不保证并发 DML 时跨页事务快照一致性。没有可验证完整唯一键（或用户排序含可空字段）时仍继续 OFFSET 并警告风险；本轮未将任何数据库数据写入能力引入数据页。

## 清理

仅停止本轮创建的 `nyadb-mysql-mygo-subtitle-it-20261022` 一次性容器及记录的 Xvfb :159 进程。其他存在的 NyaDB 进程和原有 Navicat 授权研究桌面不修改、不终止。
