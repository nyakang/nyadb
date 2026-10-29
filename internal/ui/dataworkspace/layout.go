package dataworkspace

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/locales"
)

const mysqlDataLayoutVersion = 1
const mysqlDataLayoutSaveDelay = 300 * time.Millisecond

// Layout preferences contain only presentation settings. A connection and a
// qualified table identify them independently of the current query or row.
type mysqlDataSavedLayout struct {
	Version        int                `json:"version"`
	Order          []string           `json:"order,omitempty"`
	Widths         map[string]float32 `json:"widths,omitempty"`
	HiddenColumns  map[string]bool    `json:"hiddenColumns,omitempty"`
	ShowFieldTypes bool               `json:"showFieldTypes"`
	ShowRowNumbers bool               `json:"showRowNumbers"`
}

type mysqlDataLayoutState struct {
	restored       bool
	showRowNumbers bool
	observed       string
	saved          string
	dirty          bool
	changedAt      time.Time
	saveFailed     bool
}

type mysqlDataLayoutPreferences struct {
	read  func(string) (string, error)
	write func(string, string) error
}

// SetDataLayoutPreferences connects non-secret presentation settings to the
// application's preference store. Shell remains independent of SQLite.
func (s *State) SetDataLayoutPreferences(read func(string) (string, error), write func(string, string) error) {
	s.dataLayoutPreferences = mysqlDataLayoutPreferences{read: read, write: write}
}

func mysqlDataLayoutKey(t *Document) string {
	identity, _ := json.Marshal([]any{t.profileID, t.database, t.table})
	return "mysql_data_layout:" + base64.RawURLEncoding.EncodeToString(identity)
}

func mysqlDataLayoutSnapshot(t *Document) string {
	encoded, err := json.Marshal(mysqlDataSavedLayout{
		Version: mysqlDataLayoutVersion, Order: t.grid.Columns.Order,
		Widths: t.grid.Columns.Widths, HiddenColumns: t.hiddenColumns,
		ShowFieldTypes: t.showFieldTypes, ShowRowNumbers: t.layout.showRowNumbers,
	})
	if err != nil {
		return "" // Invalid native widths must never overwrite a valid preference.
	}
	return string(encoded)
}

// Reconcile against all server columns, including hidden ones. Native Table
// keeps column identity while reordered and appends newly discovered fields.
func reconcileMySQLDataLayout(t *Document) {
	if t.page == nil || len(t.page.Columns) == 0 {
		return
	}
	known := make(map[string]bool, len(t.page.Columns))
	for _, name := range t.page.Columns {
		known[name] = true
	}
	seen := make(map[string]bool, len(known))
	order := make([]string, 0, len(known))
	for _, name := range t.grid.Columns.Order {
		if known[name] && !seen[name] {
			order = append(order, name)
			seen[name] = true
		}
	}
	for _, name := range t.page.Columns {
		if !seen[name] {
			order = append(order, name)
		}
	}
	t.grid.Columns.Order = order
	for name, width := range t.grid.Columns.Widths {
		if !known[name] || width <= 0 || math.IsNaN(float64(width)) || math.IsInf(float64(width), 0) {
			delete(t.grid.Columns.Widths, name)
		}
	}
	for name, hidden := range t.hiddenColumns {
		if !known[name] || !hidden {
			delete(t.hiddenColumns, name)
		}
	}
	if len(t.hiddenColumns) == len(known) {
		t.hiddenColumns = nil
	}
}

