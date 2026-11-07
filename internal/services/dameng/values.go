package dameng

import (
	"bytes"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func typeKind(t string) contract.CellKind {
	t = strings.ToUpper(t)
	if at := strings.IndexByte(t, '('); at >= 0 {
		t = t[:at]
	}
	switch t {
	case "DECIMAL", "NUMERIC", "NUMBER", "DEC":
		return contract.Decimal
	case "INT", "INTEGER", "BIGINT", "SMALLINT", "TINYINT", "ROWID":
		return contract.Integer
	case "BIT", "BOOLEAN":
		return contract.Boolean
	case "REAL", "DOUBLE", "FLOAT":
		return contract.Floating
	case "BINARY", "VARBINARY", "BLOB", "IMAGE":
		return contract.Binary
	case "DATE", "TIME", "TIMESTAMP", "TIME WITH TIME ZONE", "TIMESTAMP WITH TIME ZONE":
		return contract.DateTime
	}
	return contract.Text
}

func cell(v any, typ string) (contract.Cell, error) {
	if v == nil {
		return contract.Cell{Kind: contract.Null}, nil
	}
	c := contract.Cell{Kind: typeKind(typ)}
	switch v := v.(type) {
	case []byte:
		if c.Kind == contract.Binary {
			c.Bytes = bytes.Clone(v)
		} else {
			c.Text = string(v)
		}
	case string:
		c.Text = v
	case int64:
		c.Text = strconv.FormatInt(v, 10)
		if c.Kind == contract.Boolean {
			if v != 0 && v != 1 {
				return c, contract.ErrUnsupported
			}
			c.Text = strconv.FormatBool(v == 1)
		}
	case float64:
		c.Text = strconv.FormatFloat(v, 'g', -1, 64)
	default:
		return c, contract.ErrUnsupported
	}
	if c.Kind != contract.Binary && !utf8.ValidString(c.Text) {
		return c, errors.New("DM8 result text is not valid UTF-8")
	}
	return c, nil
}

func preview(c contract.Cell) contract.Cell {
	const limit = 64 << 10
	if len(c.Text) > limit {
		at := limit
		for at > 0 && !utf8.RuneStart(c.Text[at]) {
			at--
		}
		c.Text, c.Truncated = strings.Clone(c.Text[:at]), true
	}
	if len(c.Bytes) > limit {
		c.Bytes, c.Truncated = bytes.Clone(c.Bytes[:limit]), true
	}
	return c
}

func scanRows(rows *sql.Rows, index int, sink func(int, []contract.Column, []contract.Cell) error, budget *int, maxRows int) (contract.ResultSet, error) {
	names, err := rows.Columns()
	if err != nil {
		return contract.ResultSet{}, err
	}
	types, err := rows.ColumnTypes()
	if err != nil || len(names) > 4096 {
		return contract.ResultSet{}, errors.Join(err, contract.ErrUnsupported)
	}
	set := contract.ResultSet{Columns: make([]contract.Column, len(names))}
	for i, name := range names {
		nullable, _ := types[i].Nullable()
		set.Columns[i] = contract.Column{Name: name, DatabaseType: types[i].DatabaseTypeName(), Nullable: nullable}
	}
	rowLimit, byteLimit, fieldLimit := false, false, false
	for rows.Next() {
		raw, dest := make([]any, len(names)), make([]any, len(names))
		for i := range raw {
			dest[i] = &raw[i]
		}
		if err = rows.Scan(dest...); err != nil {
			return set, err
		}
		values := make([]contract.Cell, len(names))
		for i, v := range raw {
			if values[i], err = cell(v, set.Columns[i].DatabaseType); err != nil {
				return set, err
			}
		}
		if sink != nil {
			// A callback owns its copies. It cannot mutate the cached result's
			// columns, full values or binary buffers retained by this query.
			copy := make([]contract.Cell, len(values))
			for i, c := range values {
				copy[i] = c.Clone()
			}
			if err = sink(index, append([]contract.Column(nil), set.Columns...), copy); err != nil {
				return set, err
			}
		}
		size := 24 + len(values)*64
		for i := range values {
			values[i] = preview(values[i])
			fieldLimit = fieldLimit || values[i].Truncated
			size += len(values[i].Text) + len(values[i].Bytes)
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
		set.Rows = append(set.Rows, values)
	}
	if rowLimit {
		set.Truncation = append(set.Truncation, "rows")
	}
	if byteLimit {
		set.Truncation = append(set.Truncation, "bytes")
	}
	if fieldLimit {
		set.Truncation = append(set.Truncation, "cells")
	}
	return set, rows.Err()
}
