# Navicat UI/UX 工作流改进实施记录 — 2026-11-05

依据 [差距审计](navicat-ui-ux-gap-audit-2026-11-05.md) 和 [实施计划](navicat-ux-implementation-plan-2026-11-05.md)，本轮落实 UX15–29 的 15 项工作流改进。数据库读写继续使用现有模块、共享数据文档和原会话生命周期。

## 改动与验收对应

| 编号 | 实施后的行为 | 验证重点 |
| --- | --- | --- |
| UX15 | 设计字段列表扩大可用高度，重复的详细属性默认折叠到“高级字段属性” | 1280×800 和 840×560 的六字段表完整可见；展开后保留原详细编辑及保存确认 |
| UX16 | PG/SQLite 普通索引和 PRIMARY/UNIQUE/FOREIGN/CHECK 约束提供图形编辑，支持字段顺序、降序、唯一性及外键目标 | 普通定义解析/生成、PG 标识符折叠、SQLite 字段匹配和外键列配对有回归；高级定义保留完整 SQL |
| UX17 | 运行范围菜单列出当前语句、选中 SQL 和全部 SQL；运行按钮显示当前适用范围 | Ctrl+Enter/F5、选区和首次运行后的编辑器焦点保持原行为 |
| UX18 | 首次运行前编辑器占满；步骤移入消息页，结果工具压缩到两行；可独占编辑器或结果 | 字段面板默认在下方，可调大小、切右侧；SQL 页隐藏外侧对象信息面板，840×560 可阅读三列结果 |
| UX19 | “固定本次结果”保留原 SQL、命名空间、时间、类型值和截断信息，可在同页切回 | 原生验收中，新执行变为 2 行时固定结果仍显示原 5 行；字节深拷贝和限额有回归 |
| UX20 | 结果标签和设计子页使用统一活动页按钮，蓝色背景及 selected 语义明确当前页 | 中英原生截图验证活动页可辨认，白字与背景对比清晰 |
| UX21 | 当前查询结果一步打开格式/范围对话框，支持 CSV、TSV、TXT、JSON、XML、HTML、XLSX | 打开时捕获缓存结果和选区，重跑不会改变待导出的数据；明确不会重新执行 SQL，并显示截断提示 |
| UX22 | SQLite 隐藏函数、用户、其它入口，PG 隐藏未支持的用户入口；非 MySQL 表信息只保留适用属性 | 产品能力决定入口；已有索引/触发器树和设计器仍可使用，不新增未实现的管理工具 |
| UX23 | 简单和分组筛选均支持条件启停，错误在相应条件旁显示并保留底部反馈 | 发送前剪除禁用条件，转换分组保留禁用草稿，删除组清理状态；保留参数化与规则限额 |
| UX24 | 收敛默认信息层级，数据底栏优先显示读取耗时、行数、草稿；参数化 SQL 默认折叠，可显示/复制 | 结合活动页、禁用态和高级属性折叠检查窄窗口；未重绘全部图标或替换整体主题 |
| UX25 | 同一数据文档新增记录表单，字段名和值纵向展示，复用完整字段读取及编辑 | 表单和网格共享草稿、原行身份及类型值；三个引擎使用同一 State/Document |
| UX26 | 数据页文件保存菜单与 Ctrl+S 调用现有 Save | 菜单捕获原文档并再次检查活动身份；忙碌、原会话失效及不确定提交均不能绕过 |
| UX27 | 可输入页号并直接跳转，验证正整数、溢出及已知边界 | 仅相邻下一页使用 NextCursor；跨页使用 OFFSET，保留草稿离开决策及加载取消 |
| UX28 | 表的一般“导出”入口统一打开格式/范围向导；整表 JSONL 使用显式独立入口并说明忽略当前筛选 | 当前页/选区与整表语义分开；导入导出原会话失效提示适用于三个引擎 |
| UX29 | 网格暂存修改支持撤销/重做，插入、删除、修改、粘贴按操作分组 | 深拷贝类型值、字节、原身份及指纹；文字编辑仍使用文字撤销，不撤销数据库提交 |

