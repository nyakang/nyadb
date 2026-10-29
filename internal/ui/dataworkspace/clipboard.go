package dataworkspace

import (
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/datainspector"
	"github.com/nyakang/nyadb/locales"
	"io"
	"os"
	"slices"
	"strings"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

// The selection is bounded by the loaded page. Hidden fields and the row
// selector never reach the clipboard; reordered fields follow the grid.
func mysqlDataSelectedPreview(t *Document) (names []string, rows [][]string, nulls [][]bool) {
	if t == nil || t.page == nil {
		return
	}
	page := t.page
	sources := make(map[string]int, len(page.Columns))
	for col, name := range page.Columns {
		sources[name] = col
	}
	order := append([]string(nil), t.grid.Columns.Order...)
	for _, name := range page.Columns {
		if !slices.Contains(order, name) {
			order = append(order, name)
		}
	}
	visible := order[:0]
	for _, name := range order {
		if slices.Contains(page.Columns, name) && !t.hiddenColumns[name] && !slices.Contains(visible, name) {
			visible = append(visible, name)
		}
	}
	lo, hi := 0, len(visible)-1
	if !t.grid.Cells.All {
		a := slices.Index(visible, t.grid.Cells.Anchor)
		b := slices.Index(visible, t.grid.Cells.Cursor)
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
		selected := t.selection.Has(row)
		if t.selection.Len() == 0 {
			selected = row == t.selectedRow
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
			values[col], flags[col] = datainspector.CopyPreview(cells[source]), cells[source].Null
		}
		rows, nulls = append(rows, values), append(nulls, flags)
	}
	return
}

func mysqlDataHasSelectedPreview(t *Document) bool {
	if t == nil || t.page == nil || (!t.grid.Cells.All &&
		(t.grid.Cells.Cursor == "" || t.hiddenColumns[t.grid.Cells.Cursor])) {
		return false
	}
	for row := range t.page.Rows {
		if t.selection.Has(row) || (t.selection.Len() == 0 && row == t.selectedRow) {
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
	// RawMessage preserves the displayed field order while applying the
	// observed two-space JSON layout without numeric type coercion.
	pretty, _ := json.MarshalIndent(json.RawMessage(out.String()), "", "  ")
	return string(pretty)
}

func (s *State) copyMySQLDataSelection(c *ui.Context, t *Document) {
	names, rows, _ := mysqlDataSelectedPreview(t)
	if len(rows) == 0 || len(names) == 0 {
		return
	}
	text := rows[0][0]
	if len(rows) != 1 || len(names) != 1 {
		text = mysqlDataDelimitedPreview(names, rows, '\t', false, true)
	}
	c.WriteClipboard(text)
	s.setStatus(fmt.Sprintf(locales.Text("已复制 %d 行 × %d 列（当前页预览）"), len(rows), len(names)))
}

func (s *State) mysqlDataCopyAsMenu(c *ui.Context, t *Document, m *ui.Menu) {
	names, rows, nulls := mysqlDataSelectedPreview(t)
	empty := len(rows) == 0 || len(names) == 0
	for _, item := range []struct {
		label  string
		update bool
	}{{"Insert 语句", false}, {"Update 语句", true}} {
		text, err := mysqlDataSQLSelection(t, item.update)
		if m.Item(locales.Text(item.label)).Disabled(empty || err != nil).Chosen() {
			c.WriteClipboard(text)
		}
	}
	m.Separator()
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

// SQL copies are explicit text exports. Hex literals avoid dependence on a
// server's NO_BACKSLASH_ESCAPES mode and keep values out of SQL syntax.
func mysqlDataSQLLiteral(text string, null, binary bool) string {
	if null {
		return "NULL"
	}
	if binary {
		return "X'" + hex.EncodeToString([]byte(text)) + "'"
	}
	if text == "" {
		return "''"
	}
	return "_utf8mb4 X'" + hex.EncodeToString([]byte(text)) + "'"
}
func mysqlDataSQLIdentifier(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}
func mysqlDataSQLSelection(t *Document, update bool) (string, error) {
	selectedFields, _, _ := mysqlDataSelectedPreview(t)
	if len(selectedFields) == 0 || t.page == nil {
		return "", errors.New("no selection")
	}
	page := t.page
	// Unlike TSV/CSV/JSON cell selections, the observed SQL actions copy
	// complete records. Hidden fields remain part of a record's SQL output.
	names := page.Columns
	var out []string
	for row, cells := range page.Rows {
		if !t.selection.Has(row) && (t.selection.Len() != 0 || row != t.selectedRow) {
			continue
		}
		var fields, values, assignments []string
		draft := mysqlDataDraftForRow(t, row)
		for _, name := range names {
			col := slices.Index(page.Columns, name)
			if col < 0 || col >= len(cells) {
				return "", errors.New("incomplete preview")
			}
			if col < len(page.ColumnMetadata) && mysqlsvc.MySQLDataColumnGenerated(page.ColumnMetadata[col]) {
				continue
			}
			if update && col < len(page.ColumnMetadata) && strings.EqualFold(page.ColumnMetadata[col].Key, "PRI") {
				continue
			}
			literal := ""
			value, explicit := mysqlsvc.MySQLDataValue{}, false
			if draft != nil {
				value, explicit = draft.Values[name]
			}
			switch {
			case explicit && value.Default || !explicit && draft != nil && draft.Kind == "insert":
				literal = "DEFAULT"
			case explicit:
				if value.Binary {
					literal = mysqlDataSQLLiteral(string(value.Bytes), value.Null, true)
				} else {
					literal = mysqlDataSQLLiteral(value.Text, value.Null, false)
				}
			default:
				if cells[col].Truncated {
					return "", errors.New("incomplete preview")
				}
				// Binary preview spelling can be formatted or shortened; it is not a full value.
				if !cells[col].Null && col < len(page.ColumnKinds) && !mysqlDataCellFilterValueAvailable(page.ColumnKinds[col], cells[col]) {
					return "", errors.New("binary preview")
				}
				literal = mysqlDataSQLLiteral(cells[col].Text, cells[col].Null, false)
			}
			fields = append(fields, mysqlDataSQLIdentifier(name))
			values = append(values, literal)
			assignments = append(assignments, mysqlDataSQLIdentifier(name)+" = "+literal)
		}
		if len(fields) == 0 {
			return "", errors.New("no writable fields")
		}
		target := mysqlDataSQLIdentifier(page.Database) + "." + mysqlDataSQLIdentifier(page.Table)
		if update {
			if row >= len(page.RowRefs) || page.RowRefs[row] == nil {
				return "", errors.New("no unique identity")
			}
			ref := page.RowRefs[row]
			if ref.Database != page.Database || ref.Table != page.Table || len(ref.Columns) == 0 || len(ref.Columns) != len(ref.Values) {
				return "", errors.New("invalid identity")
			}
			var predicates []string
			for i, key := range ref.Columns {
				if i >= len(ref.Values) {
					return "", errors.New("incomplete identity")
				}
				// Comparing a numeric key directly with a hex literal treats the
				// literal as a number; comparing with a string can round BIGINT
				// identities through DOUBLE. Compare the original bytes instead.
				predicates = append(predicates, "CAST("+mysqlDataSQLIdentifier(key)+" AS BINARY) = X'"+hex.EncodeToString(ref.Values[i])+"'")
			}
			out = append(out, "UPDATE "+target+" SET "+strings.Join(assignments, ", ")+" WHERE "+strings.Join(predicates, " AND ")+";")
		} else {
			out = append(out, "INSERT INTO "+target+" ("+strings.Join(fields, ", ")+") VALUES ("+strings.Join(values, ", ")+");")
		}
	}
	if len(out) == 0 {
		return "", errors.New("no selection")
	}
	return strings.Join(out, "\n"), nil
}

// Clipboard text is literal text: the string NULL stays a string. SQL NULL
// is only selected by the explicit NULL action. Validate the full rectangle
// before staging any cells so a rejected paste never leaves a partial draft.
func (s *State) pasteMySQLDataSelection(c *ui.Context, t *Document, row, source int) {
	text := c.Services().ReadClipboard()
	if len(text) > mysqlsvc.MySQLDataEditMaxBytes {
		t.edit.Err = locales.Text("粘贴数据超过编辑大小限制")
		return
	}
	r := csv.NewReader(strings.NewReader(text))
	r.Comma = '\t'
	r.FieldsPerRecord = -1
	values, err := r.ReadAll()
	if err != nil && err != io.EOF {
		t.edit.Err = locales.Text("粘贴数据格式无效")
		return
	}
	if len(values) == 0 {
		values = [][]string{{""}}
	}
	indices := mysqlDataPasteColumns(t)
	start := slices.Index(indices, source)
	if start < 0 {
		return
	}
	for i, line := range values {
		for j := range line {
			if row+i >= len(t.page.Rows) || start+j >= len(indices) || !mysqlDataCanEdit(t, row+i, indices[start+j]) || mysqlDataBinaryField(t.page.ColumnMetadata[indices[start+j]]) || mysqlDataSpatialField(t.page.ColumnMetadata[indices[start+j]]) {
				t.edit.Err = locales.Text("粘贴选区包含不可编辑字段或超出当前页")
				return
			}
		}
	}
	for i, line := range values {
		for j, value := range line {
			s.setMySQLDataCell(c.Services(), t, row+i, indices[start+j], mysqlsvc.MySQLDataValue{Text: value})
		}
	}
}

func mysqlDataPasteColumns(t *Document) []int {
	order := append([]string(nil), t.grid.Columns.Order...)
	for _, name := range t.page.Columns {
		if !slices.Contains(order, name) {
			order = append(order, name)
		}
	}
	var indices []int
	for _, name := range order {
		source := slices.Index(t.page.Columns, name)
		if source >= 0 && !t.hiddenColumns[name] && !slices.Contains(indices, source) {
			indices = append(indices, source)
		}
	}
	return indices
}

type mysqlDataExportResult struct {
	path string
	err  error
}
type mysqlDataExportState struct {
	Busy    bool
	Results <-chan mysqlDataExportResult
}

// Snapshot the bounded selection on the UI thread. The dialog and disk write
// run in a worker, and only the UI thread consumes the resulting status.
func (s *State) exportMySQLDataSelection(c *ui.Context, t *Document, format string) {
	if t.export.Busy {
		return
	}
	names, rows, nulls := mysqlDataSelectedPreview(t)
	if len(names) == 0 || len(rows) == 0 {
		return
	}
	var data string
	switch format {
	case "CSV":
		data = mysqlDataDelimitedPreview(names, rows, ',', true, true)
	case "TSV":
		data = mysqlDataDelimitedPreview(names, rows, '\t', true, true)
	case "JSON":
		data = mysqlDataJSONPreview(names, rows, nulls)
	default:
		return
	}
	results := make(chan mysqlDataExportResult, 1)
	t.export = mysqlDataExportState{Busy: true, Results: results}
	services := c.Services()
	title := locales.Text("保存选区数据预览")
	message := locales.Text("仅保存当前页选区；截断字段包含预览标记")
	extension := strings.ToLower(format)
	defaultPath := t.table + "." + extension
	go func() {
		path, err := mygo.Dialog.Save(mygo.SaveDialogOptions{Title: title, Message: message, DefaultPath: defaultPath, Filters: []mygo.FileFilter{{Name: format, Extensions: []string{extension}}}})
		if err == nil && path != "" {
			err = os.WriteFile(path, []byte(data+"\n"), 0600)
		}
		results <- mysqlDataExportResult{path: path, err: err}
		services.Invalidate()
	}()
}
func (s *State) consumeMySQLDataExport(t *Document) {
	if t.export.Results == nil {
		return
	}
	select {
	case result := <-t.export.Results:
		t.export = mysqlDataExportState{}
		if result.err != nil {
			t.edit.Err = locales.Text("保存数据文件失败：") + result.err.Error()
		} else if result.path != "" {
			s.setStatus(fmt.Sprintf(locales.Text("已保存选区预览至 %s"), result.path))
		}
	default:
	}
}
