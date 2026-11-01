# 系统密钥环、MySQL 入口与 Linux 原生验收 — 2026-11-02

本轮从干净的 `c96a01a` 开始，在 2026-11-01 至 11-02 完成独立实现及运行验收。证据为 **I** 和 NyaDB 原生运行结果；没有启动 Navicat、连接生产数据库或新增原版 **O/V**。未执行 commit、push 或 Git 历史操作。

## 修复与工具

修复两处正式实现问题，保留 SQLite schema、加密格式、凭据命名空间和公开接口：

- `queries.Store.SaveWorkspace` 写入前先认证已有草稿。过去，新 Store 拿到长度合法的错误密钥后，直接保存可能覆盖旧密文；现在拒绝写入，并保留原记录和修订。新增“首次操作就是写入”的 SQLite 回归，真实 Secret Service 也验证密文摘要不变。
- MySQL `SessionAdapter.Browse` 接入已有 `ObjectsWithPrograms`，并为表、视图、函数、过程和事件附加原 profile/catalog 的中立对象身份。过去适配器只读取表和视图，使已接通的程序入口显示空列表；新增适配器回归和真实 Shell 列表验收。

新增工具：

| 工具 | 职责 |
|---|---|
| `tools/verify_entry_acceptance.py` | 创建唯一隔离环境、局部解包公开的密钥环依赖、管理私有服务、执行集成验收、记录版本/退出码/资源身份并清理 |
| `tools/entryfixture/main.go` | 正式 OSVault / WorkspaceKey / SQLite 跨进程验证、密钥失败检查、正式应用配置及自有数据准备、从仓库和数据库复核原生操作 |
| `tools/capture_entry_acceptance.py` | 最终正式二进制的普通鼠标/键盘操作、原生系统文件选择、重启、截图，以及独立 420 像素 Shell fixture |
| `tools/capture_alignment_gui_linux.py` | 增加可用截图后端选择，scrot 不可用/失败时使用 ImageMagick；F2 通过当前 X 键盘映射与 XTest 发送，并记录实际后端、窗口尺寸和二进制哈希 |

新增 Shell 集成测试使用真实 dbruntime、MySQL 服务和 OSVault，通过 `ui.NewTester` 操作入口。仅文件选择回调定向到 fixture；文件写入仍用正式的原子 `queries.WriteStream`。真实密钥环测试在独立进程执行，不运行调用 `keyring.MockInit` 的凭据测试。原生窄窗口通过专用测试开关在 `TestMain` 的主 goroutine 启动 MyGo，普通测试仍执行 `m.Run()`。

## 隔离与复现

依赖：Linux amd64、Go 1.27.3、固定的 MyGo `v0.7.3-nyadb.2`、Docker、D-Bus、Xvfb、Openbox、xdotool、XTest、xclip、ImageMagick，以及带 Pillow 的 `/usr/bin/python3`。首次缺少 keyring daemon 时，工具仅 `apt-get download` 并 `dpkg-deb -x` 到忽略目录，不安装系统包。

```bash
/usr/bin/python3 tools/verify_entry_acceptance.py --native
```

该命令使用 `GOWORK=off`，启动私有 Secret Service / D-Bus / Xvfb / Openbox 和唯一 MySQL 8.4 容器，执行跨进程、密钥失败和 Shell 集成测试，重新 `go tool mygo build` 后采集原生场景。每次都有独立配置、应用缓存、运行和证据目录。MySQL 仅绑定随机 localhost 端口。密码通过 stdin 或受限 fixture 环境传入，调试恢复所需的临时文件为 0600；没有明文密钥回退。

默认退出、失败或中断都会清理本次资源。进程以 PID 与 `/proc` 启动身份核对，容器以唯一 label 核对；命令本身也受进程组管理。同一 run 的后续命令通过锁串行执行。保留现场只用于显式 `--keep`：

```bash
/usr/bin/python3 tools/verify_entry_acceptance.py --start-only --keep
/usr/bin/python3 tools/verify_entry_acceptance.py --run build/entry-acceptance/r-<运行标识> --keep -- <命令>
/usr/bin/python3 tools/verify_entry_acceptance.py --run build/entry-acceptance/r-<运行标识> --stop
```

不能用 `--stop` 创建新环境，也不能以裸 `--run` 重写既有验收记录。`run.json` 不含密码或密钥；依赖包、容器镜像、程序、截图和窗口客户区身份分别记录。运行配置与全部图片保留在 Git 忽略目录。

## 自动测试与真实服务

完整最终运行位于 `build/entry-acceptance/r-99dd715659cd/`，`run.json` 记录逐阶段退出码和清理结果。此前成功的完整环境 `r-55f022c4822e` 及真实服务 race 环境 `r-dac2399d8de4` 保留历史证据。

