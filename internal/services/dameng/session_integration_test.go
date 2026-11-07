package dameng

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func fixtureSession(t *testing.T, c connections.DamengConfig, password string) *Session {
	t.Helper()
	p, _ := connections.Defaults(connections.Dameng)
	p.ID, p.Name, p.Host, p.Port, p.Database, p.Username, p.TLSMode, p.DamengOptions = 1201, "DM8 native session", c.Host, c.Port, c.Database, c.Username, c.TLSMode, c.Options
	opened, err := OpenSession(context.Background(), p, password)
	if err != nil {
		t.Fatal(err)
	}
	s := opened.(*Session)
	t.Cleanup(func() { s.Close() })
	return s
}

func TestNativeDamengSessionQualifiedMetadataAndSQLPhysicalLeases(t *testing.T) {
	c, password := nativeFixture(t)
	admin := nativeDB(t, c, password)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	schema := "NYADB_META_" + nativeSuffix(t) + ` 租户 "'; -- `
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+quote(schema)+" AUTHORIZATION SYSDBA"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.ExecContext(context.Background(), "DROP SCHEMA "+quote(schema)+" CASCADE") })
	prefix := quote(schema) + "."
	for _, q := range []string{
		"CREATE TABLE " + prefix + `"Parent" (ID BIGINT PRIMARY KEY)`,
		"CREATE TABLE " + prefix + `"Data" (ID BIGINT PRIMARY KEY,N DECIMAL(38,20),T VARCHAR(128) DEFAULT 'IDENTITY(1,1)',B VARBINARY(128),D TIMESTAMP(6),G BIGINT GENERATED ALWAYS AS (ID+1) VIRTUAL,I BIGINT IDENTITY(1,1),CONSTRAINT FK_PARENT FOREIGN KEY(ID) REFERENCES ` + prefix + `"Parent"(ID))`,
		"COMMENT ON TABLE " + prefix + `"Data" IS ' 中文 table '`,
		"COMMENT ON COLUMN " + prefix + `"Data".N IS ' exact NUMERIC '`,
		"CREATE VIEW " + prefix + `"View" AS SELECT ID,N,T FROM ` + prefix + `"Data"`,
		"CREATE SEQUENCE " + prefix + `"Sequence" START WITH 7 INCREMENT BY 2 CACHE 10`,
		"CREATE OR REPLACE PROCEDURE " + prefix + `"Procedure" AS BEGIN NULL; END;`,
	} {
		if _, err := admin.ExecContext(ctx, q); err != nil {
			t.Fatal("owned native metadata fixture failed", err)
		}
	}
	c.Options.DefaultSchema, c.Options.MaxConnections, c.Options.SQLMaxConnections = schema, 1, 2
	s := fixtureSession(t, c, password)
	if caps := s.Capabilities(); !caps.Query || !caps.Metadata || !caps.Schemas || !caps.EditableTables || caps.Import || caps.DesignTables || caps.Accounts || caps.Backup || caps.Restore {
		t.Fatal("native foundation advertised an unimplemented feature")
	}
	ns, err := s.Namespaces(ctx)
	if err != nil || len(ns) == 0 || ns[0].Ref != s.Namespace(schema) {
		t.Fatal("native schema identity/default was lost", err, ns)
	}
	objects, err := s.Browse(ctx, s.Namespace(schema))
	if err != nil {
		t.Fatal("native object/source browse failed", err)
	}
	found := make(map[string]contract.DBObject)
	for _, o := range objects {
		if o.Ref.ProfileID != s.ProfileID() || o.Ref.Catalog != "DAMENG" || o.Ref.Schema != schema || o.Engine != "Dameng" {
			t.Fatal("native object escaped original qualified identity")
		}
		found[o.Name] = o
	}
	for name, kind := range map[string]string{"Data": "table", "View": "view", "Sequence": "sequence", "Procedure": "procedure"} {
		if found[name].Ref.Kind != kind || kind != "table" && found[name].Definition == "" {
			t.Fatal("native object/source missing", name, found[name])
		}
	}
	metadata, err := s.TableMetadata(ctx, s.Namespace(schema).Object("table", "Data"))
	if err != nil || len(metadata.Columns) != 7 || !metadata.Editable || !strings.Contains(metadata.Definition, "IDENTITY") || len(metadata.ForeignKeys) != 1 || metadata.Comment != " 中文 table " {
		t.Fatal("native structure or feature boundary failed", err, metadata)
	}
	cols := make(map[string]contract.StructureColumn)
	for i, col := range metadata.Columns {
		if col.Position != int64(i+1) {
			t.Fatal("native zero-based positions were not converted")
		}
		cols[col.Name] = col
	}
	if !cols["I"].Generated || cols["I"].Extra != "IDENTITY" || !cols["G"].Generated || cols["T"].Generated || cols["ID"].Key != "PRI" || cols["N"].Type != "DECIMAL(38,20)" || cols["N"].Comment != " exact NUMERIC " || cols["D"].Type != "TIMESTAMP(6)" {
		t.Fatal("native identity/generated/type/default/comment proof failed", cols)
	}
	if _, err := s.TableMetadata(ctx, s.Namespace(schema).Object("view", "Data")); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("object kind mismatch accepted", err)
	}
	foreign := s.Namespace(schema)
	foreign.ProfileID++
	if _, err := s.Browse(ctx, foreign); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("another profile's namespace accepted", err)
	}
	foreign = s.Namespace(schema)
	foreign.Catalog = "OTHER"
	if _, err := s.Browse(ctx, foreign); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("another catalog's namespace accepted", err)
	}
	lease, err := s.ReserveQuery(1)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	second, err := s.ReserveQuery(2)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Release()
	if _, err = s.ReserveQuery(3); !errors.Is(err, contract.ErrSQLSessionLimit) {
		t.Fatal("SQL lease limit was not enforced", err)
	}
	if err = lease.Preconnect(ctx); err != nil {
		t.Fatal(err)
	}
	result, err := lease.Execute(ctx, schema, `CREATE GLOBAL TEMPORARY TABLE marks(ID BIGINT,T VARCHAR(128)) ON COMMIT PRESERVE ROWS; INSERT INTO marks VALUES(1,'a;b'); SELECT ID,T FROM marks`)
	if err != nil || len(result.Sets) != 3 || len(result.Sets[2].Rows) != 1 || result.Sets[2].Rows[0][1].Text != "a;b" {
		t.Fatal("original physical SQL batch failed", err, result)
	}
	other, err := second.Execute(ctx, schema, "SELECT ID FROM marks")
	if err != nil || len(other.Sets) != 1 || len(other.Sets[0].Rows) != 0 {
		t.Fatal("temporary SQL rows leaked to another physical session", err)
	}
	var backgroundRows int
	if err = s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM marks").Scan(&backgroundRows); err != nil || backgroundRows != 0 {
		t.Fatal("temporary SQL rows leaked to background metadata pool", err)
	}
	script := "CREATE OR REPLACE PROCEDURE " + prefix + `"FileProcedure" AS BEGIN INSERT INTO marks VALUES(2,'中文;'); END;` + "\n/\nCALL " + prefix + `"FileProcedure"(); SELECT ID,T FROM marks ORDER BY ID;`
	file := lease.(contract.QueryFileLease)
	result, err = file.ExecuteFile(ctx, schema, strings.NewReader("\uFEFF"+script))
	if err != nil || len(result.Sets) != 3 || len(result.Sets[2].Rows) != 2 {
		t.Fatal("native procedural SQL file lost boundaries or physical ownership", err, result)
	}
	if _, err = file.ExecuteFile(ctx, schema, strings.NewReader("INSERT INTO marks VALUES(3,'must not execute'); CREATE PROCEDURE broken AS BEGIN NULL;")); err == nil {
		t.Fatal("incomplete native SQL file was accepted")
	}
	result, err = lease.Execute(ctx, schema, "SELECT COUNT(*) FROM marks")
	if err != nil || result.Sets[0].Rows[0][0].Text != "2" {
		t.Fatal("failed SQL file ran earlier statements", err)
	}
	// A user's SET SCHEMA cannot poison the schema remembered by the next host request.
	if _, err = lease.Execute(ctx, schema, `SET SCHEMA "SYSDBA"`); err != nil {
		t.Fatal(err)
	}
	result, err = lease.Execute(ctx, schema, "SELECT SF_GET_SCHEMA_NAME_BY_ID(CURRENT_SCHID)")
	if err != nil || result.Sets[0].Rows[0][0].Text != schema {
		t.Fatal("host execution used a stale cached SQL schema", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = lease.Execute(ctx, schema, "SELECT 1"); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("closed native session lease remained usable", err)
	}
	if _, err = s.Namespaces(ctx); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("closed native metadata session remained usable", err)
	}
}

