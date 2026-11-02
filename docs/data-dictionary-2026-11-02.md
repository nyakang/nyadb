# 数据字典预览与导出 — 2026-11-02

已实现 **I**：从“工具 → 数据字典”、表/视图右键及只读结构工具栏打开原数据库/schema 或单对象字典，显式读取、搜索表名、选择对象、浏览字段/索引/外键，并导出完整已读字典为 Markdown 或 HTML。搜索只过滤预览，不改变导出范围。基础字典是本次完整工作台目标的一个交付项，其余范围见 [剩余工作](workbench-completion-2026-11-02.md)。本阶段不新增 Navicat O/S/V。

## 实现及安全边界

- `internal/services/dictionary` 只消费注入的中立 Browse/TableMetadata。按 ProfileID/Catalog/Schema/Kind/Name 校验，兼容 MySQL/PG/SQLite 及 ClickHouse 的大写对象类型，确定性排序；任何读取失败或越界返回空文档，不输出部分结果。500 个对象、50000 个元数据项、8 MiB 元数据及 32 MiB 渲染输出上限明确拒绝，要求缩小至单对象范围。
- 文档复制字段、索引、外键和默认值，不保留可变 loader 指针。默认 nil 显示“未报告”，空字符串保留为空，默认表达式保留原字符串；不会把适配器缺失值推断成“无默认”或 SQL NULL。
- 原生预览由 objectworkspace 持有，Shell 注入实际命名空间、原 generation 的读取器和原生文件选择。菜单保存打开时的范围；连接、database/schema、generation、弹窗关闭和退出都撤销原工作。工作线程只接收不可变请求，只通过私有缓冲通道发布；文件选择回执在 UI 复核后才写文件。导出使用已读文档的深副本，没有新的数据库读取。
- Markdown 的 HTML/Markdown 特殊字符从原文本一次性转义；HTML 使用自动转义模板，不执行用户元数据。文件先在目标目录创建 0600 临时文件，完整写入/Sync/关闭后才改名；失败或改名前取消保留已有文件并清理临时文件。改名已完成后到达的取消不能撤销已完成导出。
- 修复已有 PG/SQLite TableMetadata 只有字段而没有结构化索引/外键的问题：新增限定命名空间的只读呈现查询，保留复合字段顺序、SQLite DESC/部分索引与外键动作、PG 索引表达式/表字段注释及跨 schema 外键。网格选用编辑键的原规则不变，没有新增行查询或 DDL。

这些是读取时报告的元数据，不是完整目录备份，也不保证多个对象或同对象多条查询来自同一时刻。字典不包括程序、权限、完整约束定义、分区/物理属性或数据行。SQLite PRAGMA 不报告原外键名或表达式原文，显示 `foreign_key_<id>` 和 `<expression>`/隐式主键标记；原始定义仍在既有只读结构页。ClickHouse 的关系型索引/外键缺失不推断为支持这些管理能力。

## 回归与真实服务

服务回归覆盖原范围/错范围、完整读取失败不返回部分、重复/上限、深复制、默认 nil/空值、Markdown/HTML 转义、写失败及取消不替换旧文件、临时文件清理和短写。`ui.NewTester` 覆盖两个窗口尺寸、搜索与完整导出、旧菜单的连接/database/generation、关闭撤销工作及迟到文件选择。真实 SQLite Shell 用主/附加库同名表验证原附加库、视图/索引/外键及无行内容导出；能力不足与离线不开始读取。

- PostgreSQL 17.10：`build/verify/postgres-wlQgAP`，显式 fixture/race 2 个主场景通过，1.477 秒，其中新字典 0.19 秒。实际执行 fixture DDL，读取字段注释、表达式索引、复合外键及视图；原 PG 基础会话回归同轮通过。
- ClickHouse：`build/verify/clickhouse-bb9qve`，真实原生驱动/race，包括大写表/视图字典子测试；服务 1.332 秒、原 Shell 场景 14.514 秒。字典保留 ALIAS 元数据且不出现代表性精确行值。
- MySQL 8.4.11：隔离 `r-a90912cb858a` 的 `custom-9644fe9c` exit 0，真实只读 entry_read、真实 OSVault 与正式 dbruntime，由 Tester 操作工具菜单/读取/导出，数据库结构含 DECIMAL(40,20)/PRIMARY，文件不含 fixture 行值；测试 11.06 秒，包 race 12.158 秒。

这些测试明确启用各自集成标志，在本轮唯一 fixture 资源上运行；默认测试跳过不算真实服务通过。未连接生产、运行 Navicat 或复制私有资源。

## Linux 正式窗口

Go 1.27.3 / MyGo v0.7.3-nyadb.2，Linux amd64、中文/暗色，私有 Secret Service/D-Bus/Xvfb/Openbox。原生重放 `r-a90912cb858a` 的 `custom-dbb79f95` exit 0，运行中 `/proc/<pid>/exe` SHA-256 校验与构建一致：`9f70b8f4cfd62e0668f082a84ca52f68e0e397f9d2d1eee8c8769ce0aa6af3b4`。

