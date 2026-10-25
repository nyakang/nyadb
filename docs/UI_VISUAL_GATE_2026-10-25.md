# UI 场景视觉差分门禁（2026-10-25）

`tools/strict_visual_differential.py` 支持工作台和连接表单，不再要求这些离线界面伪造数据库读取证据。真实对象、结构、数据页面继续要求有效的数据库读取、服务身份和相同结果指纹。

本工具的状态是 **I：独立实现及合成测试通过**。工具 PASS 仅代表所提供证据、指定 ROI 和度量通过，不代表已核实观察记录的真实性，也不代表整页或整款应用已达到 V。运行观察仍须核验原始截图、捕获时点和操作过程。独立图标造型与系统标题栏不自动掩盖、不重绘为原版资源。

## 调用及兼容性

```bash
/usr/bin/python3 -m unittest discover -s tests -v
/usr/bin/python3 tools/strict_visual_differential.py \
  temp/research/ui-alignment/manifest.json \
  --output temp/research/ui-alignment/result.json
```

- 无 `scene` 和 `manifest_version` 的旧 manifest 保持原有严格数据库门禁、相同桌面尺寸和固定 `[160,80,1440,880]` 裁切。旧验收不会被自动升级为新场景验收。
- 新格式使用 `scene`，可显式标记 `manifest_version: 2`；允许双方桌面大小和窗口位置不同，按双方实际客户区分别裁切，**不缩放图片**。
- 退出码不变：`0 = PASS`；`1 = FAIL`（证据充分，度量未达标或缺少 ROI 阈值）；`2 = BLOCKED`（场景、状态、元数据、图片或观察证据不足）。BLOCKED 不输出像素度量。

## 场景与前置证据

| scene | 含义 | 数据库门禁 |
| --- | --- | --- |
| `workbench` | 未读取数据库的主窗口、分组、连接列表及菜单 | `ui_state.database_backed: false`，不得夹带数据页的数据库证据 |
| `connection` | 离线连接选择／编辑表单及其草稿状态 | 同上；真实连接测试结果需另外核验真实服务证据 |
| `objects` | 从数据库读取的对象列表 | 真实读取、服务／fixture／结果指纹、schema |
| `structure` | 从数据库读取的结构页 | 上述字段及 table |
| `data` | 从数据库读取的数据页 | 上述字段、table、selected_column、visible_columns、sort_rules |

数据库公共字段为 `database_host_port`、`server_identity`、`fixture_fingerprint`、`rows_fingerprint`、`schema`、`read_status`；两端必须一致，`read_status` 必须为 `success`。对象和结构的 `rows_fingerprint` 指对应元数据结果集的指纹。`unknown`、空值及无观察来源不能代替身份或指纹。修改 scene 字段不能使现有数据页 manifest 降级绕过数据库门禁。

两端共同要求：

- 独立的 `source_version`（原版版本／二进制身份；NyaDB Git 版本加工作树差异身份），以及 `platform`、`locale`、`theme`。
- 实测 `dpi_x`、`dpi_y`（差值 ≤ 0.5）；实际应用 `actual_scale`（每 DIP 对应的设备像素数，差值 ≤ 0.001）。显示器 DPI 相同不能推断 Qt／MyGo 的应用缩放相同。
- 实际解析到的 `font_family` 和 `font_size_dip`，两端一致；在观察文件中记录字体文件和解析方法。
- 像素单位的 `viewport_width`、`viewport_height` 和客户区 `client_crop: [left,top,right,bottom]`；裁切必须位于原截图内，双方客户区大小相等。
- `render_ready: true`，并提供非空 `observation_evidence` 文件列表。采集端应等待内容完成绘制；工具额外拒绝客户区 99.5% 以上像素亮度低于 4 的黑帧。
- 完全相同的 `ui_state`，至少包含 `database_backed`、`loading: false`、`selection`、`expanded`、`menu_state`。selection／expanded 使用双方约定的语义身份列表，空列表表示确实没有选择／展开。表单 tab、字段值、滚动位置、规则状态等影响画面的信息也应纳入该对象。