// The first real page establishes the server field identities. Do not mark a
// failed or pending load as restored, or discard saved widths before it arrives.
func (s *State) restoreMySQLDataLayout(t *Document) {
	if t == nil || t.closed || t.page == nil || len(t.page.Columns) == 0 {
		return
	}
	if !t.layout.restored {
		t.layout.restored = true
		if read := s.dataLayoutPreferences.read; read != nil {
			value, err := read(mysqlDataLayoutKey(t))
			if err != nil {
				s.setStatus(locales.Text("读取表格布局失败，使用默认布局"))
			} else if value != "" {
				var layout mysqlDataSavedLayout
				if json.Unmarshal([]byte(value), &layout) == nil && layout.Version == mysqlDataLayoutVersion {
					t.grid.Columns = ui.TableLayout{Order: layout.Order, Widths: layout.Widths}
					t.hiddenColumns = layout.HiddenColumns
					t.showFieldTypes = layout.ShowFieldTypes
					t.layout.showRowNumbers = layout.ShowRowNumbers
				}
			}
		}
		reconcileMySQLDataLayout(t)
		t.layout.observed = mysqlDataLayoutSnapshot(t)
		t.layout.saved = t.layout.observed
		return
	}
	reconcileMySQLDataLayout(t)
}

// MyGo changes native column layout during a frame. Tracking after the Table
// observes drag/resize as well as menu actions, without writing on every frame.
func (s *State) trackMySQLDataLayout(c *ui.Context, t *Document) {
	if t == nil || !t.layout.restored || s.dataLayoutPreferences.write == nil {
		return
	}
	current := mysqlDataLayoutSnapshot(t)
	if current == "" {
		return
	}
	if current != t.layout.observed {
		t.layout.observed = current
		t.layout.dirty = current != t.layout.saved
		t.layout.changedAt = c.Now()
		t.layout.saveFailed = false
	}
	if !t.layout.dirty || t.layout.saveFailed {
		return
	}
	remaining := mysqlDataLayoutSaveDelay - c.Now().Sub(t.layout.changedAt)
	if remaining > 0 {
		c.After(remaining)
		return
	}
	s.flushMySQLDataLayout(t)
}

// Closing a Document can precede the next tracking frame, so flush compares the
// current native state as well. One failed snapshot is not retried every frame.
func (s *State) flushMySQLDataLayout(t *Document) {
	if t == nil || !t.layout.restored || s.dataLayoutPreferences.write == nil {
		return
	}
	current := mysqlDataLayoutSnapshot(t)
	if current == "" || current == t.layout.saved || (t.layout.saveFailed && current == t.layout.observed) {
		return
	}
	t.layout.observed = current
	if err := s.dataLayoutPreferences.write(mysqlDataLayoutKey(t), current); err != nil {
		t.layout.saveFailed = true
		t.layout.dirty = true
		s.setStatus(locales.Text("保存表格布局失败；本次调整仍然有效"))
		return
	}
	t.layout.saved = current
	t.layout.dirty = false
	t.layout.saveFailed = false
}

func mysqlDataRowHeaderColumn(t *Document) ui.TableColumn {
	col := ui.TableColumn{ID: "\x1fnyadb:row", Width: 22, MinWidth: 22, Fixed: true, RowHeader: true}
	if t != nil && t.layout.showRowNumbers {
		col.Title, col.Width, col.MinWidth = "#", 42, 42
	}
	return col
}

func mysqlDataRowHeaderText(t *Document, row int) string {
	if t == nil || !t.layout.showRowNumbers {
		return ""
	}
	return fmt.Sprint(t.offset + int64(row) + 1)
}

func mysqlVisibleDataColumns(t *Document, page *mysqlsvc.MySQLDataPage) ([]int, []ui.TableColumn) {
	indices := make([]int, 0, len(page.Columns))
	visible := make([]ui.TableColumn, 0, len(page.Columns))
	for i, name := range page.Columns {
		if t.hiddenColumns[name] {
			continue
		}
		indices = append(indices, i)
		col := t.columns[i]
		kind, declaration, key := "", "", ""
		if i < len(page.ColumnKinds) {
			kind = page.ColumnKinds[i]
			declaration = kind // compatibility with pre-metadata test loaders
		}
		if i < len(page.ColumnMetadata) && page.ColumnMetadata[i].Name == name {
			meta := page.ColumnMetadata[i]
			kind, declaration, key = meta.DataType, meta.Type, meta.Key
		}
		if mysqlColumnIsNumeric(kind) {
			col.Align = ui.End
		}
		if t.showFieldTypes && declaration != "" {
			// Patched native MyGo TableColumn.Subtitle lives inside the real
			// keyed column header, sharing scroll, drag, resize and menu state.
			col.Title = name
			col.Subtitle = declaration
			col.SubtitleIcon = ColumnTypeGlyph(kind)
			col.HeaderBadge = mysqlColumnKeyBadge(key)
		}
		visible = append(visible, col)
	}
	if len(visible) == 0 && len(page.Columns) > 0 {
		t.hiddenColumns = nil
		return mysqlVisibleDataColumns(t, page)
	}
	return indices, visible
}

