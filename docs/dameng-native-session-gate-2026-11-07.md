# 达梦独立配置与逐物理会话校验 — 2026-11-07

本项在[原生 TLS 传输](dameng-native-tls-2026-11-07.md)上增加独立公开配置、
逐物理产品校验和真实初始 Schema 设置。工作台仍未注册达梦；当前已注册
产品保持 16，完整接入及管理范围继续见[对齐清单](database-middleware-alignment-2026-11-07.md)。

## 配置与身份

`connections.DamengConfig` 独立表达原始 Host/Port、实际 Catalog、认证账户、
TLS 策略和原生选项，不复用 Oracle/PostgreSQL 配置。Catalog 和账户必须
明确指定；默认端口 5236。选项包含调用方自行安装的原厂库绝对路径、初始
Schema、1–120 秒期限、1–64 个连接上限，以及显式证书和服务器名称。
规范化不读取文件、不修改传入值，公开配置不含密码。

`NewSessionConnector` 在每个原始物理句柄进入连接池前读取
`SYS.V$VERSION` 和 `SYS.V$DATABASE`，要求实际原生 DM8 固定构建
`08134283904-20220804-166351-20005`、实际 Catalog 和认证账户与配置一致。
同时要求原生 DM 模式、UTF-8、大小写敏感、按字符计算长度，以及与显式
TLS 策略一致的服务器加密模式。其它版本和兼容模式明确拒绝，不能从连接
成功推断产品支持。该步骤适用于之后的元数据连接和 SQL 物理租约。

身份表达式使用原生内建函数和无 FROM 的 SELECT。实际服务器不能以
`SYS.SF_*` 调用这些内建函数，也不存在 `SYS.DUAL`；没有使用猜测的
限定名。真实用户自建同名 `SF_*` 函数可在显式限定后调用并返回伪造值，
但不会覆盖身份表达式中的内建调用。自建 `DUAL` 视图同样可独立查询，
身份校验和 DPI Ping 不读取它。固定服务器另禁止创建与系统 `V$VERSION`
和 `V$DATABASE` 同名的用户视图（-2829）；表名也受保护（研究观察 -2828）。

## 初始 Schema 与句柄生命周期

真实实验证明，登录前设置 DPI 属性 12354 会成功返回，但服务器当前
Schema 仍为认证账户的默认 Schema。现在通过原句柄执行双引号转义的
`SET SCHEMA`，随后读取实际当前 Schema，拒绝未生效或缺失的 Schema。
含中文、大小写、空格、单双引号和 SQL 标点的 Schema 作为一个完整标识符
保留，不拼成 SQL 表达式。

产品包装器先使用账户默认 Schema 校验原始产品和身份，然后才设置所需
Schema；设置后再次核对产品、Catalog、账户、Schema 和原生模式。底层
DPI 的显式 Schema 选项也执行并验证真实设置。两个独立物理句柄分别
初始化，不影响另一个连接原有的当前 Schema。

所有步骤使用同一有界上下文。产品不匹配、设置失败、行清理失败或取消
时，只关闭捕获的拒绝句柄。取消等待原始调用确认；尚未确认的操作不会
提前释放句柄。后开的拒绝连接不会关闭先开的有效连接，失败不会转换成
`driver.ErrBadConn` 或触发自动重放。

## 验证与保留范围

```sh
LD_LIBRARY_PATH=/absolute/path/native-dependencies \
python3 tools/verify_dameng_fixture.py --tls --audit-dpi --race \
  --dpi-library /absolute/path/libdmdpi.so
```

脚本现在同时运行 `dameng` 产品校验和 `dameng/dpi` 原始边界的真实测试。
普通账户无需额外系统视图授权便能完成身份检查；空密码和错误密码拒绝。
含引号中文 Schema、两物理会话隔离、缺失 Schema 拒绝、自建同名函数/
DUAL，以及后开物理连接拒绝均有真实实例回归。注入测试另外验证模式/
版本/Catalog/账户替换、Schema 设置前后校验顺序、行清理错误和取消确认。
后开连接的版本拒绝使用真实原生句柄和注入的不匹配身份响应，并确认先开
句柄仍能执行真实 SQL；没有声称已部署其它服务器版本来完成版本拒绝测试。

2022、2023、2026 三份已允许的 Linux amd64 原厂客户端均完成真实 TLS、
精确值/事务、预处理/会话隔离、初始 Schema、认证和取消/不确定 INSERT/
COMMIT 回执回归。2026 客户端另使用脚本完整新建并清理 Docker 夹具。
日志在忽略的 `build/dameng/session-gate-reviewed-202{2,3,6}-race.log`。
另新建显式禁用 TLS 的实例，完成产品/Schema 校验和原生边界回归，见
`session-gate-reviewed-2026-plain-race.log`；两种模式没有互相回退。

`CGO_ENABLED=0 go test ./...`、Go/MyGo vet、翻译检查和 87 项 Python
测试通过；AGENTS.md 指定的工作区生命周期包列表另通过 race 检查。
Shell 的整包 race 超出默认 10 分钟总时限后，以 30 分钟总时限单独
重跑；保留初次总时限日志和完整重跑日志，不把超时记作通过。
Linux amd64 / Windows amd64 / macOS arm64 的应用以及
`dameng`、`dameng/dpi` 测试程序均完成 CGO=0 编译；Windows/macOS
不代表原厂 ABI 或原生运行已验收。

完整原厂环境/客户端配置隔离仍待完成。独立 Kind/Profile/URI/持久化/
凭据表单/provider、限定元数据、共享 SQL 和唯一共享数据工作区、完整
类型/LOB/认证/其它构建与管理工具尚未接入。固定服务器的客户端证书
强制验证限制仍以[TLS 报告](dameng-native-tls-2026-11-07.md)为准。本项
不开启工作台能力，也不宣称达梦或 Navicat/DBX 的整体对齐完成。

本报告记录会话校验阶段；后续原厂环境/客户端配置隔离已在
[进程隔离阶段](dameng-native-process-isolation-2026-11-07.md)完成限定客户端验收，
工作台注册及其余范围仍保留。
