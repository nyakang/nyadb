# KingbaseES 原生连接基础 — 2026-11-07

本阶段完成公开连接配置、原生驱动边界及真实 KingbaseES 传输验收。
尚未注册工作台产品，已有 15 个产品的支持数量不变。完整参考清单仍
保留 88 个连接类型、110 个产品配置和既有产品的全部缺口。

## 真实服务器和部署

固定第三方镜像 `lihongjie0209/kingbase` 的 Linux amd64 摘要为
`sha256:9f713f8ec943be81bd4dbb321e2158b0ba24e67cd95f2c2864225c37c2ce8d9e`。
不根据镜像名称认定产品：实际启动并通过原生驱动读取的版本为：

```text
KingbaseES V008R006C009B0014 on x86_64-pc-linux-gnu, compiled by gcc (GCC) 4.8.5 20150623 (Red Hat 4.8.5-28), 64-bit
```

`initdb --dbmode=pg` 初始化后，真实 `SHOW database_mode` 返回 `pg`。
目录包括 `pg_catalog` 和 `sys_catalog`，PG 模式下 `pg_database` 可用，
`sys_database` 不存在。默认数据库为 `test`、`kingbase`、`template0`、
`template1` 和 `security`，没有 `postgres`。连接必须显式指定数据库。
原生用户与角色分开处理；本次实际验证 `ALTER USER` 修改测试用户，
`ALTER ROLE` 对该用户返回“不是角色”。这些区别需保留到后续元数据和
管理实现中。

初始候选 `fusondocker/kingbase:8.2` 的二进制报告 V008R002C001B0102，
但实际内核因许可证过期无法启动，其 `initdb --pwfile` 也报告密码选项
冲突。它未用于产品验收。V8R6 镜像的 `--pwfile` 正常；其 PAM `su` /
`runuser` 无法创建会话，使用镜像已有 `setpriv` 切换至原生 kingbase
UID/GID 后成功初始化和运行。没有修改许可证或服务器二进制。

当前夹具绑定随机 loopback 端口、独立临时数据目录和生成的管理员及
普通用户密码。`sys_hba.conf` 的指定用户名规则在本版本未匹配普通
测试用户；夹具使用 `host all all … scram-sha-256`，仍要求真实密码。
真实 `pg_authid` 仅检查测试用户密码是否为 SCRAM-SHA-256，输出布尔值，
不读取或记录密码摘要。许可证限制 10 个连接，夹具显式使用该上限。
这不代表其它认证方式、Oracle/MySQL 模式、V8R2/V8R3/V9 或 HA 已验收。

## 原生驱动和配置

驱动为 DBX 金仓原生 agent 实际引用的
`gitea.com/kingbase/gokb v0.0.0-20201021123113-29bd62a876c3`，MIT。
模块源提交为 `29bd62a876c30fc79f64efd0985712f91b005f2e`，模块校验值为
`h1:QjslQNaH5Nuap5i4nijS0OYV6GMk5kqrAmgU90zBKd4=`。查询上游 latest
仍指向此版本。`third_party/kingbase_gokb` 保留许可证和生产源码，
逐文件来源哈希及本地改动见其 `UPSTREAM.json` 和 `NYADB_PATCHES.md`。
认证、原生 OID 和值解码未修改；未使用 PostgreSQL 驱动替代。

上游构造器会先读取全局 `KB*` 环境，部分变量甚至导致 panic，TLS 还会
隐式读取用户目录证书。新增显式配置构造器直接捕获端点、账户、数据库、
临时密码、TLS 和运行参数，不修改进程环境，不读取 `.kbpass` 或用户
目录证书。公开 `connections.KingbaseConfig` 不含密码，默认端口
54321、TLS `verify-full`；支持 IPv4、裸 IPv6 和主机名，拒绝 URI、歧义
端口、缺失数据库及非法文本。公开 JSON 往返和输入不可变验证通过。

