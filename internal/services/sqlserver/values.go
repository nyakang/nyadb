package sqlserver

import (
	"bytes"
	"database/sql"
	"fmt"
	mssql "github.com/microsoft/go-mssqldb"
	"github.com/nyakang/nyadb/internal/db/contract"
	"strconv"
	"strings"
	"time"
)

type rowSink struct {
	callback func(int, []contract.Column, []contract.Cell) error
	columns  []contract.Column
}

func (s *rowSink) BeginResult(_ int, columns []contract.Column) error {
	s.columns = columns
	return nil
}
func (s *rowSink) WriteRow(index int, cells []contract.Cell) error {
	return s.callback(index, s.columns, cells)
}
func (s *rowSink) EndResult(_ int, _ int64) error { return nil }
func streamSink(callback func(int, []contract.Column, []contract.Cell) error) contract.StreamSink {
	if callback == nil {
		return nil
	}
	return &rowSink{callback: callback}
}

func scanRows(rows *sql.Rows, index int, sink contract.StreamSink, budget *int, maxRows, maxCell int) (contract.ResultSet, error) {
	names, e := rows.Columns()
	if e != nil {
		return contract.ResultSet{}, e
	}
	cts, e := rows.ColumnTypes()
	if e != nil {
		return contract.ResultSet{}, e
	}
	if len(names) > 512 {
		return contract.ResultSet{}, fmt.Errorf("result has more than 512 columns")
	}
	set := contract.ResultSet{Columns: make([]contract.Column, len(names))}
	for i, n := range names {
		nullable, _ := cts[i].Nullable()
		set.Columns[i] = contract.Column{Name: n, DatabaseType: cts[i].DatabaseTypeName(), Nullable: nullable}
	}
	if sink != nil {
		if e = sink.BeginResult(index, set.Columns); e != nil {
			return set, e
		}
	}
	var count int64
	reasons := map[string]bool{}
	for rows.Next() {
		raw := make([]any, len(names))
		dest := make([]any, len(raw))
		for i := range dest {
			dest[i] = &raw[i]
		}
		if e = rows.Scan(dest...); e != nil {
			return set, e
		}
		cells := make([]contract.Cell, len(raw))
		if sink == nil && len(set.Rows) >= maxRows {
			count++
			reasons["rows"] = true
			continue
		}
		for i, v := range raw {
			if sink == nil {
				cells[i] = previewValue(v, set.Columns[i].DatabaseType, maxCell)
			} else {
				cells[i] = cell(v, set.Columns[i].DatabaseType)
			}
		}
		if sink != nil {
			if e = sink.WriteRow(index, cells); e != nil {
				return set, e
			}
		}
		size := len(raw)*64 + 24
		for i := range cells {
			cells[i] = previewCell(cells[i], maxCell)
			size += len(cells[i].Text) + len(cells[i].Bytes)
		}
		count++
		if len(set.Rows) >= maxRows {
			reasons["rows"] = true
			continue
		}
		if size > *budget {
			reasons["bytes"] = true
			continue
		}
		*budget -= size
		for i := range cells {
			if cells[i].Truncated {
				reasons["cell"] = true
			}
			if len(cells[i].Text) > maxCell {
				cells[i].Text = cells[i].Text[:maxCell]
				cells[i].Truncated = true
				reasons["cell"] = true
			}
			if len(cells[i].Bytes) > maxCell {
				cells[i].Bytes = cells[i].Bytes[:maxCell]
				cells[i].Truncated = true
				reasons["cell"] = true
			}
		}
		set.Rows = append(set.Rows, cells)
	}
	for _, r := range []string{"rows", "bytes", "cell"} {
		if reasons[r] {
			set.Truncation = append(set.Truncation, r)
		}
	}
	if e = rows.Err(); e != nil {
		return set, e
	}
	if sink != nil {
		e = sink.EndResult(index, count)
	}
	return set, e
}

func typeKind(decl string) contract.CellKind {
	t := strings.ToLower(strings.Split(decl, "(")[0])
	switch t {
	case "binary", "varbinary", "image", "timestamp", "rowversion":
		return contract.Binary
	case "bit":
		return contract.Boolean
	case "tinyint", "smallint", "int", "bigint":
		return contract.Integer
	case "decimal", "numeric", "money", "smallmoney":
		return contract.Decimal
	case "real", "float":
		return contract.Floating
	case "date", "time", "datetime", "datetime2", "datetimeoffset", "smalldatetime":
		return contract.DateTime
	default:
		return contract.Text
	}
}
func cell(v any, decl string) contract.Cell {
	if v == nil {
		return contract.Cell{Kind: contract.Null}
	}
	k := typeKind(decl)
	c := contract.Cell{Kind: k}
	switch n := v.(type) {
	case []byte:
		if k == contract.Binary {
			c.Bytes = bytes.Clone(n)
		} else if strings.EqualFold(decl, "uniqueidentifier") {
			var id mssql.UniqueIdentifier
			if id.Scan(n) == nil {
				c.Text = strings.ToLower(id.String())
			} else {
				c.Kind = contract.Binary
				c.Bytes = bytes.Clone(n)
			}
		} else {
			c.Text = string(n)
		}
	case int64:
		c.Text = strconv.FormatInt(n, 10)
	case float64:
		c.Text = strconv.FormatFloat(n, 'g', -1, 64)
	case bool:
		c.Text = strconv.FormatBool(n)
	case time.Time:
		switch strings.ToLower(decl) {
		case "date":
			c.Text = n.Format("2006-01-02")
		case "time":
			c.Text = n.Format("15:04:05.9999999")
		case "datetimeoffset":
			c.Text = n.Format(time.RFC3339Nano)
		default:
			c.Text = n.Format("2006-01-02T15:04:05.9999999")
		}
	case string:
		c.Text = n
	default:
		c.Text = fmt.Sprint(v)
	}
	return c
}
func previewValue(v any, decl string, limit int) contract.Cell {
	c := cell(v, decl)
	if len(c.Text) > limit {
		c.Text = strings.Clone(c.Text[:limit])
		c.Truncated = true
	}
	if len(c.Bytes) > limit {
		c.Bytes = bytes.Clone(c.Bytes[:limit])
		c.Truncated = true
	}
	return c
}
func previewCell(c contract.Cell, limit int) contract.Cell {
	if len(c.Text) > limit {
		c.Text = strings.Clone(c.Text[:limit])
		c.Truncated = true
	}
	if len(c.Bytes) > limit {
		c.Bytes = bytes.Clone(c.Bytes[:limit])
		c.Truncated = true
	}
	return c
}
