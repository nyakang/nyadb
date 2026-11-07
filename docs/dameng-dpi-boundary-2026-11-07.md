# 达梦 DM8 私有 DPI 会话边界 — 2026-11-07

后续[原生 TLS 传输基础](dameng-native-tls-2026-11-07.md)已增加显式远程
策略和服务器身份校验，并记录固定服务器的客户端证书限制；以下保留本
次 loopback 基础提交的原始验收范围。

新增 `internal/services/dameng/dpi`，使用调用方安装的原厂客户端 API，
验证精确绑定、原会话取消和不确定提交。当前构造器明确只接受 loopback
IP；尚未注册产品、远程连接、TLS 或工作台。已注册产品数量保持 16，
达梦在完整清单中仍为 `missing`。本项是可复用的私有边界，完整传输
验收和工作台接入继续推进。

## 客户端与来源

API 函数签名、类型宽度及常量来自原厂 DM8 8.1.3.12 的
`DPI.h` / `DPItypes.h` / `DPIext.h`，并对照
[DPI 编程指南](https://eco.dameng.com/document/dm/zh-cn/pm/dpi-rogramming-guide.html)。
DM64 的 `slength` / `ulength` 是 64 位，DPIRETURN 是有符号 16 位；
DSQL 类型号与 ODBC 类型号分别定义，不混用。

仅用既有 MIT `purego` 调用公开 API。厂商头文件、驱动源码、库和许可证
未加入仓库；这不代替厂商客户端自身的使用或再分发条款。没有引入上项
存在精度、重放和发布边界问题的 Go 候选驱动。

当前允许的 Linux amd64 库摘要及实际 `DSQL_ATTR_CLIENT_VERSION`：

| 原厂客户端版本 | SHA256 | 来源 |
| --- | --- | --- |
| V8.1.2.128-Build(2022.08.04-166351)CTM | `6f806e005bb260594eb31a86726a178e6dfe27b10c0fd95fcc894ba118736f90` | 前置验收的固定 Docker 镜像 |
| V8.1.3.59-Build(2023.07.25-197039)ENT | `71f21a833797914960ac78b054bdf2e52d519d8d9a3119e5c7f3d02bb087c1d8` | dmPython 2.5.8 官方 PyPI wheel；2.5.7 对应库摘要相同 |
| V8.1.5.88-Build(2026.09.22-350878)ENT | `84935dae2b77598ba32ae28eb12c2bdb89f09a7fe6555af3d48e59e098e5ccb3` | dmPython 2.6.1 官方 PyPI wheel |

8.1.3.12 / 2023-04-17 库的原始精确绑定测试返回 `-70049`，未加入允许列表。
构造器拒绝相对库路径、未验收摘要或平台；Windows / macOS 只完成编译，
没有声称其原厂客户端 ABI 已验证。应用不自动下载或安装这些库。

## 原始 I/O 与生命周期

不同客户端的 `dpi_cancel` 虽返回成功，真实运行中的查询仍等到约 5 秒
执行超时才返回；旧客户端的长调用还观察到约 10 秒。直接在容器内连接
结果相同。仅设置 `DSQL_ATTR_QUERY_TIMEOUT`，或登录后调低连接超时，
也没有解决所观察构建的延迟。

新边界为每个物理连接建立独立 loopback 转发，Go 持有唯一的原始上游
socket。它只转发字节，不解释协议、记录内容、重连或重试。取消关闭该
会话的两端 I/O；原 DPI 调用返回、取消 watcher 和转发线程全部退出后，
才释放语句、连接和环境句柄。其它会话不受影响。

调用方期限与配置的操作上限覆盖语句分配、参数/属性设置、执行和元数据，
流式 fetch / 字段读取也有独立的有界调用。Rows 保留调用方上下文，
不保留已经释放的临时操作上下文。登录包含原始拨号及握手的明确期限。
预处理语句、流式 Rows、事务、重复 Close 和并发 Close 保留原始所有权。

真实 200 毫秒期限的查询，在最终 race 验收约 **202 毫秒**后完成原调用
和资源退出；取消后原 `sql.Conn` 无法继续，显式取得新连接才能恢复。
登录测试向真实原厂客户端提供不回复的自有 socket；取消后人为挡住
原 login 返回，确认函数没有提前返回，原句柄没有提前释放。释放门闩后
只清理该次环境/连接。纯 Go 门闩测试同时验证执行中的句柄释放顺序。

所有请求均不返回 `driver.ErrBadConn`，防止 `database/sql` 自动重放。
执行期间的取消、连接失效及回复丢失保守返回 `ErrOutcomeUnknown`；
QueryContext 也可能含有副作用，因此采用同样的结果边界。

## 真实数据与故障验收

使用[前置验收](dameng-readiness-2026-11-07.md)的固定原生 DM8 服务器，
版本 `08134283904-20220804-166351-20005`，`COMPATIBLE_MODE=0`、UTF-8、
大小写敏感和 `LENGTH_IN_CHAR=1`，并非 Oracle 协议或兼容服务器。

- BIGINT 大于 2^53；原生 19 字节 NUMERIC 绑定/读取保持完整
  `DECIMAL(38,20)`，无浮点中间值；Unicode、引号、反斜线、二进制、NULL
  和空文本通过往返测试。非法指数、超精度与非有限浮点数明确拒绝。
- 普通用户的正确密码成功，错误和空密码被拒绝。公开错误只保留原生代码，
  不输出包含密码、SQL 或 DSN 的厂商诊断文本。
- 实际回滚、确认提交、预处理 INSERT 和重复读取 Rows 通过。
  同一 GLOBAL TEMPORARY TABLE 的两个物理连接分别读到 2 行和 0 行。
- 故障代理先收到真实 INSERT / COMMIT 请求，转交服务器，再丢弃其回复。
  分配语句的回复照常转发，避免把分配失败冒充实际提交丢失。
  两种情形均返回不确定结果；独立连接确认表中恰好 **1 行**，原会话不可
  继续，未触发驱动/连接池重放。

读取最多 1 MiB，截断或未知类型明确报错。该界限不代表完整 LOB、全部
日期/时区/间隔/复合类型或编辑矩阵已经验收；这些仍留在后续能力范围。

## 可复现检查

安装原厂客户端及其运行依赖后，指定调用方自己的绝对库路径：

```sh
python3 tools/verify_dameng_fixture.py --audit-dpi --dpi-library /absolute/path/libdmdpi.so
python3 tools/verify_dameng_fixture.py --audit-dpi --race --dpi-library /absolute/path/libdmdpi.so
```

审计运行在子进程中；若客户端认证需要其动态加载的加密库，调用方需在
该审计进程的 `LD_LIBRARY_PATH` 配置原始依赖目录。本次 2026 版使用
自有忽略目录中的原厂依赖完成认证，不能由此推出 TLS 验收。

脚本沿用唯一 nonce、不可变容器 ID、私有临时凭据及随机 loopback 端口。
新增 CLI 参数在部署前验证；结束清理本次资源。一次全新实例上的完整
native race 测试通过；三种允许的客户端都完成真实 race 回归。
`CGO_ENABLED=0 go test ./...`、Go vet、MyGo vet、87 项 Python 测试，
以及 Linux amd64 / Windows amd64 / macOS arm64 的应用和 native 测试
CGO=0 编译通过。日志位于忽略的 `build/dameng/`。

## 后续范围

远程端点与可信 TLS/证书身份、完整环境/客户端配置隔离、其它原生平台和
认证/版本/兼容模式、完整类型和 LOB、逐物理产品身份校验仍需独立验收。
连接 Profile/URI/配置持久化/凭据/表单/chooser/provider、限定元数据、
SQL 租约与脚本/文件、唯一共享数据 State/Document 及精确编辑/事务/
导入导出均尚未接入。表/视图/程序/账户管理、备份维护以及所有参考的
跨产品能力继续保留；本项没有开启任何工作台能力标志。
