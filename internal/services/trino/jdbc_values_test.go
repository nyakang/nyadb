package trino

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/agentbridge"
)

func TestJDBCProjectionKeepsOriginalStringsAndIncompleteValues(t *testing.T) {
	for _, test := range []struct {
		decl, text string
		kind       contract.CellKind
		incomplete bool
	}{
		{"bigint", "9007199254740993", contract.Integer, false},
		{"decimal(38,20)", "123456789012345678.12345678901234567890", contract.Decimal, false},
		{"decimal(38,20)", "1.00E-18", contract.Decimal, false},
		{"double", "Infinity", contract.Floating, false},
		{"real", "-Infinity", contract.Floating, false},
		{"double", "NaN", contract.Floating, false},
		{"boolean", "false", contract.Boolean, false},
		{"varchar", "NULL", contract.Text, false},
		{"json", `{"n":9007199254740993}`, contract.JSON, false},
		{"timestamp(9)", "2026-11-16 01:02:03.123456789", contract.DateTime, false},
		{"timestamp(12) with time zone", "2026-11-16T01:02:03.123456789Z", contract.DateTime, true},
		{"time(9)", "01:02:03", contract.DateTime, true},
		{"varbinary", "[B@76543", contract.Text, true},
		{"array(bigint)", "io.trino.jdbc.TrinoArray@1234", contract.Text, true},
		{"map(varchar,bigint)", "{中文=9007199254740993}", contract.Text, true},
		{"row(x bigint)", "{x=9007199254740993}", contract.Text, true},
		{"interval day to second", "1 00:00:01.000", contract.Text, true},
	} {
		t.Run(test.decl+"/"+test.text, func(t *testing.T) {
			raw, _ := json.Marshal(test.text)
			cell, err := DecodeJDBCValue(test.decl, raw)
			if err != nil || cell.Kind != test.kind || cell.Text != test.text || cell.Truncated != test.incomplete || cell.Bytes != nil {
				t.Fatalf("original projection changed: %#v %v", cell, err)
			}
		})
	}
	cell, err := DecodeJDBCValue("varbinary", json.RawMessage(" null "))
	if err != nil || cell.Kind != contract.Null || cell.Truncated {
		t.Fatal("SQL null became an incomplete string")
	}
}

func TestJDBCProjectionRejectsInventedWireValues(t *testing.T) {
	for _, raw := range []string{"123", "true", "{}", "[]", `"one" "two"`, `"\x00"`} {
		if _, err := DecodeJDBCValue("varchar", json.RawMessage(raw)); !errors.Is(err, agentbridge.ErrNativeResult) {
			t.Fatalf("non-native string wire was admitted: %s", raw)
		}
	}
	for _, test := range []struct{ decl, raw string }{{"bigint", `"1.5"`}, {"decimal(38,20)", `"NaN"`}, {"boolean", `"1"`}, {"json", `"{n:1}"`}} {
		if _, err := DecodeJDBCValue(test.decl, json.RawMessage(test.raw)); !errors.Is(err, agentbridge.ErrNativeResult) {
			t.Fatal("invalid scalar text was admitted")
		}
	}
}
