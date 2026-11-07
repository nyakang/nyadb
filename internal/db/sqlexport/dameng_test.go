package sqlexport

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/sqltext"
)

func intPtr(n int64) *int64 { return &n }

func damengTestTable(t *testing.T) (*DamengTable, []contract.StructureColumn) {
	t.Helper()
	cols := []contract.StructureColumn{
		{Name: "ID", DataType: "BIGINT", ValueKind: contract.Integer, Key: "PRI"},
		{Name: `N"`, DataType: "DECIMAL", ValueKind: contract.Decimal, NumericPrecision: intPtr(38), NumericScale: intPtr(20)},
		{Name: "T", DataType: "VARCHAR", ValueKind: contract.Text, Nullable: true},
		{Name: "B", DataType: "VARBINARY", Type: "VARBINARY(64)", ValueKind: contract.Binary, Nullable: true},
	}
	x, err := NewDamengTable(contract.ObjectRef{ProfileID: 7, Catalog: "DAMENG", Schema: ` Native" `, Kind: "table", Name: `Records"`}, cols)
	if err != nil {
		t.Fatal(err)
	}
	return x, cols
}

func TestDamengSQLExactValuesAndQualifiedIdentity(t *testing.T) {
	x, _ := damengTestTable(t)
	values := []contract.DataValue{{Kind: contract.Integer, Text: "9007199254740993"}, {Kind: contract.Decimal, Text: "9007199254740993.12345678901234567890"}, {Kind: contract.Text, Text: "中文🙂'\x00; DROP TABLE q;\n/\n\\"}, {Kind: contract.Binary, Binary: true, Bytes: []byte{0, 128, 255, 39}}}
	statement, err := x.Insert(values)
	if err != nil || strings.Contains(statement, "_utf8mb4") || strings.Contains(statement, "`DAMENG`") || strings.ContainsRune(statement, 0) || !strings.Contains(statement, `" Native"" "."Records"""`) || !strings.Contains(statement, "DECIMAL(38,20)") || !strings.Contains(statement, "0x0080ff27") {
		t.Fatal("native identity/value export failed", err, statement)
	}
	parts, err := sqltext.SplitDameng(statement)
	if err != nil || len(parts) != 1 {
		t.Fatal("data escaped SQL boundaries", err, parts)
	}
	key := contract.Cell{Kind: contract.Integer, Text: "9007199254740993"}
	raw, _ := json.Marshal(key)
	ref := contract.RowRef{Object: x.object, Database: x.object.Schema, Table: x.object.Name, Columns: []string{"ID"}, TypedValues: []contract.Cell{key}, Values: [][]byte{raw}}
	update, err := x.Update(values, ref)
	if err != nil || !strings.HasSuffix(update, `WHERE "ID" = 9007199254740993;`+"\n") || strings.Contains(update, `SET "ID"`) {
		t.Fatal("native UPDATE rounded/mutated identity", err, update)
	}
	ref.Object.ProfileID++
	if _, err = x.Update(values, ref); err == nil {
		t.Fatal("foreign qualified identity copied")
	}
}

func TestDamengSQLNullEmptyAndDefaultAreDistinct(t *testing.T) {
	x, _ := damengTestTable(t)
	values := []contract.DataValue{{Text: "1"}, {Default: true}, {Text: ""}, {Kind: contract.Binary, Binary: true, Bytes: []byte{}}}
	statement, err := x.Insert(values)
	if err != nil || !strings.Contains(statement, "DEFAULT, (''), CAST('' AS VARBINARY(64))") {
		t.Fatal("empty/default distinction lost", err, statement)
	}
	values[2], values[3] = contract.DataValue{Null: true}, contract.DataValue{Null: true}
	statement, err = x.Insert(values)
	if err != nil || !strings.Contains(statement, "DEFAULT, NULL, NULL") {
		t.Fatal("NULL became empty", err, statement)
	}
	values[2] = contract.DataValue{Default: true, Text: "not a default"}
	if _, err = x.Insert(values); err == nil {
		t.Fatal("conflicting default payload accepted")
	}
}

