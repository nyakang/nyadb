# 表顶部工具栏：CSV 新建表导入运行证据

本页补充 [表顶部工具栏报告](navicat-table-toolbar-2026-10-30.md) 和 [XML 传输报告](navicat-table-toolbar-xml-2026-10-30.md)。结论采用原版运行观察 **O**；没有把字段预览当成数据库结果，也没有声明独立实现 **I** 或差分通过 **V**。研究遵循 [reverse-engineer-anything](/root/.agents/skills/reverse-engineer-anything/SKILL.md) 的运行证据边界，原版程序、授权和专有资源未改动。

## 环境与写入范围

- 复用已经运行的 Navicat 18：`temp/navicat/squashfs-root/usr/bin/navicat`，PID 26874，Xvfb `:184`，1600×960，Openbox。没有动态调试或授权处理。
- 使用隔离 MySQL 8.4.11 容器 `nyadb-toolbar-xml-20261030`，`127.0.0.1:32790`，GUI 账户 `data_probe`。
- 唯一写入对象是新表 `table_edit_it.toolbar_import_original_probe_20261030`。执行前外部查询确认不存在；完成后检查 DDL 和三行实际值；错误试验仍限定同一自有表，最终删除并再次确认不存在。
- `table_edit_it.toolbar_probe_20261030` 只用于打开表顶部工具栏，没有写入它。清理后的原四行与此前 `xml-original-default-import-cleanup.tsv` 中的源行逐字一致。
- 原始 CSV、截图、动作日志和外部查询结果位于忽略目录 `temp/research/table-toolbar-20261030/`，不会作为产品图标或其它资源使用。动作记录为 `actions.jsonl`。

## O：源文件与向导结构

`import-original-fields.csv` 是 UTF-8 CSV，记录以 CRLF 分隔，最后一个字段含引用包裹的真实 LF：

```csv
id,display_name,amount,active,created_at,details
1,alpha,12.50,1,30/10/2026 12:34:56,first
2,中文,25.00,0,31/10/2026 01:02:03,second
3,,0.00,1,01/11/2026 00:00:00,"multi
line"
```

**O-160/O-161**：表顶部“导入”打开“源文件和目标表”，添加源菜单可以选择 CSV；一行绑定源文件与目标表。目标表名称可编辑。**O-162**：源设置显示 UTF-8 65001、CRLF、逗号字段分隔符、双引号文本识别符、字段名行勾选且为 1、第一个数据行 2、最后一个数据行 0。本次日期排序为 DMY，日期时间排序为“日期 时间”，日期分隔符 `/`、时间分隔符 `:`、小数点 `.`，二进制编码为 Base64。本样本没有二进制字段，不能据此证明编码结果。

源文件节点的下方预览是原始文本；点击左侧目标表子节点才显示 Field Mappings 与解析网格。两种预览均提示显示前 100 行。当前桌面上向导高于 960 像素屏幕，需要移动窗口才能查看底部按钮；截图中裁切不是产品代码的布局测量结论。

## O：类型推断与字段编辑

**O-164**（`164-original-csv-inferred-mapping.png`）显示默认推断：

| 源字段 | 默认目标字段 | 默认类型 | 长度 |
| --- | --- | --- | ---: |
| id | id | int | 0 |
| display_name | display_name | varchar | 255 |
| amount | amount | double | 0 |
| active | active | int | 0 |
| created_at | created_at | datetime | 0 |
| details | details | varchar | 255 |

初始没有主键。解析预览把 DMY 时间转换成 `2026-10-30 12:34:56`、`2026-10-31 01:02:03`、`2026-11-01 00:00:00`；CSV 第三行未引用的空 display_name 显示 `NULL`，引用字段的 LF 显示为转义文本。这些首先只是预览观察。

**O-165/O-166**：双击类型单元格打开可编辑类型控件；目标字段、类型、长度和比例单元格可以编辑，主键单元格点击后显示黄色钥匙。经 Tab 离开单元格并选择其它字段后，`166-original-csv-reviewed-fields.png` 显示：

| 字段 | 映射页最终显示 |
| --- | --- |
| id | bigint，主键 |
| display_name → label | varchar，长度 255 |
| amount | decimal，长度 10，**比例 2** |
| active | int |
| created_at | datetime |
| details | varchar，长度 255 |

当前可见映射列为字段勾选、源字段、目标字段、类型、长度、比例、主键；没有在这张可见映射表中找到 Nullable 控件。这不证明原版所有页面均没有可空设置。

## O：导入模式、高级选项和运行反馈

**O-168** 显示六种导入模式，默认选择追加：

1. 追加：添加记录到目标表。
2. 更新：更新目标和源记录相符的记录。
3. 追加或更新：如果目标存在相同记录，更新它；否则，添加它。
4. 不更新追加：如果目标中存在记录，则跳过；否则，添加它。
5. 删除：删除目标中和源记录相符的记录。
6. 复制：删除目标所有记录，并从源重新导入。

本次只执行追加；其它文字只是入口观察，实际匹配规则和写入语义未验证。后续没有主键的映射在模式页将更新、追加或更新、不更新追加、删除显示为灰色（`import-original-error-append-mode.png`）；没有进一步点击确认禁用原因。

