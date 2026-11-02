# SQLite 原生表设计器 — 2026-11-02

本阶段接通 [SQLite 结构设计后端](sqlite-schema-ddl-2026-11-02.md) 的正式 MyGo 设计器、创建及重命名入口。SQLite 删除/复制对象、PG 图形设计与导入/备份仍未完成；[完整工作清单](workbench-completion-2026-11-02.md) 继续有效。本轮没有启动 Navicat，不新增 O/V，不代表整应用复刻完成。

## 实现 I

- objectworkspace 管理独立设计文档、字段/约束/索引/触发器/表选项/原始 DDL 六页。常用字段通过名称、类型、主键、自增、非空、唯一、默认表达式及排序规则表单编辑；高级字段和定义保留完整 SQL。MyGo 的 accessibility Label 不会绘制标题，字段表单已补可见标题。
- 新建表通过空结构快照进入设计器。已有名称明确拒绝。主按钮、导航新建和表重命名按原 profile/namespace 与实际 SchemaDesign binding 开启；保持 MySQL 管理能力描述，避免错误开启 SQLite 账户、维护、复制等入口。
- 改名先形成草稿，SQL 预览、文件菜单保存表设计与 Ctrl+S 都必须再确认执行。字段只改名保留原完整尾定义和稳定 ID，避免格式化变成改类型。删除字段须输入原表名称；预览后改变字段或定义不能复用旧请求。
- 工作线程使用深复制请求和私有缓冲通道，UI 消费回执。切换连接时原回执停放；代次改变时保留草稿、禁止自动重试。取消等待回执；关闭撤销旧通道和回调。未应用表单文本也进入离开守卫。
- Shell 在原范围内刷新对象和数据缓存，verified commit 后更新表身份/标签。设计器撤销旧字段信息读取，按核验后的表身份重新加载 inspector。不确定回执只撤销捕获的原 published session；sessionbrowser 在下一帧检测失效，离开守卫保留设计草稿。
- 修复 Shell 初始化时直接捕获尚未注入的 SchemaDesign 方法导致的 nil runtime 调用；改为调用时读取 runtime 的闭包。

## 自动检查

Go 1.27.3 / MyGo v0.7.3-nyadb.2，Linux amd64，`GOWORK=off GOCACHE=/data/.cache/golang`。HEAD 保持 `5960d26`，没有 commit/push。

| 检查 | 实际结果 |
|---|---|
| `go test ./... -timeout 20m` | 最终通过，Shell 77.768 秒、objectworkspace 3.707 秒。外部服务开关未启用；跳过的集成测试不算通过证据。 |
| objectworkspace race | 最终通过，17.536 秒。此前 schema/engines/dbruntime race 通过，后续仅 UI/验收工具变更。 |
| Shell 真实 SQLite focused race | 通过，37.721 秒；本轮没有重跑 Shell 全包 race。 |
| `go vet ./...` / `go tool mygo vet` | 通过。 |
| 翻译生成 / i18n check | 通过，1338 条 zh-Hans/en 消息。 |
| `go tool mygo build` | 最终通过，23.8 秒，正式 Linux amd64 程序和 deb/tar 包。 |
| `/usr/bin/python3 -m unittest discover -s tests -v` | 最终 51 项通过，2.668 秒，含新采集工具的真实 SQLite 证据校验测试。 |
| `git diff --check` | 通过。 |

新增 Tester 回归覆盖字段 ID/原定义保留、旧预览和删字段确认、请求切片隔离、取消/关闭、停放和代次、530×430 表单及草稿守卫、只读/离线/缺 binding。真实 SQLite Shell 测试通过工具栏创建表、字段表单、Ctrl+S 预览确认、带数据重建、改名和标签、inspector 重读、840×560、原 session 撤销及未保存设计保留。改名回归调用原生入口共用的协调方法；此测试不等于实体键盘 F2 或所有右键菜单的实际桌面验收。

## 原生窗口与可重复工具

`tools/capture_sqlite_designer_acceptance.py` 通过正式二进制，在私有 D-Bus/Secret Service/Xvfb/Openbox 和自有 SQLite 文件中操作。验证配置和附加文件身份，拒绝已经改变的 fixture；每次采集目录唯一，失败不覆盖此前截图。只对 `aux.shared` 确认新增 `native_added TEXT`，数据库侧核对 `(1, "attached fixture", NULL)` 和不变的 `main.shared`。字段/定义草稿没有自动执行权限。