## 度量字段

新场景必须同时提供非空 `regions`、`geometry`、`color_samples`，明确本次验收覆盖的区域：

- `regions`：客户区内 `[left,top,right,bottom]` 像素 ROI。每个 ROI 必须有显式且有限的 `mae_thresholds`；RGB MAE 和超过 32 色阶的差异像素比例均原样输出。不得事后放宽阈值来掩盖失败。
- `geometry`：成对的 `[x,y,width,height]` **客户区相对像素坐标**；分别除以双方 `actual_scale`，四项偏差均须 ≤ 2 DIP。各方的 `*_evidence` 必须引用自己的 observation_evidence 文件，记录原始测量位置和方法；细线矩形可用于文本基线或分隔线测量。
- `color_samples`：双方独立的 `[left,top,right,bottom]` 客户区像素区域，至少 2×2。每个样本自身各通道范围须 ≤ 3，确保它是稳定纯色区域；双方平均 RGB 每通道偏差须 ≤ 3。非纯色样本属于测量前提不足，返回 BLOCKED。

2 DIP 和 3 色阶上限固定，不受宽松的 ROI MAE 阈值影响。结果保留截图和观察文件 SHA-256、原始采样区域、颜色值、几何测量及偏差，便于复核。没有对未列入度量的控件或交互作出通过声明。

## 离线工作台示例

下列内容仅演示格式，坐标、字体与版本须由实际观察替换，不能直接作为 V 证据。双方均为 1280×800 客户区，位置不同：

```json
{
  "manifest_version": 2,
  "scene": "workbench",
  "regions": {"workspace": [260, 155, 1060, 750]},
  "mae_thresholds": {"workspace": 8},
  "geometry": {
    "toolbar": {
      "navicat": [0, 25, 1280, 66],
      "nyadb": [0, 25, 1280, 66],
      "navicat_evidence": "navicat-observation.json",
      "nyadb_evidence": "nyadb-observation.json"
    }
  },
  "color_samples": {
    "workspace": {
      "navicat": [500, 400, 510, 410],
      "nyadb": [500, 400, 510, 410]
    }
  },
  "navicat": {
    "scene": "workbench",
    "source_version": "observed Navicat version and binary digest",
    "platform": "linux-x11", "locale": "zh-Hans", "theme": "dark",
    "actual_scale": 1, "font_family": "observed resolved font", "font_size_dip": 12,
    "dpi_x": 96, "dpi_y": 96,
    "viewport_width": 1280, "viewport_height": 800,
    "client_crop": [160, 100, 1440, 900],
    "render_ready": true,
    "ui_state": {
      "database_backed": false, "loading": false,
      "selection": [], "expanded": [], "menu_state": "closed"
    },
    "screenshot": "navicat.png",
    "observation_evidence": ["navicat-observation.json"]
  },
  "nyadb": {
    "scene": "workbench",
    "source_version": "observed NyaDB Git revision and worktree digest",
    "platform": "linux-x11", "locale": "zh-Hans", "theme": "dark",
    "actual_scale": 1, "font_family": "observed resolved font", "font_size_dip": 12,
    "dpi_x": 96, "dpi_y": 96,
    "viewport_width": 1280, "viewport_height": 800,
    "client_crop": [60, 90, 1340, 890],
    "render_ready": true,
    "ui_state": {
      "database_backed": false, "loading": false,
      "selection": [], "expanded": [], "menu_state": "closed"
    },
    "screenshot": "nyadb.png",
    "observation_evidence": ["nyadb-observation.json"]
  }
}
```

## 测试

23 个合成测试覆盖旧格式六项门禁、新场景异位置客户区裁切、各数据库场景读取证据、scene 降级防护、字体／缩放／状态不一致、黑帧、加载中、越界裁切、未知身份、2 DIP 归一化几何、3 色阶颜色、非纯色测量、ROI 像素失败及缺失／非法阈值。合成测试不属于原版运行差分证据。
