# MyGo 原生数据网格与 `.2` 发布（2026-10-31）

## 当前交付状态

NyaDB 与 MyGo 的原生表头、选区、编辑及布局改动已在本地实现。
`go.mod` 固定目标为 `github.com/nyakang/mygo v0.7.3-nyadb.2`，保持
`require github.com/egoist/mygo v0.7.3` 和原 module/import 路径。
`.2` 已发布到 [nyakang/mygo](https://github.com/nyakang/mygo/tree/v0.7.3-nyadb.2)，
对应提交 `aa7998b3eda6f4a21c14fd23a7f9b13a1a5b7d3f`，保留 `.1` 不变。
此前只准备了本地候选却提前更新 `go.mod`，导致没有研究工作区的检出无法
运行 `go tool mygo dev`。本次已补齐真实标签、从模块代理下载 `.2`，并通过
`GOWORK=off go mod tidy` 生成真实 `go.sum`。下载的1499个Go／模块文件
逐字节等于发布提交。普通开发现在直接运行 `go tool mygo dev`，无需本地Fork。

`.2` 模块校验和为 `h1:KWqYvBjITvavGGIp/HIievlEzKwxZKeGfyDq/FoN/os=`；
go.mod校验和为 `h1:dVav53PwF2xEotjf4oA5lTY2Jp2exgzyjLhbSXhbMwE=`。

可移植补丁：[mygo-v0.7.3-nyadb.2.patch](patches/mygo-v0.7.3-nyadb.2.patch)。
它基于 `.1` 的不可变提交 `922da57bd8b2ec6cf1576441afbe7f6565a536ca`，
包含 MyGo 生产代码、原生 Tester 回归、API 文档、维护说明及 Go-only
标签的发布工作流排除规则；不含研究
截图、数据库数据或任何专有素材。补丁保留原 MyGo MIT 许可证。

下面的本地工作区只用于重建补丁及进一步开发，正常运行无需执行：

```sh
python3 tools/prepare_mygo_grid_workspace.py
GOWORK="$PWD/temp/mygo-grid-workspace/go.work" CGO_ENABLED=0 go test ./...
GOWORK="$PWD/temp/mygo-grid-workspace/go.work" go run ./cmd/nyadb
```

脚本克隆公开 Fork，校验精确基线，再检查并应用补丁；拒绝覆盖已有目录，
不创建提交或标签。当前开发工作区另有忽略的根 `go.work`，使用
`temp/research/mygo-upgrade-20261030/fork`。自动检查的日志则固定使用
`temp/research/grid-alignment-20261031/go.work`，避免依赖其他研究工作区。

## 变更与兼容性

- MyGo 增加可选 `HeaderSelect`、`ColumnSelection`、`HeaderActionMenu`、
  `OnCellClick`，以及 `TableColumn.HeaderTooltip`／`HeaderDetail`。
  `ColumnRange` 仍可比较，其他表格默认点击排序行为保留。
- 左键选择整列；Ctrl/Cmd 支持离散字段；Shift 保留范围外的独立选择，
  连续 Shift 替换先前扩展。右键集合内保留选择，集合外选择该字段。
  `…` 独立于右键菜单，打开时保留原选区。
- 数据与零数据使用同一原生 Table；空表不制造记录。统计行不参与整列
  选择；原生 DragSelect 负责边缘滚动，编辑器只接收未拖动点击。
- 注释直接来自已有 COMMENT，空注释无提示；原生初次提示延迟为600ms，
  保留 MyGo 既有短时间相邻提示机制。第三行注释独立于字段类型显示，
  空注释为 `--`。暗色网格提示使用现有主题悬浮色，避免与工具栏融为一体。
- 复制、导出及填充使用实际选中字段、当前显示顺序；粘贴保持从游标出发、
  沿可见字段连续落点。既有编辑整体校验、草稿保存和填充200单元格限制保留。
- 精确列宽校验正整数及字段上下限；自动适配实测当前字体、表头、已加载
  单元格及按钮空间，无额外取数。缩放统一作用于表头、行高和内嵌编辑器。
- 原布局键与 version 1 不变，只增加可选 `show_comments`／`zoom`。
  旧配置默认关闭注释、100%；偏好不包含选区、COMMENT 内容或数据值。

## 发布与依赖修复

Fork 发布提交为 `feat(ui): add native grid header selection and actions`。
定制分支与新不可变标签已原子推送；继承的npm工作流排除NyaDB标签，未发布
上游npm包。`go mod tidy` 同时整理现有直接/间接依赖声明与过期校验和，
不升级其他依赖版本。发布后的检查均显式使用 `GOWORK=off`，以远端模块
验证工具解析和构建；额外空缓存下载确认无需已有本地Fork或模块缓存。

本轮独立实现、Linux 双边运行观察及剩余桌面限制分别记录在
[数据网格验收报告](MYSQL_DATA_GRID_ALIGNMENT_2026-10-31.md)。
Windows/macOS 编译结果不代表其桌面 GUI 已验证。

## 发布后远端模块验证

本次修复日志位于忽略的 `temp/research/mygo-release-fix-20261031/`。
以下检查均使用 `GOWORK=off`，不读取研究Fork。CI也固定 `GOWORK=off`、
`GOFLAGS=-mod=readonly`，使缺失校验和或依赖声明不完整直接失败。

| 检查 | 结果 |
| --- | --- |
| 默认模块代理下载 `.2` | PASS，真实module/go.mod校验和已写入go.sum |
| 空GOMODCACHE重新下载 `.2` | PASS，两个校验和与默认缓存下载一致 |
| 下载源码核对 | PASS，1499个Go／模块文件与发布提交逐字节相等 |
| `go mod tidy`／`go mod verify` | PASS，其他依赖版本不变，声明与校验和规范化 |
| CLI解析 | `go tool mygo version`成功；`go tool mygo dev -help`输出正常帮助，按CLI的帮助请求语义返回1，已越过此前缺失go.sum错误 |
| `CGO_ENABLED=0 go test ./...` | PASS，dataworkspace 53.881秒、shell 37.267秒 |
| Go vet／MyGo vet | PASS |
| 翻译生成与校验 | PASS，914条消息，catalog无新增改动 |
| Windows amd64／Darwin arm64编译 | PASS，仅编译证据 |
| MyGo Linux打包 | PASS，程序、deb和tar.gz；`go version -m`显示远端 `.2` 与真实校验和 |
| 可移植补丁 | PASS，新克隆重建10个变更文件，全部等于已发布Fork |

## 发布前本地检查记录

Go 版本为1.27.3。普通测试、编译与静态检查均 `CGO_ENABLED=0`；
race 检查按 Go 工具链要求启用 cgo。下面的 PASS 针对发布前审核的本地补丁，
与发布后远端模块检查分别记录。日志保存于忽略的
`temp/research/grid-alignment-20261031/`。

| 检查 | 结果与证据 |
| --- | --- |
| MyGo `GOWORK=off go test ./...` | PASS，最终全量101.783秒；22个新原生回归纳入 |
| MyGo Linux/Windows amd64、Darwin arm64 build/vet | PASS，最终树 |
| MyGo production-tag vet、js/wasm vet（排除CLI） | PASS |
| MyGo 表头/单元格回调/Table/List/Cell focused race | PASS，22.222秒；不是完整性能/raster race 套件 |
| NyaDB `go test ./...` | PASS，最终全量，包含全部既有数据类型、编辑及关闭测试 |
| NyaDB Go vet、MyGo vet | PASS |
| NyaDB 翻译生成、i18n check | PASS，生成catalog，无手改；zh-Hans/en共914条消息 |
| NyaDB Linux amd64、Windows amd64、Darwin arm64 编译 | PASS；后两项只代表编译 |
| NyaDB MyGo 打包 | PASS，Linux程序、deb、tar.gz；版本信息记录本地替换源码 |
| NyaDB 最终数据网格/编辑/关闭生命周期 focused race | PASS，358.084秒；范围与额外完整race的限制见下文 |
| Python visual工具回归 | PASS，`/usr/bin/python3` 33项；默认conda Python缺少Pillow，未安装额外依赖 |
| 可移植补丁与workspace脚本 | PASS，新克隆校验基线并应用；10个变更文件逐字节等于验证Fork；重建源码上的数据表头回归通过 |
| Linux桌面双边验证 | PASS，详见独立运行报告；180行/2056字节离散列复制逐字节相等 |

另对中间候选进行了额外的 NyaDB 五包完整 race 检查：shell（434.077秒）、
sessionbrowser、sqlworkspace、objectworkspace通过；dataworkspace整个包
达到默认600秒超时，当时正在运行既有
`TestDataTransferParquetLargeValuesUseBoundedRowGroupsAndCleanCanceledPages`
（该用例当时只运行10秒）。日志未报告数据竞争，不能把整包结果标记PASS，
也不能据此断定是该单例耗时600秒。未提高超时或修改既有性能用例。
最终源码上的表头、复制/粘贴、字段布局、填充、编辑、取消、代际及关闭生命周期
单独执行 focused race；筛选范围明确排除未修改的DataTransfer/DataImport
整套格式压力测试：

```sh
go test -race ./internal/ui/dataworkspace \
  -run 'Test.*(Header|Column|Layout|Edit|Cancel|Close|Refresh|Selection|Copy|Paste|Fill|Field|Inspector|Draft|Save|Generation|Late|Park|Disconnect)' \
  -skip 'TestDataTransfer|TestDataImport'
```

上述focused检查PASS，结果保存于 `nyadb-data-lifecycle-race-final.log`；完整额外套件的超时保留在
`nyadb-race.log`。不得把focused结果扩大为完整dataworkspace race通过。

未使用功能的release-size比较控制了VCS信息，基线和候选均使用
`-buildvcs=false -trimpath -tags mygo_noinspector` 及 stripped production构建：

| 平台/示例 | `.1` 字节 | `.2` 候选字节 | 增量 |
| --- | ---: | ---: | ---: |
| Linux amd64 counter-native | 9564423 | 9564423 | 0 |
| Linux amd64 hello | 9613575 | 9613575 | 0 |
| Windows amd64 counter-native | 9881088 | 9882112 | 1024 |
| Windows amd64 hello | 9974272 | 9974272 | 0 |
| Darwin arm64 counter-native | 9372866 | 9372978 | 112 |
| Darwin arm64 hello | 9561634 | 9561634 | 0 |

`nm`／`-dumpdep`确认counter未链接表格`columnAt`机器码：单元格回调
只在Table启用时注册。剩余增长来自共用行输入中的小型条件派发和回调/类型
元数据；Windows text增加160虚拟字节、rdata增加696，PE对齐成为1024字节；
Darwin text增加144，类型元数据和布局对齐吸收大部分后，文件净增112。
不能把初次含不同VCS信息的测量当成代码体积比较。
