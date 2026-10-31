package dataworkspace

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
	"strings"
)

func (s *EngineState) View(c *ui.Context, d *EngineDocument) {
	if d == nil || d.closed.Load() {
		return
	}
	if d.Page != nil {
		s.restoreEngineLayout(d)
		defer s.trackEngineLayout(c, d)
	}
	ui.Column(c).Fill().Gap(6).Children(func() {
		ui.Row(c).FillWidth().Gap(6).Children(func() {
			ui.Text(c, d.Object.Catalog+" / "+d.Object.Schema+" / "+d.Object.Name).Bold()
			if ui.Button(c, locales.Text("数据 / 结构")).Clicked() {
				d.StructureView = !d.StructureView
			}
			if ui.Button(c, locales.Text("刷新")).Disabled(d.Busy || d.HasDraft()).Clicked() {
				s.Load(d)
			}
			if ui.Button(c, locales.Text("取消")).Disabled(!d.Busy || d.CancelRequested).Clicked() {
				s.Cancel(d)
			}
			if ui.Button(c, locales.Text("保存修改")).Disabled(d.Busy || d.Uncertain || d.writeBlocked || !d.HasDraft() || d.Page == nil || !d.Page.Table.Editable).Clicked() {
				s.Save(d)
			}
			if ui.Button(c, locales.Text("放弃修改")).Disabled(d.Busy || d.Uncertain).Clicked() {
				d.drafts = make(map[int]*contract.Edit)
				d.insert = make(map[string]contract.Cell)
			}
			if ui.Button(c, locales.Text("开始事务")).Disabled(d.Busy || d.HasTransaction() || d.HasDraft()).Clicked() {
				s.Begin(d)
			}
			if ui.Button(c, locales.Text("提交")).Disabled(d.Busy || !d.HasTransaction() || d.HasDraft()).Clicked() {
				s.Commit(d)
			}
			if ui.Button(c, locales.Text("回滚")).Disabled(d.Busy || !d.HasTransaction()).Clicked() {
				s.Rollback(d)
			}
		})
		if d.Busy {
			ui.Text(c, locales.Text("正在等待数据库执行器回执…"))
		}
		if d.Error != "" {
			ui.Text(c, d.Error).TextColor(theme.Colors(c).Error)
		}
		if d.writeBlocked && !d.Uncertain {
			ui.Text(c, "写入已执行但核验尚未完成；保留修改，禁止重放")
			if ui.Button(c, "重新核验写入").Disabled(d.Busy).Clicked() {
				s.Verify(d)
			}
		}
		if d.Uncertain {
			if ui.Button(c, locales.Text("显式连接后核验提交")).Disabled(d.Busy).Clicked() {
				s.Verify(d)
			}
			ui.Text(c, locales.Text("提交结果不确定：保留修改，请在重新连接后核验，禁止重放")).TextColor(theme.Colors(c).Warning)
		}
		if d.Page == nil {
			return
		}
		if len(d.Page.Truncation) > 0 {
			ui.Text(c, locales.Text("数据页截断：")+strings.Join(d.Page.Truncation, ", ")).TextColor(theme.Colors(c).Warning)
		}
		if d.StructureView {
			s.structureView(c, d)
			return
		}
		if !d.Page.Table.Editable {
			ui.Text(c, locales.Text("只读：视图或没有完整非空唯一键"))
		}
		ui.Row(c).Gap(6).FillWidth().Children(func() {
			ui.Text(c, locales.Text("筛选字段"))
			ui.TextInput(c, &d.filterColumn).Width(120)
			ui.Text(c, locales.Text("运算符"))
			ui.Select(c, &d.filterOp, []string{"=", "!=", "<", ">", "<=", ">=", "LIKE", "IS NULL", "IS NOT NULL"}).Width(100)
			ui.TextInput(c, &d.filterValue).Width(180)
			if ui.Button(c, locales.Text("应用筛选")).Disabled(d.Busy || d.HasDraft()).Clicked() {
				d.Filters = []contract.Filter{{Column: d.filterColumn, Operator: d.filterOp, Value: contract.Cell{Kind: contract.Text, Text: d.filterValue}}}
				d.Offset = 0
				s.Load(d)
			}
			if ui.Button(c, locales.Text("清除筛选")).Disabled(d.Busy || d.HasDraft()).Clicked() {
				d.Filters = nil
				d.Offset = 0
				s.Load(d)
			}
		})
		columns := make([]ui.TableColumn, len(d.Page.Table.Columns))
		for i, f := range d.Page.Table.Columns {
			columns[i] = ui.TableColumn{ID: f.Name, Title: f.Name + " (" + f.DatabaseType + ")", Width: 150}
		}
		d.grid.Selection = &d.selection
		d.grid.CellSelect = true
		d.grid.DragSelect = true
		d.grid.HeaderSelect = true
		d.grid.OnCellClick = func(row, col int, mods ui.Modifiers) {
			if row < 0 || row >= len(d.Page.Rows) || col < 0 || col >= len(columns) {
				return
			}
			d.selectedRow, d.selectedCol = row, col
			v := d.Page.Rows[row][col]
			d.text = v.Text
			d.kind = engineKindLabel(v.Kind)
			if v.Kind == contract.Binary {
				d.text = hex.EncodeToString(v.Bytes)
			}
			d.field = columns[col].ID
		}
		d.grid.HeaderMenu = func(m *ui.Menu, col int) {
			if col < 0 || col >= len(columns) {
				return
			}
			name := columns[col].ID
			if m.Item("升序").Chosen() {
				if !d.Busy && !d.HasDraft() {
					d.Sorts = []contract.Sort{{Column: name}}
					s.Load(d)
				}
			}
			if m.Item("降序").Chosen() {
				if !d.Busy && !d.HasDraft() {
					d.Sorts = []contract.Sort{{Column: name, Descending: true}}
					s.Load(d)
				}
			}
		}
		theme.TableWithFont(c, &d.grid, columns, len(d.Page.Rows), theme.Font(c, theme.FontSize), func(row, col int) {
			v := d.Page.Rows[row][col]
			if e := d.drafts[row]; e != nil {
				if changed, ok := e.Values[columns[col].ID]; ok {
					v = changed
				}
			}
			ui.Text(c, v.Display()).SingleLine()
		}).Grow(1).MinHeight(0).FillWidth()
		ui.Row(c).FillWidth().Gap(6).Children(func() {
			if ui.Button(c, locales.Text("上一页")).Disabled(d.Busy || d.HasDraft() || d.Offset == 0).Clicked() {
				d.Offset = max(0, d.Offset-100)
				s.Load(d)
			}
			ui.Text(c, fmt.Sprintf("%d – %d", d.Offset+1, d.Offset+int64(len(d.Page.Rows))))
			if ui.Button(c, locales.Text("下一页")).Disabled(d.Busy || d.HasDraft() || !d.Page.More).Clicked() {
				d.Offset += 100
				s.Load(d)
			}
			if ui.Button(c, locales.Text("复制选区")).Clicked() {
				c.WriteClipboard(s.Copy(d))
			}
			if ui.Button(c, locales.Text("导出完整数据")).Disabled(d.Busy || d.HasDraft() || d.HasTransaction()).Clicked() {
				s.Export(d)
			}
		})
		if d.Page.Table.Editable {
			ui.Row(c).Gap(6).FillWidth().Children(func() {
				ui.Text(c, locales.Text("字段"))
				ui.TextInput(c, &d.field).Width(120)
				ui.Text(c, locales.Text("类型"))
				s.engineKindSelector(c, d)
				ui.TextInput(c, &d.text).Grow(1)
				if ui.Button(c, locales.Text("修改单元格")).Disabled(d.Busy || d.Uncertain || d.selectedRow < 0).Clicked() {
					v, e := engineInput(d.kind, d.text)
					if e != nil {
						d.Error = e.Error()
					} else {
						s.EditCell(d, d.selectedRow, d.field, v)
					}
				}
				if ui.Button(c, locales.Text("新增行字段")).Disabled(d.Busy || d.Uncertain).Clicked() {
					v, e := engineInput(d.kind, d.text)
					if e != nil {
						d.Error = e.Error()
					} else {
						d.insert[d.field] = v
					}
				}
				if ui.Button(c, locales.Text("删除行")).Disabled(d.Busy || d.Uncertain || d.selectedRow < 0).Clicked() {
					s.DeleteRow(d, d.selectedRow)
				}
			})
			ui.Text(c, locales.Text("NULL 与文本 NULL 分别保存；二进制请输入十六进制"))
			if len(d.insert) > 0 {
				v, _ := json.Marshal(d.insert)
				ui.Text(c, locales.Text("待新增：")+string(v)).SingleLine()
			}
		}
	})
}
func engineInput(kind, text string) (contract.Cell, error) {
	v := contract.Cell{Kind: engineKind(kind), Text: text}
	switch v.Kind {
	case contract.Null:
		v.Text = ""
	case contract.Binary:
		b, e := hex.DecodeString(text)
		if e != nil {
			return v, e
		}
		v.Bytes, v.Text = b, ""
	case contract.Text, contract.Integer, contract.Decimal, contract.Floating, contract.Boolean, contract.JSON, contract.DateTime:
	default:
		return v, fmt.Errorf("unknown cell type")
	}
	return v, nil
}
func (s *EngineState) EditCell(d *EngineDocument, row int, field string, v contract.Cell) {
	if d.Page == nil || !d.Page.Table.Editable || row < 0 || row >= len(d.Page.Rows) {
		return
	}
	var index = -1
	for i, f := range d.Page.Table.Columns {
		if f.Name == field && !f.Generated {
			index = i
		}
	}
	if len(v.Text) > 1<<20 || len(v.Bytes) > 1<<20 {
		d.Error = "完整编辑限于 1 MiB"
		return
	}
	if index < 0 || d.Page.Rows[row][index].Truncated {
		d.Error = "生成列、未知字段或截断值不能修改"
		return
	}
	e := d.drafts[row]
	if e == nil {
		e = &contract.Edit{Kind: "update", Identity: d.Page.Identities[row], Original: d.Page.Rows[row], Values: make(map[string]contract.Cell)}
		copy := cloneEdit(*e)
		e = &copy
		d.drafts[row] = e
	}
	e.Values[field] = v.Clone()
}
func (s *EngineState) DeleteRow(d *EngineDocument, row int) {
	if d.Page == nil || !d.Page.Table.Editable || row < 0 || row >= len(d.Page.Rows) {
		return
	}
	e := cloneEdit(contract.Edit{Kind: "delete", Identity: d.Page.Identities[row], Original: d.Page.Rows[row]})
	d.drafts[row] = &e
}
func (s *EngineState) Copy(d *EngineDocument) string {
	if d.Page == nil {
		return ""
	}
	var fields []int
	anchor, cursor := -1, -1
	for i, f := range d.Page.Table.Columns {
		if f.Name == d.grid.Cells.Anchor {
			anchor = i
		}
		if f.Name == d.grid.Cells.Cursor {
			cursor = i
		}
	}
	for i, f := range d.Page.Table.Columns {
		take := d.grid.Cells.All || d.grid.Cells.Cursor == ""
		if d.grid.ColumnSelection != nil {
			take = false
			for _, n := range d.grid.ColumnSelection {
				if n == f.Name {
					take = true
				}
			}
		} else if !take {
			take = i >= min(anchor, cursor) && i <= max(anchor, cursor)
		}
		if take {
			fields = append(fields, i)
		}
	}
	var lines []string
	for i, row := range d.Page.Rows {
		if !d.selection.Has(i) {
			continue
		}
		var values []string
		for _, j := range fields {
			v := row[j]
			if edit := d.drafts[i]; edit != nil {
				if changed, ok := edit.Values[d.Page.Table.Columns[j].Name]; ok {
					v = changed
				}
			}
			values = append(values, strings.ReplaceAll(strings.ReplaceAll(v.Display(), "\t", "\\t"), "\n", "\\n"))
		}
		lines = append(lines, strings.Join(values, "\t"))
	}
	return strings.Join(lines, "\n")
}

