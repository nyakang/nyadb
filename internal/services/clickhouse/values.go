package clickhouse

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/shopspring/decimal"
)

const previewRows = 2000
const previewCellBytes = 4096
const previewBytes = 16 << 20

func valueKind(decl string) contract.CellKind {
	for strings.HasPrefix(decl, "Nullable(") || strings.HasPrefix(decl, "LowCardinality(") {
		decl = decl[strings.IndexByte(decl, '(')+1 : len(decl)-1]
	}
	switch {
	case strings.HasPrefix(decl, "Int"), strings.HasPrefix(decl, "UInt"):
		return contract.Integer
	case strings.HasPrefix(decl, "Decimal"):
		return contract.Decimal
	case strings.HasPrefix(decl, "Float"):
		return contract.Floating
	case decl == "Bool":
		return contract.Boolean
	case strings.HasPrefix(decl, "Date"), strings.HasPrefix(decl, "Time"):
		return contract.DateTime
	case strings.HasPrefix(decl, "Array("), strings.HasPrefix(decl, "Map("), strings.HasPrefix(decl, "Tuple("), strings.HasPrefix(decl, "JSON"):
		return contract.JSON
	default:
		return contract.Text
	}
}
func nativeCell(v any, decl string) (contract.Cell, error) {
	if v == nil {
		return contract.Cell{Kind: contract.Null}, nil
	}
	r := reflect.ValueOf(v)
	for r.Kind() == reflect.Pointer || r.Kind() == reflect.Interface {
		if r.IsNil() {
			return contract.Cell{Kind: contract.Null}, nil
		}
		r = r.Elem()
	}
	v = r.Interface()
	k := valueKind(decl)
	switch x := v.(type) {
	case decimal.Decimal:
		return contract.Cell{Kind: contract.Decimal, Text: x.String()}, nil
	case big.Int:
		return contract.Cell{Kind: contract.Integer, Text: x.String()}, nil
	case time.Time:
		return contract.Cell{Kind: contract.DateTime, Text: x.Format(time.RFC3339Nano)}, nil
	case []byte:
		if k == contract.JSON {
			values := make([]uint16, len(x))
			for i, n := range x {
				values[i] = uint16(n)
			}
			raw, _ := json.Marshal(values)
			return contract.Cell{Kind: k, Text: string(raw)}, nil
		}
		return contract.Cell{Kind: contract.Binary, Bytes: bytes.Clone(x)}, nil
	case string:
		if !utf8.ValidString(x) || strings.ContainsRune(x, 0) {
			return contract.Cell{Kind: contract.Binary, Bytes: []byte(x)}, nil
		}
		return contract.Cell{Kind: k, Text: x}, nil
	}
	switch r.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return contract.Cell{Kind: contract.Integer, Text: strconv.FormatInt(r.Int(), 10)}, nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return contract.Cell{Kind: contract.Integer, Text: strconv.FormatUint(r.Uint(), 10)}, nil
	case reflect.Float32, reflect.Float64:
		return contract.Cell{Kind: contract.Floating, Text: strconv.FormatFloat(r.Float(), 'g', -1, r.Type().Bits())}, nil
	case reflect.Bool:
		return contract.Cell{Kind: contract.Boolean, Text: strconv.FormatBool(r.Bool())}, nil
	}
	// UUID/IP and other explicitly stringable native scalar types keep their
	// canonical value. Composite values use JSON without float64 decoding.
	if x, ok := v.(fmt.Stringer); ok && k != contract.JSON {
		return contract.Cell{Kind: k, Text: x.String()}, nil
	}
	if k == contract.JSON {
		raw, err := json.Marshal(v)
		if err == nil {
			return contract.Cell{Kind: k, Text: string(raw)}, nil
		}
	}
	return contract.Cell{}, contract.ErrUnsupported
}

func scanRows(rows driver.Rows, index int, sink func(int, []contract.Column, []contract.Cell) error, budget *int) (set contract.ResultSet, err error) {
	names, types := rows.Columns(), rows.ColumnTypes()
	if len(names) > 512 || len(types) != len(names) {
		return set, errors.New("ClickHouse result column limit exceeded")
	}
	set.Columns = make([]contract.Column, len(names))
	for i, n := range names {
		set.Columns[i] = contract.Column{Name: n, DatabaseType: types[i].DatabaseTypeName(), Nullable: types[i].Nullable()}
	}
	reasons := map[string]bool{}
	for rows.Next() {
		dest := make([]any, len(types))
		for i, t := range types {
			dest[i] = reflect.New(t.ScanType()).Interface()
		}
		if err = rows.Scan(dest...); err != nil {
			return set, err
		}
		if sink == nil && len(set.Rows) >= previewRows {
			reasons["rows"] = true
			continue
		}
		cells := make([]contract.Cell, len(types))
		size := 64 * len(types)
		for i, v := range dest {
			cells[i], err = nativeCell(v, set.Columns[i].DatabaseType)
			if err != nil {
				return set, err
			}
		}
		if sink != nil {
			if err = sink(index, set.Columns, cells); err != nil {
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
	for _, key := range []string{"rows", "bytes", "cell"} {
		if reasons[key] {
			set.Truncation = append(set.Truncation, key)
		}
	}
	return set, rows.Err()
}
