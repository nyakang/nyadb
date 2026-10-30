package dataworkspace

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

// TableImportEvent always carries the request's original qualified destination.
// The host validates that generation before invalidating metadata or opening it.
type TableImportEvent struct {
	ProfileID                     int64
	Generation                    uint64
	Database, Table               string
	Created, Uncertain, Completed bool
}

func (s *State) newTableImporter() mysqlDataNewTableImporter {
	if s.NewTableImporter != nil {
		return s.NewTableImporter
	}
	return s.runtime().NewTableImporter
}

func (s *State) importer() mysqlDataImporter {
	if s.Importer != nil {
		return s.Importer
	}
	return s.runtime().Importer
}

func mysqlDataImportMode(t *Document) string {
	if t.transfer.Target == "new" || t.transfer.Mode == "" {
		return "append"
	}
	return t.transfer.Mode
}

func mysqlDataImportNeedsMatch(mode string) bool {
	return mode == "update" || mode == "upsert" || mode == "insert_missing" || mode == "delete"
}

func mysqlDataImportConfirmed(t *Document) bool {
	return t.transfer.Confirmation == mysqlDataImportDestination(t) &&
		(mysqlDataImportMode(t) != "replace" || t.transfer.DestructiveConfirmation == mysqlDataImportDestination(t))
}

func mysqlDataImportModeLabel(mode string) string {
	switch mode {
	case "update":
		return locales.Text("更新匹配记录")
	case "upsert":
		return locales.Text("追加或更新")
	case "insert_missing":
		return locales.Text("仅追加未匹配记录")
	case "delete":
		return locales.Text("删除匹配记录")
	case "replace":
		return locales.Text("清空目标后复制")
	default:
		return locales.Text("追加新记录")
	}
}

func (s *State) dataImportModeControls(c *ui.Context, t *Document) {
	tr := &t.transfer
	if tr.Target != "new" {
		ui.Row(c).Gap(8).Children(func() {
			ui.Text(c, locales.Text("导入模式"))
			mysqlDataRuleSelect(c, &tr.Mode, []string{"append", "update", "upsert", "insert_missing", "delete", "replace"}, mysqlDataImportModeLabel).Width(220).Disabled(tr.Busy || tr.Submitted)
		})
	}
	ui.Checkbox(c, &tr.ContinueOnError, locales.Text("遇到行错误时继续")).Disabled(tr.Busy || tr.Submitted)
	if tr.ContinueOnError {
		ui.Text(c, locales.Text("失败行会跳过，成功行一起提交；完成后逐行显示结果。")).TextColor(theme.Colors(c).Muted).FontSize(theme.Font(c, 11))
	}
	mode := mysqlDataImportMode(t)
	if mode == "replace" {
		ui.Text(c, locales.Text("此模式会删除目标表全部记录，再导入文件；提交前可取消回滚。")).TextColor(theme.Colors(c).Error)
		mysqlDataWizardInput(c, &tr.DestructiveConfirmation, locales.Text("再次输入表名称以确认清空")).Placeholder(mysqlDataImportDestination(t)).Disabled(tr.Busy || tr.Submitted).Grow(1)
	}
	if mysqlDataImportNeedsMatch(mode) && tr.Input != nil {
		ui.Text(c, locales.Text("匹配字段（须为完整主键或唯一键）")).Bold()
		ui.Column(c).Gap(3).Children(func() {
			for _, field := range tr.Mapping {
				if field == "" {
					continue
				}
				checked := slices.Contains(tr.MatchColumns, field)
				ui.Checkbox(c, &checked, field).Disabled(tr.Busy || tr.Submitted)
				if checked && !slices.Contains(tr.MatchColumns, field) {
					tr.MatchColumns = append(tr.MatchColumns, field)
				} else if !checked {
					tr.MatchColumns = slices.DeleteFunc(tr.MatchColumns, func(x string) bool { return x == field })
				}
			}
		})
	}
}