**O-169** 和 **O-175** 的高级窗口显示一致默认值：

| 选项 | 状态 |
| --- | --- |
| 使用扩展插入语句 | 勾选 |
| 最大语句大小 | 勾选，1024 KB |
| 使用 NULL 取代空白字符串 | **未勾选** |
| 忽略外键约束 | 未勾选 |
| 遇到错误时继续 | 勾选 |

**O-170** 是执行前最终页，计数均为 0。“开始”单独 dispatch 导入；结束后该按钮变为“关闭”，日志与进度保留。**O-171** 实际显示创建唯一目标表、处理 3、增加 3、更新 0、删除 0、错误 0、Finished successfully、100%。

## O：实际 DDL、值与映射页的差异

外部 `import-original-created-table.tsv` 的 `SHOW CREATE TABLE` 为：

```sql
CREATE TABLE `toolbar_import_original_probe_20261030` (
  `id` bigint NOT NULL,
  `label` varchar(255) DEFAULT NULL,
  `amount` decimal(10,0) DEFAULT NULL,
  `active` int DEFAULT NULL,
  `created_at` datetime DEFAULT NULL,
  `details` varchar(255) DEFAULT NULL,
  PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
```

| id | label | amount 实际值 | created_at | details |
| ---: | --- | ---: | --- | --- |
| 1 | alpha | **13** | 2026-10-30 12:34:56 | first |
| 2 | 中文 | 25 | 2026-10-31 01:02:03 | second |
| 3 | SQL NULL | 0 | 2026-11-01 00:00:00 | `multi` + LF + `line` |

重命名、BIGINT 主键和日期转换已由数据库确认。label(id3) 的 `IS NULL=1`；details(id3) 的 HEX 为 `6D756C74690A6C696E65`，确认真实 LF。非主键列实际均允许 NULL。

**映射页比例 2 与实际 DECIMAL(10,0) 不一致。** 输入 12.50 最终写成 13。不能把 O-166 单元格显示宣称为 DECIMAL(10,2) 创建成功，也不能仅凭一次观察解释其内部原因。原版比例字段的可靠提交路径仍为 Unknown。

本 CSV 的未引用空字段在 NULL 选项关闭时仍写入 NULL，与 [XML 默认路径](navicat-table-toolbar-xml-2026-10-30.md) 的空元素写入空串/0 不同。不能推广为所有格式的空文本语义。

## O：建表冲突与错误边界

保留首次成功创建的自有表，新增 `import-original-errors.csv`：第一行 id=1 已存在，第二行 id=4 尚不存在。希望用它观察重复主键后的继续行为；没有改动首次源 CSV。

在新向导将唯一目标绑定到同一自有表名称。过程中目标下拉菜单能列出该表，但确认后右侧仍显示“新建表”，映射页重新推断字段。动作、截图完整保留；没有把这种状态误当成既有表映射已成功。

**O-176/O-177** 真实执行止于 `CREATE TABLE`：日志显示 `1050 - Table 'toolbar_import_original_probe_20261030' already exists`，并列出失败 DDL、Finished with error。进度为 100%，Processed/Added/Updated/Deleted/Errors **全部为 0**。外部 `import-original-after-errors.tsv` 确认第一次创建的 DDL 和三行数据未变，id=4 没有写入。

这个结果确认了新表分支的建表冲突反馈及计数边界。它没有进入行插入阶段，不能证明重复主键的逐行继续、部分提交或扩展 INSERT 的失败单位；这些仍为 Unknown。本页不把尝试目标称为实际既有表追加。

## 清理、证据质量和待验证项

`import-original-cleanup.tsv` 记录只删除自有测试表、确认目标数量为 0，并保留源四行；其源行与 `xml-original-default-import-cleanup.tsv` 完全一致。**O-178** 关闭已删除测试表的缓存标签、刷新树并恢复源表窗口；没有停止父任务使用的 Navicat 进程或其它 DISPLAY。

证据使用以下边界：

- `import-original-before.tsv` 确认目标不存在，但首次 CLI 没有指定 utf8mb4，中文输出为 `??`；保留原件，不能据此判断源 Unicode 已变。
- `163-original-csv-target-mapping.png` 是被主窗口遮挡的导航失败，不能证明映射；以 O-164/O-166 为准。
- `171-original-csv-current-summary.png` 是执行前状态；完成结果为 `171-original-csv-import-result.png`。
- `174-original-csv-error-mapping.png` 实际仍是源目标页；真正解析映射为 `import-original-error-parsed-mapping.png`。
- `import-original-final-main.png` 是 DROP 后仍显示自有表缓存数据，不能证明该表仍存在；最终源窗口和外部删除结果分别见 O-178 与 cleanup TSV。

仍需后续差分验证：比例编辑如何可靠进入 DDL、既有表导入的正确选择路径、其它导入模式、逐行错误继续/停止及部分提交、二进制和其它日期/编码选项、超过 100 行预览及大文件行为。NyaDB 新表实现、测试和真实 GUI 验证由主报告记录，本页不作 V 声明。
