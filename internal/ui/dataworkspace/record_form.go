package dataworkspace

import (
	"fmt"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

// The form uses the same qualified page, full-cell reader and draft editor.
func (s *State) recordForm(c *ui.Context, t *Document) {
	p := theme.Colors(c)
	ui.Column(c).Fill().Gap(6).Children(func() {
		row := t.selectedRow
		ui.Row(c).Gap(5).Padding(3, 6).Children(func() {
			ui.Text(c, fmt.Sprintf("%s %d", locales.Text("当前记录"), t.offset+int64(row)+1)).Bold().Grow(1)
			for _, move := range []struct {
				label string
				delta int
			}{{"上一条记录", -1}, {"下一条记录", 1}} {
				if ui.Button(c, locales.Text(move.label)).Disabled(t.page == nil || t.loading || t.edit.Busy || row+move.delta < 0 || row+move.delta >= len(t.page.Rows)).Clicked() {
					next := row + move.delta
					s.finishMySQLCellEdit(t, true)
					if t.edit.Editor == nil {
						if !t.edit.Batch && mysqlDataHasDraft(t) {
							t.edit.AfterSave = func(ui.Services) { t.selectedRow = next; t.selection.Clear(); t.selection.Add(next) }
							s.saveMySQLData(c.Services(), t)
						} else {
							t.selectedRow = next
							t.selection.Clear()
							t.selection.Add(next)
						}
					}
				}
			}
		})
		if t.page == nil || row < 0 || row >= len(t.page.Rows) {
			ui.Text(c, locales.Text("选择一条记录")).Padding(10)
			return
		}
		ui.Scroll(c).Grow(1).MinHeight(0).Children(func() {
			ui.Column(c).Padding(8).Gap(8).Children(func() {
				for source, name := range t.page.Columns {
					if source >= len(t.page.Rows[row]) {
						continue
					}
					source, name := source, name
					cell := t.page.Rows[row][source]
					ui.Column(c).Key(name).Gap(4).Border(1, p.Border).Padding(7).Children(func() {
						ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
							ui.Text(c, name).Bold().Grow(1).SingleLine()
							if source < len(t.page.ColumnKinds) {
								ui.Text(c, t.page.ColumnKinds[source]).TextColor(p.Muted).SingleLine()
							}
							if ui.Button(c, locales.Text("编辑字段")).Label(locales.Text("编辑记录字段") + " " + name).Disabled(!mysqlDataCanEdit(t, row, source)).Clicked() {
								s.beginMySQLCellEdit(c.Services(), t, row, source, false)
							}
							ui.Button(c, "▾").Label(locales.Text("记录字段操作") + " " + name).Menu(func(m *ui.Menu) {
								if m.Item("NULL").Disabled(!mysqlDataCanEdit(t, row, source) || !t.page.ColumnMetadata[source].Nullable).Chosen() {
									s.setMySQLDataCell(c.Services(), t, row, source, contract.DataValue{Kind: contract.Null, Null: true})
								}
								if m.Item(locales.Text("复制")).Chosen() {
									c.WriteClipboard(cell.Text)
								}
							})
						})
						if e := t.edit.Editor; e != nil && e.Row == row && e.Field == name {
							s.typedEditorChoice(c, t, e)
							input := ui.TextArea(c, &e.Text).Label(locales.Text("记录字段编辑") + " " + name).Height(90).ReadOnly(t.edit.Busy)
							theme.BindNativeEdit(c, input, fmt.Sprintf("data/form/%d/%s", t.id, name), s.deps.EditFocus, s.deps.EditTarget, func() bool { return !t.closed && t.recordFormMode && t.edit.Editor == e }, !t.edit.Busy, false)
							ui.Row(c).Gap(6).Children(func() {
								if ui.Button(c, locales.Text("应用字段")).Clicked() {
									s.finishMySQLCellEdit(t, true)
								}
								if ui.Button(c, locales.Text("取消")).Clicked() {
									s.finishMySQLCellEdit(t, false)
								}
							})
						} else {
							text := cell.Text
							if cell.Null {
								text = "NULL"
							}
							ui.Text(c, text).MaxHeight(70).Tooltip(text)
							if cell.Truncated {
								ui.Text(c, locales.Text("此处是字段预览；编辑时读取完整字段")).TextColor(p.Muted)
							}
						}
					})
				}
			})
		})
		if t.edit.Err != "" {
			ui.Text(c, t.edit.Err).TextColor(p.Error)
		}
	})
}
