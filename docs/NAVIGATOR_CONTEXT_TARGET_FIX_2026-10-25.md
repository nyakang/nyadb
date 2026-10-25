# 导航右键命中与选中目标修复

用户反馈：未选中的目录／连接似乎需要先左键选中，右键才生效。

本轮在隔离 Linux Xvfb 的真实 NyaDB 窗口复现了两个问题：

1. 菜单绑定的行内容只占图标和名称的宽度，名称右侧留白没有菜单命中区。
2. 原生事件合并时，Outline 可能在菜单回调选中目标后应用先前的行选择，导航同步又将它恢复为旧对象。出现连接菜单已经打开，但目录仍高亮、信息面板仍显示目录的状态。无窗口测试逐次处理输入，原先没有覆盖这个原生时序。

修复保持 MyGo `v0.6.3-nyadb.2`：

- 目录、连接、数据库／分类／对象的内容行填满可用宽度，右侧留白可以直接右键。
- 使用原生 `Selection` 在弹出菜单前选择目标，并按导航游标收敛为单选。
- 标记本轮已构建行菜单，在 Outline 返回后保留菜单的节点身份，下一绘制阶段重新解析行号，避免旧指针选择覆盖目标。
- 展开箭头继续使用原生行为；树下方真正的空白区域仍没有菜单。选择／右键不建立数据库连接。

## 验证

`go test ./...`、`go vet ./...`、`go tool mygo vet`、`go tool mygo build` 均通过；Linux 安装包已重新生成。

`TestNavigatorRightClickUnselectedRowAcrossItsWidth` 覆盖先选另一行，再在目标行图标、文字、右侧留白右键；验证菜单类别、选中身份、取消菜单后身份保持、星标写入正确连接、右键后方向键导航及 Ctrl 点击仍维持单选。

原生窗口使用独立配置，仅有 `Group` 和 `Local` 两项，未填写密码或连接数据库。客户区 1280×800；截图和 JSON 位于 ignored `temp/research/context-menu-fix-20261025/native/`：

| 场景 | 证据 |
| --- | --- |
| 修改前，连接名称右侧右键不弹菜单 | [before](../temp/research/context-menu-fix-20261025/native/before-trailing-right-click-client.png) |
| 修改后，目录选中时右键另一连接，菜单／高亮／信息面板同步 | [connection](../temp/research/context-menu-fix-20261025/native/after-trailing-connection-menu-client.png) |
| 修改后，连接选中时右键目录 | [group](../temp/research/context-menu-fix-20261025/native/after-trailing-group-menu-client.png) |
| 右键连接后执行添加星标，独立 SQLite 中 `Local.starred=1` | [action](../temp/research/context-menu-fix-20261025/native/after-star-action-client.png) |

以上是独立实现及真实桌面验证 **I**，未在本轮重新操作原版，不扩展既有 V 范围。自有 GUI 进程在采集后清理；未 commit 或 push。
