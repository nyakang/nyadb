package shell

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/locales"
)

// The selection is bounded by the loaded page. Hidden fields and the row
// selector never reach the clipboard; reordered fields follow the grid.
func mysqlDataSelectedPreview(t *tab) (names []string, rows [][]string, nulls [][]bool) {
	if t == nil || t.dataPage == nil {
		return
	}
	page := t.dataPage
	sources := make(map[string]int, len(page.Columns))
	for col, name := range page.Columns {
		sources[name] = col
	}
	order := append([]string(nil), t.dataState.Columns.Order...)
	for _, name := range page.Columns {
		if !slices.Contains(order, name) {
			order = append(order, name)
		}
	}
	visible := order[:0]
	for _, name := range order {
		if slices.Contains(page.Columns, name) && !t.dataHiddenColumns[name] && !slices.Contains(visible, name) {
			visible = append(visible, name)
		}
	}
	lo, hi := 0, len(visible)-1
	if !t.dataState.Cells.All {
		a := slices.Index(visible, t.dataState.Cells.Anchor)
		b := slices.Index(visible, t.dataState.Cells.Cursor)
		if b < 0 {
			return
		}
		if a < 0 {
			a = b
		}
		lo, hi = min(a, b), max(a, b)
	}
	if hi < lo {
		return
	}
	names = append(names, visible[lo:hi+1]...)
	for row, cells := range page.Rows {
		selected := t.dataSelection.Has(row)
		if t.dataSelection.Len() == 0 {
			selected = row == t.dataSelectedRow
		}
		if !selected {
			continue
		}
		values, flags := make([]string, len(names)), make([]bool, len(names))
		for col, name := range names {
			source := sources[name]
			if source >= len(cells) {
				// Incomplete loader rows are not silently exported as empty values.
				return nil, nil, nil
			}
			values[col], flags[col] = mysqlDataCopyPreview(cells[source]), cells[source].Null
		}
		rows, nulls = append(rows, values), append(nulls, flags)
	}
	return
}

func mysqlDataHasSelectedPreview(t *tab) bool {
	if t == nil || t.dataPage == nil || (!t.dataState.Cells.All &&
		(t.dataState.Cells.Cursor == "" || t.dataHiddenColumns[t.dataState.Cells.Cursor])) {
		return false
	}
	for row := range t.dataPage.Rows {
		if t.dataSelection.Has(row) || (t.dataSelection.Len() == 0 && row == t.dataSelectedRow) {
			return true
		}
	}
	return false
}

func mysqlDataDelimitedPreview(names []string, rows [][]string, comma rune, headers, data bool) string {
	var out strings.Builder
	w := csv.NewWriter(&out)
	w.Comma = comma
	if headers {
		_ = w.Write(names)
	}
	if data {
		w.WriteAll(rows)
	}
	w.Flush()
	return strings.TrimSuffix(out.String(), "\n")
}

// Keep server values as strings and SQL NULL as JSON null. Type coercion here
// could lose BIGINT precision, decimal scale, leading zeroes or dates.
func mysqlDataJSONPreview(names []string, rows [][]string, nulls [][]bool) string {
	var out strings.Builder
	out.WriteByte('[')
	for row, values := range rows {
		if row > 0 {
			out.WriteByte(',')
		}
		out.WriteByte('{')
		for col, name := range names {
			if col > 0 {
				out.WriteByte(',')
			}
			key, _ := json.Marshal(name)
			out.Write(key)
			out.WriteByte(':')
			if nulls[row][col] {
				out.WriteString("null")
			} else {
				value, _ := json.Marshal(values[col])
				out.Write(value)
			}
		}
		out.WriteByte('}')
	}
	out.WriteByte(']')
	return out.String()
}

func (s *State) copyMySQLDataSelection(c *ui.Context, t *tab) {
	names, rows, _ := mysqlDataSelectedPreview(t)
	if len(rows) == 0 || len(names) == 0 {
		return
	}
	text := rows[0][0]
	if len(rows) != 1 || len(names) != 1 {
		text = mysqlDataDelimitedPreview(names, rows, '\t', false, true)
	}
	c.WriteClipboard(text)
	s.status = fmt.Sprintf(locales.Text("已复制 %d 行 × %d 列（当前页预览）"), len(rows), len(names))
}

func (s *State) mysqlDataCopyAsMenu(c *ui.Context, t *tab, m *ui.Menu) {
	names, rows, nulls := mysqlDataSelectedPreview(t)
	empty := len(rows) == 0 || len(names) == 0
	for _, format := range []struct {
		labels []string
		comma  rune
	}{
		{[]string{"制表符分隔值（数据）", "制表符分隔值（字段名称）", "制表符分隔值（字段名称和数据）"}, '\t'},
		{[]string{"逗号分隔值（数据）", "逗号分隔值（字段名称）", "逗号分隔值（字段名称和数据）"}, ','},
	} {
		for i, label := range format.labels {
			if m.Item(locales.Text(label)).Disabled(empty).Chosen() {
				c.WriteClipboard(mysqlDataDelimitedPreview(names, rows, format.comma, i != 0, i != 1))
			}
		}
		m.Separator()
	}
	if m.Item("JSON").Disabled(empty).Chosen() {
		c.WriteClipboard(mysqlDataJSONPreview(names, rows, nulls))
	}
}
