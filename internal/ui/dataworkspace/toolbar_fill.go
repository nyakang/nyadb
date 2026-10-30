package dataworkspace

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

type mysqlDataFillState struct {
	Open                         bool
	Mode, Text, Start, Step, Err string
}

func (s *State) dataFillMenu(c *ui.Context, t *Document, m *ui.Menu) {
	m.Item(locales.Text("AI 服务尚未配置")).Disabled(true)
	m.Separator()
	if m.Item(locales.Text("填充所选单元格…")).Disabled(t.page == nil || t.selectedRow < 0 || t.loading || t.edit.Busy).Chosen() {
		t.fill = mysqlDataFillState{Open: true, Mode: "值", Start: "1", Step: "1"}
	}
}

// Filling is staged in the existing row drafts and is never an implicit write.
// Validate all targets and all values before changing any preview.
func (s *State) fillSelectedData(t *Document) error {
	if t == nil || t.closed || t.page == nil || t.loading || t.edit.Busy || t.edit.Uncertain != nil || dataTransactionUnavailable(t) {
		return fmt.Errorf("%s", locales.Text("当前无法填充单元格"))
	}
	if t.edit.Editor != nil {
		s.finishMySQLCellEdit(t, true)
		if t.edit.Editor != nil {
			return fmt.Errorf("%s", t.edit.Err)
		}
	}
	// The selection boundaries describe the visual column order, including
	// user rearrangement and hidden columns, just as clipboard paste does.
	columns := mysqlDataPasteColumns(t)
	if !t.grid.Cells.All {
		a := slices.Index(columns, slices.Index(t.page.Columns, t.grid.Cells.Anchor))
		b := slices.Index(columns, slices.Index(t.page.Columns, t.grid.Cells.Cursor))
		if a < 0 || b < 0 {
			return fmt.Errorf("%s", locales.Text("选择一个单元格"))
		}
		if a > b {
			a, b = b, a
		}
		columns = columns[a : b+1]
	}
	if len(columns) == 0 {
		return fmt.Errorf("%s", locales.Text("选择一个单元格"))
	}
	rows := []int{}
	for row := range t.page.Rows {
		if t.selection.Has(row) || t.selection.Len() == 0 && row == t.selectedRow {
			rows = append(rows, row)
		}
	}
	if len(rows) == 0 {
		return fmt.Errorf("%s", locales.Text("选择一个单元格"))
	}
	if len(rows)*len(columns) > 200 {
		return fmt.Errorf("%s", locales.Text("每次最多填充 200 个单元格"))
	}
	start, step := int64(0), int64(0)
	if t.fill.Mode == "序列" {
		var err error
		start, err = strconv.ParseInt(strings.TrimSpace(t.fill.Start), 10, 64)
		if err != nil {
			return fmt.Errorf("%s", locales.Text("序列起点或步长无效"))
		}
		step, err = strconv.ParseInt(strings.TrimSpace(t.fill.Step), 10, 64)
		if err != nil {
			return fmt.Errorf("%s", locales.Text("序列起点或步长无效"))
		}
	}
	if len(t.fill.Text) > mysqlsvc.MySQLDataEditMaxBytes {
		return fmt.Errorf("%s", locales.Text("编辑内容超过 1 MiB 限制"))
	}
	type target struct {
		row, source int
		value       mysqlsvc.MySQLDataValue
	}
	targets := []target{}
	value := start
	for _, row := range rows {
		for _, source := range columns {
			if !mysqlDataCanEdit(t, row, source) || mysqlDataSpatialField(t.page.ColumnMetadata[source]) ||
				mysqlDataBinaryField(t.page.ColumnMetadata[source]) && t.fill.Mode != "NULL" {
				return fmt.Errorf("%s", locales.Text("选区包含不可编辑单元格"))
			}
			v := mysqlsvc.MySQLDataValue{Text: t.fill.Text}
			if t.fill.Mode == "NULL" {
				if !t.page.ColumnMetadata[source].Nullable {
					return fmt.Errorf("%s", locales.Text("选区包含不允许 NULL 的字段"))
				}
				v = mysqlsvc.MySQLDataValue{Null: true}
			}
			if t.fill.Mode == "序列" {
				v.Text = strconv.FormatInt(value, 10)
				next := value + step
				if (step > 0 && next < value) || (step < 0 && next > value) {
					return fmt.Errorf("%s", locales.Text("序列超出整数范围"))
				}
				value = next
			}
			targets = append(targets, target{row, source, v})
		}
	}
	t.edit.Batch = true
	for _, target := range targets {
		draft := s.mysqlRowDraft(t, target.row)
		stageMySQLDataValue(t, draft, target.source, target.value)
	}
	t.fill.Open = false
	s.setStatus(locales.Text("填充已暂存，请应用更改保存到数据库"))
	return nil
}

func (s *State) dataFillModal(c *ui.Context, t *Document) {
	if !t.fill.Open {
		return
	}
	ui.Modal(c, &t.fill.Open, func() {
		ui.Column(c).Width(440).MaxWidthPercent(95).Padding(16).Gap(10).Children(func() {
			ui.Text(c, locales.Text("填充所选单元格")).Bold()
			mysqlDataRuleSelect(c, &t.fill.Mode, []string{"值", "序列", "NULL"}, locales.Text).Label(locales.Text("填充方式")).FillWidth()
			if t.fill.Mode == "值" {
				ui.TextArea(c, &t.fill.Text).Label(locales.Text("填充值")).Height(100).FillWidth()
			}
			if t.fill.Mode == "序列" {
				ui.TextInput(c, &t.fill.Start).Label(locales.Text("序列起点")).FillWidth()
				ui.TextInput(c, &t.fill.Step).Label(locales.Text("序列步长")).FillWidth()
			}
			if t.fill.Err != "" {
				ui.Text(c, t.fill.Err).TextColor(theme.Colors(c).Error)
			}
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c, locales.Text("取消")).Clicked() {
					t.fill.Open = false
				}
				if ui.PrimaryButton(c, locales.Text("填充选区")).Clicked() {
					if err := s.fillSelectedData(t); err != nil {
						t.fill.Err = err.Error()
					}
				}
			})
		})
	})
}
