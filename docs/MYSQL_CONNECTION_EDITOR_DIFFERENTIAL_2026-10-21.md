# MySQL 连接编辑器：第一轮差分（2026-10-21）

本轮范围：选择 MySQL → 下一步 → 常规配置 → 标签切换 → 返回/确定/取消。数据库连接握手不在本轮范围内。

## 原版运行观察（O）

- 原版从 temp/navicat/squashfs-root/AppRun 在独立的 Linux Xvfb :121/Openbox 和隔离 HOME、XDG_CONFIG_HOME 下正常试用运行。没有绕过授权、输入凭据或连接数据库。
- 新建连接 (MySQL) 为原生子窗口，内容区约 900×700。截图：temp/research/gui-mysql-editor-20261021/mysql-general-observed.png。
- 顶部七个标签：常规、高级、数据库、SSL、SSH、HTTP、备注。
- 常规页从上到下：连接名称、主机、端口、用户名、密码、保存密码。初始主机 localhost、端口 3306、用户名 root、连接名称为空；名称输入获得焦点。
- 底部：左侧 URI、测试连接；右侧上一步、确定、取消；固定在窗底。
- 原版保存密码默认勾选，其凭据保管行为没有研究。历史高级标签截图位于 temp/research/gui-compare-20261020/，后续尚需逐项观察控件联动。

## NyaDB 实现（I）

- MyGo v0.6.0 的原生子窗口，在选择 MySQL 后动态更新标题为 新建连接 (MySQL)，编辑档案时标题为 编辑连接 (MySQL)。
- 类型选择器为 902×732；进入编辑时通过 Window.SetContentSize 调整为 900×700；返回时还原。
- 使用 MyGo TabsBase 和原生 TextInput、Password、Checkbox、Button 重新排列常规页，使用自有 NyaDB 图标而非原版私有资源。
- MySQL 的数据库选项卡暂可编辑现有 Profile.Database 字段；其他高级选项明确标注待实现。PostgreSQL 常规页保留初始数据库字段；SQLite 继续支持本地文件路径选择。
- 密码仅存在于当前 UI 会话，返回、取消、窗口关闭或保存后清除。保存密码禁用直到系统密钥库接入。URI、测试连接禁用，不进行数据库连接。
- 保持原有连接档案 CRUD 存储边界，不储存原始密码。

## 验证结果

- go test ./...：通过；新增 MySQL 编辑标签、校验、窗口标题、返回/清理密码测试，原有 Profile CRUD 回归通过。
- go vet ./...：通过。
- go tool mygo vet：通过。
- go tool mygo build：通过（Linux amd64）。
- Linux Xvfb :122/Openbox 实际运行新构建：连接选择器打开正常，选择 MySQL 后标题正确，窗口实测 900×700。截图：temp/research/gui-mysql-editor-20261021/nyadb-mysql-final-resized.png。
- 截图和隔离配置仅保留于忽略的 temp/research，不作为产品资产。

## 差异及下轮计划

1. 信息层级、字段顺序和内容尺寸已接近原版，但字号/字重、背景色、边框、原生窗口标题栏和示意图仍有差异；未通过像素级 V。
2. 高级、SSL、SSH、HTTP、备注目前仅能切换并提示待实现；数据库选项卡尚未复刻完整原版行为。
3. 原版保存密码默认勾选，NyaDB 为保障安全明确禁用；此处故意不追求行为一致。
4. URI 导入、测试连接和真实连接需在 M2 接入数据库驱动与异步会话管理后实施。
5. Windows/macOS/实体 Linux 桌面未进行本轮实际 GUI 验证。
6. 下轮优先逐项研究原版 MySQL 高级标签控件、默认值、禁用条件及字段语义，再扩展 Profile/AdvancedSettings 并实现。
