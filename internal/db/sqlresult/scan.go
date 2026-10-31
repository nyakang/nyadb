// Package sqlresult detaches typed SQL results from database/sql row buffers.
package sqlresult

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
)

type Limits struct{ Rows, Bytes, CellBytes, Columns int }

var Preview = Limits{Rows: 1000, Bytes: 8 << 20, CellBytes: 64 << 10, Columns: 512}

// Sink receives each full row exactly once, including rows outside the preview.
// A nil row announces the columns of a new result set (empty sets included).
type Sink func(int, []contract.Column, []contract.Cell) error

func Scan(ctx context.Context, rows *sql.Rows, limits Limits, sink Sink) (out contract.SQLResult, err error) {
	defer rows.Close()
	defer out.SetLegacyView()
	if limits.Rows < 0 || limits.Bytes < 1 || limits.CellBytes < 1 || limits.Columns < 1 {
		return out, errors.New("invalid SQL result limits")
	}
	total, count := 0, 0
	for setID := 0; ; setID++ {
		if setID >= 128 {
			return out, errors.New("SQL result exceeds 128 result sets")
		}
		names, e := rows.Columns()
		if e != nil {
			return out, e
		}
		if len(names) > limits.Columns {
			return out, errors.New("SQL result has too many columns")
		}
		types, e := rows.ColumnTypes()
		if e != nil {
			return out, e
		}
		// Conservative retained-memory accounting: column strings, slice growth,
		// typed cells and the compatibility presentation arrays (64-bit sizes).
		metadata := 256 + len(names)*128
		for i, n := range names {
			metadata += len(n) + len(types[i].DatabaseTypeName())
		}
		retained := total+metadata <= limits.Bytes
		if retained {
			total += metadata
		}
		set := contract.ResultSet{Columns: make([]contract.Column, len(names))}
		for i, n := range names {
			nullable, _ := types[i].Nullable()
			set.Columns[i] = contract.Column{Name: n, DatabaseType: types[i].DatabaseTypeName(), Nullable: nullable}
		}
		if sink != nil {
			if e = sink(setID, set.Columns, nil); e != nil {
				return out, e
			}
		}
		values := make([]sql.RawBytes, len(names))
		dest := make([]any, len(names))
		for i := range values {
			dest[i] = &values[i]
		}
		limited := !retained
		if !retained {
			set.Truncation = appendReason(set.Truncation, "bytes")
		}
		for rows.Next() {
			if e = ctx.Err(); e != nil {
				return out, e
			}
			if e = rows.Scan(dest...); e != nil {
				return out, e
			}
			if sink != nil {
				full := make([]contract.Cell, len(names))
				for i, v := range values {
					full[i] = value(set.Columns[i].DatabaseType, v)
				}
				if e = sink(setID, set.Columns, full); e != nil {
					return out, e
				}
			}
			if limited {
				continue
			}
			if count >= limits.Rows {
				set.Truncation = appendReason(set.Truncation, "rows")
				limited = true
				continue
			}
			row := make([]contract.Cell, len(names))
			size := 96 + len(names)*96
			for i, v := range values {
				truncated := len(v) > limits.CellBytes
				if truncated {
					v = v[:limits.CellBytes]
					// Do not allocate the entire driver value before trimming.
					typ := strings.ToUpper(set.Columns[i].DatabaseType)
					if !binaryType(typ) && utf8.Valid(values[i]) {
						for len(v) > 0 && !utf8.Valid(v) {
							v = v[:len(v)-1]
						}
					}
					set.Truncation = appendReason(set.Truncation, "cell")
				}
				row[i] = value(set.Columns[i].DatabaseType, v)
				row[i].Truncated = truncated
				size += len(row[i].Text) + 3*len(row[i].Bytes) + 2
			}
			if total+size > limits.Bytes {
				set.Truncation = appendReason(set.Truncation, "bytes")
				limited = true
				continue
			}
			total += size
			count++
			set.Rows = append(set.Rows, row)
		}
		if e = rows.Err(); e != nil {
			return out, e
		}
		if !retained {
			set.Columns = nil
		}
		out.Sets = append(out.Sets, set)
		if !rows.NextResultSet() {
			break
		}
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	if err = ctx.Err(); err != nil {
		return out, err
	}
	out.SetLegacyView()
	return out, nil
}
func appendReason(xs []string, s string) []string {
	for _, x := range xs {
		if x == s {
			return xs
		}
	}
	return append(xs, s)
}
func value(typ string, v []byte) contract.Cell {
	if v == nil {
		return contract.Cell{Kind: contract.Null}
	}
	t := strings.ToUpper(typ)
	kind := contract.Text
	switch {
	case binaryType(t) || !utf8.Valid(v):
		return contract.Cell{Kind: contract.Binary, Bytes: append([]byte{}, v...)}
	case strings.Contains(t, "INT"):
		kind = contract.Integer
	case strings.Contains(t, "DECIMAL") || strings.Contains(t, "NUMERIC"):
		kind = contract.Decimal
	case strings.Contains(t, "FLOAT") || strings.Contains(t, "DOUBLE") || t == "REAL":
		kind = contract.Floating
	case t == "BOOL" || t == "BOOLEAN":
		kind = contract.Boolean
	case strings.Contains(t, "DATE") || strings.Contains(t, "TIME"):
		kind = contract.DateTime
	case t == "JSON" || t == "JSONB":
		kind = contract.JSON
	}
	return contract.Cell{Kind: kind, Text: string(v)}
}

func binaryType(typ string) bool {
	switch typ {
	case "BIT", "GEOMETRY", "POINT", "LINESTRING", "POLYGON", "MULTIPOINT", "MULTILINESTRING", "MULTIPOLYGON", "GEOMETRYCOLLECTION":
		return true
	}
	return strings.Contains(typ, "BLOB") || strings.Contains(typ, "BINARY") || typ == "BYTEA"
}
