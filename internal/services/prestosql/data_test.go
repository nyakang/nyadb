package prestosql

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestDataLiteralPreservesOriginalTypes(t *testing.T) {
	cases := []struct {
		typ, input, want string
		kind             contract.CellKind
	}{
		{`row(名 bigint, MixedCase varchar)`, `[9223372036854775807,"a'b"]`, `row("名" bigint,"MixedCase" varchar)`, contract.JSON},
		{`array(json)`, `["null",null,"{\"n\":9007199254740993}"]`, `json_parse('{"n":9007199254740993}')`, contract.JSON},
		{`decimal(38,20)`, `9007199254740993.12345678901234567890`, `CAST('9007199254740993.12345678901234567890' AS decimal(38,20))`, contract.Decimal},
		{`time(3)`, `12:34:56.789`, `CAST('12:34:56.789' AS time(3))`, contract.DateTime},
		{`timestamp(3) with time zone`, `2024-02-29 12:34:56.789 UTC`, `CAST('2024-02-29 12:34:56.789 UTC' AS timestamp(3) with time zone)`, contract.DateTime},
		{`map(varchar,array(bigint))`, `{"猫":[9223372036854775807,null]}`, `CAST('9223372036854775807' AS bigint)`, contract.JSON},
	}
	for _, c := range cases {
		t.Run(c.typ, func(t *testing.T) {
			typ, err := parseInsertType(c.typ)
			if err != nil {
				t.Fatal(err)
			}
			got, err := insertLiteral(typ, contract.DataValue{Kind: c.kind, Text: c.input})
			if err != nil || !strings.Contains(got, c.want) {
				t.Fatal(got, err)
			}
		})
	}
	for _, c := range []struct {
		typ, input string
		kind       contract.CellKind
	}{
		{`decimal(5,2)`, `1234.56`, contract.Decimal}, {`time(3)`, `12:34:56.7891`, contract.DateTime},
		{`map(varchar,bigint)`, `{"a":1,"a":2}`, contract.JSON}, {`array(json)`, `[{"n":1}]`, contract.JSON},
		{`bigint`, `1); DROP TABLE t`, contract.Integer}, {`varchar(2)`, `猫猫猫`, contract.Text},
	} {
		typ, err := parseInsertType(c.typ)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = insertLiteral(typ, contract.DataValue{Kind: c.kind, Text: c.input}); err == nil {
			t.Fatal("lossy or ambiguous value accepted", c.typ)
		}
	}
	for _, typ := range []string{`row("x" bigint); DROP TABLE t`, `decimal(39,0)`, `array(bigint,varchar)`, `row(名 bigint, x mystery)`, strings.Repeat("array(", 18) + "bigint" + strings.Repeat(")", 18)} {
		if _, err := parseInsertType(typ); err == nil {
			t.Fatal("unsafe type accepted", typ)
		}
	}
}

func TestDataProjectionDistinguishesNullAndBounds(t *testing.T) {
	m := &contract.TableStructure{Columns: []contract.StructureColumn{{Name: "j", Type: "json"}, {Name: "b", Type: "varbinary"}, {Name: "n", Type: "decimal(38,20)"}}}
	p, err := newDataProjection(m, 1024)
	if err != nil {
		t.Fatal(err)
	}
	cell := func(value any, truncated bool) json.RawMessage {
		envelope, _ := json.Marshal([]any{value, truncated})
		raw, _ := json.Marshal(string(envelope))
		return raw
	}
	result := queryResult{completed: true, columns: []string{"__nyadb_cell_0", "__nyadb_cell_1", "__nyadb_cell_2"}, rows: [][]json.RawMessage{{cell("null", false), cell("", false), cell("9007199254740993.12345678901234567890", false)}, {cell(nil, false), cell("AP+A", false), cell(nil, false)}}}
	rows, err := p.decode(result)
	if err != nil {
		t.Fatal(err)
	}
	if rows[0][0].Null || rows[0][0].Value.Kind != contract.JSON || rows[0][0].Value.Text != "null" || !rows[1][0].Null || rows[0][1].Value.Bytes == nil || len(rows[0][1].Value.Bytes) != 0 || string(rows[1][1].Value.Bytes) != string([]byte{0, 255, 128}) || rows[0][2].Text != "9007199254740993.12345678901234567890" {
		t.Fatal("typed projection lost values", rows)
	}
	result.rows[0][0] = cell(strings.Repeat("猫", 1025), true)
	if _, err = p.decode(result); !errors.Is(err, ErrResponse) {
		t.Fatal("oversized envelope accepted", err)
	}
	result.rows[0][0] = cell(nil, true)
	if _, err = p.decode(result); !errors.Is(err, ErrResponse) {
		t.Fatal("truncated SQL NULL accepted", err)
	}
}

func TestDataQueryQuotesOriginalIdentityAndTypedPredicates(t *testing.T) {
	m := &contract.TableStructure{Object: contract.ObjectRef{ProfileID: 65, Catalog: "mem", Schema: "def", Kind: "table", Name: `t";DROP TABLE other;--`}, Columns: []contract.StructureColumn{{Name: `n"`, Type: "bigint"}, {Name: "s", Type: "varchar"}}}
	p, err := newDataProjection(m, 1024)
	if err != nil {
		t.Fatal(err)
	}
	sql, err := dataQuery(m, p, contract.DataOptions{Limit: 2, Offset: 3, FilterColumn: "s", FilterValue: `x' OR true --`, SortColumn: `n"`})
	if err != nil || !strings.Contains(sql, `FROM "mem"."def"."t"";DROP TABLE other;--"`) || !strings.Contains(sql, `WHERE "__nyadb_data"."s" = CAST('x'' OR true --' AS varchar)`) || !strings.Contains(sql, `ORDER BY "__nyadb_data"."n""" ASC OFFSET 3 LIMIT 3`) {
		t.Fatal(sql, err)
	}
	if _, err = dataQuery(m, p, contract.DataOptions{Limit: 2, FilterColumn: `n"`, FilterValue: `0 OR true`}); err == nil {
		t.Fatal("numeric injection accepted")
	}
}

func TestConsumedDataPlanCloseKeepsAdmissionUntilSettlement(t *testing.T) {
	l := &dataLease{}
	p := &dataWritePlan{owner: l, consumed: true, statements: []string{"original"}}
	l.plan = p
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if l.plan != p || !p.closed || p.statements != nil {
		t.Fatal("closing consumed plan admitted concurrent write")
	}
	l.plan = nil
	p = &dataWritePlan{owner: l}
	l.plan = p
	_ = p.Close()
	if l.plan != nil {
		t.Fatal("unused preview failed to release admission")
	}
}
