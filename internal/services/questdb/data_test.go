package questdb

import (
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestNativeRangePagingTypedLongAndUnsupportedIdentity(t *testing.T) {
	table := &contract.TableStructure{Name: "rows Ω", Columns: []contract.StructureColumn{{Name: "seq", DataType: "LONG"}, {Name: "sym", DataType: "SYMBOL"}}}
	q, args, err := dataQuery(table, contract.DataOptions{Offset: 3, Limit: 5, FilterColumn: "seq", FilterValue: "9007199254740993", SortColumn: "sym"}, nil, true)
	if err != nil || !strings.HasSuffix(q, `ORDER BY "sym" ASC LIMIT 3,9`) || len(args) != 1 || args[0] != int64(9007199254740993) || strings.Contains(q, "9007199254740993") {
		t.Fatal("native range or typed binding changed", q, args, err)
	}
	for _, opts := range []contract.DataOptions{{UseKeyset: true}, {Offset: math.MaxInt64, Limit: 1}, {Limit: 1, FilterColumn: "seq", FilterMode: "like"}, {Limit: 1, SortColumn: "unknown"}} {
		if _, _, err := dataQuery(table, opts, nil, true); err == nil {
			t.Fatal("unsupported request admitted")
		}
	}
	for _, raw := range []string{"SELECT 'UPDATE; DROP TABLE t'", "-- UPDATE\nSELECT 1", "WITH x AS (SELECT 1) SELECT * FROM x"} {
		if !readonly(raw) {
			t.Fatal("read expression rejected")
		}
	}
	for _, raw := range []string{"UPDATE t SET v=1", "WITH x AS (SELECT 1) INSERT INTO t SELECT * FROM x", "EXPLAIN UPDATE t SET v=1"} {
		if readonly(raw) {
			t.Fatal("write admitted by readonly guard")
		}
	}
	c, err := nativeCell([]byte("9007199254740993"), pgtype.Int8OID)
	if err != nil || c.Text != "9007199254740993" || c.Kind != contract.Integer {
		t.Fatal("native integer changed")
	}
	if c, err = nativeCell(nil, pgtype.TextOID); err != nil || c.Kind != contract.Null {
		t.Fatal("null changed")
	}
	if _, err = nativeCell([]byte(`\xzz`), pgtype.ByteaOID); err == nil {
		t.Fatal("invalid binary admitted")
	}
	if _, err = filterArgument(contract.StructureColumn{DataType: "LONG256"}, "1"); !errors.Is(err, contract.ErrUnsupported) {
		t.Fatal("unverified LONG256 filter admitted")
	}
}