func (s *State) dataImportResultRows(c *ui.Context, t *Document) {
	if len(t.transfer.RowResults) == 0 && t.transfer.Journal == nil {
		return
	}
	ui.Text(c, locales.Text("逐行导入结果")).Bold()
	columns := []ui.TableColumn{{ID: "row", Title: locales.Text("逐行导入结果"), Width: 570, MinWidth: 120}}
	count := len(t.transfer.RowResults)
	if t.transfer.Journal != nil {
		count = int(t.transfer.Journal.total)
	}
	ui.Table(c, &t.transfer.ResultGrid, columns, count, func(index, _ int) {
		var row mysqlsvc.MySQLDataImportRowResult
		if t.transfer.Journal != nil {
			var err error
			committed, uncertain := t.transfer.JournalCommitted, t.transfer.JournalUncertain
			if len(t.transfer.Batch) > 0 {
				committed, uncertain = mysqlDataBatchRowSettlement(t.transfer.Batch, int64(index), t.transfer.JournalUncertain)
			}
			row, err = t.transfer.Journal.Row(int64(index), committed, uncertain)
			if err != nil {
				row = mysqlsvc.MySQLDataImportRowResult{Row: index + 1, Status: "uncertain", Error: "result journal unavailable"}
			}
		} else {
			row = t.transfer.RowResults[index]
		}
		status := mysqlDataImportRowStatus(row.Status)
		label := fmt.Sprintf(locales.Text("第 %d 行 · %s · %s"), row.Row, mysqlDataImportActionLabel(row.Kind), status)
		if row.Table != "" {
			label = row.Database + "." + row.Table + " · " + label
		}
		if row.Error != "" {
			label += " · " + row.Error
		}
		ui.Text(c, label).FontSize(theme.Font(c, 11)).SingleLine().Tooltip(label)
	}).Height(150).FillWidth()
}

func mysqlDataImportActionLabel(kind string) string {
	switch kind {
	case "insert":
		return locales.Text("追加")
	case "update":
		return locales.Text("更新")
	case "delete":
		return locales.Text("删除")
	default:
		return locales.Text("处理")
	}
}

func mysqlDataImportRowStatus(status string) string {
	switch status {
	case "committed":
		return locales.Text("已提交")
	case "skipped":
		return locales.Text("已跳过")
	case "failed":
		return locales.Text("失败")
	case "rolled_back":
		return locales.Text("已回滚")
	case "uncertain":
		return locales.Text("待核验")
	default:
		return locales.Text("未处理")
	}
}

