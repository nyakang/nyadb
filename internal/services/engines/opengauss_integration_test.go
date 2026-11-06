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

func openGaussFixture(t *testing.T) (*Session, connections.Profile) {
	t.Helper()
	if os.Getenv("NYADB_OPENGAUSS_IT") != "1" {
		t.Skip("requires explicit disposable OpenGauss fixture")
	}
	port, err := strconv.Atoi(os.Getenv("NYADB_OPENGAUSS_PORT"))
	if err != nil || port < 1024 || port > 65535 {
		t.Fatal("explicit disposable loopback port required")
	}
	p, _ := connections.Defaults(connections.OpenGauss)
	p.ID, p.Name, p.Host, p.Port, p.Database, p.Username = 902, "OpenGauss fixture", "127.0.0.1", uint16(port), "nyadb_opengauss_it", "nyadb_it"
	p.TLSMode = "disable"
	p.PostgreSQLOptions.CAFile = os.Getenv("NYADB_OPENGAUSS_CA_FILE")
	if p.PostgreSQLOptions.CAFile != "" {
		p.TLSMode = "verify-full"
	}
	m := NewManager()
	t.Cleanup(func() { m.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err = m.Connect(ctx, p, os.Getenv("NYADB_OPENGAUSS_PASSWORD")); err != nil {
		t.Fatal(err)
	}
	s, err := m.Session(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	return s, p
}
func TestOpenGaussNativeMetadataGridAndPlansFixture(t *testing.T) {
	s, p := openGaussFixture(t)
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
	schema := fmt.Sprintf("nyadb_gauss_%d", time.Now().UnixNano())
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
	run("CREATE TABLE " + table + " (id BIGINT PRIMARY KEY, note TEXT, amount DECIMAL(40,20), payload BYTEA, detail TEXT, twice DECIMAL(40,20) GENERATED ALWAYS AS (amount*2) STORED, CONSTRAINT positive CHECK(amount>=0))")
	run("INSERT INTO " + table + " (id,note,amount,payload,detail) VALUES(1,'alpha',9007199254740993.12345678901234567890,decode('00ff','hex'),repeat('长',2000)),(2,NULL,2,NULL,''),(3,'gamma',3,decode('','hex'),'NULL')")
	run("CREATE VIEW " + quote(schema) + ".record_view AS SELECT id,note FROM " + table)
	run("CREATE SEQUENCE " + quote(schema) + ".seq START 123 INCREMENT 5 CACHE 7")
	run("CREATE FUNCTION " + quote(schema) + ".plus_one(n BIGINT) RETURNS BIGINT LANGUAGE SQL AS $$ SELECT n+1 $$")
	// Native procedural definitions are read through the same browse snapshot.
	// This server form has a slash delimiter in its emitted source.
	if _, err = s.db.ExecContext(ctx, "CREATE PROCEDURE "+quote(schema)+".noop() AS BEGIN NULL; END;"); err != nil {
		t.Fatal(err)
	}
	run("CREATE TABLE " + quote(schema) + ".child(a BIGINT PRIMARY KEY, parent BIGINT REFERENCES " + table + "(id))")
	objects, err := s.Browse(ctx, s.Namespace(schema))
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]contract.DBObject{}
	for _, o := range objects {
		if o.Ref.ProfileID != p.ID || o.Ref.Catalog != p.Database || o.Ref.Schema != schema || o.Engine != "openGauss" {
			t.Fatal("namespace/product lost")
		}
		found[o.Name] = o
	}
	if found["record_view"].Ref.Kind != "view" || found["seq"].Sequence == nil || found["seq"].Sequence.Start != 123 || found["seq"].Sequence.Cache != 7 {
		t.Fatal("native sequence/view metadata", found["seq"].Sequence)
	}
	function := false
	for _, o := range objects {
		if o.Ref.Kind == "function" && strings.Contains(o.Definition, "CREATE OR REPLACE FUNCTION") {
			function = true
		}
	}
	if !function {
		t.Fatal("routine definitions unavailable")
	}
	procedure := false
	for _, o := range objects {
		if o.Ref.Kind == "procedure" && strings.Contains(o.Definition, "CREATE OR REPLACE PROCEDURE") && strings.Contains(o.Definition, "END;") {
			procedure = true
		}
	}
	if !procedure {
		t.Fatal("native procedure definition unavailable")
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
	q, err := queryplan.Build("opengauss", "SELECT id FROM "+table+" WHERE amount>0")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := queryplan.Parse("opengauss", run(q))
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
	run("CREATE TABLE " + quote(schema) + ".unkeyed (note TEXT)")
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
