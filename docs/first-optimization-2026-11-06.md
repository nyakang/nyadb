# 首轮优化实施与验证 — 2026-11-06

已实施全局审计 A01–A09。审计基线为 `b023df8091babf6f1d51c6aa7e89a0011978ad76`；原问题证据保留在 [全局审计](global-application-audit-2026-11-06.md)。本文记录实现及验证结果；提交与推送按后续指令执行。

## 行为变更

| 审计项 | 实施结果 | 主要回归覆盖 |
| --- | --- | --- |
| A01 | 删除先经过原连接文档守卫，再删除配置；成功后断开该连接，保留 SQL 文本与显式恢复 | 取消、配置删除失败、延续操作绑定原 ID、SQL 文本保留 |
| A02 | 切换前校验版本、重复 ID、SQL 标签与正文关系及呈现 JSON；Flush 成功后才释放当前标签 | 未来版本、目标错误、Flush 失败、守卫取消、延迟切换的目标副本 |
| A03 | 整次填充复用现有撤销分组，减少整页捕获次数 | 已有草稿、一条 Undo/Redo、200 个目标完整恢复、已有视觉列与 NULL 测试 |
| A04 | 用快照比较替代每帧全文 JSON 签名；Buffer.Version 缓存字符串，保存仍防抖 500 ms | 不变观察、元数据/保存基线/选择/身份/固定标签变化、可变数据独立副本、保存期间继续编辑 |
| A05 | 删除覆盖 MySQL、PG、ClickHouse 已知数据库凭据；每项都尝试，汇总错误 | 四引擎、主凭据失败后继续清理备用配置、未知 vault 条目保留 |
| A06 | BrowserReset 匹配原节点 ProfileID；实际断开也在清除身份前发布原 ID | 另一连接的选择/展开保留、实际断开回调原身份 |
| A07 | 取消结果与会话可用性分别更新；同时失效时立即显式恢复 | Canceled 与 SessionLost/Revoked 组合、原命名空间、文本保留、现有迟到回执测试 |
| A08 | 只有存在下一目标时才检查下一序列值 | 正负整数边界、零步长、真实溢出无部分草稿 |
| A09 | 按 ID/kind 检索，校验记录身份，回收操作产生的确定无引用记录 | 部分写入、凭据与隧道失败、回滚失败、成功替换/移除备用配置、复制连接共享引用、停放握手、扫描/密钥失败与 UI 重试 |

启动时若持久化工作区版本不支持，也会阻止自动保存覆盖原密文。工作区副本在守卫之前捕获；命名工作区保存不再通过全文 JSON 往返制作副本。现有版本 0/1/2、加密格式和布局键保持兼容。

新增接口包括 `queries.RecordLookup`、`queries.InitialSQLDeleter`、`queries.CloneWorkspace`、SQL 工作区拒绝恢复入口，以及只读的连接挂起检查。原 Repository、InitialSQLStore 和 bootstrap 入口签名保持兼容；新增能力由生产 localdb 实现，连接编辑器通过注入的回收回调协调。

## 初始 SQL 回收约定

继续使用新 ID 写入，不覆盖原初始 SQL。保存失败先尝试恢复原配置，随后核对实际持久化的全部主/备用配置；回滚失败仍被引用的记录保留。成功保存后，旧引用、移除的配置引用和本次写入 ID 成为检查候选；仍被引用的记录不删除。

任何未消费的连接握手或刷新都会保守地延后回收，保护停放握手捕获的旧 ID。记录删除前验证加密身份；扫描或认证失败保留记录。失败候选在当前应用会话内保留，状态栏提供“重试清理初始 SQL”；后续提供候选的保存/删除也会触发重试。

**历史孤立记录不扫描、不回收；退出不把内存中的候选转为新的清扫任务。** 原有凭据删除语义、错误密钥保护、统一数据网格、generation 绑定、取消确认及不确定提交保护保持有效。

## 性能结果

同一 Intel Xeon E5-2620 @ 2 GHz、Go 1.27.3，三次基准中位数：

| 场景 | 审计基线 | 修复后 | 结果 |
| --- | --- | --- | --- |
| 100 行 × 10 列 × 512 字节页面，填充 200 单元格 | 2.1408 秒、458,257,696 字节累计分配，保留 3 条部分撤销 | 13.948 ms、5,102,731 B/op，保留 1 条完整撤销 | 耗时约减少 99.35%，分配约减少 98.89% |
| 不变的 1 MiB SQL 工作区观察 | 5.766 ms、约 2.206 MB/op | 1.764 μs、192 B/op | 去除全文序列化与签名分配 |
| 不变的 10 MiB SQL 工作区观察 | 63.254 ms、约 23.613 MB/op | 1.564 μs、192 B/op | 成本不再随 SQL 正文大小近似线性增长 |

填充基线为单次状态探针，修复后为排除夹具构造的微基准；比较同一内容和操作，耗时比例仅作参考。累计分配/B/op 不等于峰值 RSS。观察基准测量 `ObserveWorkspace`，不包含整个原生窗口的一帧；小型呈现 JSON 和导航快照仍在 UI 线程生成。

日志、三次样本和汇总位于忽略目录 [build/first-optimization-20261106](../build/first-optimization-20261106/)。

## 验证

最终代码的全量 Go 测试：37 个有测试的包通过，顶层 1,298 PASS / 140 SKIP；SKIP 不算真实数据库集成验证。Go vet、MyGo vet、i18n 检查及入口构建通过，翻译目录为 1,906 条消息。

Python 视觉工具测试 64 项通过，原夹具有 SQLite ResourceWarning。没有可用的 DISPLAY/Wayland 桌面会话，本轮完成 headless UI 行为验证，未进行原生桌面、IME/DPI、Windows/macOS 或新增 MySQL/PG/ClickHouse 服务器夹具验收。

最终 `GOWORK=off go test -race -timeout 20m -p 2` 检查的 16 个相关包全部通过，包含 contract、connections、localdb、dbruntime、engines、mysql、queries，以及 shell、connectioncatalog、connectioneditor、navigation、sessionbrowser、sqlworkspace、dataworkspace、objectworkspace、theme。完整 shell 包约 539.902 秒，dataworkspace 约 241.001 秒；缓存结果按 Go 的有效测试缓存规则使用。日志见 `race-final.log`。

最终 Go 测试、静态检查、翻译检查、构建和基准分别记录在 `tests-final.jsonl`、`go-vet-final.log`、`mygo-vet-final.log`、`i18n-final.log`、`build-final.log`、`sql-observe-bench.log`、`fill-bench.log`；汇总与哈希见 `validation.json`、`performance.json`、`manifest.json`。早先在代码收尾前启动的重复 race 已终止，日志保留；通过结论仅以最终版本检查为准。