func (s *State) consumeDataImportResult(services ui.Services, t *Document, result mysqlDataTransferResult, stale bool) {
	tr, imported := &t.transfer, result.Imported
	tr.Created, tr.ResultTable = imported.Created, result.Destination
	tr.RowResults = append([]mysqlsvc.MySQLDataImportRowResult(nil), imported.Rows...)
	if result.NewTable {
		entry := result.CreationHistory
		entry.Failed = !imported.Created || imported.CreationUncertain || stale
		entry.Message = locales.Text("目标表已创建")
		if entry.Failed {
			entry.Message = mysqlDataHistoryError(result.Err, stale, false, imported.CreationUncertain)
		}
		s.appendDataHistory(t, entry)
	}
	committed, skipped, failed := 0, 0, 0
	settled := len(imported.Rows) == result.Rows
	for i, row := range imported.Rows {
		settled = settled && row.Row == i+1
		switch row.Status {
		case "committed":
			settled = settled && (row.Kind == "insert" || row.Kind == "update" || row.Kind == "delete")
			committed++
		case "skipped":
			settled = settled && row.Kind == "skip"
			skipped++
		case "failed":
			settled = settled && row.Kind == "error"
			failed++
		default:
			settled = false
		}
	}
	uncertain := stale || imported.CreationUncertain || imported.CommitUncertain || imported.Saved != nil && imported.Saved.CommitUncertain || imported.Committed && (!settled || result.Err != nil) || result.Err == nil && (!imported.Committed || !settled || result.NewTable && !imported.Created)
	if uncertain {
		tr.Uncertain = true
		tr.Err = locales.Text("导入结果不确定，禁止重放；请核验数据库")
		for i := range tr.RowResults {
			if tr.RowResults[i].Status == "committed" {
				tr.RowResults[i].Status = "uncertain"
			}
		}
		if !result.NewTable {
			t.edit.Uncertain = cloneEditResult(imported.Saved)
			if t.edit.Uncertain == nil {
				t.edit.Uncertain = &mysqlsvc.MySQLDataEditResult{}
			}
			t.edit.Uncertain.CommitUncertain = true
		}
	} else if result.Err != nil {
		tr.Err = locales.Text("导入失败；本次向导不会重放：") + result.Err.Error()
		if result.NewTable && imported.Created {
			tr.Err = fmt.Sprintf(locales.Text("目标表 %s 已创建，数据导入失败；表保留。本次向导不会重放："), result.Destination) + result.Err.Error()
		}
	} else {
		tr.Completed = true
		tr.Err = fmt.Sprintf(locales.Text("已提交 %d 行 · 跳过 %d 行 · 失败 %d 行"), committed, skipped, failed)
		s.setStatus(tr.Err)
		if !result.NewTable {
			s.Load(services, t, true)
		}
	}
	// History contains qualified operation summaries and actual committed counts,
	// never imported values or server diagnostics that can echo private data.
	counts := map[string]int{}
	for _, row := range tr.RowResults {
		kind := strings.ToUpper(row.Kind)
		if kind == "INSERT" || kind == "UPDATE" || kind == "DELETE" {
			if row.Status == "committed" {
				counts[kind]++
			}
		}
	}
	for _, kind := range []string{"INSERT", "UPDATE", "DELETE"} {
		if counts[kind] == 0 {
			continue
		}
		s.appendDataHistory(t, mysqlDataHistoryEntry{Kind: kind, At: time.Now(), Rows: counts[kind], Summary: true, SQL: kind + " " + mysqlDataHistoryTable(t.database, result.Destination), Message: tr.Err})
	}
	if failed > 0 || result.Err != nil || uncertain {
		s.appendDataHistory(t, mysqlDataHistoryEntry{Kind: "INSERT", At: time.Now(), Failed: true, Summary: true, SQL: "IMPORT " + mysqlDataHistoryTable(t.database, result.Destination), Message: mysqlDataHistoryError(result.Err, stale, false, uncertain)})
	}
	if result.NewTable && (imported.Created || imported.CreationUncertain) && s.deps.TableImported != nil {
		s.deps.TableImported(services, TableImportEvent{ProfileID: result.Profile, Generation: result.Generation, Database: t.database, Table: result.Destination, Created: imported.Created, Uncertain: tr.Uncertain, Completed: tr.Completed})
	}
}

func mysqlDataImportDestination(t *Document) string {
	if t.transfer.Target == "new" {
		return t.transfer.NewName
	}
	if t.transfer.TargetTable != "" {
		return t.transfer.TargetTable
	}
	return t.table
}

func (s *State) dataImportTargetControls(c *ui.Context, t *Document) {
	tr := &t.transfer
	ui.Row(c).Gap(8).Children(func() {
		ui.Text(c, locales.Text("目标表"))
		mysqlDataRuleSelect(c, &tr.Target, []string{"existing", "new"}, func(x string) string {
			if x == "new" {
				return locales.Text("新建目标表")
			}
			return locales.Text("已有表")
		}).Width(180).Disabled(tr.Busy || tr.Submitted)
	})
	if tr.Target == "new" {
		mysqlDataWizardInput(c, &tr.NewName, locales.Text("新目标表名称")).Disabled(tr.Busy || tr.Submitted).Grow(1)
		ui.Text(c, locales.Text("新表将在当前数据库创建；可修改字段名、类型、可空和主键。建表成功后若数据失败，目标表保留。")).FontSize(theme.Font(c, 11)).TextColor(theme.Colors(c).Muted)
		if s.newTableImporter() == nil && s.importer() == nil {
			ui.Text(c, locales.Text("新表导入服务不可用")).TextColor(theme.Colors(c).Error)
		}
	}
}

