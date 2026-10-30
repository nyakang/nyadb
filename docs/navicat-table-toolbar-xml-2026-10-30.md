# 表顶部工具栏：XML 传输运行证据

本页补充 [表顶部工具栏报告](navicat-table-toolbar-2026-10-30.md)，区分原版运行观察 **O**、NyaDB 独立实现 **I** 和尚未确认的行为。原版文件、授权及专有资源没有改动；用户已明确允许启动 Navicat 做对照。研究依据 [reverse-engineer-anything](/root/.agents/skills/reverse-engineer-anything/SKILL.md) 的运行观察流程，没有把独立实现宣称为原版源码。

## 环境和范围

- 原版：`temp/navicat/squashfs-root/usr/bin/navicat`，正常启动，复用既有正常授权状态和已有研究连接；Xvfb `:184`，1600×960，Openbox。首次启动没有修改授权，也没有出现要求处理的新授权步骤。
- 连接前通过原生连接信息及密码提示确认 `127.0.0.1:32790` / `data_probe`。新 disposable 容器 `nyadb-toolbar-xml-20261030` 使用 MySQL 8.4.11；不是已经不存在的旧容器。
- 数据源限定为 `table_edit_it.toolbar_probe_20261030`。原版导出没有写数据库。导入默认值验证只创建独立新表 `toolbar_xml_original_probe_20261030`；运行前确认该表不存在，运行后记录 `SHOW CREATE TABLE` 和字段值，再删除该新表。
- 所有截图、XML、TSV 和动作日志留在忽略目录 `temp/research/table-toolbar-20261030/`。原版窗口启动信息为 `xml-original-runtime.json`；动作在 `actions.jsonl`，不要将这些研究图像用作产品资源。

源数据四行：

| id | note | amount | state | details |
| ---: | --- | --- | --- | --- |
| 1 | alpha | 12.50 | new | first |
| 2 | beta | 25.00 | done | second |
| 3 | 中文 | SQL NULL | new | SQL NULL |
| 4 | SQL NULL | 0.00 | done | 空字符串 |

## O：原版导出

此前 O-122–O-128 留存 XML 元素格式导出向导及完成状态，`xml-original-four-rows.xml` 是实际输出。原版默认 XML 包裹为 `RECORDS` / `RECORD`，每个字段作为子元素。

```xml
<RECORDS>
  <RECORD><id>1</id><note>alpha</note><amount>12.5</amount>...</RECORD>
  <RECORD><id>3</id><note>中文</note><amount/><state>new</state><details/></RECORD>
  <RECORD><id>4</id><note/><amount>0</amount><state>done</state><details/></RECORD>
</RECORDS>
```

这里只是显示节选，不是完整输出文件。实际文件含全部五个字段、四行、XML 声明。`12.50` 输出为 `12.5`，`25.00` 为 `25`，`0.00` 为 `0`；数字值相同，字符串精度表示不同。第 3 行 details 的 SQL NULL 和第 4 行 details 的空字符串都输出 `<details/>`，文件无法区分两者。

本次 **O-134** 在文件格式页勾选“在 XML 中使用属性格式”，**O-135** 实际导出 4 行并显示成功。`xml-original-attributes.xml` 包含：

```xml
<RECORD id="1" note="alpha" amount="12.5" state="new" details="first"/>
<RECORD id="2" note="beta" amount="25" state="done" details="second"/>
<RECORD id="3" note="中文" amount="" state="new" details=""/>
<RECORD id="4" note="" amount="0" state="done" details=""/>
```

每行五个字段均存在；本样本没有省略 NULL 字段，NULL 和空字符串再次合并为空属性值。当前 NyaDB 属性记录解析器要求每行字段集合一致，兼容这个实际样本。不能据此声称原版所有 XML 文件、版本或设置都遵循该约束。

原版文件格式页还显示追加、遇到错误继续、日期和时间分隔符、小数点符号、二进制编码选项（可见默认 Base64）。本次源表不含二进制字段，故这张界面截图不能证明原版二进制 XML 标记或编码结果。

## O：原版导入预览与默认写入

**O-136/O-137**：表顶部“导入”打开源文件和目标表向导，通过添加源的 XML 项选择 `xml-original-four-rows.xml`。选择源行后，初始预览显示 XML 文本，并提示仅显示前 100 行。这不是解析后的数据网格。

**O-138**：源设置页可见 UTF-8、行标签 `RECORD`、未勾选“将标签的属性视为表字段”、首行 1、末行 0、日期格式和二进制编码 Base64。需要选择左侧目标表子项才能显示字段映射和解析结果；只停在文件节点会继续显示 XML 文本。

**O-139**（`139-original-xml-import-parsed-preview.png`）：新表映射自动推断 id 为 int、amount 为 double，note/state/details 为 varchar(255)。第 3 行的 `<amount/>` 在解析预览显示 `0`，note/details 空元素显示空白。此时未执行导入。早期 `139-original-xml-import-mapping.png` 实际是导入模式页，不能以它证明映射内容。

`xml-original-import-advanced-options.png` 原生“高级”窗口确认默认设置：

| 设置 | 本次默认状态 |
| --- | --- |
| 使用扩展插入语句 | 勾选 |
| 最大语句大小 | 勾选，1024 KB |
| 使用 NULL 取代空白字符串 | **未勾选** |
| 忽略外键约束 | 未勾选 |
| 遇到错误时继续 | 勾选 |