## 生命周期和资源边界

- 每个查询最多保留 **3 个结果快照、总计 16 MiB**。快照不进入工作区恢复持久化；移除快照或关闭文档释放缓存、选区和回调。复制/选区命令仍验证原 owner 和当前显示的快照，避免旧编辑目标串页。
- 草稿历史最多 **32 次、估算总计 8 MiB**。保存开始、换页、丢弃或关闭清空历史；忙碌、不确定提交、事务不可用或原会话失效时禁止撤销/重做。它只恢复未提交草稿，不重放数据库操作。
- 缓存导出对结果和选区做深拷贝；关闭导出对话框或文档释放捕获的数据。HTML/XLSX 编码器提取到纯 `internal/db/tabular`，SQL 和数据工作区复用，避免 UI 模块相互依赖。
- 高级索引表达式、谓词、opclass、INCLUDE 等继续编辑完整 SQL；图形编辑拒绝无法无损表示的高级属性。PG 普通 `USING btree` 保留，外键字段移动/删除同步维护引用列配对。
- 私有结果通道仍只由 UI 线程消费，保存取消仍等待执行器确认，原 profile/generation 和不确定提交保护保持有效。现有数据布局存储键及格式不变。

## 验证

| 检查 | 结果与证据 |
| --- | --- |
| `go test ./...` | 通过；[全量日志](../build/navicat-ux-20261105/go-tests-release.log) |
| `go vet ./...` / `go tool mygo vet` | 通过 |
| `CGO_ENABLED=0 go build ./cmd/nyadb` | 通过；产物在忽略的 `build/navicat-ux-20261105/nyadb` |
| `go tool mygo generate` / `go tool mygo i18n check` | 修改中英 JSON 后生成 catalog；1711 条消息检查通过。补充英文导出行列计数后，SQL 工作区和 locales 测试再次通过 |
| `/usr/bin/python3 -m unittest discover -s tests -v` | 64 项通过；[日志](../build/navicat-ux-20261105/python-tests.log)。该解释器已有 Pillow |
| 工作区新增行为 `go test -race` | dataworkspace、sqlworkspace、objectworkspace 通过；[日志](../build/navicat-ux-20261105/workflow-race-release.log) |
| 其余相关包 `go test -race` | contract、connections、localdb、dbruntime、engines、mysql、connectioncatalog、connectioneditor、navigation、sessionbrowser、theme 均通过；[日志](../build/navicat-ux-20261105/race.log) |
| Shell 全部顶层测试清单的 race | 四批均通过：42/42/41/41 项，分别耗时 434.020/242.599/108.750/336.229 秒；[第 1 批](../build/navicat-ux-20261105/shell-race-shard-1.log)、[第 2 批](../build/navicat-ux-20261105/shell-race-shard-2.log)、[第 3 批](../build/navicat-ux-20261105/shell-race-shard-3.log)、[第 4 批](../build/navicat-ux-20261105/shell-race-shard-4.log) |
| `git diff --check` | 通过 |

Shell 单次 race 分别在 5 分钟和 15 分钟超时，未报告 data race；MyGo headless 绘制在 race 下耗时较长。因此将 `go test -race ./internal/ui/shell -list '^Test'` 的 **166 个顶层测试**按清单分为 42/42/41/41 四批，以锚定的 `-run` 表达式执行，每批 `-count=1 -timeout=10m`。清单已核实无重复且覆盖全部 166 项：[分批清单](../build/navicat-ux-20261105/shell-race-shards.json)。四批退出码均为 0，无 data race 报告。需要显式环境标志的服务器集成测试仍按原有条件跳过，未将单次超时记为通过。