func mysqlDataImportMapping(t *Document) ([]string, []mysqlsvc.MySQLDataImportColumn, []mysqlsvc.MySQLStructureColumn, error) {
	tr := &t.transfer
	if tr.Target != "new" {
		if tr.TargetTable != "" && tr.TargetColumns == nil {
			return nil, nil, nil, fmt.Errorf("%s", locales.Text("请先读取目标表字段"))
		}
		return tr.Mapping, nil, mysqlDataImportTargetMetadata(t), nil
	}
	if tr.Input == nil || len(tr.NewColumns) != len(tr.Input.Fields) {
		return nil, nil, nil, mysqlsvc.ErrMySQLDataPage
	}
	mapping := make([]string, len(tr.NewColumns))
	var columns []mysqlsvc.MySQLDataImportColumn
	for i, col := range tr.NewColumns {
		mapping[i] = col.Name
		if col.Name != "" {
			columns = append(columns, col)
		}
	}
	metadata, err := mysqlsvc.MySQLDataImportColumnMetadata(columns)
	return mapping, columns, metadata, err
}

func mysqlDataImportTargetMetadata(t *Document) []mysqlsvc.MySQLStructureColumn {
	if t.transfer.TargetTable != "" {
		return t.transfer.TargetColumns
	}
	if t.page != nil {
		return t.page.ColumnMetadata
	}
	return nil
}

func mysqlDataResetTargetMapping(t *Document) {
	tr := &t.transfer
	tr.PreparationKey, tr.Confirmation, tr.DestructiveConfirmation = "", "", ""
	tr.MatchColumns = nil
	if tr.Input == nil {
		return
	}
	tr.Mapping = make([]string, len(tr.Input.Fields))
	for i, field := range tr.Input.Fields {
		for _, col := range mysqlDataImportTargetMetadata(t) {
			if field == col.Name && !mysqlsvc.MySQLDataColumnGenerated(col) {
				tr.Mapping[i] = field
				if col.Key == "PRI" {
					tr.MatchColumns = append(tr.MatchColumns, field)
				}
			}
		}
	}
}

func mysqlDataImportRequest(t *Document) ([]mysqlsvc.MySQLDataImportColumn, []mysqlsvc.MySQLDataEdit, error) {
	if t.page == nil {
		return nil, nil, mysqlsvc.ErrMySQLDataPage
	}
	if t.transfer.Target == "new" {
		name := t.transfer.NewName
		if name == "" || !utf8.ValidString(name) || utf8.RuneCountInString(name) > 64 || strings.ContainsAny(name, "\x00\r\n") || strings.HasSuffix(name, " ") {
			return nil, nil, fmt.Errorf("%s", locales.Text("目标表名称无效，最多 64 个字符"))
		}
	}
	mapping, columns, metadata, err := mysqlDataImportMapping(t)
	if err != nil {
		return nil, nil, err
	}
	edits, err := mysqlDataImportMappedEdits(t, mapping, metadata)
	if err == nil {
		err = mysqlsvc.ValidateMySQLDataImportRequest(metadata, mysqlDataImportServiceRequest(t, columns, edits))
	}
	return columns, edits, err
}

func mysqlDataImportServiceRequest(t *Document, columns []mysqlsvc.MySQLDataImportColumn, edits []mysqlsvc.MySQLDataEdit) mysqlsvc.MySQLDataImportRequest {
	request := mysqlsvc.MySQLDataImportRequest{Mode: mysqlDataImportMode(t), ContinueOnError: t.transfer.ContinueOnError, NewTable: t.transfer.Target == "new", Columns: append([]mysqlsvc.MySQLDataImportColumn(nil), columns...), Edits: edits}
	if mysqlDataImportNeedsMatch(request.Mode) {
		request.MatchColumns = append([]string(nil), t.transfer.MatchColumns...)
	}
	return request
}

