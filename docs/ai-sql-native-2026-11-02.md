# 可配置 OpenAI 兼容 AI SQL — 2026-11-02

按用户选择接入可配置的 Chat Completions 服务。本阶段提供独立实现 **I**、协议服务测试及 NyaDB 正式 Linux 窗口证据；未运行 Navicat、连接生产或新增原版 O/S/V。完整功能清单继续有效，按用户后续逐项归档要求本地提交，不推送。

## 实现与文档安全

- 工具菜单新增 AI 服务设置与 AI SQL 助手；SQL 工具栏同样可以打开助手。配置包括服务地址、模型、密钥、流式开关、超时、输出 token 上限和 `max_completion_tokens` / `max_tokens` 参数选择。默认不指定模型或远端地址。地址使用 HTTPS 或 localhost/loopback HTTP，不接受 URL 凭据、查询参数和 fragment。
- `internal/services/aisql` 独立处理配置及 HTTP/SSE，不依赖 UI、数据库驱动或具体存储。bootstrap 注入正式 SQLite 配置仓库及独立系统密钥环命名空间 `NyaDB AI SQL v1`，SQL 工作区拥有原生弹窗、预览、取消和建议草稿；没有 WebView。
- 非秘密配置复用 `app_preferences` 的 `ai_sql_v1`，没有迁移。API 密钥使用随机稳定账户 ID，只进入系统密钥环并绑定服务 origin。替换先创建新记录，再按旧配置条件发布；失败保留原配置/密钥，成功后清理旧记录。清理失败保留退休 ID，支持显式重试。地址 origin 变化不能自动沿用旧密钥；不提供明文回退。
- 生成、解释、优化共用请求预览。默认不包含当前 SQL 或元数据；用户明确勾选后才附带原文档选区/正文及原连接已加载的对象/字段名称，不读取表数据或主动查询元数据。请求不包括连接密码或连接配置。建议可以插入原选区、作为原连接的未执行新草稿或只供阅读；解释模式不提供 SQL 应用。
- 原文档文本版本、选区、执行请求、原 profile/命名空间及恢复状态参与插入验证。旧建议不覆盖变化后的编辑器；插入可撤销，保留已有命名资产与修改标记。另存建议保留原 profile/namespace，并要求显式恢复物理会话。AI 不执行、验证或自动保存 SQL。
- 工作者捕获不可变请求、配置和原通道；只有 UI 消费私有缓冲回执/进度。关闭弹窗、关闭文档或退出撤销原请求，新弹窗不消费旧通道。取消后等待请求回执，未完成建议不能应用。弹窗阻止底层 SQL 执行及工作台快捷键。
- HTTP 不自动重试或跟随重定向，错误只显示受控类别/状态码，不回显服务正文、密钥或 prompt。只接受完整单条文本响应；流式响应要求正常 stop 和 `[DONE]`。拒绝截断、拒绝响应、新旧工具/函数调用及终止后的额外文本。prompt 上限 256 KiB、输出上限 1 MiB，超时 5–600 秒；上下文超限明确拒绝。

