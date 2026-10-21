# MySQL 活动连接配置差分与解析接口（2026-10-22）

## 原版行为（O）

在隔离 Xvfb :121 中，Navicat 18 运行观察：

1. 编辑连接时左侧「连接配置文件」默认「主要配置文件 (Active)」；右键次级配置中的「设置为活动配置文件」，`(Active)` 立即转移到次级名称后，主配置不再显示标记。
2. 在左侧单击不同配置行，只改变当前编辑内容和蓝色高亮，不改变已有 Active 标记。
3. 点击「取消」，新创建的次级配置和刚更改的 Active 都被丢弃；重开时恢复原先主要配置的 Active。
4. 点击「确定」，再次编辑该连接时，次级配置及其 `(Active)` 标记均恢复，而且进入编辑窗口时优先选中已保存的 Active 项。
5. 原版确有连接列表及详情中的「活动配置文件」表现；**未测试连接服务器和密码保存行为**，不可推断 Navicat 的内部会话调度实现。

证据图片：`temp/research/mysql-active-config-20261022/before-set-active.png`、`after-set-active-before-confirm.png`、`after-cancel-reopen.png`、`after-save-reopen.png`。本次原版临时连接 `Active Flow` 在观察后通过原版 UI 删除，完成截图在 `original-cleanup-complete.png`。

## 独立实现（I）

- 将**正在编辑的高亮项**（`connectionActiveConfig`）与**活动连接配置**（`connectionActiveTarget`）分开。单击左侧行只切换编辑内容；右键「设置为活动配置文件」只更新窗口草稿的 Active 指针。切换、重命名与取消不会擅自向本地数据库写入变更。
- 保存时将 Active 对应的**次级名称**写入同一顶层连接的 `Profile.ActiveVariant`；空字符串表示主要配置。名称唯一且校验大小写；重命名会同步更新引用，复制不抢占 Active，删除 Active 会退回主要配置，删除前面的配置会调整所保存的索引。
- 新增 SQLite **schema v5**，添加 `active_variant TEXT NOT NULL DEFAULT ''`，对 v4 和更旧档案向后兼容。次级配置及 Active 在同一行同一次事务的更新中一起提交。旧档案默认主要配置活动。
- 新增 `connections.ResolveActiveSettings(Profile)` 与 `Service.ActiveSettings(ctx, profileID)`。它们输出不含秘密的 `ActiveSettings`（连接 ID、主连接名、活动配置名、主机、端口、数据库、用户、TLS 及 MySQLAdvanced），供未来实际 MySQL 连接工厂使用；**不会自动建立连接**。不存在的 Active 配置、损坏的配置或非 MySQL 类型严格报错，不会偷偷退回错误的连接目标。
- 主工作台右侧连接信息改由此只读解析器显示「活动配置」及对应主机、端口、用户，避免在次级活动时误显示主要配置的连接参数。明确继续显示「未连接」。
- 侧栏保留 MyGo 原生 Button 与上下方向键焦点导航；首次打开时连接名称仅一次自动聚焦，后续不抢占列表的键盘焦点；方向键只切换编辑高亮行，不改变 Active 标记。

## 验证（I，部分 V）

- 数据服务：主要配置/次级活动切换、完整解析、复制、校验悬空引用、拒绝非 MySQL、不改动传入的切片。
- 迁移：独立 v4→v5 升级、保留现有 variants、Active 缺省为空、重复打开幂等；v1/v2/v3 升级路径继续通过。
- 视图及状态：右键标记 Active、选中与活动分离、确认保存、重新打开自动选中 Active、取消/OS 关闭回滚、重命名同步、复制保留、删除回退、失败不写库、键盘上下导航和焦点稳定性。
- 真实 GUI：Xvfb :129 的新建/编辑/右键菜单/标记 Active/选择主配置但仍保留次级 Active/确定/主工作台信息/重开编辑窗口，与原版已观察到的状态一致。证据文件 `nyadb-active-menu.png`、`nyadb-marked-active.png`、`nyadb-selected-main-active-alt.png`、`nyadb-inspector-active.png`、`nyadb-reopen-active-ready.png`。
- 工程门禁：`go test ./...`、`go vet ./...`、`go tool mygo vet`、`go tool mygo build`。

## 剩余差异与约束

- 原版 Qt 与 MyGo 原生上下文菜单存在配色差异；侧栏的字体、焦点描边、选中高光和高 DPI 下的像素差分仍待细化。
- 活动选择只准备未来连接参数；尚无 MySQL 真实会话创建、隧道握手、密码密钥库存储、会话重新连接或异步取消实现。不可把配置解析通过视为真实连接测试通过。
- 活动配置以当前唯一名称引用；已经实现重命名时同步指向，未来如支持远程同步/并发多窗口编辑，应再研究持久不可变的配置 ID 和冲突更新策略。
