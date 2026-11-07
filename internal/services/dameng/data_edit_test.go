package dameng

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func intPointer(v int64) *int64 { return &v }

func TestEditParametersRejectLossBeforeBinding(t *testing.T) {
	decimal := contract.StructureColumn{DataType: "DECIMAL", ValueKind: contract.Decimal, NumericPrecision: intPointer(8), NumericScale: intPointer(2)}
	timestamp := contract.StructureColumn{DataType: "TIMESTAMP", ValueKind: contract.DateTime, DatetimePrecision: intPointer(6)}
	text := contract.StructureColumn{DataType: "VARCHAR", ValueKind: contract.Text, CharacterMaxLength: intPointer(2)}
	bin := contract.StructureColumn{DataType: "VARBINARY", Type: "VARBINARY(2)", ValueKind: contract.Binary}
	for _, test := range []struct {
		column contract.StructureColumn
		value  contract.DataValue
	}{
		{decimal, contract.DataValue{Text: "1.234"}}, {decimal, contract.DataValue{Text: "1234567.01"}},
		{decimal, contract.DataValue{Text: "1e2"}}, {decimal, contract.DataValue{Text: "NaN"}},
		{timestamp, contract.DataValue{Text: "2026-11-07 10:20:30.123456789"}},
		{timestamp, contract.DataValue{Text: "2026-11-07 10:20:30,123456789"}},
		{timestamp, contract.DataValue{Text: "2026-02-30 10:20:30"}},
		{text, contract.DataValue{Text: "中文字"}}, {text, contract.DataValue{Text: string([]byte{255})}},
		{text, contract.DataValue{Text: "a", Null: true}}, {text, contract.DataValue{Text: "a", Kind: contract.Integer}},
		{contract.StructureColumn{DataType: "CHAR", ValueKind: contract.Text, CharacterMaxLength: intPointer(4)}, contract.DataValue{Text: "中文ab"}},
		{bin, contract.DataValue{Binary: true, Bytes: []byte{1, 2, 3}}}, {bin, contract.DataValue{Text: "text", Binary: true}},
		{contract.StructureColumn{DataType: "INT", ValueKind: contract.Integer}, contract.DataValue{Text: "2147483648"}},
		{contract.StructureColumn{DataType: "REAL", ValueKind: contract.Floating}, contract.DataValue{Text: "1.2345678901234567"}},
		{contract.StructureColumn{DataType: "DOUBLE", ValueKind: contract.Floating}, contract.DataValue{Text: "+Inf"}},
		{contract.StructureColumn{DataType: "DOUBLE", Type: "FLOAT(24)", ValueKind: contract.Floating}, contract.DataValue{Text: "1.5"}},
	} {
		if _, err := editParameter(test.column, test.value); err == nil {
			t.Fatal("lossy/invalid value accepted", test.column.DataType)
		}
	}
	for _, test := range []struct {
		column contract.StructureColumn
		value  contract.DataValue
	}{
		{decimal, contract.DataValue{Text: "-123456.78"}},
		{timestamp, contract.DataValue{Text: "2026-11-07 10:20:30.123456"}},
		{text, contract.DataValue{Text: "中文"}}, {bin, contract.DataValue{Binary: true, Bytes: []byte{0, 255}}},
		{contract.StructureColumn{DataType: "BIGINT", ValueKind: contract.Integer}, contract.DataValue{Text: "-9223372036854775808"}},
		{contract.StructureColumn{DataType: "REAL", ValueKind: contract.Floating}, contract.DataValue{Text: "1.5"}},
	} {
		if _, err := editParameter(test.column, test.value); err != nil {
			t.Fatal("exact value rejected", test.column.DataType, err)
		}
	}
	if _, err := editParameter(text, contract.DataValue{Text: strings.Repeat("x", contract.DataEditMaxBytes+1)}); !errors.Is(err, contract.ErrDataEditLimit) {
		t.Fatal("edit limit lost", err)
	}
}

