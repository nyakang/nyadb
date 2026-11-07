package doris

import (
	"bytes"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
)

const previewRows = 2000
const previewCellBytes = 4096
const previewBytes = 16 << 20
const completeCellBytes = 1 << 20

func valueKind(decl string) contract.CellKind {
	u := strings.ToUpper(decl)
	switch {
	case strings.HasPrefix(u, "LARGEINT"), strings.HasPrefix(u, "BIGINT"), strings.HasPrefix(u, "SMALLINT"), strings.HasPrefix(u, "TINYINT"), strings.HasPrefix(u, "INT"):
		return contract.Integer
	case strings.HasPrefix(u, "DECIMAL"):
		return contract.Decimal
	case strings.HasPrefix(u, "DOUBLE"), strings.HasPrefix(u, "FLOAT"):
		return contract.Floating
	case strings.HasPrefix(u, "DATE"), strings.HasPrefix(u, "TIME"):
		return contract.DateTime
	case u == "BOOLEAN", u == "BOOL":
		return contract.Boolean
	case strings.HasPrefix(u, "JSON"):
		return contract.JSON
	case u == "BITMAP", u == "HLL", strings.HasPrefix(u, "QUANTILE_STATE"), strings.HasPrefix(u, "AGG_STATE"), strings.HasPrefix(u, "VARBINARY"), strings.HasPrefix(u, "BINARY"):
		return contract.Binary
	default:
		return contract.Text
	}
}
func nativeCell(v any, decl string) (contract.Cell, error) {
	if v == nil {
		return contract.Cell{Kind: contract.Null}, nil
	}
	k := valueKind(decl)
	switch x := v.(type) {
	case int64:
		return contract.Cell{Kind: contract.Integer, Text: strconv.FormatInt(x, 10)}, nil
	case uint64:
		return contract.Cell{Kind: contract.Integer, Text: strconv.FormatUint(x, 10)}, nil
	case float64:
		bits := 64
		if strings.EqualFold(decl, "FLOAT") {
			bits = 32
		}
		return contract.Cell{Kind: contract.Floating, Text: strconv.FormatFloat(x, 'g', -1, bits)}, nil
	case []byte:
		if len(x) > completeCellBytes {
			return contract.Cell{}, errors.New("Doris complete field exceeds 1 MiB")
		}
		// MySQL CHAR packets do not expose Doris aggregate-state types. Preserve
		// arbitrary bytes; never pass invalid UTF-8 through a JSON string.
		if k == contract.Binary || !utf8.Valid(x) || strings.IndexFunc(string(x), func(r rune) bool { return unicode.IsControl(r) && r != '\r' && r != '\n' && r != '\t' }) >= 0 {
			return contract.Cell{Kind: contract.Binary, Bytes: bytes.Clone(x)}, nil
		}
		return contract.Cell{Kind: k, Text: string(x)}, nil
	case string:
		return nativeCell([]byte(x), decl)
	default:
		return contract.Cell{}, contract.ErrUnsupported
	}
}

// scanRows passes complete cells to the sink before retaining bounded previews.
// A declaration override is used by table grids; wire CHAR is ambiguous for
// LARGEINT, nested types and opaque states in ad-hoc SQL results.
func scanRows(rows *sql.Rows, index int, sink func(int, []contract.Column, []contract.Cell) error, budget *int, decls []string) (set contract.ResultSet, err error) {
	types, err := rows.ColumnTypes()
	if err != nil {
		return set, err
	}
	if len(types) > 512 || len(decls) > 0 && len(decls) != len(types) {
		return set, contract.ErrUnsupported
	}
	set.Columns = make([]contract.Column, len(types))
	for i, t := range types {
		d := t.DatabaseTypeName()
		if len(decls) > 0 {
			d = decls[i]
		}
		n, _ := t.Nullable()
		set.Columns[i] = contract.Column{Name: t.Name(), DatabaseType: d, Nullable: n}
	}
	reasons := map[string]bool{}
	for rows.Next() {
		values := make([]any, len(types))
		dest := make([]any, len(types))
		for i := range dest {
			dest[i] = &values[i]
		}
		if err = rows.Scan(dest...); err != nil {
			return set, err
		}
		cells := make([]contract.Cell, len(types))
		size := len(types) * 64
		for i, v := range values {
			cells[i], err = nativeCell(v, set.Columns[i].DatabaseType)
			if err != nil {
				return set, err
			}
		}
		if sink != nil {
			complete := make([]contract.Cell, len(cells))
			for i := range cells {
				complete[i] = cells[i].Clone()
			}
			if err = sink(index, append([]contract.Column(nil), set.Columns...), complete); err != nil {
				return set, err
			}
		}
		for i := range cells {
			if len(cells[i].Text) > previewCellBytes {
				n := previewCellBytes
				for n > 0 && !utf8.ValidString(cells[i].Text[:n]) {
					n--
				}
				cells[i].Text = strings.Clone(cells[i].Text[:n])
				cells[i].Truncated = true
			}
			if len(cells[i].Bytes) > previewCellBytes {
				cells[i].Bytes = bytes.Clone(cells[i].Bytes[:previewCellBytes])
				cells[i].Truncated = true
			}
			if cells[i].Truncated {
				reasons["cell"] = true
			}
			size += len(cells[i].Text) + len(cells[i].Bytes)
		}
		if len(set.Rows) >= previewRows {
			reasons["rows"] = true
			continue
		}
		if size > *budget {
			reasons["bytes"] = true
			continue
		}
		*budget -= size
		set.Rows = append(set.Rows, cells)
	}
	for _, k := range []string{"rows", "bytes", "cell"} {
		if reasons[k] {
			set.Truncation = append(set.Truncation, k)
		}
	}
	return set, rows.Err()
}
