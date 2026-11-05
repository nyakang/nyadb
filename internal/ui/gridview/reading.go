// Package gridview contains stateless reading helpers. Documents, selection,
// requests and editing remain owned by their workspaces.
package gridview

import (
	"bytes"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"strings"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func Delimited(names []string, rows [][]string, comma rune, headers, data bool) string {
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
func Copy(set contract.ResultSet) string {
	rows := make([][]string, len(set.Rows))
	names := make([]string, len(set.Columns))
	for i, col := range set.Columns {
		names[i] = col.Name
	}
	for i, row := range set.Rows {
		rows[i] = make([]string, len(row))
		for j, cell := range row {
			rows[i][j] = cell.Display()
		}
	}
	if len(rows) == 1 && len(rows[0]) == 1 {
		return rows[0][0]
	}
	return Delimited(names, rows, '\t', false, true)
}
func TextCell(c *ui.Context, text string, null bool) ui.Element {
	e := ui.Text(c, text).SingleLine().FontSize(theme.Font(c, theme.FontSize))
	if null {
		e.TextColor(theme.Colors(c).Muted)
	}
	return e
}
func Cell(c *ui.Context, cell contract.Cell) ui.Element {
	return TextCell(c, cell.Display(), cell.Kind == contract.Null)
}
func ColumnMenu(c *ui.Context, m *ui.Menu, layout *ui.TableLayout, hidden map[string]bool, columns []ui.TableColumn, col, rows int, read func(int, int) contract.Cell, copy func(string)) {
	if col < 0 || col >= len(columns) {
		return
	}
	column := columns[col]
	if m.Item(locales.Text("复制列")).Chosen() {
		out := contract.ResultSet{Columns: []contract.Column{{Name: column.Title}}}
		for row := 0; row < rows; row++ {
			out.Rows = append(out.Rows, []contract.Cell{read(row, col)})
		}
		copy(Copy(out))
	}
	if m.Item(locales.Text("适应列宽")).Chosen() {
		if layout.Widths == nil {
			layout.Widths = map[string]float32{}
		}
		layout.Widths[column.ID] = FitColumn(c, column, rows, func(row int) contract.Cell { return read(row, col) })
	}
	if m.Item(locales.Text("隐藏列")).Disabled(len(columns)-len(hidden) <= 1).Chosen() {
		hidden[column.ID] = true
	}
	m.Submenu(locales.Text("列"), func(sub *ui.Menu) {
		for _, column := range columns {
			if sub.Item(column.Title).Checked(!hidden[column.ID]).Chosen() {
				if hidden[column.ID] {
					delete(hidden, column.ID)
				} else if len(columns)-len(hidden) > 1 {
					hidden[column.ID] = true
				}
			}
		}
	})
	if m.Item(locales.Text("重置列布局")).Chosen() {
		*layout = ui.TableLayout{}
		clear(hidden)
	}
}

// FieldPreview only consumes cached values. Images are inspected before
// decoding to bound expanded bitmap memory.
func FieldPreview(c *ui.Context, cell contract.Cell, mode *string, bind ...func(ui.Element)) {
	if *mode == "" {
		*mode = "文本"
	}
	modes := []string{"文本", "JSON", "十六进制", "图像"}
	theme.Choice(c, mode, modes, locales.Text).Label(locales.Text("字段查看模式"))
	PreviewValue(c, cell, *mode, bind...)
}

// PreviewValue lets an existing workspace keep its own mode tabs and editing
// controls while sharing the cached, read-only value presentation.
func PreviewValue(c *ui.Context, cell contract.Cell, mode string, bind ...func(ui.Element)) {
	if cell.Truncated {
		ui.Text(c, locales.Text("单元格预览已截断"))
	}
	if cell.Kind == contract.Null {
		ui.Text(c, "NULL").TextColor(theme.Colors(c).Muted)
		return
	}
	if cell.Kind == contract.Binary && len(cell.Bytes) == 0 {
		ui.Text(c, locales.Text("空二进制"))
	} else if cell.Kind != contract.Binary && cell.Text == "" {
		ui.Text(c, locales.Text("空文本"))
	}
	raw := cell.Bytes
	if raw == nil {
		raw = []byte(cell.Text)
	}
	text := cell.Display()
	switch mode {
	case "JSON":
		var pretty bytes.Buffer
		if !cell.Truncated && json.Indent(&pretty, raw, "", "  ") == nil {
			text = pretty.String()
		} else {
			ui.Text(c, locales.Text("缓存内容不是完整 JSON"))
		}
	case "十六进制":
		text = hex.Dump(raw)
	case "图像":
		cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
		if !cell.Truncated && err == nil && cfg.Width > 0 && cfg.Height > 0 && int64(cfg.Width)*int64(cfg.Height) <= 4<<20 {
			if source, err := ui.DecodeImage(raw); err == nil {
				ui.Image(c, source).MaxWidth(360).MaxHeight(300)
				return
			}
		}
		ui.Text(c, locales.Text("缓存内容无法作为图像预览"))
		return
	}
	field := ui.TextArea(c, &text).ReadOnly(true).Label(locales.Text("字段值预览")).Lines(1, 3).Grow(1).MinHeight(24).FillWidth()
	if len(bind) > 0 && bind[0] != nil {
		bind[0](field)
	}
}

// FitColumn measures only cached display values, with a bounded uniform sample.
func FitColumn(c *ui.Context, column ui.TableColumn, rows int, read func(int) contract.Cell) float32 {
	padding := 24 * max(float32(1), theme.Font(c, 12)/12)
	measure := func(text string, size float32, weight int) float32 {
		if utf8.RuneCountInString(text) > 1024 {
			return 800
		}
		width, _ := c.MeasureText(0, ui.Span{Text: text, Size: size, Weight: weight})
		return width + padding
	}
	width := max(float32(64), measure(column.Title, theme.Font(c, 12), 700))
	width = max(width, measure(column.Subtitle, theme.Font(c, 11), 400))
	samples := min(max(0, rows), 200)
	for i := 0; i < samples && width < 800; i++ {
		row := 0
		if samples > 1 {
			row = i * (rows - 1) / (samples - 1)
		}
		width = max(width, measure(read(row).Display(), theme.Font(c, 12), 400))
	}
	return min(float32(800), width)
}
