# MyGo Fork 迁移记录（2026-10-22）

## 目标与结果

NyaDB 使用公开的 MyGo 原生框架，并对其原生 `Table` 表头进行了独立扩展。为保留上游更新能力，依赖已从项目内 `third_party/mygo` 源码快照迁移至 [nyakang/mygo](https://github.com/nyakang/mygo) Fork。

- 上游：[egoist/mygo](https://github.com/egoist/mygo)，官方基线标签 `v0.6.0`（commit `081639f2a55d62a09aca742684827937dac6cb39`）。
- Fork 同步分支：`main`；保留与 GitHub upstream 的 Fork 关系，不向此分支加入 NyaDB 专有扩展。
- NyaDB 定制分支：`nyadb/v0.6.0`。
- 定制提交：`45a1d0a`，只修改 `ui/table.go`（与上游相比 38 行新增、2 行删除），并增加维护文档 `NYADB_PATCHES.md`。
- 固定版本标签：`v0.6.0-nyadb.1`（已推送），不允许重打相同标签。
- Go 依赖：

  ```go
  require github.com/egoist/mygo v0.6.0
  replace github.com/egoist/mygo => github.com/nyakang/mygo v0.6.0-nyadb.1
  ```

Fork 中原有 `module github.com/egoist/mygo` 声明保持不变，NyaDB 的 import 不需要替换。旧本地快照已从 NyaDB Git 跟踪内容移除，许可证仍在 Fork 中。历史文档中关于 `third_party/mygo` 的记录描述的是**当时实现方式**，不应当作本次迁移之后的当前依赖事实。

## 补丁边界

`ui.TableColumn` 具有可选 `Subtitle`、`SubtitleIcon`、`HeaderBadge`；字段名和类型提示在同一个 MyGo 原生 header cell 内绘制，继续复用原生 `TableLayout`、横向滚动、排序、调整列宽、拖动重排和右键菜单。该扩展未复制 Navicat 专有代码、图标或私有资源。

## 上游更新流程

1. 在 Fork 的 `main` 使用 GitHub **Sync fork** 与 `egoist/mygo:main` 保持同步，核对变更；这一同步**不会自动更新** NyaDB 的固定依赖。
2. 当上游发布新版本，基于其明确的 release/tag 创建新的 `nyadb/vX.Y.Z` 分支，不直接将持续变化的 `main` 混合进已发布的旧定制版本。
3. 对比新的 `ui/table.go`，适配最小元数据表头补丁；优先采用上游正式支持的新 API，避免永久维护兼容补丁。
4. 检查 Fork 的 `go test ./ui`、Go vet，再在 NyaDB 中完成 `go test ./...`、`go vet ./...`、`go tool mygo vet`、`go tool mygo build` 和 GUI/回归测试。
5. 发布**新的不可变** `vX.Y.Z-nyadb.N` 版本标签，修改 NyaDB 的 `go.mod` 中的 `replace` 版本，运行 `go mod tidy` 并审查 `go.sum`。更新失败时使用旧标签回退，而不是强推或改写历史。

## 本轮验证

- Fork `go test ./ui`：PASS。
- 从远程标签执行 `go mod tidy`：PASS，实际下载 `github.com/nyakang/mygo v0.6.0-nyadb.1`。
- `go list -m github.com/egoist/mygo`：确认最终解析至 Fork 的 `v0.6.0-nyadb.1`。
- NyaDB `go test ./...`：PASS（含迁移前已有未提交的独立 UI 修改）。
- 其余静态检查和构建结果以此轮实际验证命令为准；不将通过编译等同于真实桌面 GUI 差分验收。

## 操作边界

本次仅在新建的 Fork 上提交并推送 MyGo 定制分支与版本标签。NyaDB 主仓库原有未提交 UI 工作保持原样；本轮不对 NyaDB `master` 自动 commit、push、reset 或修改 Git 历史。用于准备 Fork 补丁的临时克隆位于忽略目录 `temp/research/mygo-fork-20261022`，不纳入 NyaDB 源码分发。
