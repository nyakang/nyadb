package dataworkspace

import (
	"fmt"
	"slices"
	"strconv"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func mysqlDataWizardSteps(kind string) []string {
	if kind == "export" {
		return []string{"文件格式", "范围与字段", "格式设置", "目标文件", "执行结果"}
	}
	return []string{"源与目标", "源设置", "字段映射", "模式与高级选项", "校验确认", "执行结果"}
}

func (s *State) dataTransferWizard(c *ui.Context, t *Document, available bool, title string) {
	tr := &t.transfer
	steps := mysqlDataWizardSteps(tr.Kind)
	tr.Step = max(0, min(tr.Step, len(steps)-1))
	modal := ui.Modal(c, &tr.Open, func() {
		cw, ch := c.Size()
		width, height := min(float32(800), cw-40), min(float32(620), ch-40)
		ui.Column(c).Width(width).Height(height).Padding(18).Gap(10).Background(theme.Colors(c).Header).Children(func() {
			ui.Text(c, title+" · "+locales.Text(steps[tr.Step])).Bold().FontSize(theme.Font(c, 15))
			ui.Text(c, t.database+"."+t.table).TextColor(theme.Colors(c).Muted)
			if tr.Kind == "import" {
				s.dataSourceSwitcher(c, t)
			}
			ui.Row(c).Gap(6).Children(func() {
				for i, step := range steps {
					label := fmt.Sprintf("%d %s", i+1, locales.Text(step))
					e := ui.Text(c, label).FontSize(theme.Font(c, 11))
					if i == tr.Step {
						e.Bold().TextColor(theme.Colors(c).Accent)
					} else {
						e.TextColor(theme.Colors(c).Muted)
					}
				}
			})
			if !available {
				ui.Text(c, locales.Text("原 MySQL 会话已失效，请关闭向导后重新打开")).TextColor(theme.Colors(c).Error)
			}
			ui.Scroll(c).Grow(1).FillWidth().Children(func() {
				ui.Column(c).Gap(9).FillWidth().Children(func() {
					if tr.Kind == "export" {
						s.dataExportWizardPage(c, t)
					} else {
						s.dataImportWizardPage(c, t)
					}
					if tr.Err != "" {
						color := theme.Colors(c).Error
						if tr.Completed && !tr.Uncertain {
							color = theme.Colors(c).Muted
						}
						ui.Text(c, tr.Err).TextColor(color)
					}
					if tr.Busy {
						label := "正在处理数据…"
						if tr.Canceling {
							label = "正在等待取消确认…"
						}
						ui.Text(c, locales.Text(label))
					}
				})
			})
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if tr.Step > 0 && !tr.Submitted && ui.Button(c, locales.Text("上一步")).Disabled(tr.Busy).Clicked() {
					tr.Step--
					tr.Err = ""
				}
				last := tr.Step == len(steps)-1
				label := "取消"
				if tr.Submitted && !tr.Busy {
					label = "关闭"
				}
				if ui.Button(c, locales.Text(label)).Disabled(tr.Canceling).Clicked() {
					s.stopDataTransfer(t)
				}
				if !last {
					canNext := available && !tr.Busy && !tr.Submitted
					if tr.Kind == "import" && tr.Step == 0 {
						canNext = canNext && tr.Input != nil
					}
					if tr.Kind == "import" && tr.Step == 4 {
						canNext = canNext && tr.MappedSpool != nil && tr.PreparationKey == mysqlDataPreparationKey(t) && mysqlDataImportConfirmed(t)
						if ui.PrimaryButton(c, locales.Text("确认导入")).Disabled(!canNext).Clicked() {
							s.startPreparedDataImport(c.Services(), t)
						}
					} else if tr.Kind == "export" && tr.Step == 3 {
						if ui.PrimaryButton(c, locales.Text("导出")).Disabled(!canNext || tr.ExportPath == "").Clicked() {
							tr.Step++
							s.startDataExport(c.Services(), t)
						}
					} else if ui.PrimaryButton(c, locales.Text("下一步")).Disabled(!canNext).Clicked() {
						tr.Step++
						tr.Err = ""
					}
				}
			})
		})
	}).Padding(0)
	s.finishDataTransferModal(t, modal.PressedOutside())
}