主要新增行为回归位于 `dataworkspace/workflow_commands_test.go`、`sqlworkspace/workflow_test.go`、`objectworkspace/structured_definition_test.go`、`schemaddl/basic_definition_test.go`、`tabular/cached_test.go` 和 Shell `document_menus_test.go`，包括 stale menu 保存、原会话保护、快照/导出深拷贝、页游标选择及未提交草稿恢复。

## Linux 原生证据

原生验收脚本使用真实 Shell、一个真实 runtime 和独立的 SQLite 文件。fixture 命令负责打开工作区，记录表单、快照、字段查看器、布局及索引选中通过原生点击/菜单操作。它不等同于鼠标完成连接配置的端到端测试。

可复现命令（输出目录必须尚不存在，并位于忽略的 build/temp）：

```sh
CGO_ENABLED=0 go test -c -o build/native-ux ./internal/ui/shell
/usr/bin/python3 tools/verify_navicat_workflow_native.py \
  --output build/navicat-ux-20261105/release-verified --locale both
```

最终证据目录为 `build/navicat-ux-20261105/release-verified/{zh-Hans,en}/`，每种语言采集 14 个场景，包含 1280×800 和 840×560 的窗口/客户区图片及 metadata；[原生验收日志](../build/navicat-ux-20261105/native-release-verified.log) 的命令退出码为 0：

| 场景 | 中文 | 英文 |
| --- | --- | --- |
| 六字段设计器 | [1280](../build/navicat-ux-20261105/release-verified/zh-Hans/screenshots/design-fields-1280-client.png) / [840](../build/navicat-ux-20261105/release-verified/zh-Hans/screenshots/design-fields-840-client.png) | [1280](../build/navicat-ux-20261105/release-verified/en/screenshots/design-fields-1280-client.png) / [840](../build/navicat-ux-20261105/release-verified/en/screenshots/design-fields-840-client.png) |
| 普通索引图形编辑 | [840](../build/navicat-ux-20261105/release-verified/zh-Hans/screenshots/design-index-840-client.png) | [840](../build/navicat-ux-20261105/release-verified/en/screenshots/design-index-840-client.png) |
| 记录表单 | [840](../build/navicat-ux-20261105/release-verified/zh-Hans/screenshots/form-840-client.png) | [840](../build/navicat-ux-20261105/release-verified/en/screenshots/form-840-client.png) |
| 重跑后的原 5 行快照 | [840](../build/navicat-ux-20261105/release-verified/zh-Hans/screenshots/snapshot-after-rerun-840-client.png) | [840](../build/navicat-ux-20261105/release-verified/en/screenshots/snapshot-after-rerun-840-client.png) |
| 结果独占与底部字段查看器 | [840](../build/navicat-ux-20261105/release-verified/zh-Hans/screenshots/sql-result-only-840-client.png) | [840](../build/navicat-ux-20261105/release-verified/en/screenshots/sql-result-only-840-client.png) |
| 缓存查询结果导出对话框 | [840](../build/navicat-ux-20261105/release-verified/zh-Hans/screenshots/sql-cached-export-840-client.png) | [840](../build/navicat-ux-20261105/release-verified/en/screenshots/sql-cached-export-840-client.png) |

原/末 SQLite SHA-256 均为 `c8ffba4fcd097aefc3dabe7c32ae6ca0b5332208d7ad38c37b57454e41f7fca6`。[中文核验](../build/navicat-ux-20261105/release-verified/zh-Hans/verification.json) 和 [英文核验](../build/navicat-ux-20261105/release-verified/en/verification.json) 均确认数据未变及临时应用/显示进程已退出。

## 本轮边界

这轮完成计划中的工作流改进，不代表全部 Navicat 功能或视觉等同。查询缓存导出提供上述七种常用格式；XLS、DBF、Parquet、SQL 等仍属于已有表导出能力，未新增为查询缓存格式。PG/MySQL 有解析、工作区和 Shell 回归，本轮原生场景使用 SQLite，未做 PG/MySQL 服务器原生验收。同步、自动运行、建模、完整图标重绘及全应用英文翻译不在本轮实施范围内。
