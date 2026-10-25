package shell

import (
	"strings"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/locales"
)

func mysqlVisibleDataColumns(t *tab, page *connections.MySQLDataPage) ([]int, []ui.TableColumn) {
	indices := make([]int, 0, len(page.Columns))
	visible := make([]ui.TableColumn, 0, len(page.Columns))
	for i, name := range page.Columns {
		if t.dataHiddenColumns[name] {
			continue
		}
		indices = append(indices, i)
		col := t.dataColumns[i]
		kind, declaration, key := "", "", ""
		if i < len(page.ColumnKinds) {
			kind = page.ColumnKinds[i]
			declaration = kind // compatibility with pre-metadata test loaders
		}
		if i < len(page.ColumnMetadata) && page.ColumnMetadata[i].Name == name {
			meta := page.ColumnMetadata[i]
			kind, declaration, key = meta.DataType, meta.Type, meta.Key
		}
		if t.dataShowFieldTypes && declaration != "" {
			// Patched native MyGo TableColumn.Subtitle lives inside the real
			// keyed column header, sharing scroll, drag, resize and menu state.
			col.Title = name
			col.Subtitle = declaration
			col.SubtitleIcon = mysqlColumnTypeGlyph(kind)
			col.HeaderBadge = mysqlColumnKeyBadge(key)
		}
		visible = append(visible, col)
	}
	if len(visible) == 0 && len(page.Columns) > 0 {
		t.dataHiddenColumns = nil
		return mysqlVisibleDataColumns(t, page)
	}
	return indices, visible
}

// Glyphs are NyaDB-authored, deliberately small type hints rendered by the
// native header. They do not depend on proprietary database-manager artwork.
func mysqlColumnTypeGlyph(kind string) string {
	switch strings.ToLower(kind) {
	case "tinyint", "smallint", "mediumint", "int", "integer", "bigint", "decimal", "numeric", "float", "double", "real", "bit", "bool", "boolean":
		return "#"
	case "char", "varchar", "tinytext", "text", "mediumtext", "longtext", "enum", "set":
		return "A"
	case "date", "time", "datetime", "timestamp", "year":
		return "◷"
	case "json":
		return "{}"
	case "binary", "varbinary", "tinyblob", "blob", "mediumblob", "longblob":
		return "◇"
	case "geometry", "point", "linestring", "polygon", "multipoint", "multilinestring", "multipolygon", "geometrycollection":
		return "✧"
	}
	return "·"
}

func mysqlColumnKeyBadge(key string) string {
	switch key {
	case "PRI":
		return "PK"
	case "UNI":
		return "UQ"
	case "MUL":
		return "IDX" // MySQL does not guarantee this is a foreign key.
	}
	return ""
}

func mysqlDataCopyColumnPreview(page *connections.MySQLDataPage, col int) string {
	if col < 0 || col >= len(page.Columns) {
		return ""
	}
	values := make([]string, 0, len(page.Rows)+1)
	values = append(values, page.Columns[col])
	for _, row := range page.Rows {
		if col >= len(row) {
			continue
		}
		values = append(values, mysqlDataCopyPreview(row[col]))
	}
	return strings.Join(values, "\n")
}

func mysqlDataHideColumn(t *tab, page *connections.MySQLDataPage, column string) {
	visible, _ := mysqlVisibleDataColumns(t, page)
	if len(visible) <= 1 {
		return
	}
	if t.dataHiddenColumns == nil {
		t.dataHiddenColumns = make(map[string]bool)
	}
	t.dataHiddenColumns[column] = true
	if t.dataState.Cells.Cursor == column {
		t.dataSelection.Clear()
		t.dataState.Cells = ui.ColumnRange{}
		t.dataSelectedRow = -1
	}
}

func mysqlDataFitColumn(t *tab, page *connections.MySQLDataPage, col int) {
	if col < 0 || col >= len(page.Columns) {
		return
	}
	width := float32(90)
	name := page.Columns[col]
	width = max(width, float32(utf8.RuneCountInString(name))*8+28)
	if col < len(page.ColumnMetadata) && page.ColumnMetadata[col].Name == name {
		// Fit the full server declaration without assuming it matches DATA_TYPE.
		width = max(width, float32(utf8.RuneCountInString(page.ColumnMetadata[col].Type))*7+50)
	}
	for _, r := range page.Rows {
		if col >= len(r) {
			continue
		}
		display := r[col].Text
		if r[col].Null {
			display = "NULL"
		}
		n := utf8.RuneCountInString(display)
		width = max(width, float32(min(n, 48)*8+28))
	}
	if t.dataState.Columns.Widths == nil {
		t.dataState.Columns.Widths = map[string]float32{}
	}
	t.dataState.Columns.Widths[name] = min(width, 420)
}

func (s *State) mysqlDataHeaderMenu(c *ui.Context, t *tab, page *connections.MySQLDataPage, indices []int, m *ui.Menu, col int) {
	valid := col >= 0 && col < len(indices)
	source := 0
	if valid {
		source = indices[col]
	}
	if valid {
		name := page.Columns[source]
		if m.Item(locales.Text("复制字段名称")).Chosen() {
			c.WriteClipboard(name)
		}
		if m.Item(locales.Text("复制此列（当前页）")).Chosen() {
			c.WriteClipboard(mysqlDataCopyColumnPreview(page, source))
		}
		m.Separator()
		if m.Item(locales.Text("隐藏此列")).Disabled(len(indices) <= 1).Chosen() {
			mysqlDataHideColumn(t, page, name)
		}
	}
	m.Submenu(locales.Text("显示/隐藏列"), func(sub *ui.Menu) {
		for _, name := range page.Columns {
			name := name
			checked := !t.dataHiddenColumns[name]
			if sub.Item(name).Checked(checked).Disabled(checked && len(indices) <= 1).Chosen() {
				if checked {
					mysqlDataHideColumn(t, page, name)
				} else {
					delete(t.dataHiddenColumns, name)
				}
			}
		}
	})
	if m.Item(locales.Text("显示全部列")).Chosen() {
		t.dataHiddenColumns = nil
	}
	m.Separator()
	if valid && m.Item(locales.Text("将此列宽度适配当前页")).Chosen() {
		mysqlDataFitColumn(t, page, source)
	}
	if m.Item(locales.Text("重置全部列宽")).Chosen() {
		t.dataState.Columns.Widths = nil
	}
	if m.Item(locales.Text("显示字段类型")).Checked(t.dataShowFieldTypes).Chosen() {
		t.dataShowFieldTypes = !t.dataShowFieldTypes
	}
}