func TestNativeDamengQueryValuesCancellationAndExplicitRecovery(t *testing.T) {
	c, password := nativeFixture(t)
	s := fixtureSession(t, c, password)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	l, err := s.ReserveQuery(10)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	if err = l.Preconnect(ctx); err != nil {
		t.Fatal(err)
	}
	result, err := l.ExecuteStream(ctx, "SYSDBA", `SELECT CAST(9007199254740993 AS BIGINT),CAST('123456789012345678.12345678901234567891' AS DECIMAL(38,20)),HEXTORAW('0080FF'),CAST(NULL AS VARCHAR(10)),CAST('' AS VARCHAR(10)),CAST(1 AS BIT),'中文'`, func(_ int, columns []contract.Column, row []contract.Cell) error {
		columns[0].Name, row[0].Text = "mutated", "mutated"
		row[2].Bytes[0] = 99
		return nil
	})
	if err != nil || len(result.Sets) != 1 || len(result.Sets[0].Rows) != 1 {
		t.Fatal("native typed SQL values failed", err)
	}
	row := result.Sets[0].Rows[0]
	if row[0].Kind != contract.Integer || row[0].Text != "9007199254740993" || row[1].Kind != contract.Decimal || row[1].Text != "123456789012345678.12345678901234567891" || row[2].Kind != contract.Binary || row[2].Bytes[0] != 0 || row[3].Kind != contract.Null || row[4].Kind != contract.Text || row[4].Text != "" || row[5].Kind != contract.Boolean || row[5].Text != "true" {
		t.Fatal("typed SQL values or callback ownership changed", row)
	}
	short, stop := context.WithTimeout(ctx, 200*time.Millisecond)
	defer stop()
	_, err = l.Execute(short, "", `SELECT COUNT(*) FROM SYS.SYSOBJECTS A CROSS JOIN SYS.SYSOBJECTS B CROSS JOIN SYS.SYSOBJECTS C WHERE MOD(A.ID,17)+MOD(B.ID,17)+MOD(C.ID,17)=0`)
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, contract.ErrSQLSessionLost) {
		t.Fatal("native cancellation did not settle as an original lost SQL session", err)
	}
	if _, err = l.Execute(ctx, "", "SELECT 1"); !errors.Is(err, contract.ErrSQLSessionLost) {
		t.Fatal("canceled lease silently acquired another physical session", err)
	}
	recovered, err := l.Replace()
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Release()
	result, err = recovered.Execute(ctx, "", "SELECT 42")
	if err != nil || result.Sets[0].Rows[0][0].Text != "42" {
		t.Fatal("explicit native SQL recovery failed", err)
	}
	if err := s.db.PingContext(ctx); err != nil && !errors.Is(err, sql.ErrConnDone) {
		t.Fatal("SQL cancellation affected another background physical connection", err)
	}
}
