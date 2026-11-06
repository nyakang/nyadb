package engines

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/queryplan"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func cockroachFixture(t *testing.T) (*Session, connections.Profile) {
	t.Helper()
	if os.Getenv("NYADB_COCKROACH_IT") != "1" {
		t.Skip("requires explicit disposable CockroachDB fixture")
	}
	port, err := strconv.Atoi(os.Getenv("NYADB_COCKROACH_PORT"))
	if err != nil || port < 1024 || port > 65535 {
		t.Fatal("explicit disposable loopback port required")
	}
	p, _ := connections.Defaults(connections.CockroachDB)
	p.ID, p.Name, p.Host, p.Port, p.Database, p.Username = 902, "CockroachDB fixture", "127.0.0.1", uint16(port), "nyadb_cockroach_it", "nyadb_it"
	p.PostgreSQLOptions.CAFile = os.Getenv("NYADB_COCKROACH_CA_FILE")
	m := NewManager()
	t.Cleanup(func() { m.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err = m.Connect(ctx, p, os.Getenv("NYADB_COCKROACH_PASSWORD")); err != nil {
		t.Fatal(err)
	}
	s, err := m.Session(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	return s, p
}
func TestCockroachNativeMetadataGridAndPlansFixture(t *testing.T) {
	s, p := cockroachFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	l, err := s.Reserve(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	run := func(q string) contract.SQLResult {
		t.Helper()
		r, e := l.Execute(ctx, q)
		if e != nil {
			t.Fatalf("fixture SQL: %v", e)
		}
		return r
	}
	schema := fmt.Sprintf("nyadb_crdb_%d", time.Now().UnixNano())
	run("CREATE SCHEMA " + quote(schema))
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if _, e := s.db.ExecContext(cleanup, "DROP SCHEMA "+quote(schema)+" CASCADE"); e != nil {
			t.Error(e)
		}
	}()
	o := s.Namespace(schema).Object("table", "records")
	table := qualified(o)
	run("CREATE TABLE " + table + " (id INT8 PRIMARY KEY, note STRING, amount DECIMAL(40,20), payload BYTES, detail STRING, twice DECIMAL(40,20) AS (amount*2) STORED, CONSTRAINT positive CHECK(amount>=0))")
	run("INSERT INTO " + table + " (id,note,amount,payload,detail) VALUES(1,'alpha',9007199254740993.12345678901234567890,decode('00ff','hex'),repeat('长',2000)),(2,NULL,2,NULL,''),(3,'gamma',3,decode('','hex'),'NULL')")
	run("CREATE VIEW " + quote(schema) + ".record_view AS SELECT id,note FROM " + table)
	run("CREATE SEQUENCE " + quote(schema) + ".seq START 123 INCREMENT 5 CACHE 7")
	run("CREATE FUNCTION " + quote(schema) + ".plus_one(n INT8) RETURNS INT8 LANGUAGE SQL AS $$ SELECT n+1 $$")
	run("CREATE TABLE " + quote(schema) + ".child(a INT8 PRIMARY KEY, parent INT8 REFERENCES " + table + "(id))")
	objects, err := s.Browse(ctx, s.Namespace(schema))
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]contract.DBObject{}
	for _, o := range objects {
		if o.Ref.ProfileID != p.ID || o.Ref.Catalog != p.Database || o.Ref.Schema != schema || o.Engine != "CockroachDB" {
			t.Fatal("namespace/product lost")
		}
		found[o.Name] = o
	}
	if found["record_view"].Ref.Kind != "view" || found["seq"].Sequence == nil || found["seq"].Sequence.Start != 123 || found["seq"].Sequence.Cache != 7 || !found["seq"].Sequence.DefinitionFromServer {
		t.Fatal("native sequence/view metadata", found["seq"].Sequence)
	}
	function := false
	for _, o := range objects {
		if o.Ref.Kind == "function" && strings.Contains(o.Definition, "CREATE FUNCTION") {
			function = true
		}
	}
	if !function {
		t.Fatal("routine definitions unavailable")
	}
	meta, err := s.TableMetadata(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	if !meta.Editable || len(meta.Indexes) == 0 || !strings.Contains(meta.Definition, "STORED") || !meta.Columns[5].Generated {
		t.Fatal("native complete structure", meta)
	}
	child, err := s.TableMetadata(ctx, s.Namespace(schema).Object("table", "child"))
	if err != nil || len(child.ForeignKeys) != 1 || len(child.ForeignKeys[0].Columns) != 1 {
		t.Fatal("foreign key metadata", err, child)
	}
	d := s.DataRuntime()
	page, err := d.AdvancedLoader(ctx, p.ID, schema, "records", contract.DataOptions{Object: o, Limit: 1, UseKeyset: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Rows) != 1 || page.Rows[0][2].Value.Kind != contract.Decimal || page.Rows[0][2].Text != "9007199254740993.12345678901234567890" || page.Rows[0][3].Value.Kind != contract.Binary || page.NextCursor == nil {
		t.Fatal("typed page/keyset", page)
	}
	next, err := d.AdvancedLoader(ctx, p.ID, schema, "records", contract.DataOptions{Object: o, Limit: 1, UseKeyset: true, After: page.NextCursor})
	if err != nil || next.Rows[0][0].Text != "2" {
		t.Fatal("seek page", err, next)
	}
	chunk, err := d.Chunk(ctx, p.ID, schema, "records", "detail", *page.RowRefs[0], 0, 1024)
	if err != nil || !chunk.HasMore || chunk.NextOffset != 1024 {
		t.Fatal("bounded field chunk", err, chunk)
	}
	complete, err := d.RowReader(ctx, p.ID, schema, "records", *page.RowRefs[0])
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.Saver(ctx, p.ID, schema, "records", []contract.DataEdit{{Kind: "update", Original: complete.Snapshot, Values: map[string]contract.DataValue{"twice": {Text: "0", Kind: contract.Decimal}}}})
	if err == nil {
		t.Fatal("generated column writable")
	}
	edit := contract.DataEdit{Kind: "update", Original: complete.Snapshot, Values: map[string]contract.DataValue{"note": {Text: "saved", Kind: contract.Text}}}
	saved, err := d.Saver(ctx, p.ID, schema, "records", []contract.DataEdit{edit})
	if err != nil || !saved.CommitConfirmed || !saved.Verified || saved.Applied != 1 {
		t.Fatal("confirmed save", err, saved, page.RowSnapshots[0], meta.Object)
	}
	_, err = d.Saver(ctx, p.ID, schema, "records", []contract.DataEdit{edit})
	if !errors.Is(err, ErrConflict) {
		t.Fatal("stale row not rejected", err)
	}
	q, err := queryplan.Build("cockroachdb", "SELECT id FROM "+table+" WHERE amount>0")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := queryplan.Parse("cockroachdb", run(q))
	if err != nil || len(plan.Nodes) != 1 || plan.Nodes[0].Rows == nil || plan.Nodes[0].Cost == nil {
		t.Fatal("native plan", err, plan)
	}
	analysis, err := d.Analyzer(ctx, p.ID, schema, "records", contract.DataOptions{}, []string{"amount", "payload", "note"})
	if err != nil || analysis.Rows != 3 {
		t.Fatal("data analysis", err, analysis)
	}
	count, err := d.Count(ctx, p.ID, schema, "records")
	if err != nil || count != 3 {
		t.Fatal("filter/count", err, count)
	}
	run("CREATE TABLE " + quote(schema) + ".unkeyed (note STRING)")
	run("CREATE UNIQUE INDEX partial_note ON " + quote(schema) + ".unkeyed(note) WHERE note IS NOT NULL")
	unkeyed, err := s.Structure(ctx, s.Namespace(schema).Object("table", "unkeyed"))
	if err != nil || unkeyed.Editable || len(unkeyed.Key) != 0 {
		t.Fatal("hidden/partial key incorrectly editable", err, unkeyed)
	}
	imported, err := s.importData(ctx, p.ID, schema, "records", contract.DataImportRequest{Edits: []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{"id": {Text: "4", Kind: contract.Integer}, "note": {Text: "imported"}, "amount": {Text: "4", Kind: contract.Decimal}}}}})
	if err != nil || !imported.Committed {
		t.Fatal("import existing table", err, imported)
	}
	created, err := s.importNewTable(ctx, p.ID, schema, "imported", []contract.DataImportColumn{{Name: "id", Type: "BIGINT", PrimaryKey: true}, {Name: "note", Type: "TEXT", Nullable: true}}, []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{"id": {Text: "1", Kind: contract.Integer}, "note": {Text: "imported"}}}})
	if err != nil || !created.Created || !created.Saved.CommitConfirmed {
		t.Fatal("atomic new table import", err, created)
	}
	failed, err := s.importNewTable(ctx, p.ID, schema, "failed_import", []contract.DataImportColumn{{Name: "id", Type: "BIGINT", PrimaryKey: true}}, []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{"id": {Text: "1", Kind: contract.Integer}}}, {Kind: "insert", Values: map[string]contract.DataValue{"id": {Text: "1", Kind: contract.Integer}}}})
	if err == nil || failed.Created || failed.CreationUncertain {
		t.Fatal("failed import lost atomicity", err, failed)
	}
	if run("SELECT count(*) FROM information_schema.tables WHERE table_schema='" + schema + "' AND table_name='failed_import'").Rows[0][0] != "0" {
		t.Fatal("failed import left a table")
	}
	var exported strings.Builder
	if err = d.Exporter(ctx, p.ID, schema, "records", contract.DataOptions{}, []string{"amount"}, func(values []contract.DataValue) error { exported.WriteString(values[0].Text); return nil }); err != nil {
		t.Fatal("export", err)
	}
	if !strings.Contains(exported.String(), "9007199254740993.12345678901234567890") {
		t.Fatal("export lost precision")
	}
	l.Close()
	verifyQueryPoolIsolation(t, s, schema)
}

