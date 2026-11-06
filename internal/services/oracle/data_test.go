package oracle

import (
	"errors"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	goora "github.com/sijms/go-ora/v2"
	"strings"
	"testing"
)

func TestOracleNumberRejectsSilentRounding(t *testing.T) {
	scale, precision := int64(20), int64(38)
	c := contract.StructureColumn{DataType: "NUMBER", ValueKind: contract.Decimal, NumericScale: &scale, NumericPrecision: &precision}
	value, err := valueArgument(c, contract.DataValue{Text: "9007199254740993.12345678901234567890"})
	if err != nil {
		t.Fatal(err)
	}
	text, err := value.(*goora.Number).String()
	if err != nil || text != "9007199254740993.1234567890123456789" {
		t.Fatal(text, err)
	}
	for _, text := range []string{"1.000000000000000000001", "1000000000000000000", "1/2", "NaN", "", "1e999", "1e-131", "123456789012345678901234567890123456789"} {
		if _, err = valueArgument(c, contract.DataValue{Text: text}); !errors.Is(err, connections.ErrInvalid) {
			t.Fatal("lossy number accepted", text, err)
		}
	}
}
func TestOracleQueryBindsAndBoundsValues(t *testing.T) {
	table := &contract.TableStructure{Object: contract.ObjectRef{ProfileID: 1, Catalog: "XEPDB1", Schema: "tenant", Name: `t"x`}, Columns: []contract.StructureColumn{{Name: "id", DataType: "NUMBER", ValueKind: contract.Decimal}, {Name: "note", DataType: "VARCHAR2", ValueKind: contract.Text}, {Name: "payload", DataType: "RAW", ValueKind: contract.Binary}}}
	query, args, err := dataQuery(table, contract.DataOptions{Limit: 10, Filters: []contract.DataFilterRule{{Column: "note", Mode: "contains", Value: "x%_["}}}, nil, true)
	if err != nil || len(args) != 1 || args[0] != "%x~%~_[%" || strings.Contains(query, "x%_") || !strings.Contains(query, `"tenant"."t""x"`) || !strings.Contains(query, "UTL_RAW.SUBSTR") || !strings.Contains(query, "FETCH NEXT 11 ROWS ONLY") {
		t.Fatal(query, args, err)
	}
	for _, opts := range []contract.DataOptions{{Limit: 10, UseKeyset: true}, {Limit: 10, SortColumn: "bad"}, {Limit: 201}, {Limit: 10, Filters: []contract.DataFilterRule{{Column: "id", Mode: "regexp"}}}} {
		if _, _, err := dataQuery(table, opts, nil, true); err == nil {
			t.Fatal(opts)
		}
	}
}
