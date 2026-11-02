package clickhouse

import (
	"encoding/json"
	"errors"
	"math/big"
	"reflect"
	"strings"
	"testing"

	ch "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/shopspring/decimal"
)

func TestNativeCellsRetainPrecisionNullBinaryAndCompositeValues(t *testing.T) {
	var empty *uint64
	wide, _ := new(big.Int).SetString("170141183460469231731687303715884105727", 10)
	for _, tc := range []struct {
		value   any
		decl    string
		kind    contract.CellKind
		display string
	}{
		{empty, "Nullable(UInt64)", contract.Null, "NULL"},
		{uint64(18446744073709551615), "UInt64", contract.Integer, "18446744073709551615"},
		{wide, "Int128", contract.Integer, wide.String()},
		{decimal.RequireFromString("123456789012345678.1234567890123456789"), "Decimal(38,20)", contract.Decimal, "123456789012345678.1234567890123456789"},
		{"NULL", "String", contract.Text, "NULL"},
		{"", "String", contract.Text, ""},
		{"\xff\x00", "String", contract.Binary, "0xff00"},
		{[]byte{}, "String", contract.Binary, "0x"},
		{[]byte{0, 255}, "Array(UInt8)", contract.JSON, "[0,255]"},
		{[]uint64{18446744073709551615}, "Array(UInt64)", contract.JSON, "[18446744073709551615]"},
	} {
		cell, err := nativeCell(tc.value, tc.decl)
		if err != nil || cell.Kind != tc.kind || cell.Display() != tc.display {
			t.Fatalf("type %s: %+v %v", tc.decl, cell, err)
		}
	}
	if _, err := nativeCell(make(chan int), "UnsupportedState"); !errors.Is(err, contract.ErrUnsupported) {
		t.Fatal("unsupported native value silently displayed")
	}
}

func TestNativeStatementsProtectEscapesAndRecognizeCommentedReads(t *testing.T) {
	parts, err := statements("-- heading\nSELECT 'a\\';b'; SELECT `odd;name` FROM `table`; /* next */ EXPLAIN SELECT 1;")
	if err != nil || len(parts) != 3 {
		t.Fatalf("statements %d %v", len(parts), err)
	}
	for _, q := range parts {
		if !returnsRows(q) {
			t.Fatal("read with comments classified as mutation")
		}
	}
	if returnsRows("/* INSERT source */ INSERT INTO t SELECT 1") {
		t.Fatal("insert classified as read")
	}
	for _, q := range []string{"SELECT '", strings.Repeat("x", 8<<20+1)} {
		if _, err = statements(q); err == nil {
			t.Fatal("invalid or oversized SQL accepted")
		}
	}
}

func TestNativeDataQueryValidatesIdentityFiltersAndNeverTreatsSortingKeysAsUnique(t *testing.T) {
	table := &contract.TableStructure{Object: contract.ObjectRef{ProfileID: 1, Catalog: "odd`db\\name", Name: "t`name", Kind: "TABLE"}, Columns: []contract.StructureColumn{{Name: "id"}, {Name: "name"}}}
	options := contract.DataOptions{Limit: 50, Offset: 2, Filters: []contract.DataFilterRule{{Column: "name", Mode: "equal", Value: "private ' value"}}, Sorts: []contract.DataSortRule{{Column: "id", Descending: true}}}
	q, args, err := dataQuery(table, options, nil, true)
	if err != nil || strings.Contains(q, "private") || !strings.Contains(q, "`odd\\`db\\\\name`.`t\\`name`") || !strings.Contains(q, "LIMIT 51 OFFSET 2") || !reflect.DeepEqual(args, []any{"private ' value"}) {
		t.Fatalf("parameterized SQL invalid: %v", err)
	}
	options.Filters[0].Column = "name` OR 1=1"
	if _, _, err = dataQuery(table, options, nil, true); err == nil {
		t.Fatal("unknown filter field accepted")
	}
	options.Filters = nil
	options.UseKeyset = true
	if _, _, err = dataQuery(table, options, nil, true); !errors.Is(err, contract.ErrUnsupported) {
		t.Fatal("unsupported keyset accepted")
	}
	options.UseKeyset = false
	options.Limit = 1001
	if _, _, err = dataQuery(table, options, nil, true); err == nil {
		t.Fatal("page bound bypassed")
	}
}

func TestNativeErrorsDoNotExposeServerSQLOrSecrets(t *testing.T) {
	err := safeError(&ch.Exception{Code: 62, Message: "private-password SELECT secret_value"})
	if err == nil || strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "SELECT") || !strings.Contains(err.Error(), "62") {
		t.Fatal("server exception leaked")
	}
	cell, _ := nativeCell([]byte{}, "String")
	raw, _ := json.Marshal(cell)
	if !strings.Contains(string(raw), `"bytes":""`) {
		t.Fatal("empty binary became absent")
	}
}
