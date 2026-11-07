package importspec_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/importspec"
)

func TestDamengImportTypedDDLAndUnsafeDeclarations(t *testing.T) {
	columns := []contract.DataImportColumn{{Name: `租户 "`, Type: "number ( 38, 20 )", PrimaryKey: true}, {Name: "V", Type: "varchar ( 8000 )", Nullable: true}, {Name: "D", Type: "timestamp ( 6 ) with time zone", Nullable: true}}
	ddl, err := importspec.DDL("dameng", `架构 "`, `表 "`, columns)
	if err != nil || !strings.Contains(ddl, `"租户 """ DECIMAL(38,20) NOT NULL`) || !strings.Contains(ddl, "TIMESTAMP(6) WITH TIME ZONE") || !strings.Contains(ddl, `PRIMARY KEY ("租户 """)`) {
		t.Fatal("quoted typed native DDL", ddl, err)
	}
	for _, typ := range []string{"VARCHAR(8001)", "DECIMAL(39,0)", "DECIMAL(10,11)", "TIMESTAMP(7)", "TIMESTAMP(6,1)", "INT); DROP TABLE X;--", "CLOB DEFAULT 'x'", "VARCHAR(3) WITH TIME ZONE"} {
		if _, err := importspec.Columns("dameng", []contract.DataImportColumn{{Name: "V", Type: typ}}); !errors.Is(err, contract.ErrDataPage) {
			t.Fatal("unsafe or unverified type accepted", typ, err)
		}
	}
	for _, name := range []string{"ROWID", "rowid", "", "x\x00y"} {
		if _, err := importspec.Columns("dameng", []contract.DataImportColumn{{Name: name, Type: "INT"}}); err == nil {
			t.Fatal("unsafe target column accepted", name)
		}
	}
	inferred := importspec.AdaptInference("dameng", []contract.DataImportColumn{{Name: "N", Type: "DECIMAL(65,30)"}, {Name: "B", Type: "LONGBLOB"}, {Name: "T", Type: "DATETIME"}})
	if inferred[0].Type != "CLOB" || inferred[1].Type != "BLOB" || inferred[2].Type != "TIMESTAMP(6)" {
		t.Fatal("lossless inferred type adaptation", inferred)
	}
}

func TestDamengImportIdentityIsLookupOnlyAndValuesRemainTyped(t *testing.T) {
	cols := []contract.StructureColumn{{Name: "ID", DataType: "BIGINT", Extra: "IDENTITY", Generated: true}, {Name: "V", DataType: "VARCHAR", Nullable: true}, {Name: "G", Generated: true}}
	validate := func(mode string, keys []string, values map[string]contract.DataValue) error {
		return importspec.Validate("dameng", cols, contract.DataImportRequest{Mode: mode, MatchColumns: keys, Edits: []contract.DataEdit{{Kind: "insert", Values: values}}})
	}
	if err := validate("update", []string{"ID"}, map[string]contract.DataValue{"ID": {Text: "9007199254740993"}, "V": {Text: "ok"}}); err != nil {
		t.Fatal("identity lookup rejected", err)
	}
	if err := validate("append", nil, map[string]contract.DataValue{"ID": {Default: true}}); err != nil {
		t.Fatal("server generated default rejected", err)
	}
	for _, values := range []map[string]contract.DataValue{{"ID": {Text: "1"}}, {"G": {Default: true}}, {"V": {Default: true, Text: "injected"}}, {"V": {Text: string([]byte{255})}}, {"V": {Null: true, Text: "ambiguous"}}} {
		if err := validate("append", nil, values); err == nil {
			t.Fatal("unsafe generated or ambiguous values accepted")
		}
	}
	if err := validate("update", []string{"ID"}, map[string]contract.DataValue{"ID": {Default: true}}); !errors.Is(err, contract.ErrDataReadOnly) {
		t.Fatal("default used as match key", err)
	}
	if err := validate("append", nil, map[string]contract.DataValue{"V": {Text: strings.Repeat("x", contract.DataEditMaxBytes+1)}}); !errors.Is(err, contract.ErrDataEditLimit) {
		t.Fatal("complete field limit missing", err)
	}
}
