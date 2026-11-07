# 达梦 DM8 接入前置验收 — 2026-11-07

本文保留 `c55d905` 的候选 Go 驱动审计证据。后续已完成
[私有 loopback DPI 会话边界](dameng-dpi-boundary-2026-11-07.md)，
其精确绑定和取消/不确定结果不改变本文对原候选驱动的结论。
远程/TLS 完整传输验收及工作台接入仍未完成。

达梦实例已在本机 Docker 正常部署；原生 Go 候选驱动的实际缺陷和配置
规避方法已复现。本项交付可复用的部署、身份检查和隔离审计工具，
尚未完成原生传输基础或工作台接入。达梦在完整对齐清单中继续为
`missing`，NyaDB 已注册产品仍为 16 个。完整 88 类型、110 配置及
其它产品和跨产品功能缺口均保留。

## 实例和来源

固定镜像 `sizx/dm8` 的 amd64 摘要：
`sha256:c4088323442747a45d58bd33ef59e657c9b85f9e7dd956ba60b36d04a35b141d`。
真实查询 `V$VERSION` 返回：

```text
DM Database Server 64 V8
DB Version: 0x7000c
08134283904-20220804-166351-20005
```

`V$DM_INI` 确认 `COMPATIBLE_MODE=0`，数据库 `DAMENG`；实际函数返回
UTF-8 标志 1、大小写敏感标志 1。初始化显式设置 16 KiB 页和
`LENGTH_IN_CHAR=1`。默认开发许可证在本次启动报告有效至 2026-11-21；
不修改许可证或服务器文件。该实例只证明所观察构建，不能推广到其它
版本、兼容模式、主备、MPP 或集群。

镜像原始启动脚本依赖默认密码和额外服务。夹具以独立脚本调用原始
`dminit` 和 `dmserver`，密码来自私有文件；实际需要显式设置
`LD_LIBRARY_PATH=/opt/dmdbms/bin`，否则 `dminit` / `disql` 找不到其
运行库。普通 Docker 权限即可启动，无需 privileged。端口只绑定随机
loopback 地址，数据位于自有容器中；退出核对不可变容器 ID、产品标签
和 nonce 后清理，临时凭据目录随之删除。

本地 DBX 使用 `com.dameng:DmJdbcDriver8:8.1.5.45` 和 Java agent。
`dameng.yaml` 的 Oracle transfer family 是目标声明，不能作为复用
Oracle 协议或驱动的证据。

## 驱动审计和实际结果

隔离研究模块使用未修改的 `gitee.com/chunanyong/dm v1.8.23`；来源提交
`8ba262af5d7ddcd733eb21e7485f8831689d8c28`，模块校验值
`h1:IadSVTP3l1qv41yzTaHffpsAdNtyR3Bl2FwR9Xpgpb8=`。镜像 README 将它
描述为原厂 8.1.4.200 Go 驱动的同步版本，尚未完成对应原厂发行包比对。
该模块没有许可证文件，仓库 API 也没有声明许可证。

旧 `package.dameng.com` 下载地址 TLS 失败；当前
[官方下载地址](https://download.dameng.com/eco/adapter/resource/go/dm-go-driver.zip)
可以访问。此次实际包为 8.1.3.12，包含源码版权声明，但没有许可证
文件。包哈希见 [机器证据](dameng-readiness-2026-11-07.json)。另一个
新版镜像仓库附有商业限制协议；它不能替代原厂对当前候选版本的
来源和再分发授权证明。NyaDB 主模块没有新增达梦依赖，也没有提交
厂商源码或库文件；研究模块仅保存自有测试代码和外部依赖版本。

真实实例观察：

| 情形 | 结果和接入要求 |
| --- | --- |
| BIGINT、Unicode / 引号 / 反斜线绑定、VARBINARY、NULL 与空文本 | 保持原值；尚未覆盖完整类型矩阵 |
| 直接绑定十进制字符串到 `DECIMAL(38,20)` | `123456789012345678.12345678901234567891` 变为 `123456789012345678.125`；不能直接用于精确编辑 |
| 显式 `CAST(CAST(? AS VARCHAR(100)) AS DECIMAL(38,20))` | 实际完整保存上述值；后续仍需独立类型规划和完整编辑验证 |
| 实际事务回滚、确认提交 | 行数分别保持原状和增加一行 |
| 默认连接切换配置，已写入但回复丢失 | 单次 INSERT 的调用返回错误，真实表却留下 **3 行**；发生连接池重试 |
| 显式 `doSwitch=0`，同样丢失回复 | 调用返回错误，表仅留下 **1 行**；仍需映射为不确定结果、禁止重放并退休原会话 |

故障代理先把完整 INSERT 发给真实服务器，收到服务器回复后才丢弃
回复并关闭原套接字。它不修改协议或记录凭据。默认切换 filter 将
通信错误转换为 `driver.ErrBadConn`，触发 `database/sql` 重试；单独
设置 `driverReconnect=false` 不能避免该行为。`doSwitch=0` 禁用
此 filter，本次实际不再重复写入。此规避并不证明连接失效处理、
COMMIT 未知结果或取消生命周期已正确。

候选驱动还会读取全局 `dm_svc.conf`，自定义 dial 依赖全局注册；TLS
代码默认跳过证书校验。隔离研究进程显式指定空配置文件和关闭日志，
没有把这些行为引入应用。认证拒绝矩阵、普通用户、可信 TLS、完整
启动取消和已确认取消均仍待验收。

## 重现和检查

```sh
python3 tools/verify_dameng_fixture.py
python3 tools/verify_dameng_fixture.py --audit-driver
python3 tools/verify_dameng_fixture.py --audit-driver --race
GOWORK=off go test -race ./...  # 在 tools/damengprobe 中执行
GOWORK=off go vet ./...        # 在 tools/damengprobe 中执行
/usr/bin/python3 -m unittest discover -s tests -v
```

`--audit-driver` 执行独立 Go module 的研究代码，安装其声明的外部依赖；
它不注册 NyaDB provider。审计成功表示稳定复现现状，报告明确保留
`safe_for_workbench=false`，并非批准候选驱动。`--hold` 保留自有实例
供后续研究，退出仍清理资源。

本次真实普通 / race 审计、代理生命周期 race、独立 module vet 和
86 个 Python 工具测试通过。Python visual 工具需要 Pillow；系统
`/usr/bin/python3` 已安装，默认 conda Python 未安装。新增研究命令
完成 Linux amd64、Windows amd64、macOS arm64 的 CGO=0 编译。
没有修改应用运行代码或界面，因此没有重跑上一项已通过的全量应用
测试 / 构建，也没有新增界面截图。日志位于忽略的 `build/dameng/`。

## 后续范围

继续研究用户提供的原厂客户端边界，或取得可以随应用发布且符合
生命周期要求的连接实现。镜像提供 `libdmdpi.so`，官方有 DPI 接口
文档，但本次尚未实现或验证 DPI runtime。

独立配置 / URI / 持久化 / 凭据、provider 和逐物理身份校验、限定
元数据、SQL 与共享数据网格、精确绑定、编辑 / 事务 / 导入导出、
认证 / TLS / 取消 / 断线恢复 / 未知提交，以及表设计、对象和账户
管理、备份维护和全部参考能力继续保留为未完成。
