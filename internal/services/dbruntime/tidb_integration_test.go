package dbruntime_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/mysqlddl"
	"github.com/nyakang/nyadb/internal/db/queryplan"
	"github.com/nyakang/nyadb/internal/services/backups"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

// Only tools/verify_tidb_fixture.py's disposable loopback database is used.
func TestTiDBWorkbenchDisposableFixture(t *testing.T) {
	if os.Getenv("NYADB_TIDB_IT") != "1" {
		t.Skip("requires disposable TiDB fixture")
	}
	port, err := strconv.Atoi(os.Getenv("NYADB_TIDB_PORT"))
	if err != nil || port < 1024 || port > 65535 {
		t.Fatal("explicit disposable loopback port required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)
	m := dbruntime.New()
	t.Cleanup(func() { m.Close() })
	dbruntime.RegisterBuiltins(m, nil)
	p, _ := connections.Defaults(connections.TiDB)
	p.ID, p.Name, p.Host, p.Port, p.Username, p.Database = 701, "TiDB fixture", "127.0.0.1", uint16(port), "nyadb_tidb", "nyadb_tidb_it"
	p.TLSMode, p.MySQLAdvanced.SSL.Enabled = "verify-full", true
	p.MySQLAdvanced.SSL.CACertificate = os.Getenv("NYADB_TIDB_CA_FILE")
	if err = m.Connect(ctx, p, os.Getenv("NYADB_TIDB_PASSWORD")); err != nil {
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
	t.Cleanup(lease.Release)
	execute := func(t *testing.T, sql string) contract.SQLResult {
		t.Helper()
		r, e := lease.Execute(ctx, p.Database, sql)
		if e != nil {
			t.Fatalf("fixture SQL failed: %v", e)
		}
		return r
	}
	p.Database = fmt.Sprintf("nyadb_tidb_workbench_%d", time.Now().UnixNano())
	_, err = lease.Execute(ctx, "nyadb_tidb_it", "CREATE DATABASE `"+p.Database+"`")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		// A fresh lease also works after the revocation test replaces the session.
		query, e := m.Session(p.ID)
		if e != nil {
			t.Error(e)
			return
		}
		l, e := query.(contract.QueryProvider).ReserveQuery(99)
		if e != nil {
			t.Error(e)
			return
		}
		defer l.Release()
		_, e = l.Execute(cleanup, "nyadb_tidb_it", "DROP DATABASE IF EXISTS `"+p.Database+"`")
		if e != nil {
			t.Error(e)
		}
	})
	execute(t, "SET GLOBAL tidb_enable_check_constraint=ON")
	t.Cleanup(func() {
		q, e := m.Session(p.ID)
		if e == nil {
			l, e := q.(contract.QueryProvider).ReserveQuery(98)
			if e == nil {
				defer l.Release()
				_, _ = l.Execute(context.Background(), "nyadb_tidb_it", "SET GLOBAL tidb_enable_check_constraint=OFF")
			}
		}
	})

	version := execute(t, "SELECT VERSION()")
	t.Log("server", version.Rows[0][0])
	if cipher := execute(t, "SHOW SESSION STATUS LIKE 'Ssl_cipher'"); len(cipher.Rows) != 1 || cipher.Rows[0][1] == "" {
		t.Fatal("TiDB SQL lease did not use TLS")
	}
	execute(t, "CREATE TABLE records (id BIGINT PRIMARY KEY, note VARCHAR(80), amount DECIMAL(40,20), payload BLOB, detail LONGTEXT, twice DECIMAL(40,20) GENERATED ALWAYS AS (amount*2) STORED, CONSTRAINT ck_amount CHECK(amount>=0)) ENGINE=InnoDB")
	execute(t, "INSERT INTO records (id,note,amount,payload,detail) VALUES (1,'alpha',9007199254740993.12345678901234567890,X'00ff',REPEAT('长',2000)),(2,NULL,2,NULL,''),(3,'gamma',3,X'','NULL')")
	execute(t, "CREATE VIEW record_view AS SELECT id,note,'before' AS marker FROM records")
	execute(t, "CREATE SEQUENCE fixture_seq START WITH 100 INCREMENT BY 2 CACHE 5")

	runtime := m.Data(p.ID)
	admin := m.Administration(p.ID)
	if admin == nil {
		t.Fatal("missing shared administration backend")
	}
	t.Run("CHECK execution guard", func(t *testing.T) {
		execute(t, "SET GLOBAL tidb_enable_check_constraint=OFF")
		defer execute(t, "SET GLOBAL tidb_enable_check_constraint=ON")
		table, e := mysqlddl.Parse(p.Database, "check_guard", "CREATE TABLE `check_guard`(`id` INT, CONSTRAINT `ck` CHECK(`id`>0)) ENGINE=InnoDB")
		if e != nil {
			t.Fatal(e)
		}
		plan, e := mysqlddl.Build(mysqlddl.Snapshot{Engine: "tidb", Database: p.Database, Table: "check_guard", RuntimeGeneration: session.Generation()}, mysqlddl.Draft{New: true, Table: table})
		if e != nil {
			t.Fatal(e)
		}
		out := admin.ApplyTableDesign(ctx, p.ID, plan)
		if out.State != "not_executed" || !errors.Is(out.Error, contract.ErrUnsupported) {
			t.Fatalf("CHECK disabled: %+v", out)
		}
		if execute(t, "SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA='"+p.Database+"' AND TABLE_NAME='check_guard'").Rows[0][0] != "0" {
			t.Fatal("CHECK guard mutated the target")
		}
		execute(t, "SET GLOBAL tidb_enable_check_constraint=ON")
		out = admin.ApplyTableDesign(ctx, p.ID, plan)
		if out.Error != nil || !out.Verified {
			t.Fatalf("CHECK enabled: %+v", out)
		}
		if _, e = lease.Execute(ctx, p.Database, "INSERT INTO check_guard VALUES(-1)"); e == nil {
			t.Fatal("CHECK not enforced")
		}
		execute(t, "DROP TABLE check_guard")
	})
	t.Run("accounts roles and grants", func(t *testing.T) {
		user := mysqlsvc.MySQLAccount{User: fmt.Sprintf("nyadb_u_%d", time.Now().UnixNano()), Host: "%"}
		role := mysqlsvc.MySQLAccount{User: fmt.Sprintf("nyadb_r_%d", time.Now().UnixNano()), Host: "%"}
		t.Cleanup(func() {
			a := m.Administration(p.ID)
			if a != nil {
				o := a.ChangeAccounts(context.Background(), p.ID, m.Generation(p.ID), []mysqlsvc.AccountChange{{Action: "drop", Account: user}, {Action: "drop_role", Account: role}})
				if o.Error != nil {
					t.Error(o.Error)
				}
			}
		})
		changes := []mysqlsvc.AccountChange{{Action: "create_user", Account: user, Password: os.Getenv("NYADB_TIDB_PASSWORD")}, {Action: "create_role", Account: role}, {Action: "grant", Account: role, Database: p.Database, Privileges: []string{"SELECT"}}, {Action: "grant_role", Account: user, Role: role}, {Action: "lock", Account: user}, {Action: "unlock", Account: user}, {Action: "password", Account: user, Password: os.Getenv("NYADB_TIDB_PASSWORD")}, {Action: "revoke_role", Account: user, Role: role}, {Action: "grant", Account: user, Database: p.Database, Privileges: []string{"SELECT"}}}
		out := admin.ChangeAccounts(ctx, p.ID, session.Generation(), changes)
		if out.Error != nil || !out.Verified || len(out.Steps) != len(changes) {
			t.Fatalf("account mutations: %v", out.Error)
		}
		grants, e := admin.AccountGrants(ctx, p.ID, user)
		if e != nil || !strings.Contains(strings.Join(grants, " "), "SELECT") {
			t.Fatal("account grants", e)
		}
		out = admin.ChangeAccounts(ctx, p.ID, session.Generation(), []mysqlsvc.AccountChange{{Action: "grant", Account: user, Database: p.Database, Privileges: []string{"TRIGGER"}}})
		if out.State != "not_executed" || !errors.Is(out.Error, contract.ErrUnsupported) {
			t.Fatal("unsupported privilege was executed")
		}
	})
	t.Run("capabilities and qualified metadata", func(t *testing.T) {
		caps := session.Capabilities()
		if !caps.EditableTables || !caps.Transactions || !caps.DesignTables || !caps.Backup || !caps.Import || !caps.Accounts {
			t.Fatal("incorrect TiDB capability boundaries")
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
			t.Fatal("TiDB database identity missing")
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
		for _, name := range []string{"records", "record_view", "fixture_seq"} {
			if !names[name] {
				t.Fatal("missing object", name)
			}
		}
		structure, e := meta.TableMetadata(ctx, m.Namespace(p.ID, p.Database).Object("table", "records"))
		if e != nil || len(structure.Columns) != 6 {
			t.Fatal("metadata", e)
		}
		if !strings.Contains(structure.Columns[5].Extra, "GENERATED") {
			t.Fatal("generated column lost")
		}
		if _, e := admin.ObjectDefinition(ctx, p.ID, mysqlsvc.ObjectIdentity{Database: p.Database, Name: "unsupported", Kind: "function"}); !errors.Is(e, contract.ErrUnsupported) {
			t.Fatal("TiDB program guard", e)
		}
		for _, item := range []mysqlsvc.ObjectBatchItem{{Ref: mysqlsvc.ObjectIdentity{Database: p.Database, Name: "fixture_seq", Kind: "sequence"}, Action: "copy", Destination: "sequence_copy"}, {Ref: mysqlsvc.ObjectIdentity{Database: p.Database, Name: "sequence_copy", Kind: "sequence"}, Action: "drop"}} {
			out := admin.ApplyObjectBatch(ctx, p.ID, session.Generation(), []mysqlsvc.ObjectBatchItem{item})
			if len(out) != 1 || out[0].Outcome.Error != nil || !out[0].Outcome.Verified {
				t.Fatal("sequence batch operation", out)
			}
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
	t.Run("optimistic SQL and pinned pessimistic grid transactions", func(t *testing.T) {
		execute(t, "SET GLOBAL tidb_txn_mode='optimistic'")
		defer execute(t, "SET GLOBAL tidb_txn_mode='pessimistic'")
		tx, e := runtime.Transactions.Begin(ctx, p.ID, p.Database, "records")
		if e != nil {
			t.Fatal("grid begin against optimistic default", e)
		}
		defer tx.Close()
		inside, e := runtime.Transactions.Page(ctx, tx, p.ID, p.Database, "records", contract.DataOptions{Limit: 1})
		if e != nil {
			t.Fatal(e)
		}
		_, e = runtime.Transactions.Save(ctx, tx, p.ID, p.Database, "records", []contract.DataEdit{{Kind: "update", Original: inside.RowSnapshots[0], Values: map[string]contract.DataValue{"note": {Text: "mode draft"}}}})
		if e != nil {
			t.Fatal(e)
		}
		if e = runtime.Transactions.Rollback(ctx, tx); e != nil {
			t.Fatal(e)
		}
		execute(t, "CREATE TABLE txn_mode_probe(id INT PRIMARY KEY)")
		for i, mode := range []string{"OPTIMISTIC", "PESSIMISTIC"} {
			execute(t, "BEGIN "+mode)
			execute(t, fmt.Sprintf("INSERT INTO txn_mode_probe VALUES(%d)", i+1))
			execute(t, "COMMIT")
		}
		if execute(t, "SELECT COUNT(*) FROM txn_mode_probe").Rows[0][0] != "2" {
			t.Fatal("SQL transaction modes failed")
		}
	})
	t.Run("explain import export and analysis", func(t *testing.T) {
		sql, e := queryplan.Build("tidb", "SELECT id FROM records ORDER BY amount")
		if e != nil {
			t.Fatal(e)
		}
		result := execute(t, sql)
		plan, e := queryplan.Parse("tidb", result)
		if e != nil || len(plan.Nodes) == 0 || plan.Engine != "tidb" {
			t.Fatal("TiDB EXPLAIN", e)
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
		newTable, e := runtime.NewTableImporter(ctx, p.ID, p.Database, "import_created", []contract.DataImportColumn{{Name: "id", Type: "BIGINT", PrimaryKey: true}, {Name: "note", Type: "VARCHAR(80)", Nullable: true}}, []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{"id": {Text: "1"}, "note": {Text: "导入"}}}})
		if e != nil || newTable.Saved == nil || !newTable.Saved.CommitConfirmed {
			t.Fatal("new-table import", e)
		}
	})
	t.Run("table designer check removal copy and view", func(t *testing.T) {
		original, e := admin.TableDesign(ctx, p.ID, p.Database, "records")
		if e != nil || original.Engine != "tidb" || original.RuntimeGeneration != session.Generation() {
			t.Fatal("designer original", e)
		}
		table, e := mysqlddl.Parse(p.Database, "records", original.DDL)
		if e != nil {
			t.Fatal(e)
		}
		clauses := table.Clauses[:0]
		for _, c := range table.Clauses {
			if c.Kind != mysqlddl.Check {
				clauses = append(clauses, c)
			}
		}
		table.Clauses = clauses
		plan, e := mysqlddl.Build(original, mysqlddl.Draft{Table: table, Triggers: original.Triggers})
		if e != nil || !strings.Contains(strings.Join(plan.Statements, " "), "DROP CONSTRAINT") {
			t.Fatal("TiDB check SQL", e)
		}
		out := admin.ApplyTableDesign(ctx, p.ID, plan)
		if out.Error != nil || !out.Verified {
			t.Fatalf("designer alter: %+v", out)
		}
		newTable, e := mysqlddl.Parse(p.Database, "created", "CREATE TABLE `created` (`id` bigint NOT NULL, `note` varchar(50) DEFAULT 'Case', `document` json, `active` boolean NOT NULL DEFAULT 1, PRIMARY KEY (`id`)) ENGINE=InnoDB")
		if e != nil {
			t.Fatal(e)
		}
		newOriginal := mysqlddl.Snapshot{Engine: "tidb", Database: p.Database, Table: "created", RuntimeGeneration: session.Generation()}
		createPlan, e := mysqlddl.Build(newOriginal, mysqlddl.Draft{New: true, Table: newTable})
		if e != nil {
			t.Fatal(e)
		}
		out = admin.ApplyTableDesign(ctx, p.ID, createPlan)
		if out.Error != nil || !out.Verified {
			t.Fatalf("designer create: %+v", out)
		}
		execute(t, "CREATE TABLE child(id BIGINT PRIMARY KEY, parent BIGINT, CONSTRAINT fk_parent FOREIGN KEY(parent) REFERENCES records(id))")
		indexOriginal, e := admin.TableDesign(ctx, p.ID, p.Database, "child")
		if e != nil {
			t.Fatal(e)
		}
		indexTable, e := mysqlddl.Parse(p.Database, "child", indexOriginal.DDL)
		if e != nil {
			t.Fatal(e)
		}
		index, e := mysqlddl.ParseClause("KEY `extra_idx` (`parent`) INVISIBLE")
		if e != nil {
			t.Fatal(e)
		}
		indexTable.Clauses = append(indexTable.Clauses, index)
		indexPlan, e := mysqlddl.Build(indexOriginal, mysqlddl.Draft{Table: indexTable})
		if e != nil {
			t.Fatal(e)
		}
		out = admin.ApplyTableDesign(ctx, p.ID, indexPlan)
		if out.Error != nil || !out.Verified {
			t.Fatalf("foreign key / invisible index: %+v", out)
		}
		indexOriginal = *out.Actual
		indexTable, e = mysqlddl.Parse(p.Database, "child", indexOriginal.DDL)
		if e != nil {
			t.Fatal(e)
		}
		for i, c := range indexTable.Clauses {
			if c.Name == "extra_idx" {
				indexTable.Clauses[i].SQL = strings.Replace(c.SQL, "INVISIBLE", "VISIBLE", 1)
			}
		}
		indexPlan, e = mysqlddl.Build(indexOriginal, mysqlddl.Draft{Table: indexTable})
		if e != nil {
			t.Fatal(e)
		}
		out = admin.ApplyTableDesign(ctx, p.ID, indexPlan)
		if out.Error != nil || !out.Verified {
			t.Fatalf("index replacement: %+v", out)
		}
		if e = admin.ChangeTable(ctx, p.ID, mysqlsvc.MySQLTableChange{Action: mysqlsvc.MySQLTableCopyStructure, Database: p.Database, Table: "records", NewName: "records_copy"}); e != nil {
			t.Fatal("table copy", e)
		}
		originalView, e := admin.ObjectDefinition(ctx, p.ID, mysqlsvc.ObjectIdentity{Database: p.Database, Name: "record_view", Kind: "view"})
		if e != nil {
			t.Fatal(e)
		}
		viewPlan, e := mysqlsvc.BuildObjectPlan(originalView, "alter", strings.Replace(originalView.SQL, "'before'", "'after'", 1))
		if e != nil {
			t.Fatal(e)
		}
		viewOut := admin.ApplyObject(ctx, p.ID, viewPlan)
		if viewOut.Error != nil || !viewOut.Verified {
			t.Fatalf("view alter: %+v", viewOut)
		}
	})
	t.Run("backup restore and maintenance", func(t *testing.T) {
		foreignPath := filepath.Join(t.TempDir(), "mysql.nyabackup")
		foreignFile, e := os.Create(foreignPath)
		if e != nil {
			t.Fatal(e)
		}
		writer := backups.NewWriter(foreignFile, p.Database, "8.0.36")
		if e = writer.Add(ctx, backups.Object{Kind: "DATABASE", Name: p.Database}, "CREATE DATABASE `"+p.Database+"`", nil); e != nil {
			t.Fatal(e)
		}
		if _, e = writer.Finish(ctx); e != nil {
			t.Fatal(e)
		}
		if e = foreignFile.Close(); e != nil {
			t.Fatal(e)
		}
		foreignReader, e := backups.Open(ctx, foreignPath)
		if e != nil {
			t.Fatal(e)
		}
		defer foreignReader.Close()
		if _, e = admin.PlanRestore(ctx, p.ID, session.Generation(), p.Database, "replace", foreignReader); !errors.Is(e, contract.ErrUnsupported) {
			t.Fatal("unsafe MySQL-to-TiDB restore accepted", e)
		}
		path := filepath.Join(t.TempDir(), "tidb.nyabackup")
		f, e := os.Create(path)
		if e != nil {
			t.Fatal(e)
		}
		_, e = admin.BackupMySQL(ctx, p.ID, mysqlsvc.BackupRequest{Database: p.Database, Generation: session.Generation(), Data: true}, f)
		closeErr := f.Close()
		if e != nil || closeErr != nil {
			t.Fatal("backup", e, closeErr)
		}
		reader, e := backups.Open(ctx, path)
		if e != nil {
			t.Fatal(e)
		}
		defer reader.Close()
		sequenceBackedUp := false
		for _, object := range reader.Manifest().Objects {
			if object.Kind == "SEQUENCE" && object.Name == "fixture_seq" && object.Data == "" {
				sequenceBackedUp = true
			}
		}
		if !sequenceBackedUp || !strings.Contains(strings.Join(reader.Manifest().Warnings, " "), "definition only") {
			t.Fatal("sequence backup omitted its state limitation")
		}
		execute(t, "UPDATE records SET note='changed' WHERE id=1")
		plan, e := admin.PlanRestore(ctx, p.ID, session.Generation(), p.Database, "replace", reader)
		if e != nil {
			t.Fatal("restore plan", e)
		}
		out := admin.RestoreMySQL(ctx, plan)
		if out.Error != nil || !out.Verified {
			t.Fatalf("restore: %+v", out)
		}
		restored := execute(t, "SELECT note,HEX(payload),CHAR_LENGTH(detail),amount FROM records WHERE id=1")
		if strings.Join(restored.Rows[0], "|") != "transaction|0100FF|2000|9007199254740993.12345678901234567890" {
			t.Fatal("restore lost data")
		}
		var dump strings.Builder
		if e = mysqlsvc.SQLDump(ctx, reader, &dump, true); e != nil || !strings.Contains(dump.String(), "9007199254740993.12345678901234567890") || !strings.Contains(dump.String(), "current/allocated values are not included") {
			t.Fatal("SQL backup lost precision", e)
		}
		target := contract.SchemaDesignTarget{Object: m.Namespace(p.ID, p.Database).Object("table", "records"), Generation: session.Generation()}
		maintenance := m.Maintenance(p.ID)
		mp, e := maintenance.Plan(ctx, contract.MaintenanceRequest{Target: target, Action: "ANALYZE"})
		if e != nil {
			t.Fatal(e)
		}
		mo := maintenance.Execute(ctx, mp)
		if mo.Error != nil || len(mo.Messages) == 0 {
			t.Fatal("maintenance", mo.Error)
		}
		mp, e = maintenance.Plan(ctx, contract.MaintenanceRequest{Target: target, Action: "CHECK"})
		if e != nil || !strings.HasPrefix(mp.SQL, "ADMIN CHECK TABLE") {
			t.Fatal("TiDB check plan", e)
		}
		mo = maintenance.Execute(ctx, mp)
		if mo.Error != nil {
			t.Fatal("TiDB check", mo.Error)
		}
		for _, action := range []string{"OPTIMIZE", "REPAIR"} {
			if _, e = maintenance.Plan(ctx, contract.MaintenanceRequest{Target: target, Action: action}); !errors.Is(e, contract.ErrUnsupported) {
				t.Fatal("unsupported maintenance offered", action)
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
		if e = m.Connect(ctx, p, os.Getenv("NYADB_TIDB_PASSWORD")); e != nil {
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
