# MySQL 表单公共几何差分与收敛（2026-10-21）

## 范围与证据

- **O（原版观察）**：Linux Xvfb :121 中的 Navicat 18 MySQL「常规」「高级」截图：`temp/research/gui-compare-20261020/navicat-mysql-settings.png` 与 `temp/research/mysql-advanced-20261021/navicat-advanced.png`。
- **I（NyaDB 独立实现）**：Go + MyGo v0.6.0 原生连接窗口（900×700 客户区），最终验证截图在 `temp/research/mysql-form-geometry-20261021/final-{general,advanced,ssl,ssh,http}.png`；启用状态截图 `final-{ssl,ssh,http}-enabled.png`。
- **V（本轮限定验证）**：统一布局矩形、原生开关联动和页面可见性已回归；尚未通过原版全像素级视觉验证，未进行 Windows/macOS 真机差分。

## 实测问题与修复

在 1600×960 Xvfb / Openbox 下分别采集同样的 900×700 MySQL 新建连接窗口，**先对齐客户区**，避免原版与 NyaDB 的窗口在全屏截图中存在约 5 px 装饰偏移。

| 项目 | 原版 O | 调整前 I | 调整后 I |
|---|---|---|---|
| 水平输入起点 | 约 x=664（1600×960 图的屏幕坐标） | 常规/高级约 x=664；SSL、SSH、HTTP 因 860 DIP 表单容器而偏左 | 五页全部使用 604 DIP 居中表单 + 166 DIP 标签列，输入起点对齐 |
| 常规连续字段行距 | 约 31 px | 约 40 px（行高32 + Gap8） | 31 DIP row，列 Gap0；名称与主机之间保留专门的 31 DIP 空白 |
| 高级连续字段行距 | 约 31 px | 约 37 px（行高32 + Gap5） | 31 DIP row，Gap0；数值、布尔、路径各行共享节奏 |
| 单行控件高度 | 约 25 px | 30 px | 26 DIP 原生 TextInput / Select |
| 文件浏览按钮 | 约 22×22 px | 30×30 px | 22×22 DIP，和输入框之间 7 DIP |
| 复选框 | 原版方框约 13–14 px | MyGo 原生16 DIP | 继续统一使用 MyGo 原生16 DIP，行高/纵向居中统一；更精确的外观仍待审查 |
| 顶部示意图后表单起点 | 不同 WM 标题栏会造成绝对屏幕坐标偏差 | 一些页面表单起点不一致 | 三种示意图共享 91 DIP 高度，并按客户区坐标比对 |

注意：以上尺寸属于现有环境下的 UI 研究基线，未声明 Navicat 在不同 DPI / 平台上具有完全相同的绝对像素值。仍需进一步核对字体、字重、禁用状态灰度和复选框的方框尺寸。

## 独立实现（I）

- 新增 `internal/ui/shell/connection_form_layout.go` 作为统一表单规则：`connectionFormWidth`、`connectionFormRow`、`connectionFormTextInput`、`connectionFormSelect`、`connectionFormBrowse`。
- 「常规」「高级」通过同一规则对齐标签列、31 DIP 行高、26 DIP 输入框与下拉框；高级配置复选框继续使用 MyGo 原生 Checkbox，保留原有开关联动和无凭据的生命周期。
- SSL/SSH/HTTP 将原来几乎占满窗口的 860 DIP 表单缩为 604 DIP，并改用上述共用输入/选择/文件浏览几何封装；原有原生文件选择器回调与秘密清理逻辑不变。
- 仍然不使用 WebView，不复制原版专有资源，不改数据库驱动、Profile 模型或授权相关文件。

## 测试和后续

- `ui.NewTester` 新增控件矩形测试 `TestSharedMySQLFormGeometry`：检查主机、端口、用户名、密码的行距，高级前三行的 31 DIP 节奏，SSL/SSH/HTTP 字段 x 坐标和 26 DIP 高度。既有交互回归确保原生开关、模式切换和配置安全行为不回退。
- 真实 GUI 在 Xvfb :129 验证：常规、高级、SSL、SSH、HTTP 默认布局及 SSL/SSH/HTTP 开启状态。原版 O 与独立 I 的截图位于以上研究目录。
- 工程门禁：`go test ./...`、`go vet ./...`、`go tool mygo vet`、`go tool mygo build`。
- 下一步关注更细的复选框样式、字段字体基线、禁用态灰度和悬停/聚焦态；另需回归非 MySQL 表单，并针对高 DPI / 不同系统主题进行额外验证。
