package contract_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

func TestSchemaDocumentReceiptClonePreservesIndependentOriginalEvidence(t *testing.T) {
	original := contract.SchemaDesignSnapshot{Target: contract.SchemaDesignTarget{Object: contract.ObjectRef{ProfileID: 81, Catalog: "main", Name: "source", Kind: "table"}, Generation: 2}, Schema: schemaddl.Snapshot{Engine: "sqlite", Revision: "original-proof", Exists: true, Table: schemaddl.Table{Name: "source", Clauses: []schemaddl.Clause{{ID: "original:id", Kind: "column", Name: "id", SQL: "INTEGER"}}}, SQLite: &schemaddl.SQLiteCatalog{Version: 12, Entries: []schemaddl.SQLiteCatalogEntry{{Type: "index", Name: "implicit", Table: "source", Root: 3, SQLNull: true}, {Type: "table", Name: "source", Table: "source", Root: 2, SQL: "CREATE TABLE source(id INTEGER)"}}}}}
	desired := original.Schema.Table.Clone()
	desired.Name = "destination"
	actual := original.Clone()
	actual.Target.Object.Name = "destination"
	actual.Schema.Table.Name = "destination"
	native := `{"results":[],"error":"FOREIGN KEY constraint failed","raft_index":9007199254740993}`
	out := contract.SchemaDesignOutcome{State: "uncertain", Actual: &actual, Receipt: &contract.SchemaDesignWriteReceipt{DocumentID: "structure:81:1", Sequence: 3, Operation: "apply", Original: original, Desired: &desired, Destination: "destination", AllowColumnDrop: true, Statements: []string{"ALTER TABLE main.source RENAME TO destination"}, NativeJSON: native, State: "uncertain", Actual: &actual}}
	copy := out.Clone()
	if !reflect.DeepEqual(out, copy) {
		t.Fatal("complete original evidence changed during cloning")
	}
	copy.Actual.Schema.SQLite.Entries[0].SQLNull = false
	copy.Receipt.Actual.Schema.SQLite.Entries[1].SQL = "new receipt"
	copy.Receipt.Original.Schema.Table.Clauses[0].SQL = "TEXT"
	copy.Receipt.Original.Schema.SQLite.Version++
	copy.Receipt.Desired.Clauses[0].Name = "other"
	copy.Receipt.Statements[0] = "replay"
	if out.Receipt.NativeJSON != native || out.Receipt.Original.Schema.SQLite.Version != 12 || out.Receipt.Original.Schema.Table.Clauses[0].SQL != "INTEGER" || out.Receipt.Desired.Clauses[0].Name != "id" || out.Receipt.Statements[0] == "replay" || !out.Actual.Schema.SQLite.Entries[0].SQLNull || out.Receipt.Actual.Schema.SQLite.Entries[1].SQL == "new receipt" {
		t.Fatal("a projection overwrote original commit evidence")
	}
	empty := contract.SchemaDesignWriteReceipt{Statements: []string{}}.Clone()
	if empty.Statements == nil {
		t.Fatal("complete empty statement array became absent")
	}
}

func TestSchemaDocumentRequiresStableBoundedIdentity(t *testing.T) {
	r := contract.SchemaDesignDocumentRequest{DocumentID: "结构:81:1", Target: contract.SchemaDesignTarget{Object: contract.ObjectRef{ProfileID: 81, Catalog: "main", Kind: "table", Name: "t"}, Generation: 2}}
	if !r.Valid() {
		t.Fatal("qualified stable document rejected")
	}
	for _, id := range []string{"", strings.Repeat("d", 257), "embedded\ncontrol", "bad\x00id", "\xff"} {
		x := r
		x.DocumentID = id
		if x.Valid() {
			t.Fatal("invalid stable identity accepted")
		}
	}
	for _, edit := range []func(*contract.SchemaDesignDocumentRequest){func(r *contract.SchemaDesignDocumentRequest) { r.Target.Generation = 0 }, func(r *contract.SchemaDesignDocumentRequest) { r.Target.Object.ProfileID = 0 }, func(r *contract.SchemaDesignDocumentRequest) { r.Target.Object.Kind = "view" }, func(r *contract.SchemaDesignDocumentRequest) { r.Target.Object.Name = "" }} {
		x := r
		edit(&x)
		if x.Valid() {
			t.Fatal("incomplete original document scope accepted")
		}
	}
}
