package duckdb

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/queryplan"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestDuckDBWorkbenchFixture(t *testing.T) {
	if os.Getenv("NYADB_DUCKDB_IT") != "1" {
		t.Skip("set NYADB_DUCKDB_IT=1 with the DuckDB runtime installed")
	}
	ctx := context.Background()
	p, _ := connections.Defaults(connections.DuckDB)
	p.ID, p.Name = 42, "DuckDB fixture"
	p.Database = filepath.Join(t.TempDir(), "fixture.duckdb")
	p.DuckDBOptions.RequireExisting = false
	opened, err := OpenSession(ctx, p, "")
	if err != nil {
		t.Fatal(err)
	}
	s := opened.(*Session)
	defer s.Close()
	l, err := s.ReserveQuery(1)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	setup := `CREATE SCHEMA sales; CREATE SEQUENCE sales.ids START 10;
CREATE TABLE sales.records(id BIGINT PRIMARY KEY DEFAULT nextval('sales.ids'), note VARCHAR, amount DECIMAL(38,20), payload BLOB, detail VARCHAR, uid UUID, x INTEGER, calculated INTEGER GENERATED ALWAYS AS (x*2));
INSERT INTO sales.records(note,amount,payload,detail,uid,x) VALUES ('first',9007199254740993.12345678901234567890,from_hex('00ff'),repeat('字',1500),'00112233-4455-6677-8899-aabbccddeeff',3),(NULL,NULL,NULL,'','11112233-4455-6677-8899-aabbccddeeff',4);
CREATE VIEW sales.v AS SELECT * FROM sales.records;
CREATE MACRO sales.twice(x) AS x*2;`
	result, err := l.Execute(ctx, "", setup)
	if err != nil {
		t.Fatal(err)
	}
	if result.AffectedRows != 2 {
		t.Fatalf("affected: %d %+v", result.AffectedRows, result.Sets)
	}
	if _, err := l.Execute(ctx, "sales", "CREATE INDEX records_note_idx ON sales.records(note)"); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []struct {
		sql      string
		affected int64
	}{
		{"SELECT COUNT(*) AS Count FROM sales.records", 0},
		{"WITH r AS (SELECT * FROM sales.records) SELECT COUNT(*) AS Count FROM r", 0},
		{"UPDATE sales.records SET x=x WHERE id=10 RETURNING id", 1},
		{"WITH r AS (SELECT 11 AS id) UPDATE sales.records SET x=records.x FROM r WHERE records.id=r.id RETURNING records.id", 1},
	} {
		got, err := l.Execute(ctx, "sales", statement.sql)
		if err != nil || got.AffectedRows != statement.affected {
			t.Fatalf("affected rows for %s: %d %v", statement.sql, got.AffectedRows, err)
		}
	}
	planSQL, err := queryplan.Build("duckdb", "SELECT id FROM sales.records WHERE id=10")
	if err != nil {
		t.Fatal(err)
	}
	planResult, err := l.Execute(ctx, "sales", planSQL)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := queryplan.Parse("duckdb", planResult)
	if err != nil || len(plan.Nodes) == 0 {
		t.Fatalf("real EXPLAIN: %+v %v", plan, err)
	}
	names, err := s.Namespaces(ctx)
	if err != nil || len(names) == 0 {
		t.Fatalf("namespaces: %+v %v", names, err)
	}
	objects, err := s.Browse(ctx, s.Namespace("sales"))
	if err != nil || len(objects) != 4 {
		t.Fatalf("objects: %+v %v", objects, err)
	}
	metadata, err := s.TableMetadata(ctx, s.Namespace("sales").Object("table", "records"))
	if err != nil {
		t.Fatal(err)
	}
	if !metadata.Editable || len(metadata.Columns) != 8 || !metadata.Columns[7].Generated {
		t.Fatalf("metadata: %+v", metadata)
	}
	foundIndex := false
	for _, idx := range metadata.Indexes {
		if idx.Name == "records_note_idx" && len(idx.Columns) == 1 && idx.Columns[0].Name == "note" {
			foundIndex = true
		}
	}
	if !foundIndex {
		t.Fatal("independent index metadata missing")
	}
	r := contract.ComposeData(s)
	o := metadata.Object
	page, err := r.AdvancedLoader(ctx, p.ID, "sales", "records", contract.DataOptions{Object: o, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Rows) != 2 || !page.Rows[0][4].Truncated || page.Rows[0][2].Value.Text != "9007199254740993.12345678901234567890" || !bytes.Equal(page.Rows[0][3].Value.Bytes, []byte{0, 255}) || page.Rows[0][5].Value.Text != "00112233-4455-6677-8899-aabbccddeeff" || !page.Rows[1][1].Null {
		t.Fatalf("page: %+v", page)
	}
	chunk, err := r.Chunk(ctx, p.ID, "sales", "records", "detail", *page.RowRefs[0], 1000, 500)
	if err != nil || chunk.TotalUnits != 1500 || chunk.NextOffset != 1500 || chunk.HasMore || len([]rune(chunk.Text)) != 500 {
		t.Fatalf("chunk %+v %v", chunk, err)
	}
	saved, err := r.Saver(ctx, p.ID, "sales", "records", []contract.DataEdit{{Kind: "update", Original: page.RowSnapshots[0], Values: map[string]contract.DataValue{"note": {Text: "saved\x00value"}}}})
	if err != nil || !saved.CommitConfirmed || !saved.Outcomes[0].Previews[4].Truncated {
		t.Fatalf("save %+v %v", saved, err)
	}
	if _, err = r.Saver(ctx, p.ID, "sales", "records", []contract.DataEdit{{Kind: "update", Original: page.RowSnapshots[0], Values: map[string]contract.DataValue{"note": {Text: "stale"}}}}); !errors.Is(err, contract.ErrDataConflict) {
		t.Fatalf("stale write: %v", err)
	}
	page, err = r.AdvancedLoader(ctx, p.ID, "sales", "records", contract.DataOptions{Object: o, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	row, err := r.RowReader(ctx, p.ID, "sales", "records", *page.RowRefs[0])
	if err != nil || row.Values["note"].Text != "saved\x00value" {
		t.Fatalf("full row %+v %v", row, err)
	}
	if _, err = r.Saver(ctx, p.ID, "sales", "records", []contract.DataEdit{{Kind: "update", Original: row.Snapshot, Values: map[string]contract.DataValue{"note": {Text: "values mode"}}}}); err != nil {
		t.Fatal(err)
	}
	tx, err := r.Transactions.Begin(ctx, p.ID, "sales", "records")
	if err != nil {
		t.Fatal(err)
	}
	page, err = r.Transactions.Page(ctx, tx, p.ID, "sales", "records", contract.DataOptions{Object: o, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.Transactions.Save(ctx, tx, p.ID, "sales", "records", []contract.DataEdit{{Kind: "update", Original: page.RowSnapshots[0], Values: map[string]contract.DataValue{"note": {Text: "rollback"}}}}); err != nil {
		t.Fatal(err)
	}
	if err = r.Transactions.Rollback(ctx, tx); err != nil {
		t.Fatal(err)
	}
	count, err := r.Count(ctx, p.ID, "sales", "records")
	if err != nil || count != 2 {
		t.Fatalf("count %d %v", count, err)
	}
	var exports int
	if err = r.Exporter(ctx, p.ID, "sales", "records", contract.DataOptions{Object: o}, []string{"amount", "payload"}, func(values []contract.DataValue) error { exports++; return nil }); err != nil || exports != 2 {
		t.Fatalf("export %d %v", exports, err)
	}
	analysis, err := r.Analyzer(ctx, p.ID, "sales", "records", contract.DataOptions{Object: o}, []string{"id", "note", "amount"})
	if err != nil || analysis.Rows != 2 {
		t.Fatalf("analysis %+v %v", analysis, err)
	}
	if _, err = r.Importer(ctx, p.ID, "sales", "records", contract.DataImportRequest{Mode: "append", Edits: []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{"note": {Text: "imported"}, "amount": {Text: "1.00000000000000000001"}, "x": {Text: "5"}}}}}); err != nil {
		t.Fatal(err)
	}
	stream := func(edits []contract.DataEdit) contract.DataImportStreamRequest {
		return contract.DataImportStreamRequest{Request: contract.DataImportRequest{Mode: "append"}, Rows: int64(len(edits)), Open: func(context.Context) (contract.DataImportRowSource, error) {
			return &fixtureImportSource{edits: edits}, nil
		}}
	}
	imported, err := r.StreamImporter(ctx, p.ID, "sales", "records", stream([]contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{"note": {Text: "streamed"}, "x": {Text: "6"}}}}))
	if err != nil || !imported.Committed || imported.Progress.Prepared != 1 {
		t.Fatalf("stream append: %+v %v", imported, err)
	}
	failed, err := r.StreamImporter(ctx, p.ID, "sales", "records", stream([]contract.DataEdit{
		{Kind: "insert", Values: map[string]contract.DataValue{"note": {Text: "must roll back"}, "x": {Text: "7"}}},
		{Kind: "insert", Values: map[string]contract.DataValue{"id": {Text: "10"}, "x": {Text: "8"}}},
	}))
	if err == nil || failed.Committed {
		t.Fatalf("failed stream committed: %+v %v", failed, err)
	}
	if len(failed.Rows) != 1 || failed.Rows[0].Status != "rolled_back" {
		t.Fatalf("failed import diagnostics: %+v", failed.Rows)
	}
	if n, err := r.Count(ctx, p.ID, "sales", "records"); err != nil || n != 4 {
		t.Fatalf("stream rollback row count: %d %v", n, err)
	}
	if _, err := l.Execute(ctx, "sales", "CREATE TABLE sales.special_values(id INTEGER PRIMARY KEY,t TIME_NS,ts TIMESTAMP_NS,bits BIT,n BIGNUM); INSERT INTO sales.special_values VALUES(1,'12:34:56.123456789','2026-11-06 12:34:56.123456789','101010','12345678901234567890123456789012345678901234567890')"); err != nil {
		t.Fatal(err)
	}
	special, err := r.AdvancedLoader(ctx, p.ID, "sales", "special_values", contract.DataOptions{Limit: 10})
	if err != nil || special.Rows[0][1].Value.Text != "12:34:56.123456789" || special.Rows[0][2].Value.Text != "2026-11-06 12:34:56.123456789" || special.Rows[0][3].Value.Text != "101010" || special.Rows[0][4].Value.Text != "12345678901234567890123456789012345678901234567890" {
		t.Fatalf("special grid values: %+v %v", special, err)
	}
	if _, err := r.Saver(ctx, p.ID, "sales", "special_values", []contract.DataEdit{{Kind: "update", Original: special.RowSnapshots[0], Values: map[string]contract.DataValue{"t": {Text: "13:14:15.987654321"}}}}); err != nil {
		t.Fatal("TIME_NS server cast save", err)
	}
	if _, err = l.Execute(ctx, "sales", `CREATE TEMP TABLE temp_records AS SELECT 1 AS x; SELECT * FROM temp_records; SELECT $$a;b$$ AS s;`); err != nil {
		t.Fatal(err)
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	_, err = l.Execute(cctx, "sales", "SELECT sum(a.i*b.i) FROM range(10000000) a(i),range(10000000) b(i)")
	if !errors.Is(err, contract.ErrSQLSessionLost) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("query cancellation: %v", err)
	}
	if _, err = l.Execute(ctx, "sales", "SELECT 1"); !errors.Is(err, contract.ErrSQLSessionLost) {
		t.Fatalf("query replay allowed: %v", err)
	}
	if _, err = s.db.ExecContext(ctx, "CREATE TABLE sales.large_values(id INTEGER PRIMARY KEY,detail VARCHAR,note VARCHAR)"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, "INSERT INTO sales.large_values VALUES(1,?,?)", strings.Repeat("x", contract.DataEditMaxBytes+1), "small"); err != nil {
		t.Fatal(err)
	}
	large, err := r.AdvancedLoader(ctx, p.ID, "sales", "large_values", contract.DataOptions{Limit: 10})
	if err != nil || !large.Rows[0][1].Truncated {
		t.Fatalf("large page %+v %v", large, err)
	}
	if _, err = r.RowReader(ctx, p.ID, "sales", "large_values", *large.RowRefs[0]); !errors.Is(err, contract.ErrDataEditLimit) {
		t.Fatalf("large read: %v", err)
	}
	exports = 0
	if err = r.Exporter(ctx, p.ID, "sales", "large_values", contract.DataOptions{}, []string{"detail"}, func([]contract.DataValue) error { exports++; return nil }); !errors.Is(err, contract.ErrDataEditLimit) || exports != 0 {
		t.Fatalf("large export %d %v", exports, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	p.DuckDBOptions.ReadOnly = true
	p.DuckDBOptions.RequireExisting = true
	readonly, err := OpenSession(ctx, p, "")
	if err != nil {
		t.Fatal(err)
	}
	defer readonly.Close()
	if readonly.Capabilities().EditableTables || readonly.Capabilities().Import {
		t.Fatal("readonly granted edits")
	}
}

type fixtureImportSource struct {
	edits []contract.DataEdit
	index int
}

func (s *fixtureImportSource) Next(ctx context.Context) (contract.DataEdit, error) {
	if err := ctx.Err(); err != nil {
		return contract.DataEdit{}, err
	}
	if s.index >= len(s.edits) {
		return contract.DataEdit{}, io.EOF
	}
	e := s.edits[s.index]
	s.index++
	return e, nil
}
func (s *fixtureImportSource) Close() error { return nil }

func TestDuckDBSharedFileAndCloseDuringQueryFixture(t *testing.T) {
	if os.Getenv("NYADB_DUCKDB_IT") != "1" {
		t.Skip("requires DuckDB native runtime")
	}
	p, _ := connections.Defaults(connections.DuckDB)
	p.ID, p.Name, p.Database = 51, "first", filepath.Join(t.TempDir(), "shared.duckdb")
	p.DuckDBOptions.RequireExisting = false
	ctx := context.Background()
	a, err := OpenSession(ctx, p, "")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	ql, _ := contract.Require[contract.QueryProvider](a)
	l, err := ql.ReserveQuery(1)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	if _, err = l.Execute(ctx, "main", "CREATE TABLE records(id INTEGER PRIMARY KEY); INSERT INTO records VALUES(1)"); err != nil {
		t.Fatal(err)
	}
	p.ID, p.Name = 52, "second"
	b, err := OpenSession(ctx, p, "")
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	p.DuckDBOptions.Threads++
	if conflict, err := OpenSession(ctx, p, ""); err == nil {
		conflict.Close()
		t.Fatal("conflicting instance settings accepted")
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	data := contract.ComposeData(b)
	if n, err := data.Count(ctx, b.ProfileID(), "main", "records"); err != nil || n != 1 {
		t.Fatalf("closing first profile broke second: %d %v", n, err)
	}
	provider, _ := contract.Require[contract.QueryProvider](b)
	worker, err := provider.ReserveQuery(2)
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Release()
	if err := worker.Preconnect(ctx); err != nil {
		t.Fatal(err)
	}
	completed := make(chan error, 1)
	go func() {
		_, err := worker.Execute(ctx, "main", "SELECT sum(a.i*b.i) FROM range(10000000) a(i),range(10000000) b(i)")
		completed <- err
	}()
	time.Sleep(20 * time.Millisecond)
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-completed:
		if err == nil {
			t.Fatal("closed query completed without cancellation")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("closing session did not acknowledge query cancellation")
	}
	if _, err := worker.Execute(ctx, "main", "SELECT 1"); !errors.Is(err, contract.ErrRevoked) {
		t.Fatalf("closed lease executed again: %v", err)
	}
}
