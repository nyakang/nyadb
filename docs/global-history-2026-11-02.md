# 全局历史日志与失败恢复 — 2026-11-02

本轮接续工作台完整功能清单，补齐跨模块执行日志与保存失败恢复。变更属于独立实现 I；不新增 Navicat O/V，不启动原版、不连接生产、不提交或推送。

## 实现

- `contract.ActivityRecorder` 是中立的注入接口。UI 在开始操作时捕获原 profile、UI generation、数据库、对象、来源及摘要，SQL 工作区发出私有单回执通道。工作线程仅完成捕获的回执；关闭标签、弹窗、连接切换和工作区销毁不会把结果送往新的界面范围。
- SQL 编辑器的普通执行、EXPLAIN、运行导出及 SQL 文件步骤与数据工作区、对象工作区共用加密历史。新增数据读取/编辑、事务、普通及流式批量导入、数据导出、账户读取/变更、备份/恢复预览/执行、设计器、对象 CRUD/批量操作、表操作和维护摘要。
- 数据日志不保存筛选参数、单元格、行身份值；管理日志不保存账户密码、私有 DDL、导入值、原错误正文或输出路径。摘要可查看、搜索和筛选，不能通过双击、Enter 或按钮创建 SQL 文档。SQL 编辑器的完整历史仍可恢复为新草稿，且不自动执行。
- 成功、失败、取消、会话失效、部分完成、不确定和事务未提交具有独立状态。数据请求的日志在驱动确认回执后完成；本地提前取消读 UI 不会提前宣称全局日志已完成。管理错误详情仍保留于原操作界面。
- 首次历史加载前的执行不再直接漏记。回执等待首次成功加载的加密设置和清除 token；已关闭文档也可记录，原持久化设置为停用时不写入。缺密钥或服务失败不能通过默认设置覆盖已有记录。
- 待保存的元数据总量上限 32 MiB / 10000 项，活动回执最多 256 个；脚本回执最多 256 条、正文与错误有明确界限。超限或无效日志跳过完整记录并显示累计数量，不截断 SQL 后作为可重放语句，也不堵住后续有效记录。已保存历史仍按保留设置及 32 MiB 正文上限裁剪完整旧记录。
- 保存失败保留原加密快照和待写队列。界面支持重新读取后重试，以及明确放弃本次未保存日志；放弃同时撤销原私有回执，阻止迟到完成重新入队。失败日志在退出时要求做出重试/放弃/取消退出的决定，之后继续现有文档草稿退出流程。
- 外部进程的清除/停用 token 会撤销旧回执。外部修订变化后，重新读取不会把旧清除或设置操作自动套到新记录；这类控制冲突保留待执行操作，要求先放弃后重新操作。追加记录只在原 token 仍有效时恢复。
- `queries.Store.ApplyHistoryBatch` 保留原单操作接口，按顺序验证和一次条件发布最多 256 项。UI 每次至多 64 条/约 4 MiB 追加记录，控制操作单独发布。批次无效、密钥失败或修订冲突不部分替换原密文，无 SQLite 迁移或明文回退。
- Shell 注入使用运行时查找工作区的回调，避免构造阶段提前捕获尚未创建的 SQL 工作区。

## 已取得的证据

- 回归覆盖首次加载/停用、原连接、关闭回执、数据/管理摘要隐私、事务未提交/不确定、迟到保存、clear/disable token、修订冲突、批次原子性、持续存储失败上限、重试、明确放弃和退出守卫。
- 真实自有 SQLite 的原生组件 Tester 流程验证数据读取、图形编辑、SQL 执行进入同一个全局日志，并通过新 Store 读取验证加密重启恢复。账户与备份工作线程使用注入服务验证原 UI/runtime generation、密码不进入日志，以及关闭后实际取消回执。
- 性能测量命令：`GOWORK=off go test ./internal/storage/localdb -run '^$' -bench BenchmarkSQLHistoryFullSnapshotBatch -benchtime=3x -count=1`。自有真实 SQLite、约 32 MiB 已有历史，每批 64 条新记录；Linux amd64 / Xeon E5-2620：606101108 ns/op、362717274 B/op、2316 allocs/op。该结果衡量完整加密快照发布，不能当作单条记录耗时或峰值常驻内存。
- 完整快照依然具有显著解码/加密/SQLite 分配成本；批量写减少重复整份发布，但尚未改为分段日志。执行 SQL 不在 UI 线程等待日志 IO。

## 最终检查与运行证据