func (s *EngineState) structureView(c *ui.Context, d *EngineDocument) {
	ui.Text(c, locales.Text("公开结构元数据（只读）")).Bold()
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).Gap(5).Children(func() {
			ui.Text(c, locales.Text("可靠唯一键：")+strings.Join(d.Page.Table.Key, ", "))
			for _, f := range d.Page.Table.Columns {
				label := f.Name + " · " + f.DatabaseType
				if f.Nullable {
					label += " · NULL"
				}
				if f.Generated {
					label += " · generated"
				}
				if f.Default != nil {
					label += " · default " + *f.Default
				}
				ui.Text(c, label)
			}
			for _, def := range d.Page.Table.Definitions {
				ui.Text(c, def.Kind+" · "+def.Name).Bold()
				text := def.SQL
				ui.TextArea(c, &text).ReadOnly(true).Height(120)
			}
			if d.Page.Table.Definition != "" {
				text := d.Page.Table.Definition
				ui.TextArea(c, &text).ReadOnly(true).Height(300)
			}
		})
	})
}

func engineKindLabel(kind contract.CellKind) string {
	switch kind {
	case contract.Null:
		return "NULL"
	case contract.Integer:
		return "整数"
	case contract.Decimal:
		return "精确小数"
	case contract.Floating:
		return "浮点"
	case contract.Boolean:
		return "布尔"
	case contract.Binary:
		return "二进制"
	case contract.JSON:
		return "JSON"
	case contract.DateTime:
		return "日期时间"
	default:
		return "文本"
	}
}
func engineKind(label string) contract.CellKind {
	switch label {
	case "NULL":
		return contract.Null
	case "文本":
		return contract.Text
	case "整数":
		return contract.Integer
	case "精确小数":
		return contract.Decimal
	case "浮点":
		return contract.Floating
	case "布尔":
		return contract.Boolean
	case "二进制":
		return contract.Binary
	case "JSON":
		return contract.JSON
	case "日期时间":
		return contract.DateTime
	default:
		return contract.CellKind(label)
	}
}

func (s *EngineState) engineKindSelector(c *ui.Context, d *EngineDocument) {
	kinds := []contract.CellKind{contract.Null, contract.Text, contract.Integer, contract.Decimal, contract.Floating, contract.Boolean, contract.Binary, contract.JSON, contract.DateTime}
	labels := make([]string, len(kinds))
	for i, k := range kinds {
		labels[i] = locales.Text(engineKindLabel(k))
	}
	choice := locales.Text(engineKindLabel(engineKind(d.kind)))
	if ui.Select(c, &choice, labels).Width(110).Changed() {
		for i, label := range labels {
			if label == choice {
				d.kind = engineKindLabel(kinds[i])
				break
			}
		}
	}
}