func (s *State) dataImportWizardPage(c *ui.Context, t *Document) {
	tr := &t.transfer
	switch tr.Step {
	case 0:
		oldFormat := tr.Format
		mysqlDataRuleSelect(c, &tr.Format, mysqlDataTransferFormatChoices("import"), func(x string) string { return x }).Width(180).Disabled(tr.Busy || tr.Input != nil)
		if oldFormat != tr.Format {
			tr.Options = mysqlDataSourceDefaults(tr.Format)
		}
		mysqlDataWizardFormatHint(c, tr.Format)
		s.dataImportTargetControls(c, t)
		if s.deps.ImportTargets != nil && tr.Target != "new" {
			targets := s.deps.ImportTargets(t.profileID, t.database)
			if !slices.Contains(targets, t.table) {
				targets = append([]string{t.table}, targets...)
			}
			selected := tr.TargetTable
			if selected == "" {
				selected = t.table
			}
			mysqlDataRuleSelect(c, &selected, targets, func(x string) string { return x }).Width(250).Disabled(tr.Busy)
			if selected != mysqlDataImportDestination(t) {
				tr.TargetTable = selected
				if selected == t.table {
					tr.TargetTable = ""
				}
				tr.TargetColumns = nil
				mysqlDataResetTargetMapping(t)
			}
			if tr.TargetTable != "" && tr.TargetColumns == nil && ui.Button(c, locales.Text("读取目标表字段")).Disabled(tr.Busy).Clicked() {
				s.loadImportTarget(c.Services(), t)
			}
		}
		if tr.Format == "ODBC" || tr.Format == "Access" || tr.Format == "Paradox" {
			s.dataExternalSourceControls(c, t)
			break
		}
		ui.Text(c, tr.Path).SingleLine()
		if ui.Button(c, locales.Text("选择文件")).Disabled(tr.Busy).Clicked() {
			s.chooseDataImportFile(c.Services(), t)
		}
		if tr.Input != nil {
			ui.Text(c, fmt.Sprintf(locales.Text("已读取 %d 行，预览最多 100 行"), tr.TotalRows))
		}
	case 1:
		s.dataSourceOptionControls(c, t)
		if ui.Button(c, locales.Text("应用源设置并重新读取")).Disabled(tr.Busy || tr.Path == "").Clicked() {
			if tr.Format == "ODBC" || tr.Format == "Access" || tr.Format == "Paradox" {
				s.readExternalSource(c.Services(), t)
			} else {
				s.readDataImportPath(c.Services(), t, tr.Path)
			}
		}
	case 2:
		if tr.Input != nil {
			s.dataImportReview(c, t)
		}
	case 3:
		s.dataImportModeControls(c, t)
		ui.Checkbox(c, &tr.EmptyAsNull, locales.Text("使用 NULL 取代空字符串")).Disabled(tr.Busy)
		compatibility := []string{"preserve"}
		if tr.Format == "XML" {
			compatibility = append(compatibility, "navicat_xml")
		}
		mysqlDataRuleSelect(c, &tr.Options.Compatibility, compatibility, func(x string) string {
			if x == "preserve" {
				return locales.Text("保留源值")
			}
			return locales.Text("原版 XML 空值兼容转换")
		}).Width(250)
		ui.Checkbox(c, &tr.ExtendedInsert, locales.Text("使用扩展插入语句"))
		ui.Checkbox(c, &tr.IgnoreForeignKeys, locales.Text("忽略外键约束"))
		kb := strconv.Itoa(tr.StatementKB)
		mysqlDataWizardInput(c, &kb, locales.Text("最大语句大小（KB）")).Width(180)
		if n, err := strconv.Atoi(kb); err == nil && n > 0 && n <= 16384 {
			tr.StatementKB = n
		}
	case 4:
		if len(tr.Sources) > 1 {
			ui.Text(c, locales.Text("每个目标分别提交；后续失败不会回滚已完成的目标。"))
		}
		ui.Text(c, fmt.Sprintf(locales.Text("目标 %s · %s · %d 行"), mysqlDataImportDestination(t), mysqlDataImportModeLabel(mysqlDataImportMode(t)), tr.TotalRows)).Bold()
		if tr.MappedSpool == nil || tr.PreparationKey != mysqlDataPreparationKey(t) {
			if ui.Button(c, locales.Text("完整校验全部源记录")).Disabled(tr.Busy).Clicked() {
				s.prepareDataImport(c.Services(), t)
			}
		} else {
			ui.Text(c, fmt.Sprintf(locales.Text("已完整校验 %d 行 · 转换 %d 个值"), tr.TotalRows, tr.ChangedValues))
		}
		mysqlDataWizardInput(c, &tr.Confirmation, locales.Text("输入表名称以确认导入")).Placeholder(mysqlDataImportDestination(t))
		if mysqlDataImportMode(t) == "replace" {
			mysqlDataWizardInput(c, &tr.DestructiveConfirmation, locales.Text("再次输入表名称以确认清空"))
		}
		if tr.Input != nil {
			s.dataImportConversionPreview(c, t)
		}
		s.dataTransferPresetControls(c, t)
	case 5:
		for _, receipt := range tr.Batch {
			ui.Text(c, receipt.Database+"."+receipt.Table)
			if receipt.Result.Created && !receipt.Result.CommitUncertain && s.deps.OpenImportedTable != nil && ui.Button(c, locales.Text("打开目标表")+" "+receipt.Table).Clicked() {
				s.deps.OpenImportedTable(c.Services(), TableImportEvent{ProfileID: t.profileID, Generation: tr.Generation, Database: receipt.Database, Table: receipt.Table, Created: true, Completed: receipt.Result.Committed})
			}
		}
		s.dataImportResultRows(c, t)
		if len(tr.Batch) == 0 && tr.Created && !tr.Uncertain && !tr.Busy && s.deps.OpenImportedTable != nil && ui.Button(c, locales.Text("打开目标表")).Clicked() {
			s.deps.OpenImportedTable(c.Services(), TableImportEvent{ProfileID: t.profileID, Generation: tr.Generation, Database: t.database, Table: tr.ResultTable, Created: true, Completed: tr.Completed})
		}
	}
}

