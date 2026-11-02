package objectworkspace

import (
	"strconv"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/mysqlddl"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) designConstraintView(c *ui.Context, d *Document) {
	x := d.design
	clause := x.draft.Table.Clauses[x.row]
	if x.constraint == nil || x.constraint.row != x.row {
		if !s.applyDesignForms(d) {
			return
		}
		data, err := mysqlddl.ParseConstraint(clause.SQL)
		if err != nil {
			d.err = err.Error()
			return
		}
		x.constraint = &designConstraintForm{row: x.row, columnRow: -1, prefixRow: -1, data: data, base: data.Clone()}
	}
	f := x.constraint
	if f.data.Unsupported {
		ui.Text(c, locales.Text("此约束含未支持的属性，保留完整原文并禁止修改。"))
		raw := clause.SQL
		ui.TextArea(c, &raw).ReadOnly(true).Height(160)
		return
	}
	locked := x.busy || x.blocked
	ui.Scroll(c).Height(0).Grow(1).MinHeight(100).MaxHeight(300).Children(func() {
		ui.Column(c).Padding(8).Gap(8).Children(func() {
			ui.TextInput(c, &f.data.Name).Label(locales.Text("约束名称")).ReadOnly(locked || f.data.Kind == mysqlddl.Primary)
			if f.data.Kind == mysqlddl.Index {
				ui.Row(c).Wrap().Gap(8).Children(func() {
					ui.Select(c, &f.data.IndexKind, []string{"INDEX", "UNIQUE", "FULLTEXT", "SPATIAL"}).Label(locales.Text("索引类型")).Disabled(locked)
					ui.Select(c, &f.data.Method, []string{"", "BTREE", "HASH"}).Label(locales.Text("索引方法")).Disabled(locked)
					ui.Checkbox(c, &f.data.Visible, locales.Text("索引可见")).Disabled(locked)
				})
				ui.TextInput(c, &f.data.Comment).Label(locales.Text("索引注释")).ReadOnly(locked)
			}
			if f.data.Kind == mysqlddl.Check {
				ui.TextArea(c, &f.data.Expression).Label(locales.Text("检查表达式")).Height(100).ReadOnly(locked)
				ui.Checkbox(c, &f.data.Enforced, locales.Text("启用检查约束")).Disabled(locked)
			} else {
				s.designConstraintColumns(c, d, locked)
			}
			if f.data.Kind == mysqlddl.Foreign {
				ui.Row(c).Wrap().Gap(8).Children(func() {
					ui.TextInput(c, &f.data.ReferencedDatabase).Label(locales.Text("引用数据库")).ReadOnly(locked)
					ui.TextInput(c, &f.data.ReferencedTable).Label(locales.Text("引用表")).ReadOnly(locked)
				})
				ui.Row(c).Wrap().Gap(8).Children(func() {
					actions := []string{"", "RESTRICT", "CASCADE", "SET NULL", "NO ACTION"}
					ui.Select(c, &f.data.OnDelete, actions).Label(locales.Text("删除规则")).Disabled(locked)
					ui.Select(c, &f.data.OnUpdate, actions).Label(locales.Text("更新规则")).Disabled(locked)
				})
			}
			if x.formError != "" {
				ui.Text(c, x.formError).TextColor(theme.Colors(c).Error)
			}
		})
	})
	if ui.Button(c, locales.Text("应用约束修改")).Disabled(locked).Clicked() {
		s.applyDesignForms(d)
	}
}