func TestDamengSQLRejectsMalformedValuesAndOwnsMetadata(t *testing.T) {
	x, cols := damengTestTable(t)
	*cols[1].NumericScale = 1
	values := []contract.DataValue{{Text: "1"}, {Text: "1.00000000000000000001"}, {Text: "value"}, {Binary: true}}
	if _, err := x.Insert(values); err != nil {
		t.Fatal("caller changed captured metadata", err)
	}
	for _, invalid := range []contract.DataValue{{Text: "1);DROP TABLE t;--"}, {Text: "NaN"}, {Text: "1e20"}, {Text: "1.000000000000000000001"}, {Text: "1", Bytes: []byte{1}}, {Kind: contract.Integer, Text: "1"}} {
		values[1] = invalid
		if _, err := x.Insert(values); err == nil {
			t.Fatal("unverified numeric/type value accepted")
		}
	}
	values[1] = contract.DataValue{Text: "1"}
	values[2] = contract.DataValue{Text: strings.Repeat("x", contract.DataExportFieldMaxBytes+1)}
	if _, err := x.Insert(values); !errors.Is(err, contract.ErrDataEditLimit) {
		t.Fatal("oversized field accepted", err)
	}
}

func TestDamengSQLFullLOBAndIdentityWithoutCommitOrReplay(t *testing.T) {
	o := contract.ObjectRef{ProfileID: 7, Catalog: "DAMENG", Schema: "SYSDBA", Kind: "table", Name: "Records"}
	x, err := NewDamengTable(o, []contract.StructureColumn{
		{Name: "ID", DataType: "BIGINT", ValueKind: contract.Integer, Extra: "IDENTITY", Generated: true},
		{Name: "C", DataType: "CLOB", ValueKind: contract.Text},
		{Name: "B", DataType: "BLOB", ValueKind: contract.Binary},
		{Name: "G", Generated: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	values := []contract.DataValue{{Text: "9007199254740993"}, {Text: strings.Repeat("'", contract.DataExportFieldMaxBytes)}, {Binary: true, Bytes: bytes.Repeat([]byte{0x80}, contract.DataExportFieldMaxBytes)}, {Text: "computed"}}
	statement, err := x.Insert(values)
	if err != nil || strings.Count(statement, "80808080") != contract.DataExportFieldMaxBytes/4 || strings.Contains(statement, "COMMIT") || strings.Contains(statement, `"G"`) || !strings.Contains(statement, "EXCEPTION WHEN OTHERS") {
		t.Fatal("full LOB/identity export lost values or caller transaction", err)
	}
	values[0], values[1], values[2] = contract.DataValue{Default: true}, contract.DataValue{Text: ""}, contract.DataValue{Binary: true, Bytes: []byte{}}
	statement, err = x.Insert(values)
	if err != nil || strings.Contains(statement, "IDENTITY_INSERT") || strings.Contains(statement, `"ID"`) || !strings.Contains(statement, "EMPTY_CLOB(), EMPTY_BLOB()") {
		t.Fatal("identity default or empty LOB was not native", err, statement)
	}
}

func TestDamengSQLDenseControlTextUsesBoundedAbsentMarkers(t *testing.T) {
	text := "\ue000\ue001" + strings.Repeat("\x00\r\n", (contract.DataExportFieldMaxBytes-6)/3) + "x"
	literal := damengTextLiteral(text, true)
	if strings.Count(literal, "REPLACE(") != 3 || strings.ContainsAny(literal, "\x00\r\n") || len(literal) > 4*len(text) {
		t.Fatal("dense controls became raw script lines or unbounded SQL expressions")
	}
	// These first private-use markers are part of the data. Replacement must
	// leave them intact and reserve separate absent markers for all controls.
	if !strings.Contains(literal, "\ue000\ue001") || !strings.Contains(literal, "'\ue002',CHR(0)") || !strings.Contains(literal, "'\ue003',CHR(13)") || !strings.Contains(literal, "'\ue004',CHR(10)") {
		t.Fatal("marker collision changed original Unicode/control data")
	}
}