func (s *State) dataExportWizardPage(c *ui.Context, t *Document) {
	tr := &t.transfer
	switch tr.Step {
	case 0:
		oldFormat := tr.Format
		mysqlDataRuleSelect(c, &tr.Format, mysqlDataTransferFormatChoices("export"), func(x string) string { return x }).Width(180)
		if oldFormat != tr.Format {
			tr.Options = mysqlDataSourceDefaults(tr.Format)
		}
		mysqlDataWizardFormatHint(c, tr.Format)
	case 1:
		mysqlDataRuleSelect(c, &tr.Scope, []string{"page", "selection", "all"}, func(x string) string {
			if x == "page" {
				return locales.Text("当前页")
			}
			if x == "selection" {
				return locales.Text("当前选区")
			}
			return locales.Text("完整筛选结果")
		}).Width(220)
		for _, field := range tr.Fields {
			checked := tr.Included[field]
			ui.Checkbox(c, &checked, field)
			tr.Included[field] = checked
		}
	case 2:
		s.dataSourceOptionControls(c, t)
		s.dataTransferPresetControls(c, t)
	case 3:
		mysqlDataWizardInput(c, &tr.ExportPath, locales.Text("目标文件路径")).FillWidth()
		if ui.Button(c, locales.Text("选择目标文件")).Disabled(tr.Busy).Clicked() {
			s.chooseDataExportPath(c.Services(), t)
		}
	case 4:
		if tr.Completed {
			ui.Text(c, locales.Text("导出完成"))
		}
	}
}

// Label is accessible metadata in MyGo; render the field name separately.
func mysqlDataWizardField(c *ui.Context, label string, build func()) {
	ui.Row(c).Gap(12).AlignItems(ui.Center).FillWidth().Children(func() {
		ui.Text(c, label+":").Width(300).FontSize(theme.Font(c, 12))
		build()
	})
}

func mysqlDataWizardInput(c *ui.Context, value *string, label string) ui.Element {
	var input ui.Element
	mysqlDataWizardField(c, label, func() {
		input = ui.TextInput(c, value).Label(label).FillWidth()
	})
	return input
}

func mysqlDataWizardSelect(c *ui.Context, label string, value *string, choices []string, labels func(string) string) ui.Element {
	var input ui.Element
	mysqlDataWizardField(c, label, func() {
		input = mysqlDataRuleSelect(c, value, choices, labels).Label(label)
	})
	return input
}