- `GOWORK=off go test ./... -timeout 20m` 全量通过；SQL 工作区、存储/queries、对象工作区及数据工作区生命周期 race 通过。最终数据工作区完整 race 为 517.832 秒、Shell 完整 race 为 683.642 秒；最终全量 Shell 测试为 70.310 秒。
- `go vet ./...`、`go tool mygo vet`、翻译生成/检查（1226 条）、`go tool mygo build` 通过。Go 1.27.3 / MyGo v0.7.3-nyadb.2 / Linux amd64。
- `/usr/bin/python3 -m unittest discover -s tests -q`：42 项通过；采集器新增 `--with-data` 并实际验证该路径。
- 新增正式原生窗口的 SQLite 表读取与全局历史检查点；fixture 的 SQLite 使用普通 SQLite 连接创建单一自有表，不通过配置库迁移生成应用内部表。打开路径使用选中表后 Enter，避免依赖两个普通单击的双击时间判定。
- 最终 owned run `r-92ccf4e516dd`，`custom-09f6e4a2` exit 0。正式程序 SHA-256 `014c9c27a6bb748407b4b0e3a7d3578a7f0572b945d499963ad3d3404f81e1a1`，同一二进制跨进程；真实 Secret Service、原生 SQLite 数据读取与 SQL 执行共同保存为 2 条历史，重启仍为 2 条，明确清除后持久化为 0 条且 epoch 增加。
- 正式程序截图：1280×800 SQL 结果/表数据/历史列表，以及 840×560 全局历史。实际查看窄窗口截图，表头、两条记录、搜索/筛选、清除/关闭入口可见。摘要不可重新打开由 Tester 键盘与按钮规则验证；原生截图没有额外宣称设计器/账户窗口的重新验收。
- 同一自有 MySQL 8.4 环境的 `custom-5127c00b` exit 0：`TestEntryAcceptanceRealMySQLShell` 的 9 个子项通过，包括新增真实全局日志断言。实际账户读取/创建/授权/撤权/删除、备份/转储、恢复预览/确认、锁等待取消、只读拒绝、切换/断连回执，共取得 16 条加密日志：账户 8、备份 5、恢复预览 2、恢复 1；成功 12、失败 2、取消 2。新 Store 验证恢复，未用 mock 代替该真实服务结果。
- 最初全量测试发现 Shell 对象工作区捕获 nil SQL 工作区，修复回调注入后回归及最终全量通过；中间 Shell race 的旧二进制运行被明确终止，不计作通过。最初翻译生成发现非合法目录键，改为合法键并重新生成/检查通过。
- 采集尝试 `r-95a1231c1170` / `r-d9d73a1c747e` 未真正打开目标表，检查点失败；未计入通过。根据实际截图修正对象路径，并修正 SQLite fixture 的创建方式后，最终独立 run 通过；失败证据保留在忽略目录。
- 对三个 owned runs 核验 `/proc` PID/birth、容器存在状态和临时 secrets 文件：均已停止，无本次存活进程、容器或秘密文件。未修改系统配置和共享用户密钥环。

本地证据在忽略目录：

- [正式窗口表读取](../build/entry-acceptance/r-92ccf4e516dd/evidence/history-native/global-history-data-client.png)、[840×560 全局历史](../build/entry-acceptance/r-92ccf4e516dd/evidence/history-native/history-840-client.png)、[重启恢复](../build/entry-acceptance/r-92ccf4e516dd/evidence/history-native/history-restart-client.png)、[清除后](../build/entry-acceptance/r-92ccf4e516dd/evidence/history-native/history-cleared-client.png)。
- [MySQL 加密日志汇总](../build/entry-acceptance/r-92ccf4e516dd/evidence/mysql-global-history.json)、[真实 MySQL 子项运行记录](../build/entry-acceptance/r-92ccf4e516dd/evidence/custom-5127c00b.log)。

编译、Tester、真实数据库和正式原生窗口证据分别记录，不互相替代。Windows/macOS 原生验收、实际字体/缩放及 Navicat O/V 仍 Unknown。

## 范围

常规数据库对象元数据缓存读取、字段预览和本地文件解析不作为用户执行历史逐条汇总。不会从第三方私有日志、截图或源码导入实现；原版完整日志分类仍 Unknown。

完整目标继续包含全局对象查找、URI、收藏夹、PG/SQLite 深化、跨库传输与同步、自动运行、ClickHouse、OpenAI 兼容 AI SQL，以及模型/逆向、BI、监控、字典、生成、脱敏和 CLI。该日志阶段通过不能证明整体完成。