func TestCockroachSerializationRollbackAndManagedRetryFixture(t *testing.T) {
	s, p := cockroachFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	s.db.SetMaxOpenConns(1)
	schema := fmt.Sprintf("nyadb_crdb_retry_%d", time.Now().UnixNano())
	if _, err := s.db.ExecContext(ctx, "CREATE SCHEMA "+quote(schema)); err != nil {
		t.Fatal(err)
	}
	defer s.db.ExecContext(context.Background(), "DROP SCHEMA "+quote(schema)+" CASCADE")
	o := s.Namespace(schema).Object("table", "records")
	if _, err := s.db.ExecContext(ctx, "CREATE TABLE "+qualified(o)+"(id INT PRIMARY KEY,note STRING)"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, "INSERT INTO "+qualified(o)+" VALUES(1,'original')"); err != nil {
		t.Fatal(err)
	}
	d := s.DataRuntime()
	page, err := d.AdvancedLoader(ctx, p.ID, schema, "records", contract.DataOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	transaction, err := s.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tx := transaction.(*Tx)
	if _, err = tx.Apply(ctx, o, []contract.Edit{{Kind: "insert", Values: map[string]contract.Cell{"id": {Kind: contract.Integer, Text: "2"}, "note": {Kind: contract.Text, Text: "staged"}}}}); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.tx.ExecContext(ctx, "SET inject_retry_errors_enabled=true"); err != nil {
		t.Fatal(err)
	}
	_, err = tx.Apply(ctx, o, []contract.Edit{{Kind: "insert", Values: map[string]contract.Cell{"id": {Kind: contract.Integer, Text: "3"}}}})
	if !errors.Is(err, contract.ErrTransactionRetry) || !tx.closed || tx.uncertain {
		t.Fatal("explicit transaction was not settled as aborted", err)
	}
	tx.Close()
	if _, err = s.db.ExecContext(ctx, "SET inject_retry_errors_enabled=false"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = s.db.QueryRowContext(ctx, "SELECT count(*) FROM "+qualified(o)).Scan(&count); err != nil || count != 1 {
		t.Fatal("serialization replayed staged writes", err, count)
	}
	edit := []contract.DataEdit{{Kind: "update", Original: page.RowSnapshots[0], Values: map[string]contract.DataValue{"note": {Text: "saved after retry", Kind: contract.Text}}}}
	if _, err = s.db.ExecContext(ctx, "SET inject_retry_errors_enabled=true"); err != nil {
		t.Fatal(err)
	}
	result, err := d.Saver(ctx, p.ID, schema, "records", edit)
	if !errors.Is(err, contract.ErrTransactionRetry) || result.CommitConfirmed || result.CommitUncertain || result.Applied != 0 {
		t.Fatal("bounded managed retry did not settle aborted attempts", err, result)
	}
	if _, err = s.db.ExecContext(ctx, "SET inject_retry_errors_enabled=false"); err != nil {
		t.Fatal(err)
	}
	attempts := 0
	result, err = retryCockroachSave(ctx, func() (*contract.DataEditResult, error) {
		attempts++
		if attempts == 1 {
			if _, err := s.db.ExecContext(ctx, "SET inject_retry_errors_enabled=true"); err != nil {
				return nil, err
			}
		}
		out, err := s.saveDataOnce(ctx, p.ID, schema, "records", edit, nil)
		if attempts == 1 {
			if _, reset := s.db.ExecContext(ctx, "SET inject_retry_errors_enabled=false"); reset != nil {
				return nil, reset
			}
		}
		return out, err
	})
	if err != nil || attempts != 2 || !result.CommitConfirmed || result.Applied != 1 {
		t.Fatal("real aborted batch did not retry and confirm exactly once", err, result, attempts)
	}
	var note string
	if err = s.db.QueryRowContext(ctx, "SELECT note FROM "+qualified(o)+" WHERE id=1").Scan(&note); err != nil || note != "saved after retry" {
		t.Fatal("retry result", err, note)
	}
}