| 验证 | 结果 / 证据 |
|---|---|
| `GOWORK=off go test ./...` | PASS；`build/entry-acceptance/go-test-final.log` |
| Shell 生命周期 race，全包，20 分钟超时 | PASS，659.856 秒；`race-shell.log` |
| dataworkspace 生命周期 race，单独全包，20 分钟超时 | PASS，495.249 秒；`race-data.log` |
| queries / credentials / localdb / dbruntime / mysql / sqlworkspace / objectworkspace / sessionbrowser race | PASS；`race-lifecycle.log` |
| 真实 MySQL Shell 入口 race | PASS，116.437 秒；`r-dac2399d8de4/evidence/custom-9e376359.log` |
| Go vet / MyGo vet | PASS；`go-vet-final.log`、`mygo-vet.log` |
| 翻译生成 / 检查 | PASS，zh-Hans / en 1125 条；`generate.log`、`i18n-check.log` |
| Python 测试 | PASS，42 项；`python.log`；覆盖截图后端、无修饰 F2、资源身份及清理边界 |
| 最终 MyGo build | PASS；最终 run 的 `evidence/formal-build.log`，Linux executable / .deb / .tar.gz 位于 `build/linux-amd64/` |
| `git diff --check` | PASS |

真实 Secret Service 验证：主/备用连接凭据及跨 profile 隔离、命名查询、分组、脏正文和光标的跨进程恢复；停止/重启服务；删除密钥和替换为错误密钥后拒绝覆盖，已有密文摘要保持不变，缺失密钥不会被重新生成。服务不可用时，UI 保留输入文本和修改状态，取消第一次文件选择后仍能明确另存为 SQL 文件；重启服务后的新进程可读取原记录。

真实 MySQL 验证：函数、过程和事件列表；账户读取、创建、授权、撤权、删除；只读账户的读取错误和服务端拒绝创建。备份、恢复预览及确认、两类 SQL 转储经过入口调度和数据库侧复核，比较字段结构、行数、NULL、空/非空二进制及 `DECIMAL(40,20)` 的完整值。SQL 转储保留原 schema，验收只重绑定自有转储副本到独立目标后执行。

真实锁等待验证取消和断开后的回执，既有输出文件不被替换。MySQL 服务器可能在客户端取消后继续保留元数据锁等待者直到 fixture 解锁，因此以驱动回执及正式原子写入器临时文件的清理判断完成，不把服务器线程列表消失误当作客户端取消完成条件。连接切换后旧面板不进入新 profile；旧菜单和恢复预览在断开/代次变化后不能执行，目标 sentinel 保持不变，另一连接保持有效。

## Linux 原生结果

最终正式程序在私有 Xvfb / Openbox 桌面使用真实密钥环和正式 SQLite 仓库运行，界面为中文、暗色，窗口客户区实际测量为 1280×800 和 840×560。资产操作每一步由独立进程读取正式仓库核验；恢复结果由独立数据库连接复核。

正式二进制 `build/linux-amd64/nyadb` 的 SHA256 为 `eb68a46663e61349c57f3066753e590337801ecc1c1c1343d0ef438fb30e2fe7`，与本次采集 JSON 的记录一致。

证据目录为最终 run 的 `evidence/native/`，每个场景有桌面 PNG、客户区 PNG 和 JSON：

| 场景 | 主要截图 |
|---|---|
| 离线命名查询管理 | `query-manager-offline-client.png` |
| 无修饰 F2 与改名保留脏正文 | `query-f2-client.png`、`query-renamed-client.png` |
| 删除确认与保留未命名草稿 | `query-delete-confirm-client.png`、`query-deleted-draft-client.png` |
| 重新保存、新建分组、重启恢复 | `query-grouped-client.png`、`query-after-restart-client.png` |
| 正式窗口最小宽度 | `formal-840-client.png` |
| 真实函数列表与账户操作 | `functions-native-client.png`、`accounts-created-client.png`、`accounts-granted-client.png` |
| 原生备份、恢复预览与恢复结果 | `backup-completed-client.png`、`restore-preview-client.png`、`restore-result-client.png` |
| 420 像素工具栏组件 | `toolbar-420-overflow-client.png`、`toolbar-420-backup-menu.png` |

420×320 是独立 Shell 组件验收窗口，正式 bootstrap 的最小宽度仍为 840。GTK 原生菜单可以伸出客户区，备份子菜单使用完整桌面 PNG 展示全部标签；客户区截图仍严格保持 420 像素。JSON 记录软件来源哈希、实际几何、截图后端和操作序列，没有将尺寸或 UI 状态伪装为原版差分验证。

## 失败修复与验证限制

首次验证发现的错误密钥覆盖和程序列表适配断点已在正式模块修复并回归通过。工具调试遇到：默认 Conda Python 没有 Pillow、scrot 缺失、占用显示号导致 Xvfb 输出前缀、在测试 goroutine 中启动 MyGo，以及把只读元数据条数/取消后的服务器线程消失作为等待条件。已分别采用系统 Python、可用截图后端、实际显示号解析、主 goroutine 原生 fixture 和符合实际行为的验收条件复测；失败日志保留，不计作通过结果。

本轮没有未解决的规定场景失败项。原生运行使用 Xvfb 下的软件绘制，未验证物理 GPU、其它字体/缩放组合；字体和有效缩放仍在采集 JSON 中标为 Unknown。系统文件选择器沿用本环境 GTK 语言。Windows/macOS 原生验收和 Navicat 原版 O/V 仍未验证，也没有用 mock 替代真实服务结果。SQL 历史、设计器深化和新引擎未纳入本轮。
