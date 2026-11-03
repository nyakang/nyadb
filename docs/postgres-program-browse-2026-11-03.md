# PostgreSQL 函数、过程与序列浏览 — 2026-11-03

## 正式行为与边界

接续 `95ae357` 后的未提交对象浏览改动，正式提供 PostgreSQL 函数、存储过程、序列目录和只读定义文档。导航、工具栏、对象列表、查找和收藏入口共用元数据及对象身份；浏览能力不依赖表设计保存能力。PG 正式设计 Apply、对象 CRUD、账户权限和文件工具仍在后续清单中。

- `Browse` 与 `Objects` 保持现有接口，使用同一限定目录快照。函数名使用服务器生成的引用名称及输入类型签名；OUT 参数名和默认值不参与身份，支持同名重载、引用名称及跨 schema 类型。聚合不作为普通函数定义读取。
- 目录在原物理会话的只读 repeatable-read 事务中读取，局部设置 `search_path=pg_catalog` 和标准字符串规则，完成后恢复连接池原设置。读取不调用程序，不执行序列 `nextval`，不读取当前序列值。
- `DBObject.Sequence` 指向不可变的 `SequenceInfo` 静态配置，整数均为 `int64`。序列 SQL 仅从类型、起点、步长、范围、缓存、循环参数重建；不包含当前值、所有权依赖或完整权限恢复信息，不能作为完整备份。页面明确标注“根据服务器目录参数重建”。
- 同时限制 50,000 个对象、32 MiB 估算快照和 1 MiB 单定义；快照预算包含每对象的保守开销，可能先于对象数上限触发。超限、取消和原会话失效返回错误及空结果，不发布部分快照。超大程序会阻止该 schema 的本次目录刷新。
- 文档保留 Profile/Catalog/Schema/Kind/Name 身份及独立快照，重开原对象复用标签；跨连接、schema 和重载各自独立。完整定义留在文档，界面最多呈现前 500 行及提示；关闭释放定义和序列信息。
- 强制刷新现在保留原目录，成功后同时替换普通及限定缓存；失败显示错误并等待显式重试。操作完成后的显式元数据失效仍删除缓存，避免保留已知过期对象。

不增加持久化迁移。MySQL 程序兼容行为保留，SQLite/ClickHouse 不新增程序目录能力。

## 自动验证与实际服务

使用 go.mod 指定工具链和 MyGo fork；命令使用 `GOWORK=off GOCACHE=/data/.cache/golang`。所有日志及临时资源位于忽略目录 `build/`。

| 验证 | 结果与证据 |
|---|---|
| `CGO_ENABLED=0 go test ./... -timeout=20m` | 通过，`build/verify/program-go-test-settled.log`。首次进程收到 SIGTERM，不能记为通过；重跑及缓存修复后的最终全量均通过。 |
| contract/runtime/engines/navigation/sessionbrowser/objectworkspace race | 通过，`build/verify/program-race-packages-final.log`。 |
| shell 完整 race | 通过。按 `program-shell-test-list.txt` 的 157 个测试拆为 8 批，全部 exit 0；清单与实际顶层结果逐项一致，详见 `program-shell-race-audit.json`。其中 145 项通过、12 项按显式集成或图像采集开关跳过；PG 专项另在真实服务运行。原未分批进程收到 SIGTERM，未计为通过。 |
| Go vet / MyGo vet / i18n check | 通过，`program-go-vet-final.log`、`program-mygo-vet-final.log`、`program-i18n-final.log`。翻译由工具生成，1475 条中英文消息。 |
| Python 工具回归 | 64 个测试通过，`program-python-final.log`；包含采集关闭及 manifest 失败仍清理所属资源、终止失败保留身份、拒绝删除其他拥有者容器。 |
| MyGo 正式打包 | 通过，`program-build-final.log`；Linux 正式二进制、deb 和 tar.gz 均生成。 |
| Linux / Windows / macOS amd64 无 CGO 编译 | 全部通过；产物 `build/verify/nyadb-program-linux`、`nyadb-program-windows.exe`、`nyadb-program-darwin`。不作为 Windows/macOS 实机证明。 |

