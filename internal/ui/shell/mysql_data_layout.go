package shell

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/egoist/mygo/ui"
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

func mysqlDataLayoutKey(t *tab) string {
	identity, _ := json.Marshal([]any{t.dataProfileID, t.dataDatabase, t.dataTable})
	return "mysql_data_layout:" + base64.RawURLEncoding.EncodeToString(identity)
}

func mysqlDataLayoutSnapshot(t *tab) string {
	encoded, err := json.Marshal(mysqlDataSavedLayout{
		Version: mysqlDataLayoutVersion, Order: t.dataState.Columns.Order,
		Widths: t.dataState.Columns.Widths, HiddenColumns: t.dataHiddenColumns,
		ShowFieldTypes: t.dataShowFieldTypes, ShowRowNumbers: t.dataLayout.showRowNumbers,
	})
	if err != nil {
		return "" // Invalid native widths must never overwrite a valid preference.
	}
	return string(encoded)
}

// Reconcile against all server columns, including hidden ones. Native Table
// keeps column identity while reordered and appends newly discovered fields.
func reconcileMySQLDataLayout(t *tab) {
	if t.dataPage == nil || len(t.dataPage.Columns) == 0 {
		return
	}
	known := make(map[string]bool, len(t.dataPage.Columns))
	for _, name := range t.dataPage.Columns {
		known[name] = true
	}
	seen := make(map[string]bool, len(known))
	order := make([]string, 0, len(known))
	for _, name := range t.dataState.Columns.Order {
		if known[name] && !seen[name] {
			order = append(order, name)
			seen[name] = true
		}
	}
	for _, name := range t.dataPage.Columns {
		if !seen[name] {
			order = append(order, name)
		}
	}
	t.dataState.Columns.Order = order
	for name, width := range t.dataState.Columns.Widths {
		if !known[name] || width <= 0 || math.IsNaN(float64(width)) || math.IsInf(float64(width), 0) {
			delete(t.dataState.Columns.Widths, name)
		}
	}
	for name, hidden := range t.dataHiddenColumns {
		if !known[name] || !hidden {
			delete(t.dataHiddenColumns, name)
		}
	}
	if len(t.dataHiddenColumns) == len(known) {
		t.dataHiddenColumns = nil
	}
}

// The first real page establishes the server field identities. Do not mark a
// failed or pending load as restored, or discard saved widths before it arrives.
func (s *State) restoreMySQLDataLayout(t *tab) {
	if t == nil || t.kind != tabMySQLData || t.dataPage == nil || len(t.dataPage.Columns) == 0 {
		return
	}
	if !t.dataLayout.restored {
		t.dataLayout.restored = true
		if read := s.dataLayoutPreferences.read; read != nil {
			value, err := read(mysqlDataLayoutKey(t))
			if err != nil {
				s.status = locales.Text("读取表格布局失败，使用默认布局")
			} else if value != "" {
				var layout mysqlDataSavedLayout
				if json.Unmarshal([]byte(value), &layout) == nil && layout.Version == mysqlDataLayoutVersion {
					t.dataState.Columns = ui.TableLayout{Order: layout.Order, Widths: layout.Widths}
					t.dataHiddenColumns = layout.HiddenColumns
					t.dataShowFieldTypes = layout.ShowFieldTypes
					t.dataLayout.showRowNumbers = layout.ShowRowNumbers
				}
			}
		}
		reconcileMySQLDataLayout(t)
		t.dataLayout.observed = mysqlDataLayoutSnapshot(t)
		t.dataLayout.saved = t.dataLayout.observed
		return
	}
	reconcileMySQLDataLayout(t)
}

// MyGo changes native column layout during a frame. Tracking after the Table
// observes drag/resize as well as menu actions, without writing on every frame.
func (s *State) trackMySQLDataLayout(c *ui.Context, t *tab) {
	if t == nil || !t.dataLayout.restored || s.dataLayoutPreferences.write == nil {
		return
	}
	current := mysqlDataLayoutSnapshot(t)
	if current == "" {
		return
	}
	if current != t.dataLayout.observed {
		t.dataLayout.observed = current
		t.dataLayout.dirty = current != t.dataLayout.saved
		t.dataLayout.changedAt = c.Now()
		t.dataLayout.saveFailed = false
	}
	if !t.dataLayout.dirty || t.dataLayout.saveFailed {
		return
	}
	remaining := mysqlDataLayoutSaveDelay - c.Now().Sub(t.dataLayout.changedAt)
	if remaining > 0 {
		c.After(remaining)
		return
	}
	s.flushMySQLDataLayout(t)
}

// Closing a tab can precede the next tracking frame, so flush compares the
// current native state as well. One failed snapshot is not retried every frame.
func (s *State) flushMySQLDataLayout(t *tab) {
	if t == nil || !t.dataLayout.restored || s.dataLayoutPreferences.write == nil {
		return
	}
	current := mysqlDataLayoutSnapshot(t)
	if current == "" || current == t.dataLayout.saved || (t.dataLayout.saveFailed && current == t.dataLayout.observed) {
		return
	}
	t.dataLayout.observed = current
	if err := s.dataLayoutPreferences.write(mysqlDataLayoutKey(t), current); err != nil {
		t.dataLayout.saveFailed = true
		t.dataLayout.dirty = true
		s.status = locales.Text("保存表格布局失败；本次调整仍然有效")
		return
	}
	t.dataLayout.saved = current
	t.dataLayout.dirty = false
	t.dataLayout.saveFailed = false
}

func mysqlDataRowHeaderColumn(t *tab) ui.TableColumn {
	col := ui.TableColumn{ID: "\x1fnyadb:row", Width: 22, MinWidth: 22, Fixed: true, RowHeader: true}
	if t != nil && t.dataLayout.showRowNumbers {
		col.Title, col.Width, col.MinWidth = "#", 42, 42
	}
	return col
}

func mysqlDataRowHeaderText(t *tab, row int) string {
	if t == nil || !t.dataLayout.showRowNumbers {
		return ""
	}
	return fmt.Sprint(t.dataOffset + int64(row) + 1)
}
