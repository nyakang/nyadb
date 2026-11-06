package sqlserver

import (
	"errors"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestDataQueryKeepsValuesBoundAndRejectsUnsupportedSemantics(t *testing.T) {
	table := &contract.TableStructure{Object: contract.ObjectRef{ProfileID: 1, Catalog: "db]x", Schema: "dbo", Name: "t"}, Columns: []contract.StructureColumn{{Name: "id", Type: "bigint", DataType: "bigint", Key: "PRI", ValueKind: contract.Integer}, {Name: "note]x", Type: "nvarchar(max)", DataType: "nvarchar", ValueKind: contract.Text}}}
	query, args, err := dataQuery(table, contract.DataOptions{Limit: 10, Filters: []contract.DataFilterRule{{Column: "note]x", Mode: "contains", Value: "x%_["}}}, nil, true)
	if err != nil || len(args) != 1 || args[0] != "%x~%~_~[%" || strings.Contains(query, "x%_[") || !strings.Contains(query, "[db]]x].[dbo].[t]") || !strings.Contains(query, "FETCH NEXT 11 ROWS ONLY") {
		t.Fatal(query, args, err)
	}
	for _, o := range []contract.DataOptions{{Limit: 201}, {Limit: 10, UseKeyset: true}, {Limit: 10, SortColumn: "unknown"}, {Limit: 10, Filters: []contract.DataFilterRule{{Column: "id", Mode: "regexp"}}}} {
		if _, _, err := dataQuery(table, o, nil, true); err == nil {
			t.Fatal("unsupported semantics accepted", o)
		}
	}
}
func TestSQLServerDecimalAndGeneratedIdentityValidation(t *testing.T) {
	scale := int64(20)
	col := contract.StructureColumn{DataType: "decimal", Type: "decimal(38,20)", ValueKind: contract.Decimal, NumericScale: &scale}
	if v, err := valueArgument(col, contract.DataValue{Text: "9007199254740993.12345678901234567890"}); err != nil || v != "9007199254740993.12345678901234567890" {
		t.Fatal(v, err)
	}
	for _, v := range []string{"1.000000000000000000001", "1/2", "1e20", "NaN"} {
		if _, err := valueArgument(col, contract.DataValue{Text: v}); !errors.Is(err, connections.ErrInvalid) {
			t.Fatal("precision loss accepted", v, err)
		}
	}
	if c := cell([]byte("1.00000000000000000001"), "DECIMAL"); c.Kind != contract.Decimal || c.Text != "1.00000000000000000001" {
		t.Fatal(c)
	}
}