真实窗口读取自有 SQLite aux，原生 GTK 文件选择分别导出两个格式，独立检查文件范围、字段、视图、索引和不包含行数据；前后逻辑 schema/自有行摘要一致。截图已检查 1280×800、840×560 的读取、对象选择、预览和固定导出/关闭入口；字体/缩放没有额外测量，保持 Unknown。

- [1280×800 字段预览](../build/entry-acceptance/r-a90912cb858a/evidence/dictionary-native/capture-aa6d92fe67d0/dictionary-fields-1280-client.png)
- [1280×800 原生导出完成](../build/entry-acceptance/r-a90912cb858a/evidence/dictionary-native/capture-aa6d92fe67d0/dictionary-html-exported-1280-client.png)
- [840×560 字典](../build/entry-acceptance/r-a90912cb858a/evidence/dictionary-native/capture-aa6d92fe67d0/dictionary-fields-840-client.png)
- 完整 `result.json` 和两个文件 SHA-256 保存在同目录。Markdown `0227637bbd186535ac56b789bf931fc70344642c1af4be2cf895508aadc603ad`；HTML `3f23ea1bb12ad2e7cec1f87a66f27d73b1ea5d87b8e4509cc563c8a55758cda0`。

首轮 `custom-03d6fdc7` exit 1：采集脚本坐标没有点击读取和导出按钮，文件对话框未出现。失败截图显示当时未读取，没有将此轮当作原生通过。已校正坐标并以新的 capture 目录重跑；正式表格同时补齐纵向拉伸。初始测试的未定义 Tester.Has 和转义后的文本断言已修正；真实 SQLite 回归另发现缺少索引/外键以及引号实体被再次转义，均已修复并增加回归。

重放：

```bash
GOWORK=off GOCACHE=/data/.cache/golang PATH=/usr/local/go/bin:$PATH \
  /usr/bin/python3 tools/verify_entry_acceptance.py -- \
  /usr/bin/python3 tools/capture_dictionary_acceptance.py
```

工具仅准备它拥有的 fixture；结果/截图/资源身份在忽略目录。可用 `--keep` 留待同一 owned run 执行只读 MySQL 的显式测试，最后 `--run <owned-run> --stop` 清理。

## 最终检查与归档

基线本地提交 `46b4cc1`，使用 `GOWORK=off GOCACHE=/data/.cache/golang`。

- `go test ./... -timeout=20m` 最终通过：`build/verify/dictionary-all-checks.log`；上一完整重跑 Shell 67.815 秒，最终同一源码正常缓存，字典新增输出上限测试 0.895 秒。
- dictionary/engines/objectworkspace/dbruntime 全包生命周期 race 通过，分别 1.119/3.840/21.842/1.102 秒；Shell 与 dataworkspace 分别独立运行 20 分钟超时，完整包通过 738.567/500.796 秒。完整 UI 包 race 编译早于最后的纵向拉伸；最终布局代码另追加字典/结构定向 race（Shell 68.953 秒、objectworkspace 6.653 秒）和输出上限服务 race（7.128 秒）。定向范围不代替完整范围，日志 `dictionary-race.log`、`dictionary-{shell,data}-race.log`、`dictionary-delivery-race.log`、`dictionary-output-limit.log` 均保留。
- `go vet ./...`、`go tool mygo vet`、翻译生成/i18n 检查通过，1415 条 zh-Hans/en 消息；`go tool mygo build` 通过。最终布局正式二进制与上述截图的 SHA-256 一致；最终构建日志 `dictionary-build-layout.log`，检查日志 `dictionary-delivery-{go-vet,mygo-vet,i18n}.log`。
- `/usr/bin/python3 -m unittest discover -s tests -v` 55 项通过，4.587 秒，日志 `dictionary-delivery-python.log`。采集工具验证两种转义格式与错误范围/行泄漏拒绝。
- PostgreSQL 与 ClickHouse 的原容器 ID 独立 Docker 查询 exit 0 且不存在，汇总 `build/verify/dictionary-service-cleanup-audit.json`；native/MySQL owned run 的原容器、D-Bus/keyring/Xvfb/Openbox PID/birth 逐项核对已经结束，secrets.json 已移除，结果 `build/entry-acceptance/r-a90912cb858a/evidence/dictionary-cleanup-audit.json`。原应用进程已在采集 finally 中按自己的身份清理；没有其余本轮运行命令。

代码、测试、工具和记录按用户要求单独本地提交，不推送或改写历史。Windows/macOS 原生结果与原版对照均未验收；PG 通用 Apply/表单、跨库工具、任务调度、模型/BI/监控/生成/脱敏/CLI 及其余完整目标继续有效。最新逐项审计见 [仍需补齐的工作台能力](reconstruction-remaining-2026-11-02.md)，不标记完整目标完成。
