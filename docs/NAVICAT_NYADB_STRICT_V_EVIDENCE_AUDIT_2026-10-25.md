# NyaDB × Navicat 18 — 严格 V 证据审计与条件门禁（2026-10-25）

## 结论

**严格 V：BLOCKED，不进行同状态像素差分。** 本轮完成的是研究记录的完整性审计（I：自编工具及测试）、Windows 可用窗口清单核验和差分门禁加固。没有启动 Navicat 原版，没有启动生产数据库，没有重新声称原版 O、Windows GUI V 或像素 V。

前置源码 `master` 为 `643ba375`，本轮不修改 MySQL 服务层、应用界面样式或安全边界。没有相同 fixture 的两边画面时，不根据不同数据的旧截图调节像素，以免过拟合错误状态。

## 证据来源与审计结果

本轮用 `tools/audit_visual_evidence.py` 只读检查现有 NyaDB Linux Xvfb 原生桌面截图：

| 被审核的真实 NyaDB 观察记录 | 完整性 | 严格 V 前置条件 |
| --- | --- | --- |
| `temp/research/inspector-menu-review-20261025/02-objects-1280.json` | PNG SHA-256、1600×1000 桌面、1280×800 客户区裁切、稳定帧及观察文件存在均通过 | **BLOCKED**：未独立测得应用缩放、解析字体/字号及截图绑定的数据库服务/fixture/对象结果指纹 |
| `temp/research/inspector-menu-review-20261025/04-data-1280.json` | 上述基本完整性通过，真实 GUI 数据页存在 | **BLOCKED**：同上；还缺表身份、选中列、列顺序和排序状态的结构化绑定 |
| `temp/research/inspector-menu-review-20261025/06-column-menu-1280.json` | 上述基本完整性通过，原生菜单实际图片存在 | **BLOCKED**：同上；虽有菜单状态记录，但无服务结果和字体证据 |

审计 JSON：`temp/research/nyadb-visual-evidence-audit-20261025.json`（Git 忽略，不作为产品资源）。运行命令：

```bash
python3 tools/audit_visual_evidence.py \
  temp/research/inspector-menu-review-20261025/02-objects-1280.json \
  temp/research/inspector-menu-review-20261025/04-data-1280.json \
  temp/research/inspector-menu-review-20261025/06-column-menu-1280.json \
  --expect-application nyadb \
  --output temp/research/nyadb-visual-evidence-audit-20261025.json
```

当前预期退出码 **2（BLOCKED）**，并非测试崩溃。确认截图哈希和裁切条件成立，与能够单独证明截图中的数据库对象/行数据身份，是两个不同等级的证据。原先实验的 `run-evidence.json` 可证实当时独立容器对 fixture 的 SQL 元数据查询，但**尚不能证明两个应用各自在截图同时刻完成了相同的读操作**，不能仅把该文件字符串复制到双方的结果字段里。

### Windows 真实桌面能力实查

2026-10-25 对注册 Windows Computer Agent 进行了只读 `window.list`。代理在线，报告窗口 DPI 以所列窗口为准（可见大部分 120 DPI）；但是窗口清单里**没有 NyaDB 或 Navicat 的活动目标窗口**。Windows 代理当前能够观察/控制已打开 GUI 窗口，不能执行任意 Windows 命令，也没有注册的可直接启动应用。**未进行 Windows NyaDB 实拍，更未拿 Windows DPI 与 Linux 96 DPI 做像素比较。** 为保护用户桌面，本轮没有激活、输入、启动或修改微信、浏览器等其他应用。

### 历史 Navicat O 与 V 的关系

历史原版观察图片在 `temp/research/mysql-keyset-diff-20261022/`，包括 `navicat-selected-data-1280.png`、`navicat-column-context-root.png`、`navicat-fieldmeta-crop-1280x800.png`。这些能作为已有 O 的界面元素参考，但其图像/裁切与 2026-10-25 NyaDB 的相同容器、相同服务 UUID、实际返回行/元数据结果哈希、应用解析字体/缩放和一致选中状态尚未共同确认。**不得把两端都有“child”表、尺寸一样或 DPI 接近视为 V**；不能复用旧图假称最新原版读成功。