func (s *State) designConstraintColumns(c *ui.Context, d *Document, locked bool) {
	x, f := d.design, d.design.constraint
	var fields []string
	for _, clause := range x.draft.Table.Clauses {
		if clause.Kind == mysqlddl.Column {
			fields = append(fields, clause.Name)
		}
	}
	if f.candidate == "" && len(fields) > 0 {
		f.candidate = fields[0]
	}
	ui.Row(c).Wrap().Gap(6).Children(func() {
		ui.Select(c, &f.candidate, fields).Label(locales.Text("约束字段")).Disabled(locked)
		if ui.Button(c, locales.Text("添加约束字段")).Disabled(locked || f.candidate == "").Clicked() {
			if err := f.applyPrefix(); err != nil {
				x.formError = err.Error()
			} else {
				f.data.Columns = append(f.data.Columns, mysqlddl.IndexColumn{Name: f.candidate})
				if f.data.Kind == mysqlddl.Foreign {
					f.data.ReferencedColumns = append(f.data.ReferencedColumns, "")
				}
				f.columnRow = len(f.data.Columns) - 1
			}
		}
		if ui.Button(c, locales.Text("移除约束字段")).Disabled(locked || f.columnRow < 0 || f.columnRow >= len(f.data.Columns)).Clicked() {
			i := f.columnRow
			f.data.Columns = append(f.data.Columns[:i], f.data.Columns[i+1:]...)
			if i < len(f.data.ReferencedColumns) {
				f.data.ReferencedColumns = append(f.data.ReferencedColumns[:i], f.data.ReferencedColumns[i+1:]...)
			}
			f.columnRow, f.prefixRow = -1, -1
		}
		for _, move := range []struct {
			label string
			delta int
		}{{"上移约束字段", -1}, {"下移约束字段", 1}} {
			i, j := f.columnRow, f.columnRow+move.delta
			if ui.Button(c, locales.Text(move.label)).Disabled(locked || i < 0 || i >= len(f.data.Columns) || j < 0 || j >= len(f.data.Columns)).Clicked() {
				if err := f.applyPrefix(); err != nil {
					x.formError = err.Error()
				} else {
					f.data.Columns[i], f.data.Columns[j] = f.data.Columns[j], f.data.Columns[i]
					if i < len(f.data.ReferencedColumns) && j < len(f.data.ReferencedColumns) {
						f.data.ReferencedColumns[i], f.data.ReferencedColumns[j] = f.data.ReferencedColumns[j], f.data.ReferencedColumns[i]
					}
					f.columnRow = j
					f.prefixRow = -1
				}
			}
		}
	})
	f.list.Selected = &f.columnRow
	theme.Table(c, &f.list, []ui.TableColumn{{Title: locales.Text("字段"), Width: 180}, {Title: locales.Text("索引前缀"), Width: 100}, {Title: locales.Text("顺序"), Width: 100}, {Title: locales.Text("引用字段"), Width: 180}}, len(f.data.Columns), func(row, col int) {
		v := f.data.Columns[row]
		text := v.Name
		switch col {
		case 1:
			if v.Prefix > 0 {
				text = strconv.Itoa(v.Prefix)
			} else {
				text = ""
			}
		case 2:
			text = "ASC"
			if v.Descending {
				text = "DESC"
			}
		case 3:
			text = ""
			if row < len(f.data.ReferencedColumns) {
				text = f.data.ReferencedColumns[row]
			}
		}
		ui.Text(c, text).SingleLine().Tooltip(text)
	}).Height(105)
	if f.prefixRow != f.columnRow {
		if err := f.applyPrefix(); err != nil {
			x.formError = err.Error()
			f.columnRow = f.prefixRow
			return
		}
		f.prefixRow, f.prefix = f.columnRow, ""
		if f.columnRow >= 0 && f.columnRow < len(f.data.Columns) && f.data.Columns[f.columnRow].Prefix > 0 {
			f.prefix = strconv.Itoa(f.data.Columns[f.columnRow].Prefix)
		}
	}
	if f.columnRow < 0 || f.columnRow >= len(f.data.Columns) {
		return
	}
	column := &f.data.Columns[f.columnRow]
	ui.Row(c).Wrap().Gap(8).Children(func() {
		ui.Select(c, &column.Name, fields).Label(locales.Text("所选约束字段")).Disabled(locked)
		if f.data.Kind == mysqlddl.Foreign {
			for len(f.data.ReferencedColumns) < len(f.data.Columns) {
				f.data.ReferencedColumns = append(f.data.ReferencedColumns, "")
			}
			ui.TextInput(c, &f.data.ReferencedColumns[f.columnRow]).Label(locales.Text("引用字段名称")).ReadOnly(locked)
		} else {
			ui.TextInput(c, &f.prefix).Label(locales.Text("索引前缀长度")).ReadOnly(locked)
			ui.Checkbox(c, &column.Descending, locales.Text("降序")).Disabled(locked)
		}
	})
}