真实服务通过 `tools/verify_postgres_fixture.sh` 独立创建空 PostgreSQL 容器。`TestPostgreSQL17ProgramBrowse.*Fixture` 名称保留，分别在 PG 14、17、18 运行三个测试：

- PG 17（race）：`build/verify/postgres-zEykpS/`。
- PG 14：`build/verify/postgres-q3NuoE/`。
- PG 18：`build/verify/postgres-Pm57iV/`。
- PG 17 shell（race）：`build/verify/postgres-BjSITI/`；对应 Tester 图片位于 `build/verify/program-tester-final/`，单独标识为 Tester 证据。

服务验证覆盖重载/引用名称/跨 schema 类型、默认参数、可变参数和 OUT 参数身份、过程分类、序列精确整数、连接池设置恢复、池等待取消、会话关闭、1 MiB 单定义与 32 MiB 总快照失败及后续恢复；读取前后原序列和效果表保持不变。

工作台回归覆盖工具栏陈旧代次拒绝、重载标签复用、查找结果打开、收藏原连接的新元数据、同一服务的两个连接档案隔离、刷新失败保留目录及文档、只读快捷键、断开和重复关闭。新增回归发现强制刷新预先删除缓存的问题，已修复并增加 sessionbrowser 单测。中英文 Tester 另验证属性顺序、序列来源、精确整数、缺失定义和显示行数边界。

## Linux 正式窗口

最终运行：`build/entry-acceptance/r-4b05190e41e7/`；采集目录：`evidence/postgres-program-native/capture-adcb0ec64e7f/`。正式二进制 SHA-256：

`9fbe43885a08a759fb65895b17b1b35abf154edaf640cc8d539de6d0321cbc28`

最终命令 `custom-a9906e90` exit 0。`source-check.json` 记录基线提交、包含未跟踪文件的源码摘要及文件清单；逐图记录窗口几何、操作和二进制身份。采集后检查记录的源码文件均未变化，随后仅补阶段文档。

已实际检查连接、catalog 展开、schema 对象列表、两个函数重载、函数定义、过程定义、序列定义及 840×560 序列页面。1280×800 显示定义和右侧参数信息；840×560 主区域保留完整起点 `9223372036854775805`，下方 DDL 使用滚动容器，较窄右栏会裁切长值。代表性图片：

- [函数目录](../build/entry-acceptance/r-4b05190e41e7/evidence/postgres-program-native/capture-adcb0ec64e7f/functions-1280-client.png)
- [过程定义](../build/entry-acceptance/r-4b05190e41e7/evidence/postgres-program-native/capture-adcb0ec64e7f/procedure-definition-1280-client.png)
- [序列定义及参数](../build/entry-acceptance/r-4b05190e41e7/evidence/postgres-program-native/capture-adcb0ec64e7f/sequence-definition-1280-client.png)
- [窄窗口序列页面](../build/entry-acceptance/r-4b05190e41e7/evidence/postgres-program-native/capture-adcb0ec64e7f/sequence-definition-840-client.png)

`result.json` 的前后状态均为 `9223372036854775805|f|0`，表示序列未被调用、效果表无写入；它与脚本退出码均不单独作为界面操作成功证据。最终 `review.json` 记录逐图接受结果。早期采集有未命中行、树展开导致坐标移动和未切回对象页的问题，均标记为未接受，未计入交付。

`build/verify/program-resource-audit.json` 独立检查本阶段五次窗口运行、正式与失败的 PG fixtures：记录 PID/出生时间不再存活、容器不存在、临时 secrets 文件删除。清理故障会报告错误，不能被失败截图掩盖；未触碰其他运行的资源。

## 后续范围

本阶段证据仅为 NyaDB 实现与运行验证 I。Windows/macOS 原生窗口、IME/缩放/多显示器、Navicat 的 O/S/V 和同条件性能差分仍未验证。正式 PG 设计保存、程序/视图 CRUD、文件工具、命名工作区、传输/同步/自动运行及其它完整目标继续保留，详见剩余清单。