// Mapped source values remain immutable; the service shares bounded validation
// with the review and determines each row's actual action inside its transaction.
func mysqlDataImportMappedEdits(t *Document, mapping []string, metadata []mysqlsvc.MySQLStructureColumn) ([]mysqlsvc.MySQLDataEdit, error) {
	input := t.transfer.Input
	if input == nil || len(mapping) != len(input.Fields) || len(input.Rows) == 0 || len(input.Rows) > mysqlsvc.MaxMySQLDataImportRows {
		return nil, mysqlsvc.ErrMySQLDataPage
	}
	allowed := map[string]mysqlsvc.MySQLStructureColumn{}
	for _, col := range metadata {
		allowed[col.Name] = col
	}
	seen := map[string]bool{}
	for _, field := range mapping {
		if field == "" {
			continue
		}
		col, ok := allowed[field]
		if !ok || seen[field] || mysqlsvc.MySQLDataColumnGenerated(col) || mysqlDataSpatialField(col) {
			return nil, fmt.Errorf("%s", locales.Text("字段映射包含重复、未知或生成字段"))
		}
		seen[field] = true
	}
	if len(seen) == 0 {
		return nil, fmt.Errorf("%s", locales.Text("请映射至少一个字段"))
	}
	edits := make([]mysqlsvc.MySQLDataEdit, len(input.Rows))
	for i, row := range input.Rows {
		if len(row) != len(mapping) {
			return nil, mysqlsvc.ErrMySQLDataPage
		}
		values := make(map[string]mysqlsvc.MySQLDataValue, len(seen))
		for j, field := range mapping {
			if field == "" {
				continue
			}
			value := mysqlDataImportValue(row[j], t.transfer.EmptyAsNull)
			if !value.Null && !value.Binary && mysqlDataBinaryField(allowed[field]) && strings.HasPrefix(value.Text, "0x") {
				if b, err := hex.DecodeString(value.Text[2:]); err == nil {
					value = mysqlsvc.MySQLDataValue{Binary: true, Bytes: b}
				}
			}
			value.Bytes = slices.Clone(value.Bytes)
			values[field] = value
		}
		edits[i] = mysqlsvc.MySQLDataEdit{Kind: "insert", Values: values}
	}
	return edits, nil
}

// Parsed input is immutable in the UI. Revalidate only changes to mapping or
// options while rendering; submission always builds and validates a new owned
// request. A large input therefore does not allocate its entire batch per frame.
func mysqlDataImportReviewValidation(t *Document) (int, error) {
	tr := &t.transfer
	metadata := mysqlDataImportTargetMetadata(t)
	key, _ := json.Marshal(struct {
		Target, NewName, Mode, Destination string
		Empty, Continue                    bool
		Mapping, Match                     []string
		Columns                            []mysqlsvc.MySQLDataImportColumn
		Metadata                           []mysqlsvc.MySQLStructureColumn
	}{tr.Target, tr.NewName, tr.Mode, mysqlDataImportDestination(t), tr.EmptyAsNull, tr.ContinueOnError, tr.Mapping, tr.MatchColumns, tr.NewColumns, metadata})
	if tr.ReviewInput != tr.Input || tr.ReviewKey != string(key) {
		_, edits, err := mysqlDataImportRequest(t)
		tr.ReviewInput, tr.ReviewKey, tr.ReviewRows, tr.ReviewErr = tr.Input, string(key), len(edits), err
	}
	return tr.ReviewRows, tr.ReviewErr
}

