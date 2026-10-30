package dataworkspace

import (
	"fmt"
	"math/big"
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

func mysqlDataImportDestination(t *Document) string {
	if t.transfer.Target == "new" {
		return t.transfer.NewName
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
			return locales.Text("当前表")
		}).Width(180).Disabled(tr.Busy || tr.Submitted)
	})
	if tr.Target == "new" {
		ui.TextInput(c, &tr.NewName).Label(locales.Text("新目标表名称")).Disabled(tr.Busy || tr.Submitted).Grow(1)
		ui.Text(c, locales.Text("新表将在当前数据库创建；可修改字段名、类型、可空和主键。建表成功后若数据失败，目标表保留。")).FontSize(theme.Font(c, 11)).TextColor(theme.Colors(c).Muted)
		if s.newTableImporter() == nil {
			ui.Text(c, locales.Text("新表导入服务不可用")).TextColor(theme.Colors(c).Error)
		}
	}
}

func mysqlDataImportMapping(t *Document) ([]string, []mysqlsvc.MySQLDataImportColumn, []mysqlsvc.MySQLStructureColumn, error) {
	tr := &t.transfer
	if tr.Target != "new" {
		return tr.Mapping, nil, t.page.ColumnMetadata, nil
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
	edits, err := mysqlDataImportEditsWithOptions(t.transfer.Input, mapping, metadata, t.transfer.EmptyAsNull)
	if err == nil && t.transfer.Target == "new" {
		err = mysqlsvc.ValidateMySQLDataNewTableImport(columns, edits)
	}
	return columns, edits, err
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
				for _, col := range t.page.ColumnMetadata {
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
	_, edits, err := mysqlDataImportRequest(t)
	if err != nil {
		ui.Text(c, err.Error()).TextColor(colors.Error)
	} else {
		ui.Text(c, fmt.Sprintf(locales.Text("已校验 %d 行，预览前 %d 行"), len(edits), min(5, len(edits))))
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
	ui.TextInput(c, &tr.Confirmation).Label(locales.Text("输入表名称以确认导入")).Placeholder(mysqlDataImportDestination(t)).Disabled(tr.Busy || tr.Submitted).Grow(1)
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
