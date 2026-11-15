package tdengine

import (
	"errors"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestDataWriteLiteralsPreserveExactNativeValues(t *testing.T) {
	for _, tc := range []struct {
		typ   string
		value contract.DataValue
		want  string
	}{
		{"TIMESTAMP", contract.DataValue{Kind: contract.DateTime, Text: "2024-01-01T00:00:00.123456789Z"}, "1704067200123456789"},
		{"BIGINT", contract.DataValue{Kind: contract.Integer, Text: "9007199254740993"}, "9007199254740993"},
		{"BIGINT UNSIGNED", contract.DataValue{Kind: contract.Integer, Text: "18446744073709551615"}, "18446744073709551615"},
		{"FLOAT", contract.DataValue{Kind: contract.Floating, Text: "8.6"}, "8.6"},
		{"DOUBLE", contract.DataValue{Kind: contract.Floating, Text: "8.600000381469727"}, "8.600000381469727"},
		{"BOOL", contract.DataValue{Kind: contract.Boolean, Text: "false"}, "false"},
		{"DECIMAL(38, 10)", contract.DataValue{Kind: contract.Decimal, Text: "-12345678901234567890.1234567890"}, "'-12345678901234567890.1234567890'"},
		{"VARBINARY", contract.DataValue{Kind: contract.Binary, Bytes: []byte{0, 255}, Binary: true}, "'\\x00ff'"},
		{"VARBINARY", contract.DataValue{Kind: contract.Binary}, "'\\x'"},
		{"VARCHAR", contract.DataValue{Kind: contract.Text}, "''"},
		{"NCHAR", contract.DataValue{Kind: contract.Text, Text: "猫'\\Ω\n"}, "'猫\\'\\\\Ω\n'"},
		{"VARCHAR", contract.DataValue{Kind: contract.Null}, "NULL"},
		{"INT", contract.DataValue{Kind: contract.Integer, Null: true}, "NULL"},
	} {
		t.Run(tc.typ+"/"+tc.want, func(t *testing.T) {
			column := contract.StructureColumn{DataType: tc.typ, ValueKind: valueKind(tc.typ), Nullable: true}
			got, err := nativeDataLiteral(column, tc.value, "ns")
			if err != nil || got != tc.want {
				t.Fatalf("exact native literal: got %q, err %v", got, err)
			}
		})
	}
}

func TestDataWriteLiteralsRejectLossAndContradictoryInputs(t *testing.T) {
	for _, tc := range []struct {
		typ, text string
		kind      contract.CellKind
	}{
		{"TINYINT", "128", contract.Integer}, {"TINYINT", "-129", contract.Integer},
		{"SMALLINT", "32768", contract.Integer}, {"INT", "2147483648", contract.Integer},
		{"BIGINT", "9223372036854775808", contract.Integer}, {"BIGINT UNSIGNED", "-1", contract.Integer},
		{"INT UNSIGNED", "4294967296", contract.Integer}, {"BIGINT", "1.0", contract.Integer},
		{"FLOAT", "1e40", contract.Floating}, {"DOUBLE", "NaN", contract.Floating},
		{"DOUBLE", "Inf", contract.Floating}, {"BOOL", "TRUE", contract.Boolean},
		{"DECIMAL(3,2)", "10.00", contract.Decimal}, {"DECIMAL(38,10)", "1.12345678901", contract.Decimal},
		{"DECIMAL(3,2)", "1e-2", contract.Decimal}, {"DECIMAL(3,2)", "1.0'", contract.Decimal},
		{"VARCHAR", "a\x00b", contract.Text}, {"VARCHAR", "\xff", contract.Text},
		{"TIMESTAMP", "2024-01-01T00:00:00.123456789Z", contract.Text},
	} {
		if _, err := nativeDataLiteral(contract.StructureColumn{DataType: tc.typ, ValueKind: valueKind(tc.typ), Nullable: true}, contract.DataValue{Kind: tc.kind, Text: tc.text}, "ns"); err == nil {
			t.Fatal("lossy or contradictory value admitted", tc)
		}
	}
	column := contract.StructureColumn{DataType: "VARCHAR", ValueKind: contract.Text, Nullable: true}
	for _, value := range []contract.DataValue{
		{Kind: contract.Text, Default: true}, {Kind: contract.Text, Binary: true},
		{Kind: contract.Text, Bytes: []byte{1}}, {Kind: contract.Text, Text: "x", Null: true},
		{Kind: contract.Null, Text: "x"}, {Kind: contract.Binary, Binary: true, Null: true},
	} {
		if _, err := nativeDataLiteral(column, value, "ns"); err == nil {
			t.Fatal("contradictory payload admitted", value)
		}
	}
	column.Nullable = false
	if _, err := nativeDataLiteral(column, contract.DataValue{Kind: contract.Null}, "ns"); err == nil {
		t.Fatal("required native key cleared")
	}
	column.Nullable = true
	if _, err := nativeDataLiteral(column, contract.DataValue{Kind: contract.Text, Text: strings.Repeat("x", contract.DataEditMaxBytes+1)}, "ns"); !errors.Is(err, contract.ErrDataEditLimit) {
		t.Fatal("complete editing limit lost", err)
	}
}

func TestDataWriteLengthUsesNativeBytesAndUnicodeCharacters(t *testing.T) {
	length := int64(2)
	for _, tc := range []struct {
		typ   string
		v     contract.DataValue
		valid bool
	}{
		{"NCHAR", contract.DataValue{Kind: contract.Text, Text: "猫Ω"}, true},
		{"NCHAR", contract.DataValue{Kind: contract.Text, Text: "猫Ωa"}, false},
		{"VARCHAR", contract.DataValue{Kind: contract.Text, Text: "Ω"}, true},
		{"VARCHAR", contract.DataValue{Kind: contract.Text, Text: "猫"}, false},
		{"VARBINARY", contract.DataValue{Kind: contract.Binary, Bytes: []byte{0, 255}}, true},
		{"VARBINARY", contract.DataValue{Kind: contract.Binary, Bytes: []byte{0, 255, 1}}, false},
	} {
		_, err := nativeDataLiteral(contract.StructureColumn{DataType: tc.typ, ValueKind: valueKind(tc.typ), CharacterMaxLength: &length}, tc.v, "ns")
		if (err == nil) != tc.valid {
			t.Fatal("native length units changed", tc, err)
		}
	}
}
