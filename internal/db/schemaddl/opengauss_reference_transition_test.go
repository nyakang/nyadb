package schemaddl

import (
	"encoding/json"
	"testing"
)

func TestOpenGaussReferenceRetentionUsesNativeTypesAndRejectsActionIdentityReplacement(t *testing.T) {
	original := Snapshot{Exists: true, Table: Table{Name: "child", Clauses: []Clause{
		{ID: "key", Kind: "column", Name: "key", SQL: "bigint", OpenGauss: &OpenGaussColumnProperties{Number: 1}},
		{ID: "fk:stable", Kind: "constraint", Name: "parent_fk", SQL: "CONSTRAINT parent_fk FOREIGN KEY(key) REFERENCES public.parent(id)"},
	}}, OpenGauss: &OpenGaussCatalog{RelationOID: 5, Entries: []OpenGaussCatalogEntry{
		{Kind: "column", OID: 5, SubID: 1, Data: json.RawMessage(`{"attname":"key","attisdropped":false,"atttypid":20,"atttypmod":-1,"attcollation":0}`)},
		{Kind: "constraint", OID: 201, Data: json.RawMessage(`{"conkey":[1]}`)},
	}}}
	desired := original.Table.Clone()
	// This changes the parsed spelling, but native bigint and int8 are one
	// type. It cannot grant permission to replace retained parent RI triggers.
	desired.Clauses[0].SQL = "int8"
	actual := original.OpenGauss.Clone()
	oldConstraints := map[uint64]string{201: "fk:stable"}
	oldTriggers := map[uint64]string{301: "fk:stable:9", 302: "fk:stable:17"}
	if err := gaussReferenceRetainedIdentities(original, desired, actual, oldConstraints, oldTriggers, oldConstraints, oldTriggers); err != nil {
		t.Fatal("native aliases changed retained FK identities", err)
	}
	if err := gaussReferenceRetainedIdentities(original, desired, actual, oldConstraints, oldTriggers, oldConstraints, map[uint64]string{401: "fk:stable:9", 402: "fk:stable:17"}); err == nil {
		t.Fatal("native type alias authorized parent action trigger replacement")
	}
	if err := gaussReferenceRetainedIdentities(original, desired, actual, oldConstraints, oldTriggers, map[uint64]string{401: "fk:stable"}, oldTriggers); err == nil {
		t.Fatal("unchanged native key authorized FK identity replacement")
	}
	// An actual native key type transition can rebuild RI objects, but the
	// complete independent semantics and parent-context proof remains required
	// by VerifyOpenGaussReferenceTransition, outside this retention check.
	actual.Entries[0].Data = json.RawMessage(`{"attname":"key","attisdropped":false,"atttypid":23,"atttypmod":-1,"attcollation":0}`)
	if err := gaussReferenceRetainedIdentities(original, desired, actual, oldConstraints, oldTriggers, map[uint64]string{401: "fk:stable"}, map[uint64]string{501: "fk:stable:9", 502: "fk:stable:17"}); err != nil {
		t.Fatal("native type change lost explicit rebuild scope", err)
	}
}
