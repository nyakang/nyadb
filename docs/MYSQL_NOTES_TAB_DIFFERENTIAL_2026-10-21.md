# MySQL「备注」标签页复刻与差分验证（2026-10-21）

## 原版运行观察（O）

- Navicat 18 原版在隔离的 Linux Xvfb `:121` / Openbox 中以正常试用流程运行；未修改原版文件，不连接生产数据库。
- MySQL「备注」为最后一个主标签。顶部保留连接示意图；当此前已开启 HTTP 隧道时，示意图仍显示 HTTP 服务器，切换标签不会丢掉隧道状态。
- 页面结构：左边距约 20 px 的「备注：*」标题、深色的大面积多行编辑区、底部提示「* 更改将应用于所有连接配置文件。」。在 900×700 的连接窗口中，编辑区约 860×475 px，宽度占满内容区。
- 初始备注为空；实测可输入两行文字，包含换行；从「备注」切到「常规」再切回，尚未确认保存的编辑仍保留。
- 本轮输入的是无敏感信息的测试文本，随后通过原版「取消」关闭，不向原版连接目录写入新的长期配置。原版实际落盘格式、字数上限，以及底部“所有连接配置文件”的精确作用范围尚属 **Unknown**，不据 UI 文本推断底层实现。
- 截图：
  - `temp/research/mysql-notes-20261021/navicat-default.png`
  - `temp/research/mysql-notes-20261021/navicat-multiline.png`
  - `temp/research/mysql-notes-20261021/navicat-switch-back.png`

## NyaDB 独立实现（I）

- 在现有 MyGo 主标签导航下使用原生 `ui.TextArea` 构建可多行编辑的备注页，沿用观察到的标题、占满内容区的面积和底部提示；没有使用 WebView 或 Navicat 私有素材。
- 备注属于连接档案而不是 MySQL 专有驱动选项，因此增设 `connections.Profile.Notes`。SQLite 本地配置 schema 从 v2 升级到 **v3**：增加 `notes TEXT NOT NULL DEFAULT ''`，用单个事务迁移。v1→v2→v3 与 v2→v3 都保留原有连接配置。SQLite 备注只是普通文本，**未经加密，不应填写密码、令牌或其他秘密**。
- 备注最多 64 KiB UTF-8，保留原样、空格及换行，拒绝 NUL 与损坏的 UTF-8。不会修剪前后空白，避免破坏用户的原始排版。
- 点击「确定」并成功保存后，备注进入 SQLite；编辑同一连接会加载旧备注，取消或通过 OS 关闭编辑器则放弃未保存修改；复制档案保留原备注。这是 NyaDB 的独立实现行为，不宣称已验证 Navicat 对应持久化细节。
- 当前「备注」页面对 MySQL、PostgreSQL、SQLite 共用，但本轮仅对 MySQL 的原版做运行对照。

## 测试和 GUI 观察

- `connections` 服务测试：MySQL 与 SQLite 多行/中文/emoji 备注创建、更新、列表、复制，64 KiB 上限与无效字符拒绝。
- `localdb` 测试：模拟 v2 文件升级 v3，原始非秘密配置保留，升级后支持文本存取；重新打开不会重复添加列；既有 v1 升级用例继续通过。
- MyGo `ui.NewTester`：标签导航、在多行控件中实际输入、页面切换保留草稿、确认后存储、取消不覆盖已保存备注、再次编辑与复制、超长备注留在编辑器并提示错误。
- 在隔离 Linux Xvfb `:129` 上真实运行 NyaDB 900×700 子窗口，验证备注尺寸、两行输入、切换标签回填；截图：
  - `temp/research/mysql-notes-20261021/nyadb-notes-default.png`
  - `temp/research/mysql-notes-20261021/nyadb-notes-multiline.png`
  - `temp/research/mysql-notes-20261021/nyadb-notes-return.png`
- 无密码、SQL 执行、生产连接或 Navicat 私有资源被复制。尚未完成逐像素 V 和 Windows/macOS 实机验证。

## 后续

MySQL 新建连接的主要配置页已全部完成第一轮独立实现。下一阶段建议采用 `docs/MYSQL_EDITOR_VISUAL_REFINEMENT_PLAN_2026-10-21.md` 的标准逐页收敛布局、状态、交互与性能；不将“编译通过”视作已完成原版差分验证。
