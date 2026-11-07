package schemaddl

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestOpenGaussIncomingGraphBindsNativeChildSlotsAndConstraintIdentities(t *testing.T) {
	original, desired, child := gaussIncomingBaselineFixture(t)
	baseline, err := CaptureOpenGaussIncomingBaseline(original, desired, child)
	if err != nil {
		t.Fatal(err)
	}
	names := OpenGaussNamesForTable(original.Table)
	names.ReferenceRelations = map[uint64]string{13: `@reference:"public"."child"`, 47: "@another-reference"}
	names.ReferenceColumns = map[uint64]map[int]string{47: {2: "other-field"}}
	bound, err := BindOpenGaussIncomingGraph(names, baseline, original, desired, original.OpenGauss, child, nil)
	if err != nil || bound.IncomingConstraints[701] != "incoming:701" || bound.ReferenceColumns[13][3] != "incoming-column:13:parent_id" {
		t.Fatal("native incoming graph names", bound, err)
	}
	row, err := gaussJSON(baseline.Catalog().Entries[3].Data)
	if err != nil {
		t.Fatal(err)
	}
	if row["native_unknown"] != json.Number("90071992547409931234567890.12345678901234567890") {
		t.Fatal("unknown incoming property lost precision", row)
	}
	bound.ReferenceColumns[47][2] = "changed"
	bound.Columns["id"] = "changed"
	if names.ReferenceColumns[47][2] != "other-field" || names.Columns["id"] != original.Table.Columns()[0].ID || names.IncomingConstraints != nil {
		t.Fatal("incoming graph joins mutate caller-owned names")
	}
}

func TestOpenGaussIncomingGraphRejectsUnboundConstraintsAndForeignContext(t *testing.T) {
	for _, mutate := range []func(*OpenGaussCatalog){
		func(c *OpenGaussCatalog) { c.RoleOID++ },
		func(c *OpenGaussCatalog) { c.DatabaseOID++ },
		func(c *OpenGaussCatalog) { c.Name = "other" },
		func(c *OpenGaussCatalog) { c.Entries = c.Entries[:3] },
		func(c *OpenGaussCatalog) {
			c.Entries[3].Data = json.RawMessage(`{"contype":"f","conname":"unknown","confrelid":3}`)
		},
	} {
		original, desired, child := gaussIncomingBaselineFixture(t)
		baseline, err := CaptureOpenGaussIncomingBaseline(original, desired, child)
		if err != nil {
			t.Fatal(err)
		}
		mutate(child)
		if _, err = BindOpenGaussIncomingGraph(OpenGaussNamesForTable(desired), baseline, original, desired, original.OpenGauss, child, nil); err == nil {
			t.Fatal("foreign or unbound incoming graph accepted")
		}
	}
	original, desired, child := gaussIncomingBaselineFixture(t)
	baseline, _ := CaptureOpenGaussIncomingBaseline(original, desired, child)
	names := OpenGaussNamesForTable(desired)
	names.ReferenceRelations = map[uint64]string{13: "an unrelated alias"}
	if _, err := BindOpenGaussIncomingGraph(names, baseline, original, desired, original.OpenGauss, child, nil); !errors.Is(err, ErrConflict) {
		t.Fatal("incoming child replaced another reference identity", err)
	}
}