func (s *State) dataImportReview(c *ui.Context, t *Document) {
	tr := &t.transfer
	colors := theme.Colors(c)
	ui.Text(c, fmt.Sprintf(locales.Text("字段映射 · %d 行"), len(tr.Input.Rows))).Bold()
	ui.Scroll(c).MaxHeight(190).Children(func() {
		ui.Column(c).Gap(4).Children(func() {
			if tr.Target == "new" {
				ui.Row(c).Gap(5).Children(func() {
					for _, cell := range []struct {
						label string
						width float32
					}{{"源字段", 100}, {"目标字段", 145}, {"数据类型", 155}, {"可空", 58}, {"主键", 58}} {
						ui.Text(c, locales.Text(cell.label)).Width(cell.width).TextColor(colors.Muted)
					}
				})
				for i, field := range tr.Input.Fields {
					if i >= len(tr.NewColumns) {
						break
					}
					col := &tr.NewColumns[i]
					ui.Row(c.Key(fmt.Sprint("new-mapping", i))).Gap(5).Children(func() {
						ui.Text(c, field).Width(100).SingleLine().Tooltip(field)
						ui.TextInput(c, &col.Name).Label(fmt.Sprintf(locales.Text("%s 的目标字段"), field)).Placeholder(locales.Text("留空以忽略")).Width(145).Disabled(tr.Busy || tr.Submitted)
						ui.TextInput(c, &col.Type).Label(fmt.Sprintf(locales.Text("%s 的数据类型"), field)).Width(155).Disabled(tr.Busy || tr.Submitted)
						ui.Checkbox(c, &col.Nullable, locales.Text("可空")).Width(58).Disabled(tr.Busy || tr.Submitted || col.PrimaryKey)
						ui.Checkbox(c, &col.PrimaryKey, locales.Text("主键")).Width(58).Disabled(tr.Busy || tr.Submitted)
						if col.PrimaryKey {
							col.Nullable = false
						}
					})
				}
			} else {
				choices := []string{""}
				for _, col := range mysqlDataImportTargetMetadata(t) {
					if !mysqlsvc.MySQLDataColumnGenerated(col) {
						choices = append(choices, col.Name)
					}
				}
				for i, field := range tr.Input.Fields {
					ui.Row(c.Key(fmt.Sprint("mapping", i))).Gap(8).Children(func() {
						ui.Text(c, field).Width(180)
						mysqlDataRuleSelect(c, &tr.Mapping[i], choices, func(x string) string {
							if x == "" {
								return locales.Text("忽略此字段")
							}
							return x
						}).Disabled(tr.Busy || tr.Submitted).Grow(1)
					})
				}
			}
		})
	})
	mapping, _, _, mappingErr := mysqlDataImportMapping(t)
	count, err := mysqlDataImportReviewValidation(t)
	if err != nil {
		ui.Text(c, err.Error()).TextColor(colors.Error)
	} else {
		ui.Text(c, fmt.Sprintf(locales.Text("已校验 %d 行，预览前 %d 行"), count, min(5, count)))
	}
	if mappingErr == nil {
		ui.Scroll(c).MaxHeight(90).Children(func() {
			ui.Column(c).Children(func() {
				for i, row := range tr.Input.Rows[:min(5, len(tr.Input.Rows))] {
					var cells []string
					for j, value := range row {
						if j >= len(mapping) || mapping[j] == "" {
							continue
						}
						value = mysqlDataImportValue(value, tr.EmptyAsNull)
						text := value.Text
						if value.Null {
							text = "NULL"
						} else if value.Binary {
							text = fmt.Sprintf("0x%x", value.Bytes[:min(39, len(value.Bytes))])
							if len(value.Bytes) > 39 {
								text += "…"
							}
						}
						if utf8.RuneCountInString(text) > 80 {
							text = string([]rune(text)[:80]) + "…"
						}
						cells = append(cells, mapping[j]+"="+text)
					}
					ui.Text(c, fmt.Sprintf("%d  %s", i+1, strings.Join(cells, " · "))).SingleLine().FontSize(theme.Font(c, 11))
				}
			})
		})
	}
	mysqlDataWizardInput(c, &tr.Confirmation, locales.Text("输入表名称以确认导入")).Placeholder(mysqlDataImportDestination(t)).Disabled(tr.Busy || tr.Submitted).Grow(1)
}

