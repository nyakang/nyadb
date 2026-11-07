# 达梦连接配置、URI 与持久化基础 — 2026-11-07

在[原生客户端进程隔离](dameng-native-process-isolation-2026-11-07.md)基础上，
完成独立 `connections.Dameng`、Profile 设置、URI 解析与本地持久化。
配置可通过既有逐物理会话校验连接到真实 DM8。达梦工作台尚未注册，
已支持的独立产品数量仍为 16；此项不代表完整工作台或参考能力对齐。

默认值为 localhost:5236、Catalog DAMENG、用户 SYSDBA 和 verify-full。
使用前须明确填写调用方安装的原厂客户端绝对路径及客户端证书/密钥路径。
公开设置保存独立版本、初始 Schema、连接/操作超时、后台/SQL 连接数以及
证书文件路径。配置只校验这些设置，不加载库、不访问证书或发起连接；
客户端摘要和服务器身份校验继续由原生连接边界执行。

Catalog、用户和 Schema 保留大小写、Unicode、引号和首尾空格，避免改变
原生限定身份。达梦设置不会归入 Oracle、MySQL 或 PostgreSQL 家族。
配置拒绝其它引擎的选项、MySQL 替代配置以及缺少原厂库/显式端口的 Profile。

## URI 与保存行为

`dameng://` 和 `dm://` 支持明确账户、Catalog、主机及方括号 IPv6，
省略 URI 端口时采用 5236。以下示例仅展示配置格式，不含密码：

```text
dameng://SYSDBA@localhost/DAMENG?library_file=%2Fabsolute%2Fpath%2Flibdmdpi.so&sslmode=disable
```

支持的参数为 `sslmode`、`library_file`、`schema`、`ca_file`、
`certificate_file`、`key_file`、`server_name`、`connect_timeout_seconds`、
`operation_timeout_seconds`、`max_connections` 和 `sql_max_connections`。
重复/未知/空参数、歧义 IPv6、无效端口、缺少账户或 Catalog、控制字符和
未验证的 TLS 策略均被拒绝。TLS 默认为 verify-full，要求明确客户端身份；
明文须显式指定 disable。密码仅存于临时 `URIImport.Password`，错误、
格式化结果、JSON 和保存的 Profile 不含密码。

SQLite 继续使用既存 `mysql_advanced_json` 列，新增版本 1、kind=dameng
的独立 envelope，无需迁移或改变 MySQL 保存格式。缺少/不匹配的 envelope、
不完整设置、未知字段及混入其它引擎选项会失败；不从空的达梦记录猜测
客户端路径或 TLS 设置。创建、更新、复制、失败更新保留旧值、关闭数据库
后重启读取均验证通过，旧 MySQL/PostgreSQL/SQLite 空配置继续恢复既有默认值。

界面 URI 导入要求该产品已有工作台 descriptor。达梦尚无表单/provider，
当前会保留已停放的编辑器和原凭据并报告不支持。该限制避免原生配置被
错误的通用表单改写，后续正式注册时即可沿用导入流程。

## 真实验证

自有 Docker TLS 与显式明文夹具仍使用原生 DM8
`08134283904-20220804-166351-20005`，原生模式、UTF-8、区分大小写和字符长度。
2022/2023/2026 三份已允许原厂客户端均完成 URI 导入、配置 SQLite 保存、
关闭并重新打开配置数据库、重新读取 Profile、用临时密码建立两条独立
物理连接，验证原 Catalog、认证账户、带特殊字符与尾部空格的 Schema 和
38 位精确 DECIMAL 参数。新 Profile 的初始 Schema 不改变原管理会话。
2026 客户端另通过两种传输模式的 race 验证。

最初测试将 DECIMAL 的尾部零格式也作为字符串身份；原生查询返回同值的
19 位小数表示，断言失败。独立查询记录确认数值一致，未发现精度损失。
最终测试使用非零结尾的 20 位小数参数，完整 38 位值通过；原始失败与
诊断保留在忽略的 `build/dameng/`。

可在自有已核验夹具上执行：

```sh
NYADB_DAMENG_DPI_IT=1 \
NYADB_DAMENG_DPI_CONFIG=/absolute/path/private-probe.json \
NYADB_DAMENG_DPI_LIBRARY=/absolute/path/libdmdpi.so \
LD_LIBRARY_PATH=/absolute/path/native-dependencies \
go test -count=1 -run TestNativeDamengURIProfileRestart -v ./internal/services/dameng
```

`tools/verify_dameng_fixture.py --hold` 创建明文夹具；添加 `--tls` 创建 TLS
夹具，输出配置文件路径。夹具密码只保留在私有本机文件中。
完整 CGO=0 Go 测试、Go/MyGo vet、翻译检查、87 项 Python 工具测试、
配置/存储/原生包和连接编辑器 race，以及 Linux amd64、Windows amd64、
macOS arm64 的 CGO=0 应用构建均通过。后两平台构建不代表原厂 ABI 已支持。

## 后续范围

独立原生连接表单、凭据库保存/回滚、chooser/provider 注册、限定元数据、
共用 SQL 与唯一数据网格、编辑/事务/导入导出及工作台生命周期继续待完成。
完整原生类型/LOB、其它认证/客户端/服务器构建、表设计、对象/账户管理、
备份维护和所有参考缺口继续留在[完整清单](database-middleware-alignment-2026-11-07.md)。
原生固定服务器不强制客户端证书信任的边界仍以
[TLS 报告](dameng-native-tls-2026-11-07.md)为准。