## 门禁增强

1. 新增 `tools/audit_visual_evidence.py`：只读核验观察 JSON 与原始 PNG 哈希、PNG 像素尺寸、客户区裁切、稳定帧、源版本标识及状态；数据库页强制列出缺少的读取来源、fixture、服务器身份与结果指纹。输出 `SIDE_READY` 的含义仅为**单侧内部证据齐备**，并不意味着 V 或独立真实性认证。
2. 强化 `tools/strict_visual_differential.py`：当清单 `observation_evidence` 指向包含 `screenshot_sha256` 的原生采集 JSON 时，必须核实同一张截图的实际 SHA、观察记录的 `application`、源版本、scene、客户区几何、语言、主题、字体/缩放和 `ui_state`。错把 NyaDB 观察文件当 Navicat、换图、改状态均 BLOCKED。无法验证来源的文本文件仍需要操作者独立核查，不能将自编合成测试的 PASS 当成真实 V。
3. 新增 8 项单侧审计测试及 2 项双端绑定测试；Python 总计 **33 项** PASS，覆盖缺失字体、数据库证据、越界裁切、篡改图像摘要、误标原版、绘制未稳定等。保留 `tools/strict_visual_differential.py` 既有要求：2 DIP 几何差、3 RGB 色阶、每个 ROI 单独的 MAE 阈值；并未降级准入标准。

## 补证步骤（不能自行补填未知字段）

1. **隔离数据库**：在专用一次性回环 fixture 容器内提供确定的服务器身份（如 `@@server_uuid`、`VERSION()`）、SQL fixture SHA 和确定排序的对象/字段/行序列化结果哈希；两端经真实只读连接分别证明同一快照。InnoDB `TABLE_ROWS` 为估算、`UPDATE_TIME` 可能为空或变化，不可拿它作为两个端的精确数据一致性凭证。
2. **两侧真实采集**：在获得合法授权的原版运行许可后，在隔离桌面和同 fixture 下，分别记录同表、同列、同排序/筛选、同右栏和同弹出菜单及渲染稳定的客户区画面；不得绕过授权，也不得从历史截图补造当前 `read_status=success`。
3. **字体和缩放**：实测两端真实 DPI、客户端实际 px/DIP、GTK/MyGo 与 Qt 的最终字体文件和字号。单凭 Xvfb `-dpi 96` 或某个 `fc-match` 默认值不能断言运行时最终字体相同；保持 Unknown 直到双端验证。
4. **Windows**：先以明确来源的 NyaDB 可执行文件在 Windows 目标机器单独打开（不影响现有桌面任务），确认 Windows DPI 感知模式、主题和原生菜单高度；窗口存在后才能进行 Computer Agent 观察。不同 OS 的 DirectWrite/GTK 渲染差异应在各自平台建立同条件 pair，不能直接跨 OS 声称 3 RGB PASS。
5. **触发最终 V**：只有两边有一致的服务器身份/结果指纹、完整捕获记录且通过单侧审计，再构造 `manifest_version:2` 的 `objects/data` 场景清单，填入明确采样 ROI 与几何/颜色来源；运行 `tools/strict_visual_differential.py`。输出 `PASS/FAIL/BLOCKED` 并由操作记录复核。任何项不足均维持 BLOCKED，不得凭猜测修图或宣布 V。

## 本轮独立测试

- `python3 -m unittest discover -s tests -v`：**PASS，33 项**。
- `go test ./...`：PASS。
- `go vet ./...`：PASS。
- `go tool mygo vet`：PASS。
- `go tool mygo build`：PASS，Linux/amd64。
- `git diff --check`：PASS。

证据分类：本轮新门禁/自检是 **I**；历史原版图片是先前已确认范围内的 **O**；本轮 **V = BLOCKED**，Windows 实测 GUI = Unknown。