// Infer from every parsed value rather than just the preview. The suggestion is
// editable, never a conversion: values remain exact and unsupported numeric
// precision falls back to text instead of approximate floating point.
func mysqlDataInferImportColumns(input *mysqlDataImportInput) []mysqlsvc.MySQLDataImportColumn {
	if input == nil {
		return nil
	}
	columns := make([]mysqlsvc.MySQLDataImportColumn, len(input.Fields))
	for i, name := range input.Fields {
		var texts []string
		binary, textual, maxBytes, maxChars := false, false, 0, 0
		for _, row := range input.Rows {
			if i >= len(row) || row[i].Null {
				continue
			}
			v := row[i]
			if v.Binary {
				binary = true
				maxBytes = max(maxBytes, len(v.Bytes))
			} else {
				textual = true
				texts = append(texts, v.Text)
				maxBytes = max(maxBytes, len(v.Text))
				maxChars = max(maxChars, utf8.RuneCountInString(v.Text))
			}
		}
		kind := "VARCHAR(255)"
		if binary && !textual {
			kind = "LONGBLOB"
		} else if maxChars > 255 {
			kind = "TEXT"
			if maxBytes > 65535 {
				kind = "LONGTEXT"
			}
		}
		if len(texts) > 0 && !binary {
			integer, numeric, date, datetime := true, true, true, true
			integerDigits, scale := 0, 0
			for _, value := range texts {
				n, ok := new(big.Int).SetString(value, 10)
				integer = integer && ok && n.IsInt64()
				digits, decimalScale, ok := mysqlDataInferDecimal(value)
				numeric = numeric && ok
				integerDigits = max(integerDigits, digits)
				scale = max(scale, decimalScale)
				_, dateErr := time.Parse("2006-01-02", value)
				date = date && dateErr == nil
				_, timeErr := time.Parse("2006-01-02 15:04:05.999999", value)
				datetime = datetime && timeErr == nil
				if dot := strings.LastIndexByte(value, '.'); dot >= 0 && len(value)-dot-1 > 6 {
					datetime = false
				}
			}
			if integer {
				kind = "BIGINT"
			} else if numeric && scale <= 30 && integerDigits+scale <= 65 {
				kind = fmt.Sprintf("DECIMAL(%d,%d)", max(1, integerDigits+scale), scale)
			} else if date {
				kind = "DATE"
			} else if datetime {
				kind = "DATETIME(6)"
			}
		}
		columns[i] = mysqlsvc.MySQLDataImportColumn{Name: name, Type: kind, Nullable: true}
	}
	return columns
}

func mysqlDataImportCreationHistory(database, table string, columns []mysqlsvc.MySQLDataImportColumn) mysqlDataHistoryEntry {
	var fields, primary []string
	for _, col := range columns {
		field := mysqlDataHistoryIdentifier(col.Name) + " " + strings.ToUpper(strings.TrimSpace(col.Type))
		if col.Nullable {
			field += " NULL"
		} else {
			field += " NOT NULL"
		}
		fields = append(fields, field)
		if col.PrimaryKey {
			primary = append(primary, mysqlDataHistoryIdentifier(col.Name))
		}
	}
	if len(primary) > 0 {
		fields = append(fields, "PRIMARY KEY ("+strings.Join(primary, ",")+")")
	}
	return mysqlDataHistoryEntry{At: time.Now(), Kind: "CREATE", Summary: true, SQL: "CREATE TABLE " + mysqlDataHistoryTable(database, table) + " (" + strings.Join(fields, ",") + ") ENGINE=InnoDB"}
}

func mysqlDataInferDecimal(text string) (int, int, bool) {
	if len(text) > 256 || !mysqlDataImportNumber.MatchString(text) {
		return 0, 0, false
	}
	exponent := 0
	if e := strings.IndexAny(text, "eE"); e >= 0 {
		n, err := strconv.Atoi(text[e+1:])
		if err != nil || n < -1000 || n > 1000 {
			return 0, 0, false
		}
		exponent = n
		text = text[:e]
	}
	text = strings.TrimPrefix(strings.TrimPrefix(text, "-"), "+")
	fraction := 0
	if dot := strings.IndexByte(text, '.'); dot >= 0 {
		fraction = len(text) - dot - 1
		text = text[:dot] + text[dot+1:]
	}
	decimalScale := max(0, fraction-exponent)
	integerDigits := max(0, len(strings.TrimLeft(text, "0"))-fraction+exponent)
	return integerDigits, decimalScale, true
}
