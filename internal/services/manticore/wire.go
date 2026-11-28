package manticore

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/sqltext"
)

const previewBytes = 8 << 20
const previewRows = 10000

func execute(ctx context.Context, c driver.Conn, source string, index int, sink func(int, []contract.Column, []contract.Cell) error, budget *int) (contract.ResultSet, error) {
	tokens, err := sqltext.Tokens(source)
	if err != nil {
		return contract.ResultSet{}, contract.ErrUnsupported
	}
	first := ""
	for _, t := range tokens {
		if !t.Comment {
			first = strings.ToUpper(t.Text)
			break
		}
	}
	switch first {
	case "SELECT", "SHOW", "DESCRIBE", "DESC", "EXPLAIN", "CALL":
		return read(ctx, c, source, index, sink, budget)
	default:
		// ExecContext returns the original OK packet's affected count. Using
		// QueryContext here would discard it. No database/sql retry layer is used.
		r, e := c.(driver.ExecerContext).ExecContext(ctx, source, nil)
		if e != nil {
			return contract.ResultSet{}, e
		}
		count, e := r.RowsAffected()
		return contract.ResultSet{AffectedRows: count}, e
	}
}

func read(ctx context.Context, c driver.Conn, source string, index int, sink func(int, []contract.Column, []contract.Cell) error, budget *int) (set contract.ResultSet, err error) {
	rows, err := c.(driver.QueryerContext).QueryContext(ctx, source, nil)
	if err != nil {
		return set, err
	}
	// Close acknowledges the original driver before the caller can replace or
	// release its owner. Cancellation remains attached until rows are consumed.
	defer func() { err = errors.Join(err, rows.Close()) }()
	types, ok := rows.(driver.RowsColumnTypeDatabaseTypeName)
	if !ok {
		return set, contract.ErrUnsupported
	}
	nullable, _ := rows.(driver.RowsColumnTypeNullable)
	for i, name := range rows.Columns() {
		col := contract.Column{Name: name, DatabaseType: types.ColumnTypeDatabaseTypeName(i)}
		if nullable != nil {
			col.Nullable, _ = nullable.ColumnTypeNullable(i)
		}
		set.Columns = append(set.Columns, col)
	}
	local := previewBytes
	if budget == nil {
		budget = &local
	}
	raw := make([]driver.Value, len(set.Columns))
	for {
		if e := ctx.Err(); e != nil {
			return set, e
		}
		e := rows.Next(raw)
		if e == io.EOF {
			break
		}
		if e != nil {
			return set, e
		}
		cells := make([]contract.Cell, len(raw))
		size := 0
		for i, value := range raw {
			cells[i], e = cell(value, set.Columns[i].DatabaseType)
			if e != nil {
				return set, e
			}
			size += len(cells[i].Text) + len(cells[i].Bytes) + 32
		}
		if sink != nil {
			copyCells := make([]contract.Cell, len(cells))
			for i := range cells {
				copyCells[i] = cells[i].Clone()
			}
			if e = sink(index, append([]contract.Column(nil), set.Columns...), copyCells); e != nil {
				return set, e
			}
		}
		if len(set.Rows) < previewRows && size <= *budget {
			set.Rows = append(set.Rows, cells)
			*budget -= size
		} else if len(set.Truncation) == 0 {
			set.Truncation = []string{"preview limit"}
		}
	}
	if next, ok := rows.(driver.RowsNextResultSet); ok && next.HasNextResultSet() {
		// A multi-result procedure needs a separate acceptance path. Never silently
		// present only its first result as complete.
		return set, contract.ErrUnsupported
	}
	return set, nil
}

func valueKind(typ string) contract.CellKind {
	switch strings.ToUpper(typ) {
	case "BIGINT", "UNSIGNED BIGINT", "INT", "UNSIGNED INT", "INTEGER", "UINT", "SMALLINT", "TINYINT":
		return contract.Integer
	case "FLOAT", "DOUBLE":
		return contract.Floating
	case "DECIMAL":
		return contract.Decimal
	case "JSON":
		return contract.JSON
	case "BINARY", "VARBINARY", "BLOB":
		return contract.Binary
	default:
		return contract.Text
	}
}
func cell(value driver.Value, typ string) (contract.Cell, error) {
	if value == nil {
		return contract.Cell{Kind: contract.Null}, nil
	}
	result := contract.Cell{Kind: valueKind(typ)}
	switch v := value.(type) {
	case []byte:
		if result.Kind == contract.Binary {
			result.Bytes = append([]byte{}, v...)
		} else {
			result.Text = string(v)
		}
	case string:
		result.Text = v
	case int64:
		result.Kind = contract.Integer
		result.Text = strconv.FormatInt(v, 10)
	case uint64:
		result.Kind = contract.Integer
		result.Text = strconv.FormatUint(v, 10)
	case float64:
		result.Kind = contract.Floating
		result.Text = strconv.FormatFloat(v, 'g', -1, 64)
	case float32:
		result.Kind = contract.Floating
		result.Text = strconv.FormatFloat(float64(v), 'g', -1, 32)
	default:
		return contract.Cell{}, fmt.Errorf("unsupported Manticore wire value")
	}
	return result, nil
}
