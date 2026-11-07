package schemaddl

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func gaussIncomingBaselineFixture(t *testing.T) (Snapshot, Table, *OpenGaussCatalog) {
	t.Helper()
	original := nativeDesignTestSnapshot(t)
	child := original.OpenGauss.Clone()
	child.Name, child.RelationOID, child.CanManage = "child", 13, false
	fk := json.RawMessage(`{"oid":701,"conname":"incoming_fk","contype":"f","conrelid":13,"confrelid":3,"conkey":[3],"confkey":[1],"conindid":702,"confmatchtype":"u","confupdtype":"r","confdeltype":"c","convalidated":true,"condeferrable":true,"condeferred":true,"native_unknown":90071992547409931234567890.12345678901234567890}`)
	child.Entries = []OpenGaussCatalogEntry{
		original.OpenGauss.Entries[0],
		{Kind: "relation", OID: 13, Data: json.RawMessage(`{"relname":"child","relkind":"r","relpersistence":"p","relhastriggers":true,"relacl":null}`)},
		{Kind: "column", OID: 13, SubID: 3, Data: json.RawMessage(`{"attrelid":13,"attnum":3,"attname":"parent_id","attisdropped":false,"atttypid":20}`)},
		{Kind: "constraint", OID: 701, Data: fk},
	}
	original.OpenGauss.Entries = append(original.OpenGauss.Entries, OpenGaussCatalogEntry{Kind: "incoming_constraint", OID: 701, Data: append(json.RawMessage{}, fk...)})
	var err error
	original.Revision, err = original.OpenGauss.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	return original, original.Table.Clone(), child
}

func TestOpenGaussIncomingBaselineBindsBothCatalogsAndKeepsExactUnknownValues(t *testing.T) {
	original, desired, child := gaussIncomingBaselineFixture(t)
	baseline, err := CaptureOpenGaussIncomingBaseline(original, desired, child)
	if err != nil {
		t.Fatal(err)
	}
	constraints := baseline.Constraints()
	if len(constraints) != 1 || len(constraints[0].ColumnNumbers) != 1 || constraints[0].ColumnNumbers[0] != 3 || constraints[0].ParentIDs[0] != original.Table.Columns()[0].ID {
		t.Fatal("incoming key identity", constraints)
	}
	actual := child.Clone()
	child.Entries[3].Data[0] = '['
	constraints[0].Columns[0] = "changed"
	baseline.Catalog().Entries[3].Data[0] = '['
	if err = VerifyOpenGaussIncomingUnchanged(baseline, original, desired, actual); err != nil || baseline.Constraints()[0].Columns[0] != "parent_id" {
		t.Fatal("incoming evidence aliases caller", err)
	}
	row, _ := gaussJSON(actual.Entries[3].Data)
	row["native_unknown"] = json.Number("90071992547409931234567890.12345678901234567891")
	actual.Entries[3].Data, _ = json.Marshal(row)
	if _, err = actual.Fingerprint(); err != nil || !errors.Is(VerifyOpenGaussIncomingUnchanged(baseline, original, desired, actual), ErrConflict) {
		t.Fatal("changed exact child value accepted", err)
	}
	foreign := desired.Clone()
	foreign.Name = "another"
	if !errors.Is(VerifyOpenGaussIncomingUnchanged(baseline, original, foreign, baseline.Catalog()), ErrConflict) {
		t.Fatal("incoming proof reused for other design")
	}
}

func TestOpenGaussIncomingBaselineRejectsIncompleteForeignAndSynthesizedEvidence(t *testing.T) {
	for _, mutate := range []func(*OpenGaussCatalog){
		func(c *OpenGaussCatalog) { c.RoleOID++ },
		func(c *OpenGaussCatalog) { c.NamespaceOID++ },
		func(c *OpenGaussCatalog) { c.Hooks = true },
		func(c *OpenGaussCatalog) { c.Entries = c.Entries[:3] },
		func(c *OpenGaussCatalog) { c.Entries[2].SubID = 2 },
		func(c *OpenGaussCatalog) {
			c.Entries[3].Data = json.RawMessage(strings.ReplaceAll(string(c.Entries[3].Data), `"confdeltype":"c"`, `"confdeltype":"a"`))
		},
		func(c *OpenGaussCatalog) {
			c.Entries = append(c.Entries, OpenGaussCatalogEntry{Kind: "copy_definition", OID: 13, Data: json.RawMessage(`{"sequences":[]}`)})
		},
	} {
		original, desired, child := gaussIncomingBaselineFixture(t)
		mutate(child)
		if _, err := CaptureOpenGaussIncomingBaseline(original, desired, child); err == nil {
			t.Fatal("foreign or incomplete incoming evidence accepted")
		}
	}
}

func TestOpenGaussIncomingProbeUsesOnlyKeyTypesAndStableRenamedParentColumns(t *testing.T) {
	original, desired, child := gaussIncomingBaselineFixture(t)
	desired.Clauses[0].Name = "renamed_id"
	baseline, err := CaptureOpenGaussIncomingBaseline(original, desired, child)
	if err != nil {
		t.Fatal(err)
	}
	fields := map[string]PostgreSQLField{"parent_id": {Type: "bigint", HasDefault: true, Default: "nextval('private_sequence')", Generation: "untrusted_callback()", NotNull: true}}
	probe, names, err := OpenGaussIncomingProbeTable(baseline, original, desired, "private_child", "private_parent", fields)
	if err != nil || len(probe.Columns()) != 1 || probe.Columns()[0].SQL != "bigint" || len(names.Constraints) != 1 {
		t.Fatal("incoming probe copied executable expressions", probe, err)
	}
	text := probe.Clauses[1].SQL
	if !strings.Contains(text, `REFERENCES "public"."private_parent"("renamed_id")`) || !strings.Contains(text, "ON UPDATE RESTRICT ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED") || strings.Contains(text, "NOT VALID") {
		t.Fatal("incoming identity or native FK properties lost", text)
	}
	fields["parent_id"] = PostgreSQLField{Type: "bigserial"}
	if _, _, err = OpenGaussIncomingProbeTable(baseline, original, desired, "private_child", "private_parent", fields); !errors.Is(err, ErrUnsupported) {
		t.Fatal("private incoming probe creates a sequence", err)
	}
	if got, err := OpenGaussIncomingRelations(original); err != nil || len(got) != 1 || got[0] != 13 {
		t.Fatal("incoming OID enumeration", got, err)
	}
}