func mysqlColumnIsNumeric(kind string) bool {
	switch strings.ToLower(kind) {
	case "tinyint", "smallint", "mediumint", "int", "integer", "bigint", "decimal", "numeric", "float", "double", "real", "bool", "boolean":
		return true
	}
	return false
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

func mysqlDataCopyColumnPreview(page *mysqlsvc.MySQLDataPage, col int) string {
	if col < 0 || col >= len(page.Columns) {
		return ""
	}
	values := make([]string, 0, len(page.Rows)+1)
	values = append(values, page.Columns[col])
	for _, row := range page.Rows {
		if col >= len(row) {
			continue
		}
		values = append(values, CopyPreview(row[col]))
	}
	return strings.Join(values, "\n")
}

func mysqlDataHideColumn(t *Document, page *mysqlsvc.MySQLDataPage, column string) {
	visible, _ := mysqlVisibleDataColumns(t, page)
	if len(visible) <= 1 {
		return
	}
	if t.hiddenColumns == nil {
		t.hiddenColumns = make(map[string]bool)
	}
	t.hiddenColumns[column] = true
	if t.grid.Cells.Cursor == column {
		t.selection.Clear()
		t.grid.Cells = ui.ColumnRange{}
		t.selectedRow = -1
	}
}

func mysqlDataFitColumn(t *Document, page *mysqlsvc.MySQLDataPage, col int) {
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
	if t.grid.Columns.Widths == nil {
		t.grid.Columns.Widths = map[string]float32{}
	}
	t.grid.Columns.Widths[name] = min(width, 420)
}

func (s *State) mysqlDataHeaderMenu(c *ui.Context, t *Document, page *mysqlsvc.MySQLDataPage, indices []int, m *ui.Menu, col int) {
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
	if valid && m.Item(locales.Text("将此列宽度适配当前页")).Chosen() {
		mysqlDataFitColumn(t, page, source)
	}
	s.mysqlDataColumnsMenu(c, t, page, m)
}

// The toolbar and each native header expose the same column preferences.
func (s *State) mysqlDataColumnsMenu(c *ui.Context, t *Document, page *mysqlsvc.MySQLDataPage, m *ui.Menu) {
	if page == nil {
		return
	}
	indices, _ := mysqlVisibleDataColumns(t, page)
	m.Submenu(locales.Text("显示/隐藏列"), func(sub *ui.Menu) {
		for _, name := range page.Columns {
			name := name
			checked := !t.hiddenColumns[name]
			if sub.Item(name).Checked(checked).Disabled(checked && len(indices) <= 1).Chosen() {
				if checked {
					mysqlDataHideColumn(t, page, name)
				} else {
					delete(t.hiddenColumns, name)
				}
			}
		}
	})
	if m.Item(locales.Text("显示全部列")).Chosen() {
		t.hiddenColumns = nil
	}
	m.Separator()
	if m.Item(locales.Text("适配全部列宽（当前页）")).Chosen() {
		for _, source := range indices {
			mysqlDataFitColumn(t, page, source)
		}
	}
	if m.Item(locales.Text("重置全部列宽")).Chosen() {
		t.grid.Columns.Widths = nil
	}
	if m.Item(locales.Text("显示字段类型")).Checked(t.showFieldTypes).Chosen() {
		t.showFieldTypes = !t.showFieldTypes
	}
	if m.Item(locales.Text("显示行号")).Checked(t.layout.showRowNumbers).Chosen() {
		t.layout.showRowNumbers = !t.layout.showRowNumbers
	}
}
