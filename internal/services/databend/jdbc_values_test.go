package databend

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestDatabendExactOriginalNativeScalarAndJSONValues(t *testing.T) {
	for _, v := range []struct {
		typ, raw, text string
		kind           contract.CellKind
	}{
		{"uint64", "9007199254740993", "9007199254740993", contract.Integer},
		{"uint64", "18446744073709551615", "18446744073709551615", contract.Integer},
		{"int64", "-9223372036854775808", "-9223372036854775808", contract.Integer},
		{"decimal", "9007199254740993.12345678901234567890", "9007199254740993.12345678901234567890", contract.Decimal},
		{"string", "\"NULL\"", "NULL", contract.Text},
		{"string", "\"\"", "", contract.Text},
		{"string", "\"中文\"", "中文", contract.Text},
		{"boolean", "false", "false", contract.Boolean},
		{"array", "\"[9007199254740993]\"", "[9007199254740993]", contract.JSON},
		{"variant", "\"{\\\"id\\\":9007199254740993}\"", "{\"id\":9007199254740993}", contract.JSON},
		{"variant", "\"null\"", "null", contract.JSON},
	} {
		cell, err := DecodeJDBCValue(v.typ, json.RawMessage(v.raw))
		if err != nil || cell.Kind != v.kind || cell.Text != v.text || cell.Truncated {
			t.Fatal("original value changed", v.typ, cell, err)
		}
	}
	for _, v := range []struct {
		raw   string
		value []byte
	}{
		{"\"00FF80\"", []byte{0, 255, 128}}, {"\"\"", []byte{}},
	} {
		cell, err := DecodeJDBCValue("binary", json.RawMessage(v.raw))
		if err != nil || cell.Kind != contract.Binary || !bytes.Equal(cell.Bytes, v.value) {
			t.Fatal("original bytes changed", err)
		}
	}
	for _, typ := range []string{"string", "variant", "binary", "uint64"} {
		cell, err := DecodeJDBCValue(typ, json.RawMessage("null"))
		if err != nil || cell.Kind != contract.Null {
			t.Fatal("SQL NULL became a textual/JSON value", err)
		}
	}
}

func TestDatabendRejectsGuessedNativeTypesAndLostRepresentations(t *testing.T) {
	for _, v := range []struct{ typ, raw string }{
		{"uint64", "18446744073709551616"}, {"uint64", "-1"}, {"uint64", "1e10"}, {"uint64", "\"42\""},
		{"int8", "128"}, {"int64", "-9223372036854775809"}, {"uint128", "1"},
		{"decimal", "\"9007199254740993.12345678901234567890\""}, {"boolean", "\"false\""},
		{"binary", "\"0x00ff\""}, {"binary", "\"0ff\""}, {"binary", "15"}, {"string", "15"},
		{"array", "\"{\\\"x\\\":1}\""}, {"variant", "\"invalid\""}, {"tuple", "\"(7,'seven')\""},
		{"unverified", "\"display\""}, {"string", "\"one\" \"two\""},
	} {
		if _, err := DecodeJDBCValue(v.typ, json.RawMessage(v.raw)); err == nil {
			t.Fatal("guessed original representation became complete data", v.typ, v.raw)
		}
	}
}
