package dbruntime_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	mssql "github.com/microsoft/go-mssqldb"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/queryplan"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
)

func TestSQLServerWorkbenchDisposableFixture(t *testing.T) {
	if os.Getenv("NYADB_SQLSERVER_IT") != "1" {
		t.Skip("requires disposable SQL Server fixture")
	}
	port, err := strconv.Atoi(os.Getenv("NYADB_SQLSERVER_PORT"))
	if err != nil || port < 1024 || port > 65535 {
		t.Fatal("explicit loopback fixture port required")
	}
	password := os.Getenv("NYADB_SQLSERVER_PASSWORD")
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	u := url.URL{Scheme: "sqlserver", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), User: url.UserPassword("sa", password)}
	u.RawQuery = url.Values{"database": {"master"}, "encrypt": {"true"}, "TrustServerCertificate": {"true"}}.Encode()
	c, err := mssql.NewConnector(u.String())
	if err != nil {
		t.Fatal("fixture connector failed")
	}
	db := sql.OpenDB(c)
	defer db.Close()
	if _, err = db.ExecContext(ctx, "IF DB_ID(N'nyadb_sqlserver_it') IS NULL CREATE DATABASE nyadb_sqlserver_it"); err != nil {
		t.Fatal("fixture database creation failed")
	}
	m := dbruntime.New()
	defer m.Close()
	dbruntime.RegisterBuiltins(m, nil)
	p, _ := connections.Defaults(connections.SQLServer)
	p.ID, p.Name, p.Host, p.Port, p.Database = 801, "SQL Server fixture", "127.0.0.1", uint16(port), "nyadb_sqlserver_it"
	p.TLSMode = "require"
	if ca := os.Getenv("NYADB_SQLSERVER_CA_FILE"); ca != "" {
		p.TLSMode = "verify-full"
		p.SQLServerOptions.CAFile = ca
	}
	if err = m.Connect(ctx, p, password); err != nil {
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
	run := func(source string) contract.SQLResult {
		t.Helper()
		r, err := lease.Execute(ctx, "dbo", source)
		if err != nil {
			t.Fatal("fixture SQL failed", err)
		}
		return r
	}
	version := run("SELECT CONVERT(nvarchar(100),SERVERPROPERTY('ProductVersion'))")
	t.Log("SQL Server", version.Rows[0][0])
	run(`DROP TABLE IF EXISTS dbo.records;
CREATE TABLE dbo.records(id bigint IDENTITY PRIMARY KEY,note nvarchar(100),amount decimal(38,20),payload varbinary(max),detail nvarchar(max),guid uniqueidentifier DEFAULT NEWID(),stamp rowversion,twice AS amount*2);
INSERT dbo.records(note,amount,payload,detail) VALUES (N'alpha',9007199254740993.12345678901234567890,0x00ff,REPLICATE(CONVERT(nvarchar(max),N'长'),4000)),(NULL,2,NULL,N''),(N'gamma',3,0x,N'NULL');`)
	run("CREATE OR ALTER VIEW dbo.record_view AS SELECT id,note FROM dbo.records")
	run("CREATE OR ALTER PROCEDURE dbo.fixture_proc AS BEGIN SELECT COUNT_BIG(*) AS n FROM dbo.records; END")
	run("CREATE OR ALTER FUNCTION dbo.fixture_fn(@value int) RETURNS int AS BEGIN RETURN @value+1; END")
	run("IF OBJECT_ID(N'dbo.fixture_seq',N'SO') IS NOT NULL DROP SEQUENCE dbo.fixture_seq; CREATE SEQUENCE dbo.fixture_seq AS bigint START WITH 9223372036854775805 INCREMENT BY -2 MINVALUE -9223372036854775807 MAXVALUE 9223372036854775807 CACHE 17 CYCLE")
	r := m.Data(p.ID)
	object := m.Namespace(p.ID, "dbo").Object("table", "records")
	page := func() *contract.DataPage {
		t.Helper()
		v, err := r.AdvancedLoader(ctx, p.ID, "dbo", "records", contract.DataOptions{Object: object, Limit: 2})
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	t.Run("metadata typed paging and inspectors", func(t *testing.T) {
		meta, err := m.Metadata(p.ID)
		if err != nil {
			t.Fatal(err)
		}
		ns, err := meta.Namespaces(ctx)
		if err != nil || len(ns) == 0 || ns[0].Ref.Catalog != p.Database || ns[0].Ref.Schema != "dbo" {
			t.Fatal("namespace", err, ns)
		}
		objects, err := meta.Browse(ctx, object.Namespace())
		if err != nil {
			t.Fatal(err)
		}
		found := map[string]bool{}
		for _, o := range objects {
			found[o.Ref.Kind] = true
			if o.Ref.Kind == "sequence" && (o.Sequence == nil || o.Sequence.Start != 9223372036854775805 || o.Sequence.Increment != -2 || o.Sequence.Cache != 17 || !strings.Contains(o.Definition, "START WITH 9223372036854775805")) {
				t.Fatal("sequence snapshot lost exact values")
			}
		}
		if !found["table"] || !found["view"] || !found["procedure"] || !found["function"] || !found["sequence"] {
			t.Fatal("objects missing", found)
		}
		table, err := meta.TableMetadata(ctx, object)
		if err != nil || !table.Editable || len(table.Columns) != 8 || len(table.Indexes) == 0 {
			t.Fatal("table metadata", err)
		}
		v := page()
		if len(v.Rows) != 2 || !v.HasMore || !v.StableOrder || v.RowSnapshots[0] == nil || v.RowSnapshots[0].Mode != "fingerprints" {
			t.Fatal("paging identities")
		}
		if v.Rows[0][2].Value.Kind != contract.Decimal || v.Rows[0][2].Value.Text != "9007199254740993.12345678901234567890" || v.Rows[0][3].Value.Kind != contract.Binary || !bytes.Equal(v.Rows[0][3].Value.Bytes, []byte{0, 255}) || !v.Rows[0][4].Truncated || v.Rows[1][1].Value.Kind != contract.Null {
			t.Fatal("typed preview", v.Rows[0][2], v.Rows[0][3])
		}
		chunk, err := r.Chunk(ctx, p.ID, "dbo", "records", "detail", *v.RowRefs[0], 100, 1024)
		if err != nil || len([]rune(chunk.Text)) != 1024 || chunk.TotalUnits != 4000 || chunk.NextOffset != 1124 {
			t.Fatal("bounded field", err, chunk)
		}
		row, err := r.RowReader(ctx, p.ID, "dbo", "records", *v.RowRefs[0])
		if err != nil || row.Snapshot.Mode != "values" || len([]rune(row.Values["detail"].Text)) != 4000 {
			t.Fatal("complete row", err)
		}
		if n, err := r.Count(ctx, p.ID, "dbo", "records"); err != nil || n != 3 {
			t.Fatal("count", n, err)
		}
		view := object
		view.Kind, view.Name = "view", "record_view"
		v, err = r.AdvancedLoader(ctx, p.ID, "dbo", "record_view", contract.DataOptions{Object: view, Limit: 10})
		if err != nil || v.Editable == nil || *v.Editable {
			t.Fatal("view writable", err)
		}
	})
	t.Run("atomic writes conflict and generated keys", func(t *testing.T) {
		v := page()
		saved, err := r.Saver(ctx, p.ID, "dbo", "records", []contract.DataEdit{{Kind: "update", Original: v.RowSnapshots[0], Values: map[string]contract.DataValue{"note": {Text: "saved"}}}})
		if err != nil || saved == nil || !saved.CommitConfirmed || saved.Applied != 1 || saved.Outcomes[0].Saved == nil {
			t.Fatal("save", err, saved)
		}
		if !saved.Outcomes[0].Previews[4].Truncated {
			t.Fatal("save dropped large field truncation")
		}
		if _, err = r.Saver(ctx, p.ID, "dbo", "records", []contract.DataEdit{{Kind: "update", Original: v.RowSnapshots[0], Values: map[string]contract.DataValue{"note": {Text: "stale"}}}}); !errors.Is(err, contract.ErrDataConflict) {
			t.Fatal("stale write accepted", err)
		}
		v = page()
		row, err := r.RowReader(ctx, p.ID, "dbo", "records", *v.RowRefs[0])
		if err != nil {
			t.Fatal(err)
		}
		if _, err = r.Saver(ctx, p.ID, "dbo", "records", []contract.DataEdit{{Kind: "update", Original: row.Snapshot, Values: map[string]contract.DataValue{"note": {Text: "values saved"}}}}); err != nil {
			t.Fatal("values snapshot save", err)
		}
		if _, err = r.Saver(ctx, p.ID, "dbo", "records", []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{"note": {Text: "inserted"}, "amount": {Text: "1.00000000000000000001"}}}}); err != nil {
			t.Fatal("identity insert", err)
		}
		v = page()
		run("UPDATE dbo.records SET note=N'VALUES SAVED ' WHERE id=1")
		if _, err = r.Saver(ctx, p.ID, "dbo", "records", []contract.DataEdit{{Kind: "delete", Original: v.RowSnapshots[0]}}); !errors.Is(err, contract.ErrDataConflict) {
			t.Fatal("case/trailing-space conflict ignored", err)
		}
	})
	t.Run("large values stay bounded without losing drafts or exports", func(t *testing.T) {
		run("DROP TABLE IF EXISTS dbo.large_values; CREATE TABLE dbo.large_values(id int PRIMARY KEY,note nvarchar(100),detail nvarchar(max)); INSERT dbo.large_values VALUES(1,N'small',REPLICATE(CONVERT(nvarchar(max),N'长'),600000))")
		o := m.Namespace(p.ID, "dbo").Object("table", "large_values")
		v, err := r.AdvancedLoader(ctx, p.ID, "dbo", "large_values", contract.DataOptions{Object: o, Limit: 2})
		if err != nil || len(v.Rows) != 1 || !v.Rows[0][2].Truncated {
			t.Fatal("large preview", err)
		}
		if _, err = r.RowReader(ctx, p.ID, "dbo", "large_values", *v.RowRefs[0]); !errors.Is(err, contract.ErrDataEditLimit) {
			t.Fatal("oversize complete read accepted", err)
		}
		writes := 0
		if err = r.Exporter(ctx, p.ID, "dbo", "large_values", contract.DataOptions{Object: o}, []string{"detail"}, func([]contract.DataValue) error { writes++; return nil }); !errors.Is(err, contract.ErrDataEditLimit) || writes != 0 {
			t.Fatal("oversize export silently changed data", err, writes)
		}
		if err = r.Exporter(ctx, p.ID, "dbo", "large_values", contract.DataOptions{Object: o}, []string{"note"}, func([]contract.DataValue) error { writes++; return nil }); err != nil || writes != 1 {
			t.Fatal("bounded selected export", err, writes)
		}
		saved, err := r.Saver(ctx, p.ID, "dbo", "large_values", []contract.DataEdit{{Kind: "update", Original: v.RowSnapshots[0], Values: map[string]contract.DataValue{"note": {Text: "saved beside large field"}}}})
		if err != nil || saved == nil || !saved.CommitConfirmed || !saved.Outcomes[0].Previews[2].Truncated {
			t.Fatal("large field edit safety", err, saved)
		}
	})
	t.Run("transaction export and analysis", func(t *testing.T) {
		v := page()
		tx, err := r.Transactions.Begin(ctx, p.ID, "dbo", "records")
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Close()
		if _, err = r.Transactions.Save(ctx, tx, p.ID, "dbo", "records", []contract.DataEdit{{Kind: "update", Original: v.RowSnapshots[0], Values: map[string]contract.DataValue{"note": {Text: "rolled back"}}}}); err != nil {
			t.Fatal(err)
		}
		if err = r.Transactions.Rollback(ctx, tx); err != nil {
			t.Fatal(err)
		}
		if page().Rows[0][1].Text == "rolled back" {
			t.Fatal("rollback persisted")
		}
		var values []contract.DataValue
		if err = r.Exporter(ctx, p.ID, "dbo", "records", contract.DataOptions{Object: object}, []string{"amount", "payload"}, func(row []contract.DataValue) error {
			if len(values) == 0 {
				values = row
			}
			return nil
		}); err != nil || values[0].Kind != contract.Decimal || values[1].Kind != contract.Binary {
			t.Fatal("typed export", err, values)
		}
		analysis, err := r.Analyzer(ctx, p.ID, "dbo", "records", contract.DataOptions{Object: object}, []string{"id"})
		if err != nil || analysis.Rows != 4 || analysis.Columns[0].Distinct != 4 {
			t.Fatal("analysis", err, analysis)
		}
	})
	t.Run("import batches and cancellation retain uncertain drafts", func(t *testing.T) {
		run("DROP TABLE IF EXISTS dbo.import_rows; CREATE TABLE dbo.import_rows(id bigint IDENTITY PRIMARY KEY,note nvarchar(100),amount decimal(38,20))")
		source := &sqlServerImportSource{rows: []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{"note": {Text: "stream one"}, "amount": {Text: "1.00000000000000000001"}}}, {Kind: "insert", Values: map[string]contract.DataValue{"note": {Text: "stream two"}, "amount": {Text: "2"}}}}}
		imported, err := r.StreamImporter(ctx, p.ID, "dbo", "import_rows", contract.DataImportStreamRequest{Generation: session.Generation(), Rows: 2, Request: contract.DataImportRequest{Mode: "append"}, Open: func(context.Context) (contract.DataImportRowSource, error) { return source, nil }})
		if err != nil || imported == nil || !imported.Committed || imported.Progress.Prepared != 2 {
			t.Fatal("stream import", err, imported)
		}
		if n, err := r.Count(ctx, p.ID, "dbo", "import_rows"); err != nil || n != 2 {
			t.Fatal("streamed rows missing", n, err)
		}
		run("CREATE OR ALTER TRIGGER dbo.records_delay ON dbo.records AFTER UPDATE AS BEGIN WAITFOR DELAY '00:00:10'; END")
		v := page()
		cctx, ccancel := context.WithTimeout(ctx, 150*time.Millisecond)
		saved, err := r.Saver(cctx, p.ID, "dbo", "records", []contract.DataEdit{{Kind: "update", Original: v.RowSnapshots[0], Values: map[string]contract.DataValue{"note": {Text: "must not replay"}}}})
		ccancel()
		if saved == nil || !saved.CommitUncertain || !errors.Is(err, contract.ErrDataCommitUncertain) {
			t.Fatal("cancelled write allowed replay", err, saved)
		}
		run("DROP TRIGGER dbo.records_delay")
		if page().Rows[0][1].Text == "must not replay" {
			t.Fatal("cancelled fixture write survived rollback")
		}
	})
	if p.TLSMode == "verify-full" {
		t.Run("certificate identity and SQL authentication", func(t *testing.T) {
			bad := p
			bad.ID = 802
			bad.SQLServerOptions.ServerName = "wrong.fixture.example"
			if err := m.Connect(ctx, bad, password); err == nil {
				t.Fatal("certificate hostname mismatch accepted")
			}
			bad = p
			bad.ID = 803
			if err := m.Connect(ctx, bad, "incorrect-fixture-password"); err == nil {
				t.Fatal("incorrect SQL password accepted")
			}
		})
	}
	t.Run("batch leases showplan and cancellation", func(t *testing.T) {
		if result := run("CREATE TABLE #lease_keep(n int); INSERT #lease_keep VALUES (42)"); result.AffectedRows != 1 {
			t.Fatal("mutation row count", result.AffectedRows)
		}
		if v := run("DECLARE @n int=3; SELECT @n AS n; SELECT n FROM #lease_keep"); len(v.Sets) != 2 || v.Sets[1].Rows[0][0].Text != "42" {
			t.Fatal("batch or physical lease changed", v)
		}
		text, err := queryplan.Build("sqlserver", "SELECT note FROM dbo.records WHERE id=1")
		if err != nil {
			t.Fatal(err)
		}
		plan, err := queryplan.Parse("sqlserver", run(text))
		if err != nil || len(plan.Nodes) == 0 {
			t.Fatal("showplan", err)
		}
		if run("SELECT COUNT_BIG(*) FROM dbo.records").Rows[0][0] != "4" {
			t.Fatal("SHOWPLAN not restored")
		}
		cctx, ccancel := context.WithTimeout(ctx, 100*time.Millisecond)
		defer ccancel()
		_, err = lease.Execute(cctx, "dbo", "WAITFOR DELAY '00:00:10'; SELECT 1")
		if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, contract.ErrSQLSessionLost) {
			t.Fatal("cancel state", err)
		}
		if _, err = lease.Execute(ctx, "dbo", "SELECT 1"); !errors.Is(err, contract.ErrSQLSessionLost) {
			t.Fatal("lost lease replayed", err)
		}
	})
	t.Run("generation revokes old data bindings", func(t *testing.T) {
		old := m.Data(p.ID)
		if err = m.Disconnect(p.ID); err != nil {
			t.Fatal(err)
		}
		if _, err = old.AdvancedLoader(ctx, p.ID, "dbo", "records", contract.DataOptions{Object: object, Limit: 2}); !errors.Is(err, contract.ErrRevoked) {
			t.Fatal("revoked data request accepted", err)
		}
		if err = m.Connect(ctx, p, password); err != nil {
			t.Fatal(err)
		}
		current, _ := m.Session(p.ID)
		if current.Generation() <= session.Generation() {
			t.Fatal("generation did not advance")
		}
	})
}

type sqlServerImportSource struct {
	rows  []contract.DataEdit
	index int
}

func (s *sqlServerImportSource) Next(ctx context.Context) (contract.DataEdit, error) {
	if err := ctx.Err(); err != nil {
		return contract.DataEdit{}, err
	}
	if s.index == len(s.rows) {
		return contract.DataEdit{}, io.EOF
	}
	row := s.rows[s.index]
	s.index++
	return row, nil
}
func (s *sqlServerImportSource) Close() error { return nil }
