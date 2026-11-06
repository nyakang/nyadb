package duckdb

import (
	"bytes"
	"database/sql"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func typeKind(t string) contract.CellKind {
	t = strings.ToUpper(t)
	switch {
	case t == "BLOB":
		return contract.Binary
	case t == "BOOLEAN":
		return contract.Boolean
	case t == "JSON":
		return contract.JSON
	case strings.HasPrefix(t, "DECIMAL("):
		return contract.Decimal
	case t == "TINYINT" || t == "SMALLINT" || t == "INTEGER" || t == "BIGINT" || t == "HUGEINT" || t == "UTINYINT" || t == "USMALLINT" || t == "UINTEGER" || t == "UBIGINT" || t == "UHUGEINT" || t == "BIGNUM":
		return contract.Integer
	case t == "FLOAT" || t == "DOUBLE":
		return contract.Floating
	case t == "DATE" || strings.HasPrefix(t, "TIME"):
		return contract.DateTime
	default:
		return contract.Text
	}
}
func streamSink(sink func(int, []contract.Column, []contract.Cell) error) func(int, []contract.Column, []contract.Cell) error {
	return sink
}
func valueLength(c contract.StructureColumn) string {
	if c.ValueKind == contract.Binary {
		return "octet_length(" + quote(c.Name) + ")"
	}
	return "octet_length(encode(CAST(" + quote(c.Name) + " AS VARCHAR)))"
}
func cell(v any, decl string) contract.Cell {
	if v == nil {
		return contract.Cell{Kind: contract.Null}
	}
	c := contract.Cell{Kind: typeKind(decl)}
	if b, ok := v.([]byte); ok {
		if c.Kind == contract.Binary {
			c.Bytes = bytes.Clone(b)
		} else {
			c.Text = string(b)
		}
	} else {
		c.Text = fmt.Sprint(v)
	}
	return c
}
func previewCell(c contract.Cell, limit int) contract.Cell {
	if len(c.Text) > limit {
		at := limit
		for at > 0 && !utf8.RuneStart(c.Text[at]) {
			at--
		}
		c.Text = strings.Clone(c.Text[:at])
		c.Truncated = true
	}
	if len(c.Bytes) > limit {
		c.Bytes = bytes.Clone(c.Bytes[:limit])
		c.Truncated = true
	}
	return c
}
func previewValue(v any, decl string, limit int) contract.Cell {
	return previewCell(cell(v, decl), limit)
}
func scanRows(rows *sql.Rows, index int, sink func(int, []contract.Column, []contract.Cell) error, budget *int, maxRows, maxCell int) (contract.ResultSet, error) {
	names, err := rows.Columns()
	if err != nil {
		return contract.ResultSet{}, err
	}
	cts, err := rows.ColumnTypes()
	if err != nil {
		return contract.ResultSet{}, err
	}
	if len(names) > 512 {
		return contract.ResultSet{}, contract.ErrUnsupported
	}
	set := contract.ResultSet{Columns: make([]contract.Column, len(names))}
	for i, name := range names {
		nullable, _ := cts[i].Nullable()
		set.Columns[i] = contract.Column{Name: name, DatabaseType: cts[i].DatabaseTypeName(), Nullable: nullable}
	}
	rowLimit, byteLimit, cellLimit := false, false, false
	for rows.Next() {
		raw := make([]any, len(names))
		dest := make([]any, len(names))
		for i := range raw {
			dest[i] = &raw[i]
		}
		if err = rows.Scan(dest...); err != nil {
			return set, err
		}
		cells := make([]contract.Cell, len(raw))
		for i, v := range raw {
			cells[i] = cell(v, set.Columns[i].DatabaseType)
		}
		if sink != nil {
			if err = sink(index, set.Columns, cells); err != nil {
				return set, err
			}
		}
		size := 24 + len(raw)*64
		for i := range cells {
			cells[i] = previewCell(cells[i], maxCell)
			cellLimit = cellLimit || cells[i].Truncated
			size += len(cells[i].Text) + len(cells[i].Bytes)
		}
		if len(set.Rows) >= maxRows {
			rowLimit = true
			continue
		}
		if size > *budget {
			byteLimit = true
			continue
		}
		*budget -= size
		set.Rows = append(set.Rows, cells)
	}
	if rowLimit {
		set.Truncation = append(set.Truncation, "rows")
	}
	if byteLimit {
		set.Truncation = append(set.Truncation, "bytes")
	}
	if cellLimit {
		set.Truncation = append(set.Truncation, "cells")
	}
	return set, rows.Err()
}