func (s *State) dataSourceOptionControls(c *ui.Context, t *Document) {
	o := &t.transfer.Options
	if mysqlDataTextFormat(t.transfer.Format) || t.transfer.Kind == "import" && t.transfer.Format == "DBF" {
		encodings := []string{"UTF-8", "UTF-16LE", "UTF-16BE", "GB18030", "Windows-1252", "Shift_JIS"}
		if t.transfer.Format == "DBF" {
			encodings = append([]string{""}, encodings...)
		}
		mysqlDataWizardSelect(c, locales.Text("文本编码"), &o.Encoding, encodings, func(x string) string {
			if x == "" {
				return locales.Text("自动检测编码")
			}
			return x
		}).Width(180)
	}
	if t.transfer.Format == "CSV" || t.transfer.Format == "TSV" || t.transfer.Format == "TXT" {
		mysqlDataWizardInput(c, &o.Delimiter, locales.Text("字段分隔符（使用 \\t 表示制表符）")).Width(250)
		mysqlDataWizardInput(c, &o.Quote, locales.Text("文本识别符")).Width(180)
		mysqlDataWizardSelect(c, locales.Text("换行符"), &o.Newline, []string{"LF", "CRLF"}, func(x string) string { return x }).Width(180)
	}
	if t.transfer.Kind == "import" {
		for _, item := range []struct {
			name  string
			value *int64
		}{{"字段名行", &o.HeaderRow}, {"首个数据行", &o.FirstRow}, {"最后数据行（0 为全部）", &o.LastRow}} {
			if item.value == &o.HeaderRow && !slices.Contains([]string{"CSV", "TSV", "TXT", "XLS", "XLSX", "HTML"}, t.transfer.Format) {
				continue
			}
			value := strconv.FormatInt(*item.value, 10)
			mysqlDataWizardInput(c, &value, locales.Text(item.name)).Width(230)
			if n, err := strconv.ParseInt(value, 10, 64); err == nil && n >= 0 {
				*item.value = n
			}
		}
	} else if slices.Contains([]string{"CSV", "TSV", "TXT"}, t.transfer.Format) {
		header := o.HeaderRow != 0
		ui.Checkbox(c, &header, locales.Text("包含字段名行"))
		o.HeaderRow = 0
		if header {
			o.HeaderRow = 1
		}
	}
	if t.transfer.Format == "XML" {
		mysqlDataWizardInput(c, &o.XMLRoot, locales.Text("XML 根标签"))
		mysqlDataWizardInput(c, &o.XMLRow, locales.Text("XML 行标签"))
		ui.Checkbox(c, &o.XMLAttributes, locales.Text("将 XML 属性作为字段"))
	}
	if t.transfer.Kind == "import" && (t.transfer.Format == "XLSX" || t.transfer.Format == "XLS") {
		value := strconv.Itoa(o.Sheet + 1)
		mysqlDataWizardInput(c, &value, locales.Text("工作表序号（从 1 开始）"))
		if n, err := strconv.Atoi(value); err == nil && n > 0 {
			o.Sheet = n - 1
		}
	}
	if t.transfer.Kind == "export" && t.transfer.Format == "SQL" {
		return
	}
	mysqlDataWizardSelect(c, locales.Text("日期顺序"), &o.DateOrder, []string{"YMD", "DMY", "MDY"}, func(x string) string { return x }).Width(180)
	mysqlDataWizardInput(c, &o.DateSeparator, locales.Text("日期分隔符"))
	mysqlDataWizardInput(c, &o.TimeSeparator, locales.Text("时间分隔符"))
	mysqlDataWizardInput(c, &o.DecimalSeparator, locales.Text("小数点符号"))
	if t.transfer.Kind == "import" || mysqlDataTextFormat(t.transfer.Format) {
		mysqlDataWizardSelect(c, locales.Text("二进制编码"), &o.BinaryEncoding, []string{"hex", "base64"}, func(x string) string { return x }).Width(180)
	}
}

func mysqlDataWizardFormatHint(c *ui.Context, format string) {
	if format == "XML" {
		ui.Text(c, locales.Text("XML 使用 RECORDS/RECORD；空元素作为空字符串，xsi:nil 表示 NULL，仅显式 encoding=base64/hex 解码二进制。")).FontSize(theme.Font(c, 11)).TextColor(theme.Colors(c).Muted)
	}
}
