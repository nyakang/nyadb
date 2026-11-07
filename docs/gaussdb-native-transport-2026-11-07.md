# GaussDB 原生连接基础 — 2026-11-07

完成的是原生传输与公开配置基础及真实 Docker 验证。GaussDB 尚未注册
到 NyaDB 工作台，连接选择器仍为展示项；已有 14 个产品的支持数量不变。
本阶段不表示 GaussDB 产品接入完成，也不缩减完整对齐清单。

## 原生驱动与服务器依据

- 驱动为 `github.com/HuaweiCloudDeveloper/gaussdb-go v1.0.0-rc1`，MIT。
  模块源提交 `1eccdbac5e7790f8c13668fd2506c8b712c12993`，标签发布时间
  `2025-04-27T09:33:29Z`。仓库当前重定向至华为云样例组织的
  [database-gaussdb-go](https://github.com/huaweicloud-samples/database-gaussdb-go)。
  `go.sum` 保留模块和 go.mod 的校验值。直接使用原生 `stdlib` 和
  `gaussdbconn`，不使用 PostgreSQL 驱动替代原生认证。
- 候选镜像为第三方 ApeCloud 发布的 `apecloud/gaussdb:2.23.01.200-r2`。
  验证脚本固定 Linux amd64 镜像摘要
  `sha256:0e997ccb1efde0e7dadde899c1b4aec6361b5b1e262a89f9abadb36fa8643540`。
  不把仓库名或标签当作身份验证；实际启动后读取 `version()`，结果为：

  ```text
  gaussdb (GaussDB Kernel 503.1.0 build f53b4ed8) compiled at 2023-03-27 00:43:20 commit 5356 last mr 10743 release
  ```

- 初次直接执行内核时缺少 `libeSDKOBS.so` 的搜索路径；配置镜像自带的
  `LD_LIBRARY_PATH=/opt/gaussdb/app/lib` 后可正常初始化和启动。
  使用 `gs_initdb --dbcompatibility=PG`、`gaussdb --single_node`，真实
  `SHOW sql_compatibility` 返回 `PG`。这是原生内核单节点夹具，不代表
  主备切换、分布式集群或 DWS 已验收。
- `alessmicro/gaussdb` 的仓库说明明确指向 openGauss；它未被用于本次
  GaussDB 证明。商业内核及其文件仅留在本机 Docker，不进入代码仓库。

## 已实现与真实验证

`connections.GaussDBConfig` 只保存公开设置，密码单独传入。
规范化深拷贝 1–16 个端点，保留各自端口，支持 IPv4、裸 IPv6 和主机名，
拒绝重复端点、DSN/用户信息、歧义端口及非法文本。公开配置 JSON 往返
和输入不可变测试通过。

`gaussdb.NewConnector` 使用无真实端点/密码的常量初始化种子，再设置
显式账户、数据库、超时、应用名、引用后的 schema、独立端点和 TLS。
libpq 环境密码、证书、目标会话属性和运行参数不替代公开配置；关闭
跟踪和隐式语句缓存。`OpenDB` 配置有界操作池；工作台 SQL 文档仍需
由后续适配器提供独立池与物理租约。

TLS 默认为 `verify-full`，每个候选端点拥有独立 TLS 配置和原始服务器
名称。支持 `disable/prefer/require/verify-ca/verify-full`，配置根证书
和成对客户端证书路径；最低 TLS 1.2。`require` 配置 CA 时校验证书链。
只有显式 `prefer` 增加各端点的明文回退。真实验证包括可信 CA 成功、
错误端点名称拒绝、`verify-ca` 不强制名称、明文服务器拒绝强制 TLS
及允许显式 prefer。

真实普通测试账户使用 SHA256 认证；正确密码成功，空密码和错误密码
被拒绝，错误不暴露测试密码。测试覆盖：

- 大于 2^53 的 int64、精确 NUMERIC(40,20)、原始 bytea、Unicode/引号/
  反斜线参数、布尔及日期；NULL、空 bytea、空文本保持区别。
- 无法连接的第一端点回退至第二端点，以及原生 read-write 目标选择。
- 两个物理连接的临时表隔离、原生事务回滚和已确认提交。
- 运行中取消 `pg_sleep` 后捕获的原生连接关闭、`CleanupDone` 完成，
  原 `sql.Conn` 无法静默替换为新会话继续执行。

原生驱动默认取消处理会异步清理。测试显式等待原句柄的 `CleanupDone`；
这尚未提供工作台取消完成语义。后续 runtime 必须捕获原句柄、等待其
清理，并使 SQL 文档进入显式恢复状态。不得在取消返回时直接复用连接，
也不得自动重放用户 SQL 或不确定提交。

## 可复现验证

```sh
python3 tools/verify_gaussdb_fixture.py
python3 tools/verify_gaussdb_fixture.py --plain
NYADB_GAUSSDB_RACE=1 python3 tools/verify_gaussdb_fixture.py
```

脚本创建唯一 nonce 标记的容器，绑定随机 loopback 端口，初始化独立
数据目录、普通账户及临时证书；凭据写入 0700 临时目录中的 0600 文件。
结束仅删除已核实 nonce 的不可变容器 ID。清理对其它产品、替换容器和
缺失/非法检查结果的回归通过。`--hold` 可保留当前进程拥有的夹具供
后续元数据研究，退出仍清理自己的资源。

本次 TLS、明文和 race 原生集成通过，日志位于忽略的
`build/gaussdb/{tls-transport,plain-transport,race-transport}.log`。
`CGO_ENABLED=0 go test ./...`、`go vet ./...`、`go tool mygo vet` 通过；
Go race 覆盖配置和真实原生连接。Linux amd64、Windows amd64、macOS arm64
的应用及新增原生包均完成 CGO=0 编译。Python 默认解释器缺少 Pillow，
改用已安装 Pillow 的系统 `/usr/bin/python3` 跑视觉工具测试。

## 保留的工作

GaussDB 的 Profile/URI/配置持久化、凭据保存回滚、连接表单与选择器、
provider 注册、逐物理连接的产品/版本验证、SQL 租约和工作台取消恢复，
schema/对象/原生类型元数据、共享数据浏览/编辑/事务/导入导出仍待接入。
结构/数据对比、表设计、对象/账户管理、备份恢复、监控及其它参考能力
继续留在清单中。SM3、双向 TLS、其它认证方式、主备/分布式/DWS 及
其它版本尚无本阶段验收证据。
