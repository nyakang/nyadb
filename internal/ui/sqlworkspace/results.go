package sqlworkspace

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/locales"
)

func truncationText(reasons []string) string {
	labels := make([]string, len(reasons))
	for i, r := range reasons {
		switch r {
		case "sets":
			labels[i] = locales.Text("结果集预览上限")
		case "rows":
			labels[i] = locales.Text("行数上限")
		case "bytes":
			labels[i] = locales.Text("内存上限")
		case "cell":
			labels[i] = locales.Text("单元格上限")
		default:
			labels[i] = r
		}
	}
	return locales.Text("结果截断：") + strings.Join(labels, ", ")
}

func selectedResult(d *Document) contract.ResultSet {
	if d == nil || d.result == nil {
		return contract.ResultSet{}
	}
	sets := d.result.ResultSets()
	if len(sets) == 0 {
		return contract.ResultSet{}
	}
	d.resultSet = max(0, min(d.resultSet, len(sets)-1))
	return sets[d.resultSet]
}
func resultData(d *Document, selection bool) contract.ResultSet {
	s := selectedResult(d)
	if !selection {
		return s
	}
	out := contract.ResultSet{}
	var fields []int
	for i, c := range s.Columns {
		id := fmt.Sprintf("sql_%d", i)
		take := d.grid.Cells.All || d.grid.Cells.Cursor == ""
		if d.grid.ColumnSelection != nil {
			take = false
			for _, x := range d.grid.ColumnSelection {
				if x == id {
					take = true
				}
			}
		} else if !take {
			a, b := -1, -1
			for j := range s.Columns {
				if fmt.Sprintf("sql_%d", j) == d.grid.Cells.Anchor {
					a = j
				}
				if fmt.Sprintf("sql_%d", j) == d.grid.Cells.Cursor {
					b = j
				}
			}
			take = i >= min(a, b) && i <= max(a, b)
		}
		if take {
			out.Columns = append(out.Columns, c)
			fields = append(fields, i)
		}
	}
	for i, row := range s.Rows {
		if !d.selection.Has(i) {
			continue
		}
		r := make([]contract.Cell, len(fields))
		for j, f := range fields {
			r[j] = row[f]
		}
		out.Rows = append(out.Rows, r)
	}
	return out
}
func EncodeResult(set contract.ResultSet, format string) ([]byte, error) {
	if format == "JSON" {
		type output struct {
			Columns    []contract.Column `json:"columns"`
			Rows       [][]contract.Cell `json:"rows"`
			Truncation []string          `json:"truncation,omitempty"`
		}
		return json.MarshalIndent(output{set.Columns, set.Rows, set.Truncation}, "", "  ")
	}
	var b bytes.Buffer
	w := csv.NewWriter(&b)
	if format == "TSV" {
		w.Comma = '\t'
	} else if format != "CSV" {
		return nil, fmt.Errorf("unsupported SQL export format")
	}
	names := make([]string, len(set.Columns))
	for i, c := range set.Columns {
		names[i] = c.Name
	}
	if e := w.Write(names); e != nil {
		return nil, e
	}
	for _, row := range set.Rows {
		r := make([]string, len(row))
		for i, c := range row {
			r[i] = c.Display()
			if c.Kind == contract.Null {
				r[i] = `\N`
			} else if strings.HasPrefix(r[i], `\`) {
				r[i] = `\` + r[i]
			}
		}
		if e := w.Write(r); e != nil {
			return nil, e
		}
	}
	w.Flush()
	return b.Bytes(), w.Error()
}
func (s *State) copyResult(c *ui.Context, d *Document) {
	data := resultData(d, true)
	if len(data.Rows) == 0 {
		return
	}
	b, e := EncodeResult(data, "TSV")
	if e == nil {
		c.WriteClipboard(string(b))
	}
}
func (s *State) exportResult(d *Document, format string, selection bool) {
	set := resultData(d, selection)
	b, e := EncodeResult(set, format)
	if e != nil {
		d.err = e.Error()
		return
	}
	f := s.persistence.files
	if f.SavePath == nil || f.Write == nil {
		return
	}
	name := d.Name() + "." + strings.ToLower(format)
	s.startIO(d, nil, func() ioResult {
		path, e := f.SavePath(name)
		if e == nil && path != "" {
			e = f.Write(path, string(b))
		}
		return ioResult{kind: "export", err: e}
	})
}
