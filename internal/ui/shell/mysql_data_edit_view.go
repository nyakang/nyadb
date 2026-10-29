package shell

import (
	"math"
	"slices"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

// Pointer observers leave modified clicks and right-clicks to the native grid.
// A primary click is held until release so a drag remains a rectangular choice.
func (s *State) mysqlDataCellPointer(services ui.Services, t *tab, row, source int, bounds ui.Rect, ev ui.InputEvent) bool {
	if ev.Button != 0 && (ev.Kind == ui.InputPointerDown || ev.Kind == ui.InputPointerUp) {
		return false
	}
	switch ev.Kind {
	case ui.InputPointerDown:
		if t.dataEdit.Busy {
			return false
		}
		t.dataEdit.PointerActive = true
		oldRow := t.dataEdit.SelectionAnchor
		if t.dataSelectedRow < 0 {
			oldRow = -1
		}
		oldField := t.dataState.Cells.Anchor
		t.dataEdit.PointerRow, t.dataEdit.PointerSource = row, source
		t.dataEdit.PointerMods = ev.Mods
		t.dataEdit.PointerX, t.dataEdit.PointerY = bounds.X+ev.X, bounds.Y+ev.Y
		t.dataEdit.Drag = false
		s.finishMySQLCellEdit(t, true)
		field := t.dataPage.Columns[source]
		switch {
		case ev.Mods&ui.Shift != 0 && oldRow >= 0:
			t.dataSelection.Clear()
			for i := min(oldRow, row); i <= max(oldRow, row); i++ {
				t.dataSelection.Add(i)
			}
			t.dataState.Cells = ui.ColumnRange{Anchor: oldField, Cursor: field}
		case ev.Mods&ui.Cmd != 0:
			if t.dataSelection.Has(row) {
				t.dataSelection.Remove(row)
			} else {
				t.dataSelection.Add(row)
			}
			if t.dataState.Cells.Anchor == "" {
				t.dataState.Cells = ui.ColumnRange{Anchor: field, Cursor: field}
			}
		default:
			t.dataEdit.SelectionAnchor = row
			t.dataSelection.Clear()
			t.dataSelection.Add(row)
			t.dataState.Cells = ui.ColumnRange{Anchor: field, Cursor: field}
		}
		t.dataSelectedRow = row

		return true
	case ui.InputPointerMove:
		if !t.dataEdit.PointerActive {
			return false
		}
		x, y := bounds.X+ev.X, bounds.Y+ev.Y
		if math.Abs(float64(x-t.dataEdit.PointerX))+math.Abs(float64(y-t.dataEdit.PointerY)) > 4 {
			t.dataEdit.Drag = true
		}
		if !t.dataEdit.Drag {
			return true
		}
		for _, hit := range t.dataEdit.Hits {
			b := hit.Bounds
			if x >= b.X && x < b.X+b.W && y >= b.Y && y < b.Y+b.H {
				t.dataSelection.Clear()
				for i := min(t.dataEdit.PointerRow, hit.Row); i <= max(t.dataEdit.PointerRow, hit.Row); i++ {
					t.dataSelection.Add(i)
				}
				t.dataSelectedRow = hit.Row
				t.dataState.Cells.Cursor = t.dataPage.Columns[hit.Source]
				break
			}
		}
		return true
	case ui.InputPointerUp:
		t.dataEdit.PointerActive = false
		if !t.dataEdit.Drag && t.dataEdit.PointerMods == 0 {
			s.beginMySQLCellEdit(services, t, row, source, false)
		}
		return true
	}
	return false
}
func mysqlDataEditFont(t *tab) float32 {
	zoom := t.dataEdit.Zoom
	if zoom == 0 {
		zoom = 100
	}
	return 12 * float32(zoom) / 100
}
func (s *State) mysqlDataInlineEditor(c *ui.Context, t *tab, row, source int) bool {
	e := t.dataEdit.Editor
	if e == nil || e.Dedicated || e.Row != row || e.Field != t.dataPage.Columns[source] || e.Key != mysqlDataRowKey(t, row) {
		return false
	}
	services := c.Services()
	input := ui.TextInputBase(c, &e.Text).Label(locales.Text("单元格编辑")+" "+e.Field).FillWidth().MinWidth(0).
		FontSize(theme.Font(c, mysqlDataEditFont(t))).Padding(0, 2).Background(theme.Colors(c).App).Border(1, theme.Colors(c).Blue).AutoFocus().ReadOnly(t.dataEdit.Busy)
	input.OnTextKey(func(k ui.TextKey) bool {
		if e.Composing && (k.Key == ui.KeyEnter || k.Key == ui.KeyTab || k.Key == ui.KeyEscape) {
			return true
		}
		switch {
		case k.Key == ui.KeyEscape && k.Mods == 0:
			e.Text = e.Initial
			s.finishMySQLCellEdit(t, false)
			t.dataEdit.FocusGrid = true
			return true
		case k.Key == ui.KeyEnter && k.Mods == 0:
			e.Text = k.Text()
			s.finishMySQLCellEdit(t, true)
			t.dataEdit.FocusGrid = t.dataEdit.Editor == nil
			return true
		case k.Key == ui.KeyTab && (k.Mods == 0 || k.Mods == ui.Shift):
			e.Text = k.Text()
			s.finishMySQLCellEdit(t, true)
			if t.dataEdit.Editor != nil {
				return true
			}
			s.navigateMySQLCellEdit(services, t, row, e.Field, k.Mods == ui.Shift)
			return true
		}
		return false
	})
	if !e.Selected {
		input.SetTextSelection(0, utf8.RuneCountInString(e.Text))
		e.Selected = true
	}
	e.Composing = input.Composing()
	if input.Focused() {
		e.Focused = true
	} else if e.Focused && !e.Composing {
		s.finishMySQLCellEdit(t, true)
	}
	return true
}
func (s *State) navigateMySQLCellEdit(services ui.Services, t *tab, row int, field string, back bool) {
	visible := []string{}
	order := slices.Clone(t.dataState.Columns.Order)
	for _, name := range t.dataPage.Columns {
		if !slices.Contains(order, name) {
			order = append(order, name)
		}
	}
	for _, name := range order {
		source := slices.Index(t.dataPage.Columns, name)
		if source >= 0 && !t.dataHiddenColumns[name] && !mysqlsvc.MySQLDataColumnGenerated(t.dataPage.ColumnMetadata[source]) {
			visible = append(visible, name)
		}
	}
	if len(visible) == 0 {
		return
	}
	at := slices.Index(visible, field)
	if back {
		at--
	} else {
		at++
	}
	if at >= len(visible) {
		row++
		at = 0
	}
	if at < 0 {
		row--
		at = len(visible) - 1
	}
	if row < 0 || row >= len(t.dataPage.Rows) {
		return
	}
	s.beginMySQLCellEdit(services, t, row, slices.Index(t.dataPage.Columns, visible[at]), false)
}
func (s *State) mysqlDataEditModals(c *ui.Context) {
	for _, t := range s.tabs {
		if t.kind != tabMySQLData {
			continue
		}
		if e := t.dataEdit.Editor; e != nil && e.Dedicated {
			ui.Modal(c, &e.Open, func() {
				ui.Column(c).Width(640).MaxWidthPercent(95).Padding(12).Gap(8).Children(func() {
					ui.Text(c, locales.Text("单元格编辑器")+" · "+e.Field).FontSize(theme.Font(c, theme.FontSize))
					if e.Original.Binary {
						ui.Text(c, locales.Text("二进制值以十六进制编辑"))
					}
					ui.TextArea(c, &e.Text).Label(locales.Text("专用单元格编辑") + " " + e.Field).FillWidth().Height(280).AutoFocus().FontSize(theme.Font(c, 12))
					if t.dataEdit.Err != "" {
						ui.Text(c, t.dataEdit.Err).TextColor(theme.Colors(c).Error)
					}
					ui.Row(c).Gap(8).Children(func() {
						if ui.Button(c, locales.Text("取消单元格编辑")).Clicked() {
							s.finishMySQLCellEdit(t, false)
						}
						if ui.Button(c, locales.Text("应用单元格编辑")).Clicked() {
							s.finishMySQLCellEdit(t, true)
						}
					})
				})
			})
			if !e.Open && t.dataEdit.Editor == e {
				s.finishMySQLCellEdit(t, false)
			}
		}
		if t.dataEdit.LeaveOpen {
			choice := ui.AlertDialog(c, &t.dataEdit.LeaveOpen, locales.Text("待处理记录"), locales.Text("是否将待保存的数据更改应用到数据库？"), locales.Text("应用更改"), locales.Text("放弃"), locales.Text("取消"))
			next := t.dataEdit.LeaveNext
			if choice == 1 {
				t.dataEdit.LeaveNext = nil
				s.discardMySQLData(t)
				if next != nil {
					next(c.Services())
				}
			}
			if choice == 0 {
				t.dataEdit.LeaveNext = nil
				t.dataEdit.AfterSave = next
				s.activateMySQLProfile(t.dataProfileID)
				for i, candidate := range s.tabs {
					if candidate == t {
						s.activeTab = i
						break
					}
				}
				s.saveMySQLData(c.Services(), t)
			}
		}
	}
}
func (s *State) mysqlDataEditStatus(c *ui.Context, t *tab) {
	if t.dataEdit.Err != "" {
		ui.Row(c).Padding(2, 6).Gap(6).FillWidth().Children(func() {
			ui.Text(c, t.dataEdit.Err).FontSize(theme.Font(c, 12)).TextColor(theme.Colors(c).Error).SingleLine().Grow(1).MinWidth(0).Tooltip(t.dataEdit.Err)
			if t.dataEdit.Uncertain != nil && ui.Button(c, locales.Text("核验提交结果")).Disabled(t.dataEdit.Busy).Clicked() {
				s.verifyMySQLDataSave(c.Services(), t)
			}
		})
	}
	if t.dataEdit.Busy {
		ui.Row(c).Padding(2, 6).Gap(6).Children(func() { ui.Spinner(c).Size(14, 14); ui.Text(c, locales.Text("正在处理表数据…")) })
	}
}

// The native table padding also belongs to its cell. This fallback observes
// those gaps while header, row-number and modified selection remain available.
func (s *State) mysqlDataGridPointer(services ui.Services, t *tab, bounds ui.Rect, ev ui.InputEvent) bool {
	x, y := bounds.X+ev.X, bounds.Y+ev.Y
	for _, hit := range t.dataEdit.Hits {
		b := hit.Bounds
		in := x >= b.X-5 && x < b.X+b.W+5 && y >= b.Y-5 && y < b.Y+b.H+5
		if ev.Kind == ui.InputPointerDown && in || t.dataEdit.PointerActive && hit.Row == t.dataEdit.PointerRow && hit.Source == t.dataEdit.PointerSource {
			ev.X, ev.Y = x-b.X, y-b.Y
			return s.mysqlDataCellPointer(services, t, hit.Row, hit.Source, b, ev)
		}
	}
	return false
}