TLS 支持 `disable/require/verify-ca/verify-full`，最低 TLS 1.2，支持
显式 CA 和成对客户端证书路径。`require` 配置 CA 时校验证书链；不提供
隐式明文回退。真实验收覆盖可信 CA 成功、错误名称拒绝、`verify-ca`
允许名称不匹配、移除 CA 后拒绝自签名证书、明文服务器拒绝强制 TLS，
并通过 `pg_stat_ssl` 确认原生连接确实使用 TLS。客户端证书配置已实现，
双向 TLS 尚未做真实服务器验收。

## 数据、事务和生命周期证据

真实普通用户以 SCRAM-SHA-256 认证。正确密码成功，空密码和错误密码
均被拒绝，错误不暴露测试密码。测试覆盖：

- 大于 2^53 的 int64、NUMERIC(40,20) 完整精度、原始 bytea、Unicode /
  引号 / 反斜线绑定参数、布尔及日期；NULL、空文本、空 bytea 保持区别。
- 两个物理会话的临时表隔离、原生事务回滚及已确认提交。
- 查询、Exec、预编译 Query/Exec、事务内查询的真实运行中取消；通过
  原始 backend PID 确认请求启动及完成后原查询停止。
- 取消后的原 `sql.Conn` 无法继续执行；显式获取的新连接使用不同 PID。
  TLS 和明文取消均通过，真实 race 未发现并发状态或连接清理问题。

本地驱动修复使取消请求使用独立的实际 TLS 套接字，并等待 EOF 确认；
取消 I/O 有界，失败交付也关闭捕获的原查询套接字。BEGIN 和预编译执行
发送首个命令前已安装取消；已经取消的请求不执行协议 I/O。连接初始化
覆盖 TLS / 认证 / 启动的取消和超时，成功后清除初始化 deadline。
完成回调幂等，取消状态只在所属操作等待清理结束后更新。

失去写入或 COMMIT 确认时保留 `ErrOutcomeUnknown`，不会返回允许
`database/sql` 自动重放的 `driver.ErrBadConn`。独立协议夹具验证完整
写入 / COMMIT 已收到而回复丢失的情形：连接池不重放，结果保持未知，
原会话不可复用。这些是传输证明；后续工作台适配器仍须将未知结果映射
为保留草稿、禁止重放，并实现原 profile / generation 的租约与显式恢复。

## 可复现检查

```sh
python3 tools/verify_kingbase_fixture.py
python3 tools/verify_kingbase_fixture.py --plain
NYADB_KINGBASE_RACE=1 python3 tools/verify_kingbase_fixture.py
```

脚本使用唯一 nonce 标签，只删除已核实身份的不可变容器 ID。凭据位于
0700 临时目录中的 0600 文件，证书和所有夹具均在退出时清理；其它
容器不受影响。`--hold` 供后续元数据研究使用，退出仍清理所属资源。
清理的缺失 / 非法检查、其它产品和替换容器回归通过。

真实 TLS、明文和 race 日志位于忽略的 `build/kingbase/`。
驱动嵌套模块 `GOWORK=off go test -race ./...` 和 `go vet ./...` 通过；
它不包含在根模块测试中。根模块 Go 全量测试、配置 / 连接 race、
Go / MyGo vet、i18n 检查和 84 个 Python 工具测试通过。
Linux amd64、Windows amd64、macOS arm64 的应用及新增原生包完成
CGO=0 编译。

## 继续保留的工作

KingbaseES Profile / URI / 持久化与凭据回滚、表单和选择器、provider、
逐物理连接产品与版本校验、SQL 租约及工作台取消恢复仍未接入。
命名空间 / 对象 / 原生类型元数据、共享网格浏览编辑 / 事务 / 导入导出、
原生脚本文件执行需独立验收；表设计、对象和账户管理、备份恢复、
维护监控、结构 / 数据对比以及其它完整参考能力继续保留。
