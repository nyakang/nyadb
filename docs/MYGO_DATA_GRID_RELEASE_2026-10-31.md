# MyGo 原生数据网格与 `.2` 发布候选（2026-10-31）

## 当前交付状态

NyaDB 与 MyGo 的原生表头、选区、编辑及布局改动已在本地实现。
`go.mod` 固定目标为 `github.com/nyakang/mygo v0.7.3-nyadb.2`，保持
`require github.com/egoist/mygo v0.7.3` 和原 module/import 路径。
`.2` 尚未提交、打标签或推送；本轮按方案准备发布材料，未发布远端版本。
因此本轮检查使用真实源码工作区，不能视为远端 `.2` 下载验证。
`go.sum` 未伪造新版本的校验和；远端发布后须从 Go 模块下载结果生成。
发布之前，禁用工作区的普通模块构建将无法解析尚不存在的标签。

可移植补丁：[mygo-v0.7.3-nyadb.2.patch](patches/mygo-v0.7.3-nyadb.2.patch)。
它基于 `.1` 的不可变提交 `922da57bd8b2ec6cf1576441afbe7f6565a536ca`，
包含 MyGo 生产代码、原生 Tester 回归、API 文档、维护说明及 Go-only
标签的发布工作流排除规则；不含研究
截图、数据库数据或任何专有素材。补丁保留原 MyGo MIT 许可证。

在新检出中可创建独立、忽略的本地候选工作区：

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

## 发布步骤（未执行）

1. 审阅两仓库改动与本记录，将 Fork 补丁提交到 `nyadb/v0.7.3`，
   使用 `feat(ui): add native grid header selection and actions` 等历史格式。
2. 发布新的不可变 `v0.7.3-nyadb.2` 标签，绝不移动 `.1`。
   本补丁已让继承的上游 `release.yml` 排除 `v*-nyadb.*`，避免 Go-only
   定制标签触发上游 npm 发布。发布时只推送本定制分支与新 Go 标签，
   不能套用上游 npm 全包发布命令。
3. 标签在远端可读后，在独立检出运行 `GOWORK=off go mod tidy`、
   `GOWORK=off go mod verify`，核对下载模块与审核后的 Fork 源码完全一致。
   保留这里准备的 `.2` replace，提交由真实下载生成的 `go.sum`。
4. 从远端固定模块重新跑 NyaDB 全量测试、vet、MyGo vet、翻译校验与打包，
   用 `go version -m` 确认发行程序记录的 `.2` 版本和校验和。

本轮独立实现、Linux 双边运行观察及剩余桌面限制分别记录在
[数据网格验收报告](MYSQL_DATA_GRID_ALIGNMENT_2026-10-31.md)。
Windows/macOS 编译结果不代表其桌面 GUI 已验证。

## 本地检查记录

Go 版本为1.27.3。普通测试、编译与静态检查均 `CGO_ENABLED=0`；
race 检查按 Go 工具链要求启用 cgo。所有 PASS 都针对审核后的本地补丁，
不是尚未发布的远端 `.2`。日志保存于忽略的
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