func TestDataSaveOwnsSnapshotsEditsAndSerializedKeyBytes(t *testing.T) {
	cell := contract.Cell{Kind: contract.Integer, Text: "9007199254740993"}
	raw, _ := json.Marshal(cell)
	snapshot := &contract.RowSnapshot{Mode: "values", Columns: []string{"ID", "B"}, Ref: contract.RowRef{Columns: []string{"ID"}, TypedValues: []contract.Cell{cell}, Values: [][]byte{raw}}, Original: []contract.Cell{cell, {Kind: contract.Binary, Bytes: []byte{0, 1, 255}}}}
	input := []contract.DataEdit{{Kind: "update", Original: snapshot, Values: map[string]contract.DataValue{"B": {Binary: true, Bytes: []byte{2, 3, 255}}}}}
	owned, err := cloneDataEdits(input)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Ref.Values[0][0] = 1
	snapshot.Ref.TypedValues[0].Text = "other"
	snapshot.Columns[0] = "changed"
	snapshot.Original[1].Bytes[0] = 1
	value := input[0].Values["B"]
	value.Bytes[0] = 1
	if owned[0].Original.Columns[0] != "ID" || owned[0].Original.Ref.TypedValues[0].Text != cell.Text || owned[0].Original.Ref.Values[0][0] == 1 || owned[0].Original.Original[1].Bytes[0] != 0 || owned[0].Values["B"].Bytes[0] != 2 {
		t.Fatal("captured save payload shares mutable state")
	}
	if _, err = cloneDataEdits([]contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{"B": {Bytes: make([]byte, contract.DataEditMaxBytes+1)}}}}); !errors.Is(err, contract.ErrDataEditLimit) {
		t.Fatal("oversize payload accepted", err)
	}
}

func TestCompleteSnapshotRejectsMetadataColumnKeyAndPreviewMismatch(t *testing.T) {
	s := &Session{dataScope: "original"}
	table := &contract.TableStructure{Object: contract.ObjectRef{ProfileID: 1, Catalog: "DAMENG", Schema: "SYSDBA", Kind: "table", Name: "T"}, Columns: []contract.StructureColumn{{Name: "ID", DataType: "BIGINT", ValueKind: contract.Integer}, {Name: "V", DataType: "VARCHAR", ValueKind: contract.Text}}}
	key := []string{"ID"}
	row := []contract.Cell{{Kind: contract.Integer, Text: "9007199254740993"}, {Kind: contract.Text, Text: "exact"}}
	valid := s.fullSnapshot(table, key, row, "physical")
	if err := validateDataSnapshot(s, table, key, "physical", valid); err != nil {
		t.Fatal(err)
	}
	mutations := []func(*contract.RowSnapshot){
		func(v *contract.RowSnapshot) { v.Metadata = "foreign" }, func(v *contract.RowSnapshot) { v.Columns[0] = "V" },
		func(v *contract.RowSnapshot) { v.Ref.TypedValues[0].Text = "1" }, func(v *contract.RowSnapshot) { v.Ref.Values[0][0] = 1 },
		func(v *contract.RowSnapshot) { v.Original[0].Text = "1" }, func(v *contract.RowSnapshot) { v.Original[1].Truncated = true },
		func(v *contract.RowSnapshot) { v.Mode = "fingerprints" }, func(v *contract.RowSnapshot) { v.Original = v.Original[:1] },
		func(v *contract.RowSnapshot) { v.Ref.Object.ProfileID++ },
	}
	for _, change := range mutations {
		v := cloneDataSnapshot(valid)
		change(v)
		if err := validateDataSnapshot(s, table, key, "physical", v); err == nil {
			t.Fatal("invalid original snapshot accepted")
		}
	}
	copy := cloneDataSnapshot(valid)
	if !reflect.DeepEqual(valid, copy) || !bytes.Equal(copy.Ref.Values[0], valid.Ref.Values[0]) {
		t.Fatal("snapshot clone changed content")
	}
}
