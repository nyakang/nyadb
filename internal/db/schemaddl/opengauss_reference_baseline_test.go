package schemaddl

import (
	"encoding/json"
	"errors"
	"testing"
)

func gaussReferenceBaselineFixture(t *testing.T) (Snapshot, Table, *OpenGaussCatalog) {
	t.Helper()
	original := nativeDesignTestSnapshot(t)
	desired := original.Table.Clone()
	desired.Clauses = append(desired.Clauses, Clause{ID: "new:fk", Kind: "constraint", Name: "parent_fk", SQL: "CONSTRAINT parent_fk FOREIGN KEY(id) REFERENCES public.parent(id)"})
	parent := original.OpenGauss.Clone()
	parent.Name, parent.RelationOID, parent.CanManage = "parent", 13, false
	parent.Entries = []OpenGaussCatalogEntry{
		original.OpenGauss.Entries[0],
		{Kind: "relation", OID: 13, Data: json.RawMessage(`{"relname":"parent","relkind":"r","relpersistence":"p","relhastriggers":false,"relacl":null,"native_unknown":90071992547409931234567890.12345678901234567890}`)},
	}
	return original, desired, parent
}

func TestOpenGaussReferenceBaselineKeepsIndependentMetadataAndRequestScope(t *testing.T) {
	original, desired, parent := gaussReferenceBaselineFixture(t)
	baseline, err := CaptureOpenGaussReferenceBaseline(original, desired, parent)
	if err != nil {
		t.Fatal(err)
	}
	actual := parent.Clone()
	parent.Entries[1].Data[0] = '['
	if err = VerifyOpenGaussReferenceUnchanged(baseline, original, desired, actual); err != nil {
		t.Fatal("native reference proof aliases its mutable caller input", err)
	}
	foreign := desired.Clone()
	foreign.Clauses[0].SQL = "bigint DEFAULT 7"
	if !errors.Is(VerifyOpenGaussReferenceUnchanged(baseline, original, foreign, actual), ErrConflict) {
		t.Fatal("native reference evidence reused for another request")
	}
	var row map[string]json.RawMessage
	if json.Unmarshal(actual.Entries[1].Data, &row) != nil {
		t.Fatal("native reference fixture")
	}
	row["native_unknown"] = json.RawMessage(`90071992547409931234567890.12345678901234567891`)
	actual.Entries[1].Data, _ = json.Marshal(row)
	if _, err = actual.Fingerprint(); err != nil || !errors.Is(VerifyOpenGaussReferenceUnchanged(baseline, original, desired, actual), ErrConflict) {
		t.Fatal("high precision unknown parent change became unchanged context", err)
	}
}

func TestOpenGaussReferenceBaselineRejectsForeignIdentityAndEphemeralDeclarations(t *testing.T) {
	for _, mutate := range []func(*OpenGaussCatalog){
		func(p *OpenGaussCatalog) { p.DatabaseOID++ },
		func(p *OpenGaussCatalog) { p.RoleOID++ },
		func(p *OpenGaussCatalog) { p.NamespaceOID++ },
		func(p *OpenGaussCatalog) { p.RelationOID = 3; p.Entries[1].OID = 3 },
		func(p *OpenGaussCatalog) { p.Hooks = true },
		func(p *OpenGaussCatalog) {
			p.Entries = append(p.Entries, OpenGaussCatalogEntry{Kind: "copy_definition", OID: 13, Data: json.RawMessage(`{"sequences":[]}`)})
		},
		func(p *OpenGaussCatalog) {
			p.Entries[1].Data = json.RawMessage(`{"relname":"other","relkind":"r","relpersistence":"p"}`)
		},
	} {
		original, desired, parent := gaussReferenceBaselineFixture(t)
		mutate(parent)
		if _, err := CaptureOpenGaussReferenceBaseline(original, desired, parent); err == nil {
			t.Fatal("foreign, unsupported or ephemeral native reference admitted")
		}
	}
}
