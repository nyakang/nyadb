package trino

import (
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestInsertLiteralsPreserveNativeLexemesAndRejectCoercion(t *testing.T) {
	cases := []struct {
		decl     string
		value    contract.DataValue
		contains string
	}{
		{"bigint", contract.DataValue{Kind: contract.Integer, Text: "9007199254740993"}, "'9007199254740993'"},
		{"decimal(38,20)", contract.DataValue{Kind: contract.Decimal, Text: "123456789012345678.12345678901234567890"}, "123456789012345678.12345678901234567890"},
		{"varchar", contract.DataValue{Kind: contract.Text, Text: "中文'quote"}, "中文''quote"},
		{"varbinary", contract.DataValue{Kind: contract.Binary, Bytes: []byte{0, 255}}, "X'00ff'"},
		{"timestamp(12)", contract.DataValue{Kind: contract.DateTime, Text: "2026-11-16 01:02:03.123456789123"}, ".123456789123"},
		{"array(bigint)", contract.DataValue{Kind: contract.JSON, Text: "[9007199254740993,null]"}, "'9007199254740993'"},
		{"map(varchar,decimal(38,20))", contract.DataValue{Kind: contract.JSON, Text: `{"中文":"123456789012345678.12345678901234567890"}`}, "123456789012345678.12345678901234567890"},
		{`row("UPPER" bigint,s varchar)`, contract.DataValue{Kind: contract.JSON, Text: `[9007199254740993,"中文"]`}, `"UPPER"`},
		{"array(varbinary)", contract.DataValue{Kind: contract.JSON, Text: `["AP8="]`}, "X'00ff'"},
	}
	for _, c := range cases {
		t.Run(c.decl, func(t *testing.T) {
			typ, err := parseInsertType(c.decl)
			if err != nil {
				t.Fatal(err)
			}
			s, err := insertLiteral(typ, c.value)
			if err != nil || !strings.Contains(s, c.contains) {
				t.Fatal(s, err)
			}
		})
	}
	bad := []struct {
		decl string
		v    contract.DataValue
	}{
		{"tinyint", contract.DataValue{Kind: contract.Integer, Text: "128"}},
		{"decimal(4,2)", contract.DataValue{Kind: contract.Decimal, Text: "1.234"}},
		{"decimal(4,2)", contract.DataValue{Kind: contract.Decimal, Text: "123.00"}},
		{"timestamp(6)", contract.DataValue{Kind: contract.DateTime, Text: "2026-11-16 01:02:03.123456789"}},
		{"varchar(1)", contract.DataValue{Kind: contract.Text, Text: "中文"}},
		{"json", contract.DataValue{Kind: contract.JSON, Text: `{"k":1,"k":2}`}},
		{"array(bigint)", contract.DataValue{Kind: contract.JSON, Text: `[9223372036854775808]`}},
		{"varchar", contract.DataValue{Kind: contract.Text, Default: true}},
		{"varchar", contract.DataValue{Kind: contract.Text, Null: true, Text: "hidden"}},
	}
	for _, c := range bad {
		typ, err := parseInsertType(c.decl)
		if err != nil {
			t.Fatal(err)
		}
		if s, err := insertLiteral(typ, c.v); err == nil {
			t.Fatalf("unsafe literal accepted: %s", s)
		}
	}
	for _, decl := range []string{"decimal(38,20)); DROP TABLE victim; --", "timestamp(6) /*x*/", `row("broken bigint)`, "varchar(1) with time zone", "array(varchar); SELECT 1", "map(bigint)", "array(array(array(unknown)))"} {
		if _, err := parseInsertType(decl); err == nil {
			t.Fatalf("unsafe metadata accepted: %s", decl)
		}
	}
}