协议依据是已实际获取的 [OpenAI Chat Completions 创建接口](https://developers.openai.com/api/reference/resources/chat/subresources/completions/methods/create/)。发送 `store:false`，默认使用 `max_completion_tokens`，兼容服务可显式选择旧 `max_tokens`；保留最终 usage 空 choices 块。使用 OpenAI Docs 技能核对协议，没有使用商业账号/API 密钥或把所有兼容服务计为通过。

## 自动验证

Go 1.27.3 / MyGo v0.7.3-nyadb.2 / Linux amd64，使用 `GOWORK=off GOCACHE=/data/.cache/golang`。日志在忽略目录 `build/verify/ai-sql-20261102`。

- `go test ./... -timeout=20m` 通过：最终追加测试版本 sqlworkspace 3.815 秒，Shell 缓存（此前同一正式代码版本 74.959 秒）；最终 fixture 增补后单独编译通过。UI 表单补充测试覆盖键盘修改模型、保存/重开和不发送请求，取消按钮覆盖 HTTP 请求资源确认。
- aisql/localdb/sqlworkspace 生命周期 race 通过：1.229 秒 / 缓存 / 29.161 秒。最终正式代码的 Shell 全包 race 通过，753.756 秒，使用 20 分钟超时并独立运行；追加 UI 回归 `-race -run TestAI -count=1` 也通过，sqlworkspace/Shell 2.606/2.145 秒。
- `go vet ./...`、`go tool mygo vet`、翻译生成和 i18n 检查通过，1396 条 zh-Hans/en 消息。`go tool mygo build` 通过，正式 Linux 程序及 deb/tar 包已生成。
- `/usr/bin/python3 -m unittest discover -s tests -v`：54 项通过。新增本地 HTTP fixture 测试检查鉴权、模型/流式/存储标志、未选择的 SQL/表数据泄漏拒绝、格式错误及记录不包含 prompt/密钥。默认 conda `python3` 缺少 Pillow 的首次失败保留在 `python-final.log`，不计为通过；无需安装系统依赖。
- 服务测试覆盖普通 JSON/SSE、真实 HTTP 取消、重定向不转发密钥、截断/拒绝/新旧调用格式、settings 条件冲突/失败及退休密钥重试。`ui.NewTester` 覆盖 opt-in 预览、插入/撤销、修改后的旧建议、关闭/新范围回执、底层快捷键隔离和旧工具菜单的标签/generation 撤销。

追加表单测试首轮点击同名可见标题，未聚焦实际输入，测试失败而未改写配置；已改为原生 Tab 导航后编辑，完整及 race 回归通过。失败日志 `ai-ui-race-reviewed.log` 保留，最终结果为 `ai-ui-race-final.log`。发现的响应缺陷是旧 `function_call` 未被拒绝，已同时修复 JSON/SSE 并增加回归。

## 正式原生窗口与真实密钥环

最终正式程序 SHA-256：`08aa65d8d9963b551e26325ebf7ac1fccfbf7df95ee4f41cd804cd583d3877ca`，每次启动同时核对 `/proc/<pid>/exe`。最终 run `3ff4ec1acb7f` 使用私有 D-Bus、gnome-keyring 50.0、Xvfb、Openbox、自有 SQLite 和唯一 localhost HTTP 协议服务。配置/命名资产由验收工具通过正式仓库及密钥环预置；窗口内执行自然语言请求、插入和重启。没有把预置配置当作原生输入 API 密钥的验证。

实际请求一次，鉴权/协议校验通过；未选择的原草稿正文和表行值没有发送。最终建议插入原草稿，独立辅助进程核对草稿保留原文、SavedText/命名资产未变、原 profile/namespace 保留且执行历史为空；重启后窗口显示同一草稿和已有设置。UI 测试另确认建议及弹窗快捷键不调用 SQL 执行器。

随后独立进程 `native-ai-keyfailures` 读取真实 AI 密钥环记录，分别删除记录、写入错误 origin 记录，确认请求密钥获取明确失败且 SQLite 配置字节保持不变；defer 恢复原密钥环记录。没有使用 `keyring.MockInit`。该命令的退出 0 及身份记录位于同一 run 的 `custom-6ddafec3.log` / `run.json`，不是 mock 通过结果。

已查看的最终截图：

- [设置与密钥关联](../build/entry-acceptance/r-3ff4ec1acb7f/evidence/ai-sql-native/settings-client.png)、[1280×800 请求预览](../build/entry-acceptance/r-3ff4ec1acb7f/evidence/ai-sql-native/request-preview-1280-client.png)。
- [1280×800 建议](../build/entry-acceptance/r-3ff4ec1acb7f/evidence/ai-sql-native/suggestion-1280-client.png)、[840×560 建议与固定底栏](../build/entry-acceptance/r-3ff4ec1acb7f/evidence/ai-sql-native/suggestion-840-client.png)。
- [插入后草稿](../build/entry-acceptance/r-3ff4ec1acb7f/evidence/ai-sql-native/inserted-draft-client.png)、[重启恢复](../build/entry-acceptance/r-3ff4ec1acb7f/evidence/ai-sql-native/restart-draft-client.png)。

每张图附实际客户区几何、稳定帧、后端、动作和二进制哈希；字体、实际缩放仍 Unknown。截图来自正式原生窗口，HTTP 服务为自有协议 fixture，没有商业模型质量结论。旧 run `a1df170ad4df` 属于末尾工具调用响应修复前二进制，不作为最终二进制证据。

两个 runs 的私有进程、唯一容器及临时秘密文件均已清理；独立 PID/birth 与成功 Docker 查询审计见 `build/verify/ai-sql-20261102/cleanup-audit.json`。截图/日志/配置没有 API 密钥；遗留加密密钥环证据保持在忽略的隔离目录。

重放工具（每次创建新的空配置）：

```bash
GOWORK=off GOCACHE=/data/.cache/golang PATH=/usr/local/go/bin:$PATH \
  go tool mygo build
GOWORK=off GOCACHE=/data/.cache/golang PATH=/usr/local/go/bin:$PATH \
  /usr/bin/python3 tools/verify_entry_acceptance.py --start-only --keep
# 使用上一步打印的本轮目录；后续无论成功或失败均执行 --stop。
GOWORK=off GOCACHE=/data/.cache/golang PATH=/usr/local/go/bin:$PATH \
  /usr/bin/python3 tools/verify_entry_acceptance.py --run <本轮目录> --keep -- \
  /usr/bin/python3 tools/capture_ai_sql_acceptance.py
/usr/bin/python3 tools/verify_entry_acceptance.py --run <本轮目录> --stop
```

随机密钥仅通过进程环境进入正式密钥环，服务不记录鉴权/请求正文。新版本采集器包含重启后的 `native-ai` 和 `native-ai-keyfailures` 检查；本轮后者在采集完成后同一隔离环境中独立执行。

## 限制及完整剩余目标

没有实测商业服务、不同模型、HTTPS/TLS 失败、代理或服务供应商的参数差异。密钥环服务停/启的 AI 专用网络请求、真实原生取消/错误/解释/优化/另存草稿、原生键盘输入密钥和多平台还没有独立窗口结果；自动测试不替代这些场景。配置崩溃发生于新密钥写入与条件发布之间时，可能留下未发布的随机密钥环记录；已发布旧密钥/配置仍保留。不是 agent、自动执行器、AI 表设计或 AI 数据填充。

ClickHouse 已交付基础原生 SQL/只读共享网格，本阶段 AI SQL 基础也已交付；两者高级管理/类型/模型体验仍可深化。PG 正式通用 Apply/图形表单与证明、PG/SQLite 对象/导入/备份/隧道、跨库传输/数据及结构同步、自动运行、模型/逆向/BI/监控/字典/生成/脱敏/命令行和精细/跨平台/原版验收仍在 [完整实施清单](workbench-completion-2026-11-02.md) 内，本阶段不标记整体完成。
