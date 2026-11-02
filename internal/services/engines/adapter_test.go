package engines

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"path/filepath"
	"testing"
)

func adapterFixture(t *testing.T) *Session {
	t.Helper()
	raw, e := OpenSession(context.Background(), connections.Profile{ID: 42, Kind: connections.SQLite, Database: filepath.Join(t.TempDir(), "adapter.sqlite")}, "")
	if e != nil {
		t.Fatal(e)
	}
	s := raw.(*Session)
	t.Cleanup(func() { s.Close() })
	lease, e := s.Reserve(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	defer lease.Close()
	_, e = lease.Execute(context.Background(), `CREATE TABLE records(a INTEGER NOT NULL,b TEXT NOT NULL,value,payload BLOB,generated TEXT GENERATED ALWAYS AS (b||'x'),PRIMARY KEY(a,b)); INSERT INTO records(a,b,value,payload) VALUES(9223372036854775807,'NULL','',x''),(2,'second',NULL,x'00ff'); CREATE VIEW readonly AS SELECT * FROM records;`)
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func TestAdapterSnapshotTypesAndConflictAreLossless(t *testing.T) {
	s := adapterFixture(t)
	r := s.DataRuntime()
	ctx := context.Background()
	page, e := r.AdvancedLoader(ctx, 42, "main", "records", contract.DataOptions{Limit: 20})
	if e != nil {
		t.Fatal(e)
	}
	if len(page.Rows) != 2 || page.RowSnapshots[0].Mode != "values" || len(page.RowSnapshots[0].Original) != 5 {
		t.Fatal("full row snapshot absent")
	}
	row := page.Rows[1]
	if row[0].Value.Kind != contract.Integer || row[0].Value.Text != "9223372036854775807" || row[1].Value.Kind != contract.Text || row[1].Value.Text != "NULL" || row[2].Value.Text != "" || row[2].Value.Kind == contract.Null || row[3].Value.Kind != contract.Binary || len(row[3].Value.Bytes) != 0 {
		t.Fatalf("typed values changed: %+v", row)
	}
	raw, e := json.Marshal(page.RowSnapshots[1])
	if e != nil {
		t.Fatal(e)
	}
	var restored contract.RowSnapshot
	if e = json.Unmarshal(raw, &restored); e != nil {
		t.Fatal(e)
	}
	if restored.Original[0].Text != row[0].Value.Text || restored.Original[3].Kind != contract.Binary || len(restored.Ref.TypedValues) != 2 {
		t.Fatal("snapshot round trip changed key or raw values")
	}
	read, e := r.RowReader(ctx, 42, "main", "records", restored.Ref)
	if e != nil {
		t.Fatal(e)
	}
	if read.Values["payload"].Kind != contract.Binary || read.Values["value"].Kind == contract.Null {
		t.Fatal("row editor inferred NULL")
	}
	lease, _ := s.Reserve(ctx)
	_, e = lease.Execute(ctx, `UPDATE records SET value='external' WHERE b='NULL'`)
	lease.Close()
	if e != nil {
		t.Fatal(e)
	}
	_, e = r.Saver(ctx, 42, "main", "records", []contract.DataEdit{{Kind: "update", Original: &restored, Values: map[string]contract.DataValue{"value": {Kind: contract.Text, Text: "overwrite"}}}})
	if !errors.Is(e, ErrConflict) {
		t.Fatalf("external conflict accepted: %v", e)
	}
	view, e := r.AdvancedLoader(ctx, 42, "main", "readonly", contract.DataOptions{Limit: 20})
	if e != nil {
		t.Fatal(e)
	}
	if *view.Editable || view.Object.Kind != "view" || view.RowSnapshots[0] != nil {
		t.Fatal("view granted editable identity")
	}
}
func TestAdapterTransactionFailedBatchRollsBackOnlyThatRequest(t *testing.T) {
	s := adapterFixture(t)
	r := s.DataRuntime()
	ctx := context.Background()
	tx, e := r.Transactions.Begin(ctx, 42, "main", "records")
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Close()
	page, e := r.Transactions.Page(ctx, tx, 42, "main", "records", contract.DataOptions{Limit: 20})
	if e != nil {
		t.Fatal(e)
	}
	first := contract.DataEdit{Kind: "update", Original: page.RowSnapshots[0], Values: map[string]contract.DataValue{"value": {Kind: contract.Integer, Text: "3"}}}
	if _, e = r.Transactions.Save(ctx, tx, 42, "main", "records", []contract.DataEdit{first}); e != nil {
		t.Fatal(e)
	}
	page, e = r.Transactions.Page(ctx, tx, 42, "main", "records", contract.DataOptions{Limit: 20})
	if e != nil {
		t.Fatal(e)
	}
	batch := []contract.DataEdit{{Kind: "update", Original: page.RowSnapshots[0], Values: map[string]contract.DataValue{"value": {Kind: contract.Text, Text: "must rollback"}}}, {Kind: "update", Original: page.RowSnapshots[1], Values: map[string]contract.DataValue{"generated": {Kind: contract.Text, Text: "illegal"}}}}
	if _, e = r.Transactions.Save(ctx, tx, 42, "main", "records", batch); e == nil {
		t.Fatal("generated field accepted")
	}
	if e = r.Transactions.Commit(ctx, tx); e != nil {
		t.Fatal(e)
	}
	page, e = r.AdvancedLoader(ctx, 42, "main", "records", contract.DataOptions{Limit: 20})
	if e != nil {
		t.Fatal(e)
	}
	if page.Rows[0][2].Value.Kind != contract.Integer || page.Rows[0][2].Value.Text != "3" {
		t.Fatalf("prior staged request lost or partial batch applied: %+v", page.Rows[0])
	}
	if _, e = r.AdvancedLoader(ctx, 42, "main", "records", contract.DataOptions{Limit: 20, FilterMode: "contains", FilterColumn: "b", FilterValue: "x"}); !errors.Is(e, contract.ErrUnsupported) {
		t.Fatal("filter semantics silently changed")
	}
}
func TestAdapterRejectsForeignTransactionAndValidationPayload(t *testing.T) {
	s, other := adapterFixture(t), adapterFixture(t)
	ctx := context.Background()
	tx, e := s.DataRuntime().Transactions.Begin(ctx, 42, "main", "records")
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Close()
	if e = other.DataRuntime().Transactions.Commit(ctx, tx); e == nil {
		t.Fatal("foreign transaction accepted")
	}
	page, e := s.DataRuntime().AdvancedLoader(ctx, 42, "main", "records", contract.DataOptions{Limit: 20})
	if e != nil {
		t.Fatal(e)
	}
	snapshot := page.RowSnapshots[0]
	snapshot.Mode = "fingerprints"
	if _, e = s.DataRuntime().Saver(ctx, 42, "main", "records", []contract.DataEdit{{Kind: "delete", Original: snapshot}}); e == nil {
		t.Fatal("foreign verification payload accepted")
	}
}

func TestSQLiteConflictChecksStorageTypeAndExactText(t *testing.T) {
	s := adapterFixture(t)
	ctx := context.Background()
	lease, e := s.Reserve(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer lease.Close()
	if _, e = lease.Execute(ctx, `CREATE TABLE strict_snapshot(id INTEGER PRIMARY KEY,value COLLATE NOCASE); INSERT INTO strict_snapshot VALUES(1,1),(2,'Alpha');`); e != nil {
		t.Fatal(e)
	}
	r := s.DataRuntime()
	page, e := r.AdvancedLoader(ctx, 42, "main", "strict_snapshot", contract.DataOptions{Limit: 20})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = lease.Execute(ctx, `UPDATE strict_snapshot SET value=1.0 WHERE id=1; UPDATE strict_snapshot SET value='alpha' WHERE id=2;`); e != nil {
		t.Fatal(e)
	}
	for _, snapshot := range page.RowSnapshots {
		if _, e = r.Saver(ctx, 42, "main", "strict_snapshot", []contract.DataEdit{{Kind: "delete", Original: snapshot}}); !errors.Is(e, ErrConflict) {
			t.Fatalf("raw row change was missed: %v", e)
		}
	}
}
func TestAdapterRejectsMalformedTypedValuesWithoutChangingRow(t *testing.T) {
	s := adapterFixture(t)
	r := s.DataRuntime()
	ctx := context.Background()
	page, e := r.AdvancedLoader(ctx, 42, "main", "records", contract.DataOptions{Limit: 20})
	if e != nil {
		t.Fatal(e)
	}
	for _, value := range []contract.DataValue{{Kind: contract.Integer, Text: "not an integer"}, {Kind: contract.Integer, Text: "9223372036854775808"}, {Kind: contract.Boolean, Text: "maybe"}, {Kind: contract.Floating, Text: "not a float"}, {Kind: contract.Decimal, Text: "bad decimal"}, {Kind: contract.JSON, Text: "{"}} {
		if _, e = r.Saver(ctx, 42, "main", "records", []contract.DataEdit{{Kind: "update", Original: page.RowSnapshots[0], Values: map[string]contract.DataValue{"value": value}}}); e == nil {
			t.Fatalf("invalid typed value silently changed semantics: %+v", value)
		}
	}
	after, e := r.AdvancedLoader(ctx, 42, "main", "records", contract.DataOptions{Limit: 20})
	if e != nil {
		t.Fatal(e)
	}
	if after.Rows[0][2].Text != page.Rows[0][2].Text {
		t.Fatal("invalid request wrote data")
	}
}

func TestPortableUnsupportedFeaturesFailAtContractLookup(t *testing.T) {
	s := adapterFixture(t)
	if _, err := contract.Require[contract.DataImportsProvider](s); !errors.Is(err, contract.ErrUnsupported) {
		t.Fatalf("unsupported imports resolved: %v", err)
	}
	if op, err := contract.Require[contract.DataAnalysisProvider](s); err != nil || op.DataAnalysisOperations().Analyzer == nil {
		t.Fatalf("implemented analysis unavailable: %v", err)
	}
}