运行方式（每次独立环境、成功或失败均自动清理本次资源）：

```bash
GOWORK=off GOCACHE=/data/.cache/golang /usr/bin/python3 tools/verify_entry_acceptance.py -- /usr/bin/python3 tools/capture_sqlite_designer_acceptance.py
```

若只检查字段草稿，向采集命令追加 `--draft-only`，不确认结构 SQL。正式窗口保持 840 像素最小宽度；530×430 是中间组件 Tester 验收。

首次工具运行遗漏 `loading=False`，随后调用不存在的 `NativeCapture.type`，均记录失败并修复，没有数据库修改。一次完整重放因确认坐标落在按钮外失败；实际预览和数据库确认前状态正确，但不计为执行通过。补齐可见字段标题时发现 Tester 点击与文本同名，先出现表单没有获得焦点的回归失败，改用带冒号的可见标题后最终检查通过。

已通过的原生执行 run `r-32daa668ad37` / `custom-fe288f60`，二进制 SHA-256 `af617cd5e7c11ad3df6d0c94956da394166d21ad553efb65f0442bb123a1c80d`。证据在 `build/entry-acceptance/r-32daa668ad37/evidence/sqlite-designer-native/capture-b0d68b716139/`；1280×800 字段/预览/执行和 840×560 列表已逐图检查。该 run 已停止：原 PID/birth 均无活动进程、原容器 inspect 不存在、临时 secrets 文件删除。manifest 保留历史资源身份，不能把非空历史 processes 字典误认为进程仍存活。

更新工具在全新环境 `r-421b9c8bbd51` / `custom-6edb0fec` exit 0，运行二进制哈希与上述最终构建相同。六页截图均核对实际页面；840×560 字段、索引完整 SQL 表单和 Ctrl+W 草稿守卫通过，字段表单下部需滚动。索引草稿从数据库侧确认未创建。wrapper 在命令退出后自动停止本次资源，随后 PID/birth、容器 inspect 和秘密文件核对全部清理；审计保存在该 run 的 `evidence/sqlite-designer-cleanup-audit.json`。

代表性本地证据（忽略目录，不携带密码或密钥）：

- [字段表单 1280×800](../build/entry-acceptance/r-421b9c8bbd51/evidence/sqlite-designer-native/capture-ada358843f34/designer-name-draft-1280-client.png)、[确认前 SQL 预览](../build/entry-acceptance/r-421b9c8bbd51/evidence/sqlite-designer-native/capture-ada358843f34/designer-preview-1280-client.png)、[执行后字段表单 840×560](../build/entry-acceptance/r-421b9c8bbd51/evidence/sqlite-designer-native/capture-ada358843f34/designer-applied-840-client.png)。
- [表选项](../build/entry-acceptance/r-421b9c8bbd51/evidence/sqlite-designer-native/capture-ada358843f34/designer-options-1280-client.png)、[核验后原始 DDL](../build/entry-acceptance/r-421b9c8bbd51/evidence/sqlite-designer-native/capture-ada358843f34/designer-ddl-1280-client.png)、[索引草稿](../build/entry-acceptance/r-421b9c8bbd51/evidence/sqlite-designer-native/capture-ada358843f34/designer-index-draft-840-client.png)、[未保存草稿提示](../build/entry-acceptance/r-421b9c8bbd51/evidence/sqlite-designer-native/capture-ada358843f34/designer-dirty-guard-840-client.png)。

实际缩放、字体精确指标、Windows/macOS 原生行为及 Navicat 差分仍 Unknown。MySQL 容器由隔离工具创建，但本次采集只连接 SQLite，PG 配置保持离线；不能据此声称新增 MySQL/PG 服务验收。

## 仍需实施

SQLite 常用字段已结构化；高级约束、索引和触发器仍是完整 SQL 编辑。只读、schema 修订冲突、rowid 模式转换和有损类型转换的限制沿用后端报告。尚缺表删除/复制、视图对象 CRUD、导入/备份扩展和实体 F2/右键改名桌面流程。PG SchemaDesign binding 仍为空。

后续顺序：PG 完整 catalog 快照与事务设计、PG/SQLite 对象/导入/备份及隧道；跨库映射/传输/数据和结构同步；任务调度；优先 ClickHouse；可配置 OpenAI 兼容 AI SQL；模型/逆向、BI、监控、字典、生成、脱敏和命令行。详细原版行为缺 O/S 时继续标 Unknown。
