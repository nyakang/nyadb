package dbruntime_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/mysqlddl"
	"github.com/nyakang/nyadb/internal/db/queryplan"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

// Only tools/verify_oceanbase_fixture.sh's disposable loopback database is used.
func TestOceanBaseWorkbenchDisposableFixture(t *testing.T) {
	if os.Getenv("NYADB_OCEANBASE_IT") != "1" {
		t.Skip("requires disposable OceanBase fixture")
	}
	port, err := strconv.Atoi(os.Getenv("NYADB_OCEANBASE_PORT"))
	if err != nil || port < 1024 || port > 65535 {
		t.Fatal("explicit disposable loopback port required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	m := dbruntime.New()
	defer m.Close()
	dbruntime.RegisterBuiltins(m, nil)
	p, _ := connections.Defaults(connections.OceanBase)
	p.ID, p.Name, p.Host, p.Port, p.Username, p.Database = 701, "OceanBase fixture", "127.0.0.1", uint16(port), "nyadb_ob@test", "nyadb_oceanbase_it"
	p.TLSMode, p.MySQLAdvanced.SSL.Enabled = "verify-full", true
	p.MySQLAdvanced.SSL.CACertificate = os.Getenv("NYADB_OCEANBASE_CA_FILE")
	if err = m.Connect(ctx, p, os.Getenv("NYADB_OCEANBASE_PASSWORD")); err != nil {
		t.Fatal(err)
	}
	session, err := m.Session(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	query, err := contract.Require[contract.QueryProvider](session)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := query.ReserveQuery(1)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	execute := func(t *testing.T, sql string) contract.SQLResult {
		t.Helper()
		r, e := lease.Execute(ctx, p.Database, sql)
		if e != nil {
			t.Fatalf("fixture SQL failed: %v", e)
		}
		return r
	}
	p.Database = fmt.Sprintf("nyadb_ob_runtime_%d", time.Now().UnixNano())
	if _, err = lease.Execute(ctx, "nyadb_oceanbase_it", "CREATE DATABASE `"+p.Database+"`"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		current, e := m.Session(p.ID)
		if e != nil {
			return
		}
		l, e := current.(contract.QueryProvider).ReserveQuery(99)
		if e != nil {
			t.Error(e)
			return
		}
		defer l.Release()
		if _, e = l.Execute(cleanup, "nyadb_oceanbase_it", "DROP DATABASE IF EXISTS `"+p.Database+"`"); e != nil {
			t.Error(e)
		}
	}()
	version := execute(t, "SELECT VERSION()")
	t.Log("server", version.Rows[0][0])
	execute(t, "DROP TABLE IF EXISTS records")
	execute(t, "CREATE TABLE records (id BIGINT PRIMARY KEY, note VARCHAR(80), amount DECIMAL(40,20), payload BLOB, detail LONGTEXT, twice DECIMAL(40,20) GENERATED ALWAYS AS (amount*2) STORED, CONSTRAINT ck_amount CHECK(amount>=0)) ENGINE=InnoDB")
	execute(t, "INSERT INTO records (id,note,amount,payload,detail) VALUES (1,'alpha',9007199254740993.12345678901234567890,X'00ff',REPEAT('长',2000)),(2,NULL,2,NULL,''),(3,'gamma',3,X'','NULL')")
	execute(t, "CREATE VIEW record_view AS SELECT id,note,'before' AS marker FROM records")
	execute(t, "CREATE FUNCTION fixture_fn(v INT) RETURNS INT DETERMINISTIC RETURN v+1")
	execute(t, "CREATE PROCEDURE fixture_proc() SELECT COUNT(*) FROM records")
	execute(t, "CREATE EVENT fixture_event ON SCHEDULE EVERY 1 DAY DISABLE DO SELECT 1")
	execute(t, "CREATE SEQUENCE fixture_sequence START WITH 123 INCREMENT BY 5 CACHE 7 ORDER")
	runtime := m.Data(p.ID)
	admin := m.Administration(p.ID)
	if admin == nil {
		t.Fatal("missing shared administration backend")
	}
	t.Run("capabilities and qualified metadata", func(t *testing.T) {
		caps := session.Capabilities()
		if !caps.EditableTables || !caps.Transactions || !caps.Import || !caps.Explain || !caps.Objects || !caps.Maintenance || !caps.DesignViews || caps.Accounts || caps.DesignTables || caps.Backup || caps.Restore {
			t.Fatal("incorrect OceanBase capability boundaries")
		}
		if _, e := admin.Accounts(ctx, p.ID); !errors.Is(e, contract.ErrUnsupported) {
			t.Fatal("MySQL account queries enabled for OceanBase", e)
		}
		meta, e := m.Metadata(p.ID)
		if e != nil {
			t.Fatal(e)
		}
		ns, e := meta.Namespaces(ctx)
		if e != nil {
			t.Fatal(e)
		}
		found := false
		for _, n := range ns {
			if n.Ref.Catalog == p.Database && n.Ref.ProfileID == p.ID && n.Ref.Schema == "" {
				found = true
			}
		}
		if !found {
			t.Fatal("OceanBase database identity missing")
		}
		objects, e := meta.Browse(ctx, m.Namespace(p.ID, p.Database))
		if e != nil {
			t.Fatal(e)
		}
		names := map[string]bool{}
		for _, o := range objects {
			names[o.Name] = true
			if o.Ref.ProfileID != p.ID || o.Ref.Catalog != p.Database || o.Ref.Schema != "" {
				t.Fatal("unqualified object")
			}
		}
		for _, name := range []string{"records", "record_view", "fixture_fn", "fixture_proc", "fixture_event", "fixture_sequence"} {
			if !names[name] {
				t.Fatal("missing object", name)
			}
		}
		for _, o := range objects {
			if o.Name == "fixture_sequence" && (o.Sequence == nil || o.Sequence.Start != 123 || o.Sequence.Increment != 5 || o.Sequence.Cache != 7 || o.Sequence.DefinitionFromServer || !strings.Contains(o.Definition, p.Database) || !strings.HasSuffix(o.Definition, "ORDER")) {
				t.Fatal("qualified reconstructed sequence metadata lost")
			}
		}
		structure, e := meta.TableMetadata(ctx, m.Namespace(p.ID, p.Database).Object("table", "records"))
		if e != nil || len(structure.Columns) != 6 || !strings.Contains(structure.Definition, "COMPRESSION") {
			t.Fatal("metadata", e)
		}
		if !strings.Contains(structure.Columns[5].Extra, "GENERATED") {
			t.Fatal("generated column lost")
		}
		definition, e := admin.ObjectDefinition(ctx, p.ID, mysqlsvc.ObjectIdentity{Database: p.Database, Name: "fixture_fn", Kind: "function"})
		if e != nil || !strings.Contains(definition.SQL, "RETURN") {
			t.Fatal("program definition", e)
		}
	})
	t.Run("unsupported administration never executes", func(t *testing.T) {
		if _, e := admin.BackupMySQL(ctx, p.ID, mysqlsvc.BackupRequest{Database: p.Database, Generation: session.Generation(), Data: true}, io.Discard); !errors.Is(e, contract.ErrUnsupported) {
			t.Fatal("logical archive enabled", e)
		}
		out := admin.ApplyTableDesign(ctx, p.ID, mysqlddl.Plan{})
		if out.State != "not_executed" || !errors.Is(out.Error, contract.ErrUnsupported) {
			t.Fatal("unverified designer executed", out)
		}
		if _, e := admin.ObjectDefinition(ctx, p.ID, mysqlsvc.ObjectIdentity{Database: p.Database, Name: "fixture_sequence", Kind: "sequence"}); !errors.Is(e, contract.ErrUnsupported) {
			t.Fatal("sequence advertised as full public server DDL", e)
		}
		if _, e := admin.MaintainTable(ctx, p.ID, p.Database, "records", "REPAIR"); !errors.Is(e, contract.ErrUnsupported) {
			t.Fatal("unsupported maintenance accepted", e)
		}
	})
	page := func(t *testing.T, options contract.DataOptions) *contract.DataPage {
		t.Helper()
		r, e := runtime.AdvancedLoader(ctx, p.ID, p.Database, "records", options)
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	t.Run("typed paging filtering and bounded fields", func(t *testing.T) {
		first := page(t, contract.DataOptions{Limit: 1, UseKeyset: true})
		if !first.KeysetAvailable || first.NextCursor == nil || first.Rows[0][2].Value.Kind != contract.Decimal || first.Rows[0][2].Text != "9007199254740993.12345678901234567890" || first.Rows[0][3].Value.Kind != contract.Binary {
			t.Fatal("page lost keyset or typed precision")
		}
		second := page(t, contract.DataOptions{Limit: 1, UseKeyset: true, Offset: 1, After: first.NextCursor})
		if second.Rows[0][0].Text != "2" || !second.Rows[0][1].Null {
			t.Fatal("cursor boundary or NULL lost")
		}
		filtered := page(t, contract.DataOptions{Limit: 10, Filters: []contract.DataFilterRule{{Column: "note", Mode: "contains", Value: "a"}}, SortColumn: "id", SortDescending: true})
		if len(filtered.Rows) != 2 || filtered.Rows[0][0].Text != "3" {
			t.Fatal("filter/sort")
		}
		full, e := runtime.RowReader(ctx, p.ID, p.Database, "records", *first.RowRefs[0])
		if e != nil || len([]rune(full.Values["detail"].Text)) != 2000 || len(full.Values["payload"].Bytes) != 2 {
			t.Fatal("whole-cell read", e)
		}
		chunk, e := runtime.Chunk(ctx, p.ID, p.Database, "records", "detail", *first.RowRefs[0], 0, 1024)
		if e != nil || len([]rune(chunk.Text)) > 1024 || !chunk.HasMore {
			t.Fatal("bounded field read", e)
		}
		info, e := runtime.TableInfo(ctx, p.ID, p.Database, "records")
		if e != nil || info.Engine != "InnoDB" {
			t.Fatal("table information", e)
		}
	})
	t.Run("edits conflicts and transactions", func(t *testing.T) {
		original := page(t, contract.DataOptions{Limit: 1}).RowSnapshots[0]
		saved, e := runtime.Saver(ctx, p.ID, p.Database, "records", []contract.DataEdit{{Kind: "update", Original: original, Values: map[string]contract.DataValue{"note": {Text: "saved"}, "payload": {Binary: true, Bytes: []byte{1, 0, 255}}}}})
		if e != nil || !saved.CommitConfirmed || saved.Applied != 1 {
			t.Fatal("data save", e)
		}
		if _, e = runtime.Saver(ctx, p.ID, p.Database, "records", []contract.DataEdit{{Kind: "update", Original: original, Values: map[string]contract.DataValue{"note": {Text: "stale"}}}}); !errors.Is(e, mysqlsvc.ErrMySQLDataConflict) {
			t.Fatal("stale fingerprint accepted", e)
		}
		current := page(t, contract.DataOptions{Limit: 1}).RowSnapshots[0]
		if _, e = runtime.Saver(ctx, p.ID, p.Database, "records", []contract.DataEdit{{Kind: "update", Original: current, Values: map[string]contract.DataValue{"twice": {Text: "1"}}}}); e == nil {
			t.Fatal("generated column writable")
		}
		for _, commit := range []bool{false, true} {
			tx, e := runtime.Transactions.Begin(ctx, p.ID, p.Database, "records")
			if e != nil {
				t.Fatal(e)
			}
			defer tx.Close()
			inside, e := runtime.Transactions.Page(ctx, tx, p.ID, p.Database, "records", contract.DataOptions{Limit: 1})
			if e != nil {
				t.Fatal(e)
			}
			_, e = runtime.Transactions.Save(ctx, tx, p.ID, p.Database, "records", []contract.DataEdit{{Kind: "update", Original: inside.RowSnapshots[0], Values: map[string]contract.DataValue{"note": {Text: "transaction"}}}})
			if e != nil || page(t, contract.DataOptions{Limit: 1}).Rows[0][1].Text != "saved" {
				t.Fatal("transaction isolation", e)
			}
			if commit {
				e = runtime.Transactions.Commit(ctx, tx)
			} else {
				e = runtime.Transactions.Rollback(ctx, tx)
			}
			if e != nil {
				t.Fatal(e)
			}
		}
		if page(t, contract.DataOptions{Limit: 1}).Rows[0][1].Text != "transaction" {
			t.Fatal("commit not visible")
		}
	})
	t.Run("import export and analysis", func(t *testing.T) {
		sql, e := queryplan.Build("oceanbase", "SELECT id FROM records ORDER BY amount")
		if e != nil {
			t.Fatal(e)
		}
		plan, e := queryplan.Parse("oceanbase", execute(t, sql))
		if e != nil || len(plan.Nodes) != 1 || len(plan.Nodes[0].Children) == 0 {
			t.Fatal("OceanBase native plan", e)
		}
		imported, e := runtime.Importer(ctx, p.ID, p.Database, "records", contract.DataImportRequest{Edits: []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{"id": {Text: "4"}, "note": {Text: "imported"}, "amount": {Text: "4"}}}}})
		if e != nil || !imported.Committed {
			t.Fatal("import", e)
		}
		n := 0
		e = runtime.Exporter(ctx, p.ID, p.Database, "records", contract.DataOptions{}, []string{"id", "amount"}, func(row []contract.DataValue) error {
			n++
			if len(row) != 2 {
				return errors.New("export projection lost")
			}
			return nil
		})
		if e != nil || n != 4 {
			t.Fatal("export", n, e)
		}
		analysis, e := runtime.Analyzer(ctx, p.ID, p.Database, "records", contract.DataOptions{}, []string{"id"})
		if e != nil || analysis.Rows != 4 {
			t.Fatal("analysis", e)
		}
		created, e := runtime.NewTableImporter(ctx, p.ID, p.Database, "import_created", []contract.DataImportColumn{{Name: "id", Type: "BIGINT", PrimaryKey: true}, {Name: "note", Type: "VARCHAR(80)", Nullable: true}}, []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{"id": {Text: "1"}, "note": {Text: "导入"}}}})
		if e != nil || !created.Created || created.Saved == nil || !created.Saved.CommitConfirmed {
			t.Fatal("new table import", e)
		}
	})
	t.Run("view and complete table copy and maintenance", func(t *testing.T) {
		definition, e := admin.ObjectDefinition(ctx, p.ID, mysqlsvc.ObjectIdentity{Database: p.Database, Name: "record_view", Kind: "view"})
		if e != nil {
			t.Fatal(e)
		}
		plan, e := mysqlsvc.BuildObjectPlan(definition, "alter", strings.Replace(definition.SQL, "'before'", "'after'", 1))
		if e != nil {
			t.Fatal(e)
		}
		view := admin.ApplyObject(ctx, p.ID, plan)
		if view.Error != nil || !view.Verified {
			t.Fatal("view definition update", view.Error)
		}
		unsafe, e := mysqlsvc.BuildObjectPlan(*view.ActualDefinition, "alter", strings.Replace(view.ActualDefinition.SQL, "CREATE VIEW", "CREATE SQL SECURITY INVOKER VIEW", 1))
		if e != nil {
			t.Fatal(e)
		}
		refused := admin.ApplyObject(ctx, p.ID, unsafe)
		if refused.State != "not_executed" || !errors.Is(refused.Error, contract.ErrUnsupported) {
			t.Fatal("ignored view security change executed", refused)
		}
		for _, name := range []string{"fixture_fn", "fixture_proc"} {
			kind, old, updated := "function", "v+1", "v+2"
			if name == "fixture_proc" {
				kind, old, updated = "procedure", "COUNT(*)", "COUNT(id)"
			}
			original, e := admin.ObjectDefinition(ctx, p.ID, mysqlsvc.ObjectIdentity{Database: p.Database, Name: name, Kind: kind})
			if e != nil {
				t.Fatal(e)
			}
			change, e := mysqlsvc.BuildObjectPlan(original, "alter", strings.Replace(original.SQL, old, updated, 1))
			if e != nil {
				t.Fatal(e)
			}
			out := admin.ApplyObject(ctx, p.ID, change)
			if out.Error != nil || !out.Verified {
				t.Fatal("routine definition update", name, out.Error)
			}
		}
		if e = admin.ChangeTable(ctx, p.ID, mysqlsvc.MySQLTableChange{Action: mysqlsvc.MySQLTableCopyData, Database: p.Database, Table: "records", NewName: "records_copy"}); e != nil {
			t.Fatal("complete table copy", e)
		}
		ddl := execute(t, "SHOW CREATE TABLE records_copy")
		if !strings.Contains(ddl.Rows[0][1], "COMPRESSION") || execute(t, "SELECT COUNT(*) FROM records_copy").Rows[0][0] != "4" {
			t.Fatal("table copy lost native attributes or rows")
		}
		target := contract.SchemaDesignTarget{Object: m.Namespace(p.ID, p.Database).Object("table", "records"), Generation: session.Generation()}
		maintenance := m.Maintenance(p.ID)
		if len(maintenance.Actions(target)) != 2 {
			t.Fatal("maintenance exposed ignored commands")
		}
		for _, action := range []string{"ANALYZE", "CHECK"} {
			mp, e := maintenance.Plan(ctx, contract.MaintenanceRequest{Target: target, Action: action})
			if e != nil {
				t.Fatal(e)
			}
			out := maintenance.Execute(ctx, mp)
			if out.Error != nil || len(out.Messages) == 0 {
				t.Fatal("maintenance", out.Error)
			}
		}
	})
	t.Run("cancellation and original session revocation", func(t *testing.T) {
		slow, e := query.ReserveQuery(2)
		if e != nil {
			t.Fatal(e)
		}
		defer slow.Release()
		if e = slow.Preconnect(ctx); e != nil {
			t.Fatal(e)
		}
		timeout, stop := context.WithTimeout(ctx, 100*time.Millisecond)
		_, e = slow.Execute(timeout, p.Database, "SELECT SLEEP(5)")
		stop()
		if !errors.Is(e, context.DeadlineExceeded) {
			t.Fatal("query cancellation", e)
		}
		oldGeneration := session.Generation()
		if e = m.Disconnect(p.ID); e != nil {
			t.Fatal(e)
		}
		if e = m.Connect(ctx, p, os.Getenv("NYADB_OCEANBASE_PASSWORD")); e != nil {
			t.Fatal(e)
		}
		if m.Generation(p.ID) <= oldGeneration {
			t.Fatal("generation did not advance")
		}
		if _, e = lease.Execute(ctx, p.Database, "SELECT 1"); !errors.Is(e, contract.ErrRevoked) {
			t.Fatal("old lease routed to new session", e)
		}
		if _, e = runtime.Count(ctx, p.ID, p.Database, "records"); !errors.Is(e, contract.ErrRevoked) {
			t.Fatal("old data routed to replacement", e)
		}
		if _, e = admin.TableDesign(ctx, p.ID, p.Database, "records"); !errors.Is(e, contract.ErrRevoked) {
			t.Fatal("old administration routed to replacement", e)
		}
	})
}