为确认预览是否等于写入结果，按明确授权将目标改为独立新表 `toolbar_xml_original_probe_20261030`，确认新表标记及唯一目标后，使用上述默认设置实际导入。`xml-original-default-target-mapping.png` 绑定目标和五列映射；`xml-original-default-import-complete.png` 显示建表、处理 4 行、增加 4、错误 0。外部 `xml-original-default-import-values.tsv` 记录真实结果：

| 源字段值 | 自动建表字段 | 实际写入 |
| --- | --- | --- |
| id3 `<amount/>`（源数据库 NULL） | double DEFAULT NULL | **0，IS NULL=0** |
| id3 `<details/>`（源数据库 NULL） | varchar(255) DEFAULT NULL | **空字符串，IS NULL=0，LENGTH=0** |
| id4 `<note/>`（源数据库 NULL） | varchar(255) DEFAULT NULL | **空字符串，IS NULL=0，LENGTH=0** |
| id4 `<details/>`（源数据库空串） | varchar(255) DEFAULT NULL | **空字符串，IS NULL=0，LENGTH=0** |

这些是这个原版 XML 文件、默认导入设置、自动推断新表的 O；不是既有 DECIMAL 表、所有数值类型或其它 NULL 选项的普适语义。原版 XML 默认导出再默认导入不能保持这个样本的 NULL 值。

随后只删除该新表。`xml-original-default-import-cleanup.tsv` 确认新表剩余数量为 0，源 `toolbar_probe_20261030` 的原四行完全保留。没有修改源表来迎合结果。

### 勾选空字符串选项的追加观察

按明确授权再次确认上述独立新表不存在，在新向导使用同一源文件、同样自动推断的字段和新表目标。本次勾选“使用 NULL 取代空白字符串”，保存 `xml-original-nil-advanced-enabled.png` 后点击“确定”，再实际导入。`xml-original-nil-import-complete.png` 同样显示增加 4、错误 0。

外部 `xml-original-nil-import-values.tsv` 显示 amount(id3) 仍为 **0 非 NULL**，note(id4)、details(id3/id4) 仍为 **空字符串非 NULL**；`SHOW CREATE TABLE` 确认相关列均允许 NULL，与默认导入的 DDL 一致。`xml-original-nil-import-cleanup.tsv` 再次确认只删除新表、源四行未变。

因此，本样本的自动建表 XML 导入路径在勾选此选项后仍得到上述结果。不能仅根据选项文字宣称它已把本 XML 中的空元素转为 NULL，也不能据此推断内部执行顺序、说所有格式都忽略该选项，或推广到既有 DECIMAL 目标。实际结果与当前 NyaDB 显式空文本转换的语义不同，不标 V。

## I：NyaDB 的独立格式和边界

[toolbar_transfer_xml.go](../internal/ui/dataworkspace/toolbar_transfer_xml.go) 与 [XML 专项测试](../internal/ui/dataworkspace/toolbar_transfer_xml_test.go) 提供：

- `RECORDS` / `RECORD` 元素和属性记录解析；相同字段集合可以调整顺序，重复或缺失字段不会静默丢失。
- 普通空元素保留为空文本；按 schema-instance 命名空间 URI 识别 `xsi:nil`，保留 NULL。没有从空元素猜测来源数据库里的 NULL。
- 只有明确 `encoding="base64"` 或 `encoding="hex"` 才解码二进制；字节不能静默映射成文本。自身导出以 Base64 加明确 encoding 标记保留字节。
- 非法/保守 NCName 字段名使用可逆的 `FIELD name="..."` 表达，保持空格、标点、Unicode 等数据库名称。
- 拒绝 DTD、非 XML 声明处理指令、未知实体、嵌套字段、错误 namespace/nil/encoding 和非法 XML 字符。导出失败保留原文件。
- 当前输入仍受 1 MiB / 200 行限制，完整 XML 导出使用既有流式读取和原子替换，可以超过此导入行数限制。

这些是独立实现选择；原生 UI/worker、完整值与取消检查结果由主报告的当前验证日志证明，不能只凭本页源码索引宣称运行通过。

NyaDB 新版自身 XML 导出用显式 nil/encoding 保留 NULL、空串及字节，这与原版默认 XML 存在有意且应披露的格式差异。主实现已增加默认关闭的“使用 NULL 取代空字符串”选择；开启后只转换真正空文本，不转换空白文本和空二进制，也不改来源数据。这个选项允许用户明确处理原版 XML 已丢失的 NULL/空串信息，实际 UI/数据库验证由主报告记录。

关闭该选择时，原版元素 XML 中的空数值会继续被 NyaDB 解码为空文本并触发数值映射校验，原版默认自动建表路径则写入 0；开启时 NyaDB 把空文本转 NULL，原版本次勾选选项仍写入 0/空串。两条路径都没有证明“原版所有默认 XML 可直接导入”或原版默认行为完全等价。后续仍需补齐基于实际目标类型的明确兼容转换，不应静默猜回源 NULL。

## 待补齐的差分

- 既有目标表及数值、日期、二进制字段在不同导入选项下的转换与错误处理。
- 属性格式导入选项和实际结果；当前只确认属性导出的实际字段集合。
- 任意 XML 根/行标签、字段选择/范围、追加、日期格式和编码选项。
- XML 数据预览、映射和逐步向导的完整 UI 差分；NyaDB 目前为简化原生向导。

本页没有声明严格像素 V，也没有把预览代替数据库结果。完整工具栏复刻目标仍保持进行中。
